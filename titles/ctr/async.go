package ctr

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/wire"
)

// Async matchmaking (service 145): the server groups searches into lobbies, picks a host,
// and tells the host and guests through pushes; the race itself is peer to peer
// (docs/protocol.md §8). Lobby merges (event 2008) are not implemented.

const (
	evJoin   = 2001
	evCreate = 2005
	evUpdate = 2006
)

// Player states in a host doc.
const (
	stateHost      = 1
	stateConnected = 2
	stateFailed    = 3
)

type playerInfo struct {
	addr, secID, secKey string // base64, as uploaded
}

type search struct {
	id       uint64
	conn     *lobby.Conn
	player   uint64
	members  []uint64
	min, max int
	filter   string          // canonical JSON of ruleset_payload.filter
	ruleset  json.RawMessage // ruleset_payload as sent
	info     playerInfo
}

type room struct {
	id, version uint64
	created     int64
	host        *search
	searches    []*search // host's included
	doc         map[string]any
	capacity    int
	told        map[uint64]bool // searches already sent 2001 (or 2005)
}

type notice struct {
	to      *lobby.Conn
	event   uint32
	ids     []uint64
	host    string
	backend string
}

func (n notice) payload() []byte {
	return wire.Push(n.event, func(w *wire.Writer) {
		for _, id := range n.ids {
			w.U64(id)
		}
		w.String(n.host).String(n.backend)
	})
}

type matcher struct {
	mu      sync.Mutex
	next    uint64
	infos   map[uint64]playerInfo // lobby conn id -> uploaded player info
	waiting []*search
	rooms   map[uint64]*room
	now     func() time.Time
}

func newMatcher() *matcher {
	return &matcher{infos: map[uint64]playerInfo{}, rooms: map[uint64]*room{}, now: time.Now}
}

func (m *matcher) id() uint64 { m.next++; return m.next }

// setInfo keeps the listen address and DTLS identity a player uploads (145/2).
func (m *matcher) setInfo(c *lobby.Conn, doc string) error {
	var d struct {
		Listen struct {
			Addr string `json:"local_address"`
			ID   string `json:"security_id"`
			Key  string `json:"security_key"`
		} `json:"listen_server"`
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		return err
	}
	for s, n := range map[string]int{d.Listen.Addr: 67, d.Listen.ID: 8, d.Listen.Key: 16} {
		if b, err := base64.StdEncoding.DecodeString(s); err != nil || len(b) != n {
			return errors.New("player info: bad address or DTLS identity")
		}
	}
	m.mu.Lock()
	m.infos[c.ID] = playerInfo{d.Listen.Addr, d.Listen.ID, d.Listen.Key}
	m.mu.Unlock()
	return nil
}

// parseSearch validates a search doc (145/18). A party searches once, from a leader who is
// among the members.
func (m *matcher) parseSearch(c *lobby.Conn, raw string) (*search, error) {
	var d struct {
		Members []uint64 `json:"members"`
		Slots   struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"lobby_slots"`
		Ruleset json.RawMessage `json:"ruleset_payload"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, err
	}
	var rules struct {
		Filter map[string]json.RawMessage `json:"filter"`
	}
	_ = json.Unmarshal(d.Ruleset, &rules)
	me := c.OnlineID()
	if len(d.Members) == 0 || len(d.Members) > 8 || d.Slots.Min < 1 || d.Slots.Max < d.Slots.Min ||
		d.Slots.Max > 8 || len(d.Members) > d.Slots.Max || len(rules.Filter) == 0 {
		return nil, errors.New("search: bad members, slots or filter")
	}
	seen, leader := map[uint64]bool{}, false
	for _, id := range d.Members {
		if id == 0 || seen[id] {
			return nil, errors.New("search: zero or repeated member")
		}
		seen[id], leader = true, leader || id == me
	}
	if !leader {
		return nil, errors.New("search: the searching player is not a member")
	}
	filter, _ := json.Marshal(rules.Filter) // map keys sorted: a canonical form
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.infos[c.ID]
	if !ok {
		return nil, errors.New("search: no player info uploaded")
	}
	return &search{id: m.id(), conn: c, player: me, members: d.Members, min: d.Slots.Min, max: d.Slots.Max,
		filter: string(filter), ruleset: d.Ruleset, info: info}, nil
}

func num(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	}
	return 0
}

func (r *room) stateOf(id uint64) int64 {
	states, _ := r.doc["player_state"].(map[string]any)
	return num(states[strconv.FormatUint(id, 10)])
}

func (r *room) timedOut(id uint64) bool { return id != r.host.player && r.stateOf(id) == stateFailed }

func (r *room) open() bool { v, _ := r.doc["lobby_open"].(bool); return v }

// seated counts people, not searches: a search may be a party.
func (r *room) seated() int {
	n := 0
	for _, s := range r.searches {
		for _, id := range s.members {
			if !r.timedOut(id) {
				n++
			}
		}
	}
	return n
}

func (r *room) forget(members []uint64) {
	if states, ok := r.doc["player_state"].(map[string]any); ok {
		for _, id := range members {
			delete(states, strconv.FormatUint(id, 10))
		}
	}
}

func (r *room) bump(now time.Time) {
	r.version++
	r.doc["update_id"] = r.version
	r.doc["update_time"] = now.Unix()
}

// docs are the host doc and the backend doc of a room.
func (r *room) render(now time.Time) (string, string) {
	players := map[string]string{}
	for _, s := range r.searches {
		for _, id := range s.members {
			if !r.timedOut(id) {
				players[strconv.FormatUint(id, 10)] = "expected"
			}
		}
	}
	ruleset := r.host.ruleset
	if len(ruleset) == 0 {
		ruleset = json.RawMessage("{}")
	}
	backend, _ := json.Marshal(map[string]any{
		"update_id": r.version, "update_time": now.Unix(), "lobby_id": r.id, "create_time": r.created,
		"players": players, "dtls": map[string]string{"security_id": r.host.info.secID, "security_key": r.host.info.secKey},
		"ruleset_payload": ruleset,
	})
	host, _ := json.Marshal(r.doc)
	return string(host), string(backend)
}

func (r *room) tell(to *search, event uint32, now time.Time) notice {
	h, b := r.render(now)
	ids := []uint64{to.id, r.id}
	switch event {
	case evCreate:
		ids = append(ids, r.version)
	case evUpdate:
		ids = []uint64{r.id, r.version}
	}
	return notice{to.conn, event, ids, h, b}
}

// decode parses JSON keeping numbers exact.
func decode(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil || v == nil {
		return nil, errors.New("bad document")
	}
	return v, nil
}

// removePlayerLocked takes a player out of the queue and out of any room: a player who searches
// again is not in a lobby any more, whatever the server believed.
func (m *matcher) removePlayerLocked(player uint64) {
	kept := m.waiting[:0]
	for _, s := range m.waiting {
		if s.player != player {
			kept = append(kept, s)
		}
	}
	m.waiting = kept
	for id, r := range m.rooms {
		if r.host.player == player {
			delete(m.rooms, id)
			continue
		}
		for i, s := range r.searches {
			if s.player == player {
				r.searches = append(r.searches[:i], r.searches[i+1:]...)
				delete(r.told, s.id)
				break
			}
		}
	}
}

func (m *matcher) fits(r *room, s *search) bool {
	return r.open() && r.host.filter == s.filter && r.seated()+len(s.members) <= min(r.capacity, s.max)
}

// place puts a search: into an open room with the same filter and room for its party,
// or into the queue, forming a new room once enough compatible searches wait.
func (m *matcher) place(s *search) []notice {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.removePlayerLocked(s.player)

	rooms := make([]*room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	sort.Slice(rooms, func(i, j int) bool {
		if a, b := rooms[i].seated(), rooms[j].seated(); a != b {
			return a > b
		}
		return rooms[i].id < rooms[j].id
	})
	for _, r := range rooms {
		if !m.fits(r, s) {
			continue
		}
		r.forget(s.members)
		r.searches = append(r.searches, s)
		r.capacity = min(r.capacity, s.max)
		r.bump(now)
		return []notice{r.tell(r.host, evUpdate, now)}
	}

	m.waiting = append(m.waiting, s)
	var group []*search
	seats, lo, hi := 0, 2, 8
	for _, w := range m.waiting {
		if w.filter != s.filter {
			continue
		}
		nlo, nhi := max(lo, w.min), min(hi, w.max)
		if nlo > nhi || seats+len(w.members) > nhi {
			continue
		}
		group, seats, lo, hi = append(group, w), seats+len(w.members), nlo, nhi
	}
	if seats < lo {
		return nil
	}
	h := group[0]
	r := &room{id: m.id(), version: 1, created: now.Unix(), host: h, searches: group, capacity: hi,
		told: map[uint64]bool{h.id: true}}
	initial, _ := json.Marshal(map[string]any{
		"update_time": now.Unix(), "update_id": r.version, "game_id": r.id,
		"lobby_open": true, "lobby_open_for_pres_join": true,
		"player_state":    map[string]int{strconv.FormatUint(h.player, 10): stateHost},
		"listen_server":   map[string]any{"host_address": h.info.addr, "host_player_id": h.player},
		"team_balance":    map[string]bool{"can_change_teams": true},
		"ruleset_payload": map[string]any{}, "attachment": nil,
	})
	r.doc, _ = decode(initial)
	m.rooms[r.id] = r
	inGroup := map[*search]bool{}
	for _, g := range group {
		inGroup[g] = true
	}
	kept := m.waiting[:0]
	for _, w := range m.waiting {
		if !inGroup[w] {
			kept = append(kept, w)
		}
	}
	m.waiting = kept
	return []notice{r.tell(h, evCreate, now)}
}

var syncKeys = []string{"lobby_open", "lobby_open_for_pres_join", "player_state", "listen_server",
	"team_balance", "ruleset_payload", "attachment"}

// sync takes the host's new doc (145/15). Accepted only from the host, for the current
// version; then guests not yet told are sent 2001, and waiting searches fill open seats.
func (m *matcher) sync(c *lobby.Conn, id, version uint64, raw string) (bool, []notice) {
	doc, err := decode([]byte(raw))
	if err != nil {
		return false, nil
	}
	for _, k := range syncKeys {
		if _, ok := doc[k]; !ok {
			return false, nil
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || r.host.conn != c || r.version != version {
		return false, nil
	}
	now := m.now()
	doc["update_id"], doc["update_time"] = r.version, now.Unix()
	r.doc = doc

	// Guests the host reports as timed out free their seats.
	kept := r.searches[:0]
	for _, s := range r.searches {
		var left []uint64
		for _, id := range s.members {
			if !r.timedOut(id) {
				left = append(left, id)
			}
		}
		if len(left) == 0 {
			delete(r.told, s.id)
			continue
		}
		s.members = left
		kept = append(kept, s)
	}
	r.searches = kept

	var out []notice
	for _, s := range r.searches {
		if !r.told[s.id] {
			r.told[s.id] = true
			out = append(out, r.tell(s, evJoin, now))
		}
	}
	if r.open() {
		added := false
		waiting := m.waiting[:0]
		for _, w := range m.waiting {
			if !m.fits(r, w) {
				waiting = append(waiting, w)
				continue
			}
			r.forget(w.members)
			r.searches = append(r.searches, w)
			r.capacity = min(r.capacity, w.max)
			added = true
		}
		m.waiting = waiting
		if added {
			r.bump(now)
			out = append(out, r.tell(r.host, evUpdate, now))
		}
	}
	return true, out
}

// documents answers 145/13 for a member of the room.
func (m *matcher) docsFor(c *lobby.Conn, id uint64) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil {
		return "", "", false
	}
	for _, s := range r.searches {
		if s.conn == c {
			h, b := r.render(m.now())
			return h, b, true
		}
	}
	return "", "", false
}

// cancel drops a waiting search of this connection (145/8).
func (m *matcher) withdraw(c *lobby.Conn, id uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.waiting {
		if s.conn == c && s.id == id {
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
			return true
		}
	}
	return false
}

// leave takes a connection out of the queue and out of room id (every room when id is 0).
// A host leaving ends its room.
func (m *matcher) remove(c *lobby.Conn, id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.waiting[:0]
	for _, s := range m.waiting {
		if s.conn != c {
			kept = append(kept, s)
		}
	}
	m.waiting = kept
	for rid, r := range m.rooms {
		if id != 0 && rid != id {
			continue
		}
		if r.host.conn == c {
			delete(m.rooms, rid)
			continue
		}
		for i, s := range r.searches {
			if s.conn == c {
				r.searches = append(r.searches[:i], r.searches[i+1:]...)
				break
			}
		}
	}
}

func (m *matcher) closed(c *lobby.Conn) {
	m.remove(c, 0)
	m.mu.Lock()
	delete(m.infos, c.ID)
	m.mu.Unlock()
}

func (m *matcher) counts() (waiting, rooms int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.waiting), len(m.rooms)
}

// searchReply is 145/18's result.
func searchReply(id uint64) string { return `{"mm_id":` + strconv.FormatUint(id, 10) + `}` }
