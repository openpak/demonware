package play

import (
	"crypto/rand"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/redact"
	"github.com/openpak/demonware/wire"
)

// Session-based matchmaking: service 21 (Diablo III public games) and service 138 (the same
// tasks under a context string, Crash Team Racing). docs/protocol.md §6.
const (
	mmCreate     = 1
	mmUpdate     = 2
	mmDelete     = 3
	mmFind       = 5
	mmPlayers    = 12
	mmFriends    = 14 // 138 only
	mmReserve    = 19
	mmInitialize = 20
)

const (
	// hostGrace keeps a game whose host dropped, for friends, while the client re-logs in.
	hostGrace = 2 * time.Minute
	// staleAfter drops a game nobody has touched in this long.
	staleAfter = 6 * time.Hour
	maxResults = 50
)

type game struct {
	id      [8]byte
	title   uint32
	context string

	owner    uint64 // host's lobby connection, 0 once it closed
	ownerID  uint64 // host's online id
	orphaned time.Time
	updated  time.Time
	reserved bool // id handed out (19), nothing published yet

	hostAddr   []byte // bdCommonAddr
	gameType   uint32
	maxPlayers uint32
	players    uint32
	attrs      []byte // title attributes, echoed in results as they were sent
}

type info struct {
	hostAddr             []byte
	gameType, maxPlayers uint32
	attrs                []byte
}

// parseInfo reads a client bdMatchMakingInfo. Under a context (CTR) the attributes come
// wrapped in a blob and anything after it is request options, not attributes.
func parseInfo(r *wire.Reader, wrapped bool) (info, error) {
	var in info
	var err error
	if in.hostAddr, err = r.Blob(); err != nil {
		return in, err
	}
	if in.gameType, err = r.U32(); err != nil {
		return in, err
	}
	if in.maxPlayers, err = r.U32(); err != nil {
		return in, err
	}
	if !wrapped {
		in.attrs = r.Rest()
	} else if r.Next() == wire.TBlob {
		in.attrs, err = r.Blob()
	}
	return in, err
}

func (g *game) set(in info) {
	g.hostAddr, g.gameType, g.maxPlayers, g.attrs = in.hostAddr, in.gameType, in.maxPlayers, in.attrs
}

func (s *Shared) owns(c *lobby.Conn, g *game) bool {
	return g.title == c.Title() && (g.owner == c.ID || g.ownerID == c.OnlineID())
}

func newID() [8]byte {
	var id [8]byte
	_, _ = rand.Read(id[:])
	return id
}

func (s *Shared) readID(r *wire.Reader) ([8]byte, bool) {
	var id [8]byte
	b, err := r.Blob()
	if err != nil || len(b) != 8 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

func (s *Shared) matchmaking(c *lobby.Conn, r *lobby.Request, ctx string) []byte {
	wrapped := ctx != ""
	now := s.Now()
	switch r.Task {
	case mmReserve:
		g := &game{id: newID(), title: c.Title(), context: ctx, owner: c.ID, ownerID: c.OnlineID(),
			reserved: true, updated: now}
		s.mu.Lock()
		for k, old := range s.games {
			if old.reserved && old.context == ctx && s.owns(c, old) {
				delete(s.games, k)
			}
		}
		s.games[g.id] = g
		s.mu.Unlock()
		return wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.Blob(g.id[:]); return 1 })

	case mmCreate:
		in, err := parseInfo(r.Args, wrapped)
		if err != nil {
			return wire.Fail(wire.Failed)
		}
		g := &game{id: newID(), title: c.Title(), context: ctx, owner: c.ID, ownerID: c.OnlineID(),
			updated: now, players: 1}
		g.set(in)
		s.mu.Lock()
		for k, old := range s.games { // a host recreating its game replaces the old one
			if old.context == ctx && s.owns(c, old) {
				delete(s.games, k)
			}
		}
		s.games[g.id] = g
		s.mu.Unlock()
		c.Log().Info("game created", "context", redact.Tag(ctx), "type", in.gameType, "max", in.maxPlayers,
			"attrs", wire.Describe(in.attrs))
		return wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.Blob(g.id[:]); return 1 })

	case mmUpdate, mmInitialize, mmPlayers:
		id, ok := s.readID(r.Args)
		if !ok {
			return wire.Fail(wire.Failed)
		}
		var players uint32
		if r.Task == mmPlayers {
			var err error
			if players, err = r.Args.U32(); err != nil {
				return wire.Fail(wire.Failed)
			}
		}
		in, infoErr := parseInfo(r.Args, wrapped)
		if infoErr != nil && r.Task != mmPlayers {
			return wire.Fail(wire.Failed)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		g := s.games[id]
		if g == nil || g.context != ctx || !s.owns(c, g) {
			return wire.Fail(wire.Failed)
		}
		g.owner, g.ownerID, g.updated = c.ID, c.OnlineID(), now
		if infoErr == nil {
			g.set(in)
			c.Log().Info("game updated", "task", r.Task, "type", in.gameType, "max", in.maxPlayers,
				"players", g.players, "attrs", wire.Describe(in.attrs))
		}
		switch r.Task {
		case mmInitialize:
			g.reserved, g.players = false, 1
		case mmPlayers:
			g.players = players
		}
		return wire.Empty(r.Task)

	case mmDelete:
		if id, ok := s.readID(r.Args); ok {
			s.mu.Lock()
			if g := s.games[id]; g != nil && g.context == ctx && s.owns(c, g) {
				delete(s.games, id)
			}
			s.mu.Unlock()
		}
		return wire.Empty(r.Task)

	case mmFind:
		query, _ := r.Args.U32()
		start, _ := r.Args.U32()
		limit, _ := r.Args.U32()
		filters := r.Args.Rest()
		if limit == 0 || limit > maxResults {
			limit = maxResults
		}
		match := s.Matchers[c.Title()]
		if ctx != "" {
			match = nil // the contextual (138) searches carry no filters of this kind
		}
		s.mu.Lock()
		var found []game
		rank := map[[8]byte]int{}
		open, refused := 0, map[string]int{}
		for _, g := range s.games {
			if g.title != c.Title() || g.context != ctx || g.reserved || g.owner == 0 || s.owns(c, g) ||
				(g.maxPlayers != 0 && g.players >= g.maxPlayers) {
				continue
			}
			open++
			if match != nil {
				r, ok, why := match(query, filters, g.attrs)
				if !ok {
					refused[why]++
					continue
				}
				rank[g.id] = r
			}
			found = append(found, *g)
		}
		s.mu.Unlock()
		sort.Slice(found, func(i, j int) bool {
			if ri, rj := rank[found[i].id], rank[found[j].id]; ri != rj {
				return ri < rj
			}
			return found[i].updated.After(found[j].updated)
		})
		found = found[min(int(start), len(found)):]
		found = found[:min(int(limit), len(found))]
		c.Log().Info("games searched", "context", redact.Tag(ctx), "query", query, "filters", wire.Describe(filters),
			"open", open, "returned", len(found), "refused", fmt.Sprint(refused))
		return s.results(c, r.Task, found)

	case mmFriends:
		if !wrapped {
			return wire.Empty(r.Task)
		}
		ids := r.Args.U64List(512)
		want := map[uint64]bool{}
		for _, id := range ids {
			if id != c.OnlineID() {
				want[id] = true
			}
		}
		s.mu.Lock()
		var found []game
		for _, g := range s.games {
			if g.title == c.Title() && g.context == ctx && !g.reserved && want[g.ownerID] {
				found = append(found, *g)
			}
		}
		s.mu.Unlock()
		return s.results(c, r.Task, found)
	}
	c.Log().Info("matchmaking task not implemented", "service", r.Service, "task", r.Task)
	return wire.Fail(wire.Failed)
}

// results writes session results; the joiner may get a relay endpoint in its copy.
func (s *Shared) results(c *lobby.Conn, task byte, games []game) []byte {
	var ip net.IP = c.RemoteIP()
	return wire.Rows(task, func(w *wire.Writer) uint32 {
		for _, g := range games {
			w.Blob(s.Relay.Offer(g.hostAddr, g.ownerID, c.OnlineID(), ip))
			w.Blob(g.id[:])
			w.U32(g.gameType).U32(g.maxPlayers).U32(g.players)
			w.Raw(g.attrs)
		}
		return uint32(len(games))
	})
}

func (s *Shared) expire() {
	now := s.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, g := range s.games {
		if (g.owner == 0 && now.Sub(g.orphaned) > hostGrace) || now.Sub(g.updated) > staleAfter {
			delete(s.games, k)
		}
	}
}

// Games is the number of open games, for /health.
func (s *Shared) Games() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.games)
}
