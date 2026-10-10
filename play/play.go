// Package play holds the remote tasks both titles use: server time, friend names, user data,
// rich presence, A/B testing and the session-based matchmaking of services 21 and 138
// (docs/protocol.md §6).
package play

import (
	"log/slog"
	"sync"
	"time"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/nat"
	"github.com/openpak/demonware/wire"
)

// Services.
const (
	SvcStorage     = 10
	SvcTitleUtils  = 12
	SvcMatchMaking = 21
	SvcUserData    = 29
	SvcPresence    = 68
	SvcContextMM   = 138
	SvcABTesting   = 194
)

// Shared is the state and handlers every title shares.
type Shared struct {
	Lobby *lobby.Server
	Relay *nat.Relay
	Log   *slog.Logger
	Now   func() time.Time

	// Matchers decide, per title, which open games a search may return (service 21 find).
	// A title without one gets every open game.
	Matchers map[uint32]Matcher

	mu       sync.Mutex
	games    map[[8]byte]*game
	userData map[dataKey][]byte
	presence map[presenceKey]presence
}

type dataKey struct {
	title uint32
	id    uint64
	ctx   string
}

type presenceKey struct {
	title uint32
	id    uint64
}

type presence struct {
	platform string
	flag     byte
	data     []byte
}

// New builds the shared services and starts the stale-game sweep.
func New(l *lobby.Server, relay *nat.Relay, log *slog.Logger) *Shared {
	s := &Shared{Lobby: l, Relay: relay, Log: log, Now: time.Now,
		games: map[[8]byte]*game{}, userData: map[dataKey][]byte{}, presence: map[presenceKey]presence{}}
	go func() {
		for range time.Tick(30 * time.Second) {
			s.expire()
		}
	}()
	return s
}

// Handle answers a shared task; ok false means the task is not one of these.
func (s *Shared) Handle(c *lobby.Conn, r *lobby.Request) (lobby.Response, bool) {
	switch {
	case r.Service == SvcTitleUtils && r.Task == 6:
		now := uint32(s.Now().Unix())
		return reply(wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.U32(now); return 1 })), true
	case r.Service == SvcTitleUtils && r.Task == 9:
		return reply(s.userNames(c, r)), true
	case r.Service == SvcUserData && (r.Task == 1 || r.Task == 4):
		return reply(s.userDataTask(c, r)), true
	case r.Service == SvcPresence:
		return reply(s.presenceTask(c, r)), true
	case r.Service == SvcMatchMaking:
		return reply(s.matchmaking(c, r, "")), true
	case r.Service == SvcContextMM:
		ctx, err := r.Args.String()
		if err != nil || ctx == "" {
			return reply(wire.Fail(wire.Failed)), true
		}
		return reply(s.matchmaking(c, r, ctx)), true
	case r.Service == SvcABTesting && r.Task == 1:
		body := []byte(`{"expiresIn":3600,"ABToken":"openpak","enrollments":[]}`)
		return reply(wire.StructReply(r.Task, wire.PB{}.Varint(1, 200).Varint(2, 200).Bytes(3, body))), true
	}
	return lobby.Response{}, false
}

// Matcher compares a search (its query id and the typed filter values after start and max)
// with an open game's attributes. ok false leaves the game out and why says which value
// differed (for the log: no ids, names or addresses); among the games kept, a lower rank is
// returned first.
type Matcher func(query uint32, filters, attrs []byte) (rank int, ok bool, why string)

func reply(b []byte) lobby.Response { return lobby.Response{Body: b} }

// Closed detaches a closing connection's games and, when the player has no other connection,
// forgets their rich presence.
func (s *Shared) Closed(c *lobby.Conn) {
	id := c.OnlineID()
	still := false
	s.Lobby.Each(func(o *lobby.Conn) {
		if o != c && o.OnlineID() == id && o.Title() == c.Title() {
			still = true
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.games {
		if g.owner == c.ID {
			g.owner, g.orphaned = 0, s.Now()
		}
	}
	if !still {
		delete(s.presence, presenceKey{c.Title(), id})
	}
}

// Online returns the connection of a player of title by online id (or PID), or nil.
func (s *Shared) Online(title uint32, id uint64) *lobby.Conn {
	var found *lobby.Conn
	s.Lobby.Each(func(c *lobby.Conn) {
		if found == nil && c.Title() == title && (c.OnlineID() == id || c.Player().PID == id) {
			found = c
		}
	})
	return found
}

// 12/9: nicknames of the asked ids that are online in the same game.
func (s *Shared) userNames(c *lobby.Conn, r *lobby.Request) []byte {
	ids := r.Args.U64List(512)
	type hit struct {
		id   uint64
		name string
	}
	var hits []hit
	for _, id := range ids {
		if o := s.Online(c.Title(), id); o != nil && o != c {
			hits = append(hits, hit{id, o.Player().Nickname})
		}
	}
	c.Log().Info("user names", "asked", len(ids), "online", len(hits))
	return wire.Rows(r.Task, func(w *wire.Writer) uint32 {
		for _, h := range hits {
			w.U64(h.id).String(h.name)
		}
		return uint32(len(hits))
	})
}

// 29/1 stores the caller's data for a context; 29/4 reads it back for a list of ids.
func (s *Shared) userDataTask(c *lobby.Conn, r *lobby.Request) []byte {
	ctx, err := r.Args.String()
	if err != nil {
		return wire.Fail(wire.Failed)
	}
	if r.Task == 1 {
		data, err := r.Args.Blob()
		if err != nil || len(data) > 64<<10 {
			return wire.Fail(wire.Failed)
		}
		s.mu.Lock()
		s.userData[dataKey{c.Title(), c.OnlineID(), ctx}] = data
		s.mu.Unlock()
		return wire.Empty(r.Task)
	}
	if r.Args.Next() == wire.TBool {
		_, _ = r.Args.Bool()
	}
	ids := r.Args.U64List(512)
	s.mu.Lock()
	type hit struct {
		id   uint64
		data []byte
	}
	var hits []hit
	for _, id := range ids {
		if d, ok := s.userData[dataKey{c.Title(), id, ctx}]; ok {
			hits = append(hits, hit{id, d})
		}
	}
	s.mu.Unlock()
	return wire.Rows(r.Task, func(w *wire.Writer) uint32 {
		for _, h := range hits {
			w.U64(h.id).Blob(h.data)
		}
		return uint32(len(hits))
	})
}

// 68: rich presence. A player sets only their own; others see it while that player is online.
func (s *Shared) presenceTask(c *lobby.Conn, r *lobby.Request) []byte {
	if _, err := r.Args.String(); err != nil { // context
		return wire.Empty(r.Task)
	}
	me := c.OnlineID()
	switch r.Task {
	case 3:
		id, e1 := r.Args.U64()
		platform, e2 := r.Args.String()
		flag, e3 := r.Args.U8()
		data, e4 := r.Args.Blob()
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || (id != 0 && id != me) || len(data) > 16<<10 {
			return wire.Empty(r.Task)
		}
		s.mu.Lock()
		s.presence[presenceKey{c.Title(), me}] = presence{platform, flag, data}
		s.mu.Unlock()
		c.Log().Info("rich presence set", "flag", flag, "bytes", len(data))
		return wire.Empty(r.Task)
	case 4, 5:
		n, err := r.Args.U32()
		if err != nil || n > 1024 {
			return wire.Empty(r.Task)
		}
		type hit struct {
			id uint64
			p  presence
		}
		var hits []hit
		for range n {
			id, e1 := r.Args.U64()
			_, e2 := r.Args.String()
			if e1 != nil || e2 != nil {
				break
			}
			if id == 0 {
				id = me
			}
			if id != me && s.Online(c.Title(), id) == nil {
				continue
			}
			s.mu.Lock()
			p, ok := s.presence[presenceKey{c.Title(), id}]
			s.mu.Unlock()
			if ok {
				hits = append(hits, hit{id, p})
			}
		}
		c.Log().Info("rich presence read", "task", r.Task, "asked", n, "found", len(hits))
		return wire.Rows(r.Task, func(w *wire.Writer) uint32 {
			for _, h := range hits {
				w.U64(h.id).String(h.p.platform).U8(h.p.flag).Blob(h.p.data)
			}
			return uint32(len(hits))
		})
	}
	c.Log().Info("rich presence task", "task", r.Task, "args", wire.Describe(r.Args.Rest()))
	return wire.Empty(r.Task)
}
