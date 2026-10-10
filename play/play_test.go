package play

import (
	"bytes"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

type fixture struct {
	lob    *lobby.Server
	shared *Shared
	now    time.Time
}

func newFixture() *fixture {
	lob := &lobby.Server{Log: slog.New(slog.DiscardHandler)}
	f := &fixture{lob: lob, now: time.Now()}
	f.shared = &Shared{Lobby: lob, Log: lob.Log, Now: func() time.Time { return f.now },
		games: map[[8]byte]*game{}, userData: map[dataKey][]byte{}, presence: map[presenceKey]presence{}}
	return f
}

func (f *fixture) player(pid uint64, nsa string, title uint32, ip string) *lobby.Conn {
	return f.lob.Fake(&session.Session{Title: title, Expires: time.Now().Add(time.Hour),
		Player: account.Player{PID: pid, BaasUserID: nsa, Nickname: "p" + strconv.FormatUint(pid, 10)}}, ip)
}

func (f *fixture) call(c *lobby.Conn, svc, task byte, args func(w *wire.Writer)) *wire.Reader {
	w := &wire.Writer{}
	w.B = []byte{svc}
	w.U8(task)
	if args != nil {
		args(w)
	}
	w.B = append(w.B, 0)
	r := wire.NewReader(w.B)
	r.Off = 1
	_, _ = r.U8()
	resp, ok := f.shared.Handle(c, &lobby.Request{Service: svc, Task: task, Args: r, Raw: w.B})
	if !ok {
		return nil
	}
	return wire.NewReader(resp.Body)
}

// head reads the reply envelope and returns (error, count).
func head(t *testing.T, r *wire.Reader) (uint32, uint32) {
	t.Helper()
	if _, err := r.U64(); err != nil {
		t.Fatal(err)
	}
	code, _ := r.U32()
	if code != 0 {
		return code, 0
	}
	_, _ = r.U8()
	n, _ := r.U32()
	if n > 0 {
		_, _ = r.U32()
	}
	return 0, n
}

func hostAddr(b byte) []byte { return bytes.Repeat([]byte{b}, 67) }

func writeInfo(w *wire.Writer, addr []byte, max uint32, attrs []byte, wrapped bool) {
	w.Blob(addr).U32(1).U32(max)
	if wrapped {
		w.Blob(attrs)
	} else {
		w.Raw(attrs)
	}
}

func TestDiabloGames(t *testing.T) {
	f := newFixture()
	host := f.player(1, "a1", 5745, "198.51.100.1")
	joiner := f.player(2, "a2", 5745, "198.51.100.2")
	ctrPlayer := f.player(3, "a3", 5775, "198.51.100.3")
	attrs := (&wire.Writer{}).U32(9).U8(1).B

	r := f.call(host, SvcMatchMaking, mmCreate, func(w *wire.Writer) { writeInfo(w, hostAddr(7), 4, attrs, false) })
	if code, n := head(t, r); code != 0 || n != 1 {
		t.Fatal("create", code, n)
	}
	id, _ := r.Blob()

	find := func(c *lobby.Conn) (uint32, *wire.Reader) {
		r := f.call(c, SvcMatchMaking, mmFind, func(w *wire.Writer) { w.U32(0).U32(0).U32(10) })
		_, n := head(t, r)
		return n, r
	}
	n, r := find(joiner)
	if n != 1 {
		t.Fatalf("joiner finds %d games", n)
	}
	addr, _ := r.Blob()
	gotID, _ := r.Blob()
	gt, _ := r.U32()
	max, _ := r.U32()
	players, _ := r.U32()
	rest := r.Rest()
	if !bytes.Equal(addr, hostAddr(7)) || !bytes.Equal(gotID, id) || gt != 1 || max != 4 || players != 1 ||
		!bytes.Equal(rest, attrs) {
		t.Fatalf("result %x %x %d %d %d %x", addr, gotID, gt, max, players, rest)
	}
	if n, _ := find(host); n != 0 {
		t.Fatal("host finds its own game")
	}
	if n, _ := find(ctrPlayer); n != 0 {
		t.Fatal("another title sees the game")
	}

	// Only the host may change it.
	if code, _ := head(t, f.call(joiner, SvcMatchMaking, mmPlayers, func(w *wire.Writer) {
		w.Blob(id).U32(2)
		writeInfo(w, hostAddr(7), 4, attrs, false)
	})); code == 0 {
		t.Fatal("a joiner updated the host's game")
	}
	head(t, f.call(host, SvcMatchMaking, mmPlayers, func(w *wire.Writer) {
		w.Blob(id).U32(4)
		writeInfo(w, hostAddr(7), 4, attrs, false)
	}))
	if n, _ := find(joiner); n != 0 {
		t.Fatal("a full game is offered")
	}
	head(t, f.call(host, SvcMatchMaking, mmPlayers, func(w *wire.Writer) {
		w.Blob(id).U32(2)
		writeInfo(w, hostAddr(7), 4, attrs, false)
	}))

	// A dropped host's game disappears from searches at once and from memory after the grace.
	f.lob.Drop(host)
	f.shared.Closed(host)
	if n, _ := find(joiner); n != 0 {
		t.Fatal("a hostless game is offered")
	}
	f.now = f.now.Add(hostGrace + time.Second)
	f.shared.expire()
	if f.shared.Games() != 0 {
		t.Fatal("orphaned game not reaped")
	}
}

func TestDeleteAndReserve(t *testing.T) {
	f := newFixture()
	host := f.player(1, "a1", 5745, "198.51.100.1")
	other := f.player(2, "a2", 5745, "198.51.100.2")
	r := f.call(host, SvcMatchMaking, mmReserve, nil)
	head(t, r)
	id, _ := r.Blob()
	if n, _ := head(t, f.call(other, SvcMatchMaking, mmFind, func(w *wire.Writer) { w.U32(0).U32(0).U32(0) })); n != 0 {
		t.Fatal("reserved game offered")
	}
	head(t, f.call(host, SvcMatchMaking, mmInitialize, func(w *wire.Writer) { w.Blob(id); writeInfo(w, hostAddr(1), 4, nil, false) }))
	f.call(other, SvcMatchMaking, mmDelete, func(w *wire.Writer) { w.Blob(id) })
	if f.shared.Games() != 1 {
		t.Fatal("another player deleted the game")
	}
	f.call(host, SvcMatchMaking, mmDelete, func(w *wire.Writer) { w.Blob(id) })
	if f.shared.Games() != 0 {
		t.Fatal("host could not delete its game")
	}
}

func TestFriendSessionsCTR(t *testing.T) {
	f := newFixture()
	host := f.player(1, "00000000000000a1", 5775, "198.51.100.1")
	friend := f.player(2, "00000000000000a2", 5775, "198.51.100.2")
	ctx := func(w *wire.Writer) { w.String("Delta") }
	attrs := []byte("ctr attributes")
	r := f.call(host, SvcContextMM, mmCreate, func(w *wire.Writer) { ctx(w); writeInfo(w, hostAddr(2), 8, attrs, true); w.U32(99) })
	if code, _ := head(t, r); code != 0 {
		t.Fatal("create", code)
	}
	ask := func() (uint32, *wire.Reader) {
		r := f.call(friend, SvcContextMM, mmFriends, func(w *wire.Writer) { ctx(w); w.U64(0xa1) })
		_, n := head(t, r)
		return n, r
	}
	n, r := ask()
	if n != 1 {
		t.Fatalf("%d friend games", n)
	}
	r.Blob()
	r.Blob()
	r.U32()
	r.U32()
	r.U32()
	if got := r.Rest(); !bytes.Equal(got, attrs) {
		t.Fatalf("CTR attributes must come back unwrapped: %q", got)
	}
	// The friend stays joinable while the host reconnects, then not.
	f.shared.Closed(host)
	if n, _ := ask(); n != 1 {
		t.Fatal("friend game lost during the grace")
	}
	f.now = f.now.Add(hostGrace + time.Second)
	f.shared.expire()
	if n, _ := ask(); n != 0 {
		t.Fatal("abandoned friend game still offered")
	}
}

func TestNamesPresenceUserData(t *testing.T) {
	f := newFixture()
	me := f.player(1, "00000000000000a1", 5745, "198.51.100.1")
	friend := f.player(2, "00000000000000a2", 5745, "198.51.100.2")
	f.player(3, "00000000000000a3", 5775, "198.51.100.3") // playing the other game

	r := f.call(me, SvcTitleUtils, 9, func(w *wire.Writer) { w.U64(0xa2).U64(0xa3).U64(0xa9) })
	if _, n := head(t, r); n != 1 {
		t.Fatalf("%d names", n)
	}
	if id, _ := r.U64(); id != 0xa2 {
		t.Fatal("wrong friend", id)
	}
	if name, _ := r.String(); name != "p2" {
		t.Fatal("name", name)
	}

	// Rich presence: set own, not someone else's; visible while online.
	f.call(friend, SvcPresence, 3, func(w *wire.Writer) { w.String("").U64(0).String("nintendo").U8(2).Blob([]byte("racing")) })
	f.call(me, SvcPresence, 3, func(w *wire.Writer) { w.String("").U64(0xa2).String("nintendo").U8(1).Blob([]byte("forged")) })
	get := func() (uint32, *wire.Reader) {
		r := f.call(me, SvcPresence, 5, func(w *wire.Writer) { w.String("").U32(1).U64(0xa2).String("nintendo") })
		_, n := head(t, r)
		return n, r
	}
	n, r := get()
	if n != 1 {
		t.Fatalf("%d presences", n)
	}
	r.U64()
	r.String()
	r.U8()
	if data, _ := r.Blob(); string(data) != "racing" {
		t.Fatalf("presence %q", data)
	}
	f.lob.Drop(friend)
	f.shared.Closed(friend)
	if n, _ := get(); n != 0 {
		t.Fatal("presence of a player who left")
	}

	// User data.
	f.call(me, SvcUserData, 1, func(w *wire.Writer) { w.String("ctx").Blob([]byte("hero")) })
	r = f.call(friend, SvcUserData, 4, func(w *wire.Writer) { w.String("ctx").Bool(false).U64(0xa1) })
	if _, n := head(t, r); n != 1 {
		t.Fatal("user data not returned")
	}
	r.U64()
	if d, _ := r.Blob(); string(d) != "hero" {
		t.Fatalf("user data %q", d)
	}

	// Server time and A/B testing.
	r = f.call(me, SvcTitleUtils, 6, nil)
	if _, n := head(t, r); n != 1 {
		t.Fatal("server time")
	}
	if ts, _ := r.U32(); int64(ts) != f.now.Unix() {
		t.Fatal("server time value", ts)
	}
	r = f.call(me, SvcABTesting, 1, nil)
	r.U64()
	r.U32()
	r.U8()
	sb, err := r.Struct()
	fs, _ := wire.ParsePB(sb)
	if err != nil || len(fs) != 3 || fs[0].Value != 200 || fs[1].Value != 200 || !bytes.Contains(fs[2].Data, []byte(`"ABToken":"openpak"`)) {
		t.Fatalf("A/B reply %x", sb)
	}
}

// A title's matcher decides which open games a search returns, best rank first.
func TestFindUsesTitleMatcher(t *testing.T) {
	f := newFixture()
	f.shared.Matchers = map[uint32]Matcher{5745: func(query uint32, filters, attrs []byte) (int, bool, string) {
		if attrs[1] != filters[1] { // the first u8 value must equal
			return 0, false, "mode"
		}
		return int(attrs[3]), true, "" // then the second u8 is the rank
	}}
	a := f.player(1, "00000000000000b1", 5745, "198.51.100.1")
	b := f.player(2, "00000000000000b2", 5745, "198.51.100.2")
	c := f.player(3, "00000000000000b3", 5745, "198.51.100.3")
	joiner := f.player(4, "00000000000000b4", 5745, "198.51.100.4")
	create := func(p *lobby.Conn, mode, rank byte) {
		head(t, f.call(p, SvcMatchMaking, mmCreate, func(w *wire.Writer) { writeInfo(w, hostAddr(byte(p.ID)), 4, []byte{3, mode, 3, rank}, false) }))
	}
	create(a, 1, 5)
	create(b, 2, 0) // other mode: never returned
	create(c, 1, 1)
	r := f.call(joiner, SvcMatchMaking, mmFind, func(w *wire.Writer) { w.U32(1).U32(0).U32(10).U8(1) })
	if _, n := head(t, r); n != 2 {
		t.Fatalf("found %d games, want the 2 of the searched mode", n)
	}
	_, _ = r.Blob()
	_, _ = r.Blob()
	_, _ = r.U32()
	_, _ = r.U32()
	_, _ = r.U32()
	if first, _ := r.U8(); first != 1 {
		t.Fatalf("first result has mode %d", first)
	}
	if rank, _ := r.U8(); rank != 1 {
		t.Fatalf("best-ranked game is not first (rank %d)", rank)
	}
}
