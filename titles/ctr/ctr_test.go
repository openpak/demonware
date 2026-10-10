package ctr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/openpak/demonware/account"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/session"
	"github.com/openpak/demonware/wire"
)

func TestData(t *testing.T) {
	if len(catalog) < 1000 || len(raceRewards) < 30 {
		t.Fatalf("catalog %d SKUs, %d tracks", len(catalog), len(raceRewards))
	}
	for id, k := range catalog {
		for _, p := range k.parts {
			if _, ok := catalog[p]; !ok {
				t.Fatalf("bundle %d names unknown part %d", id, p)
			}
		}
	}
}

func wallets(t *testing.T, start int64) *Wallets {
	return &Wallets{Dir: t.TempDir(), Start: start}
}

// firstSKU finds a plain SKU and a bundle whose parts are plain SKUs.
func samples(t *testing.T) (plain uint32, bundle uint32) {
	for id, k := range catalog {
		if len(k.parts) == 0 && len(k.items) == 1 && k.price > 0 && plain == 0 {
			plain = id
		}
		if len(k.parts) >= 2 && bundle == 0 {
			bundle = id
		}
	}
	if plain == 0 || bundle == 0 {
		t.Fatal("no sample SKUs")
	}
	return
}

func TestPurchase(t *testing.T) {
	s := wallets(t, 100000)
	plain, bundle := samples(t)
	price := catalog[plain].price
	debit, err := s.Buy(7, []uint32{plain})
	if err != nil || debit != price {
		t.Fatal(debit, err)
	}
	// Owned: free the second time, and it persists.
	if debit, _ := s.Buy(7, []uint32{plain}); debit != 0 {
		t.Fatal("paid twice for an owned SKU", debit)
	}
	w, _ := s.view(7)
	if w.Balance[coins] != 100000-price || !w.owns(catalog[plain]) {
		t.Fatal("wallet not saved", w.Balance)
	}

	// A bundle costs its unowned parts.
	b := catalog[bundle]
	var want int64
	for _, p := range b.parts {
		want += catalog[p].price
	}
	s.Buy(8, []uint32{b.parts[0]})
	w, _ = s.view(8)
	if got := w.price(b); got != want-catalog[b.parts[0]].price {
		t.Fatalf("bundle price %d, want %d", got, want-catalog[b.parts[0]].price)
	}

	// Not enough coins: nothing changes.
	poor := wallets(t, 10)
	if _, err := poor.Buy(9, []uint32{plain}); !errors.Is(err, errFunds) {
		t.Fatal("bought without coins", err)
	}
	if w, _ := poor.view(9); w.Balance[coins] != 10 || len(w.Items) != 0 {
		t.Fatal("a refused purchase changed the wallet")
	}
	if _, err := poor.Buy(9, []uint32{999999999}); err == nil {
		t.Fatal("unknown SKU sold")
	}
}

type ev struct {
	name   string
	at     uint64
	values map[string]uint64
}

func eventsPB(evs ...ev) []byte {
	var sb wire.PB
	for _, e := range evs {
		one := wire.PB{}.Str(1, e.name).Varint(2, e.at)
		for k, v := range e.values {
			one = one.Bytes(3, wire.PB{}.Str(1, k).Varint(2, v))
		}
		sb = sb.Bytes(2, one)
	}
	return sb
}

func trackHash(t *testing.T) (uint64, [4]int64) {
	for h, c := range raceRewards {
		return h, c
	}
	t.Fatal("no tracks")
	return 0, [4]int64{}
}

func TestRaceRewards(t *testing.T) {
	s := wallets(t, 0)
	h, coinsBy := trackHash(t)
	// A Wednesday noon, not a weekend.
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	race := func(rank, online, golden uint64, at time.Time) ev {
		return ev{"end_race", uint64(at.Unix()), map[string]uint64{"Track": h, "Rank": rank, "Time": 120,
			"CompletedLap": 3, "Online": online, "RewardGoldenWumpa": golden}}
	}
	events, err := parseEvents(eventsPB(race(1, 0, 0, now)))
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	credit, _, _ := s.report(1, events, now)
	if credit != coinsBy[0] {
		t.Fatalf("1st place paid %d, want %d", credit, coinsBy[0])
	}
	// The same event again pays nothing.
	if credit, _, _ := s.report(1, events, now); credit != 0 {
		t.Fatal("a retried race paid again")
	}
	// 6th place is paid as 4th; online x5; golden +200.
	e, _ := parseEvents(eventsPB(race(6, 1, 1, now.Add(-time.Minute))))
	if credit, _, _ := s.report(1, e, now); credit != coinsBy[3]*5+200 {
		t.Fatalf("online race paid %d, want %d", credit, coinsBy[3]*5+200)
	}
	// Weekend doubles.
	sat := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	e, _ = parseEvents(eventsPB(race(1, 0, 0, sat)))
	if credit, _, _ := s.report(1, e, sat); credit != coinsBy[0]*2 {
		t.Fatalf("weekend race paid %d", credit)
	}
	// Too old, from the future, unknown track: nothing.
	for _, at := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(time.Hour)} {
		e, _ = parseEvents(eventsPB(race(1, 0, 0, at)))
		if credit, _, _ := s.report(1, e, now); credit != 0 {
			t.Fatal("a race out of window paid", at)
		}
	}
	for d, want := range map[time.Time]bool{
		time.Date(2026, 9, 25, 16, 59, 0, 0, time.UTC): false, time.Date(2026, 9, 25, 17, 0, 0, 0, time.UTC): true,
		time.Date(2026, 9, 28, 7, 59, 0, 0, time.UTC): true, time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC): false,
	} {
		if weekend(d) != want {
			t.Errorf("weekend(%s) = %v", d, !want)
		}
	}
}

func TestPitStopRefreshAndWindowShopping(t *testing.T) {
	s := wallets(t, 0)
	now := time.Date(2026, 9, 23, 16, 0, 0, 0, time.UTC) // after the 15:00 UTC day start
	refresh := func(i uint64) ev { return ev{refreshName, uint64(now.Unix()) - i, map[string]uint64{"value": 1}} }
	e, _ := parseEvents(eventsPB(refresh(1), refresh(2), refresh(3)))
	_, changed, err := s.report(1, e, now)
	if err != nil || len(changed) != 1 || changed[0].progress != 2 || changed[0].status != statusCompleted {
		t.Fatalf("refresh state %+v %v", changed, err)
	}
	w, _ := s.view(1)
	if st := refreshState(w, now.Add(24*time.Hour)); st.progress != 0 {
		t.Fatal("refresh count carried into the next day")
	}

	// Find an active Window Shopping day and a quiet one.
	var active, quiet time.Time
	for d := now; active.IsZero() || quiet.IsZero(); d = d.Add(24 * time.Hour) {
		if _, on := windowState(w, d); on && active.IsZero() {
			active = d
		} else if !on && quiet.IsZero() {
			quiet = d
		}
	}
	visit := func(at time.Time) []event {
		e, _ := parseEvents(eventsPB(ev{"visit_pit_stop", uint64(at.Unix()), map[string]uint64{"value": 1}}))
		return e
	}
	if credit, _, _ := s.report(1, visit(active), active); credit != 25 {
		t.Fatal("Window Shopping paid", credit)
	}
	if credit, _, _ := s.report(1, visit(active.Add(time.Minute)), active.Add(time.Minute)); credit != 0 {
		t.Fatal("Window Shopping paid twice in one day")
	}
	if credit, _, _ := s.report(1, visit(quiet), quiet); credit != 0 {
		t.Fatal("Window Shopping paid on an inactive day")
	}
	w, _ = s.view(1)
	if st, _ := windowState(w, active); st.status != statusCompleted || st.done == 0 {
		t.Fatalf("completion not persisted: %+v", st)
	}
}

// ---- async matchmaking ----

type mmFixture struct {
	lob *lobby.Server
	m   *matcher
}

func (f *mmFixture) player(t *testing.T, id uint64) *lobby.Conn {
	c := f.lob.Fake(&session.Session{Title: 5775, Expires: time.Now().Add(time.Hour),
		Player: account.Player{PID: id, BaasUserID: strconv.FormatUint(id, 16)}}, "198.51.100."+strconv.FormatUint(id, 10))
	b64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	doc := fmt.Sprintf(`{"listen_server":{"local_address":%q,"security_id":%q,"security_key":%q,"nat":1}}`, b64(67), b64(8), b64(16))
	if err := f.m.setInfo(c, doc); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *mmFixture) search(t *testing.T, c *lobby.Conn, members []uint64, lo, hi int, mode string) (*search, []notice) {
	raw, _ := json.Marshal(map[string]any{"members": members, "lobby_slots": map[string]int{"min": lo, "max": hi},
		"ruleset_payload": map[string]any{"filter": map[string]any{"mode": mode}}})
	s, err := f.m.parseSearch(c, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s, f.m.place(s)
}

func hostDoc(open bool, states map[uint64]int, version uint64) string {
	ps := map[string]int{}
	for id, st := range states {
		ps[strconv.FormatUint(id, 10)] = st
	}
	b, _ := json.Marshal(map[string]any{"update_id": version, "lobby_open": open, "lobby_open_for_pres_join": open,
		"player_state": ps, "listen_server": map[string]any{}, "team_balance": map[string]any{},
		"ruleset_payload": map[string]any{}, "attachment": nil})
	return string(b)
}

func TestAsyncMatchmaking(t *testing.T) {
	f := &mmFixture{lob: &lobby.Server{Log: slog.New(slog.DiscardHandler)}, m: newMatcher()}
	a, b, c, d := f.player(t, 1), f.player(t, 2), f.player(t, 3), f.player(t, 4)

	if _, err := f.m.parseSearch(a, `{"members":[2],"lobby_slots":{"min":2,"max":8},"ruleset_payload":{"filter":{"x":1}}}`); err == nil {
		t.Fatal("a search for someone else was accepted")
	}

	_, ns := f.search(t, a, []uint64{1}, 2, 8, "race")
	if ns != nil {
		t.Fatal("a lone search made a lobby")
	}
	_, ns = f.search(t, d, []uint64{4}, 2, 8, "battle")
	if ns != nil {
		t.Fatal("different filters matched")
	}
	sb, ns := f.search(t, b, []uint64{2}, 2, 8, "race")
	if len(ns) != 1 || ns[0].event != evCreate || ns[0].to != a {
		t.Fatalf("no lobby for two compatible searches: %+v", ns)
	}
	lobbyID, version := ns[0].ids[1], ns[0].ids[2]

	// The guest is told only after the host syncs, and only with the current version.
	if ok, _, _ := f.m.sync(a, lobbyID, version+1, hostDoc(true, map[uint64]int{1: 1}, version)); ok {
		t.Fatal("stale version accepted")
	}
	if ok, _, _ := f.m.sync(b, lobbyID, version, hostDoc(true, nil, version)); ok {
		t.Fatal("a guest synced the host doc")
	}
	ok, _, ns := f.m.sync(a, lobbyID, version, hostDoc(true, map[uint64]int{1: 1}, version))
	if !ok || len(ns) != 1 || ns[0].event != evJoin || ns[0].to != b || ns[0].ids[0] != sb.id {
		t.Fatalf("guest not told: %v %+v", ok, ns)
	}
	var backend map[string]any
	json.Unmarshal([]byte(ns[0].backend), &backend)
	if players := backend["players"].(map[string]any); len(players) != 2 {
		t.Fatalf("backend doc players %v", players)
	}
	if h, _, ok := f.m.docsFor(b, lobbyID); !ok || h == "" {
		t.Fatal("guest cannot read the documents")
	}
	if _, _, ok := f.m.docsFor(d, lobbyID); ok {
		t.Fatal("an outsider read the documents")
	}

	// A third compatible player joins the open lobby: the host is asked for a new doc.
	_, ns = f.search(t, c, []uint64{3}, 2, 8, "race")
	if len(ns) != 1 || ns[0].event != evUpdate || ns[0].to != a || ns[0].ids[1] != version+1 {
		t.Fatalf("backfill: %+v", ns)
	}
	version++
	// The host reports player 3 timed out: the seat is released.
	ok, _, _ = f.m.sync(a, lobbyID, version, hostDoc(true, map[uint64]int{1: 1, 2: 2, 3: 3}, version))
	r := f.m.rooms[lobbyID]
	if !ok || r.seated() != 2 {
		t.Fatalf("failed join still holds a seat: %d", r.seated())
	}
	// A closed lobby takes nobody.
	f.m.sync(a, lobbyID, version, hostDoc(false, map[uint64]int{1: 1, 2: 2}, version))
	e := f.player(t, 5)
	if _, ns = f.search(t, e, []uint64{5}, 2, 8, "race"); ns != nil {
		t.Fatalf("a closed lobby took a player: %+v", ns)
	}
	// The host leaving ends the lobby.
	f.m.closed(a)
	if f.m.rooms[lobbyID] != nil {
		t.Fatal("lobby survived its host")
	}
	if f.m.withdraw(e, 999) {
		t.Fatal("cancelled a search that does not exist")
	}
}

func TestPartySearch(t *testing.T) {
	f := &mmFixture{lob: &lobby.Server{Log: slog.New(slog.DiscardHandler)}, m: newMatcher()}
	leader, solo := f.player(t, 1), f.player(t, 3)
	// A party of three with room for three only, and a solo: they do not fit together.
	f.search(t, leader, []uint64{1, 2, 9}, 2, 3, "race")
	if _, ns := f.search(t, solo, []uint64{3}, 2, 8, "race"); ns != nil {
		t.Fatal("a fourth seat was given in a lobby of three")
	}
	// A party that fills the minimum on its own makes a lobby.
	g := &mmFixture{lob: f.lob, m: newMatcher()}
	p := g.player(t, 6)
	if _, ns := g.search(t, p, []uint64{6, 7}, 2, 4, "race"); len(ns) != 1 || ns[0].event != evCreate {
		t.Fatal("a party of two did not get a lobby")
	}
}
