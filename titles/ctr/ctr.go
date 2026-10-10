// Package ctr is Crash Team Racing Nitro-Fueled (Demonware title 5775, "lavender"): async
// matchmaking, the Pit Stop economy with persistent wallets, and the achievements engine
// that pays race rewards and challenges (docs/protocol.md §8). Friend sessions (138) and
// rich presence (68) are the shared services.
package ctr

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/openpak/demonware"
	"github.com/openpak/demonware/lobby"
	"github.com/openpak/demonware/redact"
	"github.com/openpak/demonware/wire"
)

const (
	svcMarketplace  = 80
	svcAchievements = 125
	svcAsyncMM      = 145

	pushAchievements = 75
	pushBalance      = 76

	marketContext = "octane_switch"
)

// Title is the running CTR state.
type Title struct {
	Wallets *Wallets
	mm      *matcher
	log     *slog.Logger
	now     func() time.Time
}

// New opens the wallets under dir. start is a new wallet's Wumpa Coins.
func New(dir string, start int64, log *slog.Logger) (*demonware.Title, *Title, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	t := &Title{Wallets: &Wallets{Dir: dir, Start: start}, mm: newMatcher(), log: log, now: time.Now}
	return &demonware.Title{
		Slug: "crash-team-racing", ID: 5775, Name: "Crash Team Racing Nitro-Fueled", AppID: "0100F9F00C696000",
		AuthHosts:  []string{"lavender-switch-auth3.prod.demonware.net"},
		LobbyHosts: []string{"lavender-switch-lobby.prod.demonware.net"},
		Handle:     t.Handle,
		Closed:     t.mm.closed,
	}, t, nil
}

// Counts reports matchmaking load for /health.
func (t *Title) Counts() (waiting, rooms int) { return t.mm.counts() }

func resp(b []byte) lobby.Response { return lobby.Response{Body: b} }

// Handle answers CTR's own services.
func (t *Title) Handle(c *lobby.Conn, r *lobby.Request) (lobby.Response, bool) {
	switch r.Service {
	case svcAsyncMM:
		return t.async(c, r), true
	case svcMarketplace:
		return t.market(c, r), true
	case svcAchievements:
		return t.achievements(c, r), true
	}
	return lobby.Response{}, false
}

func (t *Title) send(ns []notice) {
	for _, n := range ns {
		if err := n.to.Push(n.payload()); err != nil {
			n.to.Log().Info("ctr: push failed", "event", n.event, "err", redact.Err(err))
			continue
		}
		n.to.Log().Info("ctr: pushed", "event", n.event)
	}
}

func (t *Title) async(c *lobby.Conn, r *lobby.Request) lobby.Response {
	a := r.Args
	switch r.Task {
	case 2: // setPlayerInfo
		doc, err := a.String()
		if err == nil {
			err = t.mm.setInfo(c, doc)
		}
		if err != nil {
			c.Log().Info("ctr: player info rejected", "err", redact.Err(err))
		}
		return resp(wire.Empty(r.Task)) // the game's registration stops if this one fails
	case 3: // player token
		id := c.OnlineID()
		return resp(wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.U64(id); return 1 }))
	case 5, 6, 7, 17: // string results
		id := strconv.FormatUint(c.OnlineID(), 10)
		return resp(wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.String(id); return 1 }))
	case 4, 14:
		c.Log().Info("ctr: mm task", "task", r.Task)
		return resp(wire.Empty(r.Task))
	case 8: // cancel search
		id, err := a.U64()
		ok := err == nil && t.mm.withdraw(c, id)
		c.Log().Info("ctr: search cancelled", "search", redact.Tag(id), "found", ok)
		return resp(wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.Bool(ok); return 1 }))
	case 10: // lobby disbanded
		if id, err := a.U64(); err == nil {
			c.Log().Info("ctr: lobby left", "lobby", redact.Tag(id))
			t.mm.remove(c, id)
		}
		return resp(wire.Empty(r.Task))
	case 13: // lobby documents
		id, err := a.U64()
		h, b, ok := t.mm.docsFor(c, id)
		c.Log().Info("ctr: lobby documents", "lobby", redact.Tag(id), "found", ok)
		if err != nil || !ok {
			return resp(wire.Fail(wire.Failed))
		}
		return resp(wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.String(h).String(b); return 1 }))
	case 15: // sync lobby documents
		id, e1 := a.U64()
		version, e2 := a.U64()
		doc, e3 := a.String()
		ok, why := false, "unreadable arguments"
		var ns []notice
		if e1 == nil && e2 == nil && e3 == nil {
			ok, why, ns = t.mm.sync(c, id, version, doc)
		}
		if ok {
			c.Log().Info("ctr: sync", "lobby", redact.Tag(id), "version", version, "accepted", ok, "notices", len(ns))
		} else {
			c.Log().Info("ctr: sync", "lobby", redact.Tag(id), "version", version, "accepted", ok, "why", why)
		}
		return lobby.Response{
			Body: wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.Bool(ok); return 1 }),
			Then: func() { t.send(ns) },
		}
	case 18: // start search
		doc, err := a.String()
		if err != nil {
			return resp(wire.Fail(wire.Failed))
		}
		s, err := t.mm.parseSearch(c, doc)
		if err != nil {
			c.Log().Info("ctr: search rejected", "err", redact.Err(err))
			return resp(wire.Fail(wire.Failed))
		}
		return lobby.Response{
			Body: wire.Rows(r.Task, func(w *wire.Writer) uint32 { w.String(searchReply(s.id)); return 1 }),
			Then: func() {
				ns := t.mm.place(s)
				c.Log().Info("ctr: searching", "search", redact.Tag(s.id), "party", len(s.members), "notices", len(ns))
				t.send(ns)
			},
		}
	}
	c.Log().Info("ctr: async matchmaking task not implemented", "task", r.Task)
	return resp(wire.Empty(r.Task))
}

// structArg is the struct buffer argument of a struct task.
func structArg(r *lobby.Request) []byte {
	if r.Args.Next() != wire.TStruct {
		return nil
	}
	b, _ := r.Args.Struct()
	return b
}

func (t *Title) pushBalance(c *lobby.Conn) {
	w, err := t.Wallets.view(c.Player().PID)
	if err != nil {
		return
	}
	owner := wire.PB{}.Varint(1, c.OnlineID()).Str(2, "nintendo")
	entry := wire.PB{}.Bytes(1, owner).Varint(2, coins).Varint(3, 0xFFFFFFFF).Varint(4, 0).
		Signed(5, w.Balance[coins])
	if err := c.Push(wire.Push(pushBalance, func(pw *wire.Writer) { pw.Struct(wire.PB{}.Bytes(1, entry)) })); err != nil {
		c.Log().Info("ctr: balance push failed", "err", redact.Err(err))
	}
}

func (t *Title) market(c *lobby.Conn, r *lobby.Request) lobby.Response {
	pid := c.Player().PID
	switch r.Task {
	case 245: // balances
		limit := len(currencies)
		if fields, err := wire.ParsePB(structArg(r)); err == nil {
			for _, f := range fields {
				if f.Num == 2 && f.Value > 0 && int(f.Value) < limit {
					limit = int(f.Value)
				}
			}
		}
		w, err := t.Wallets.view(pid)
		if err != nil {
			c.Log().Error("ctr: wallet", "err", redact.Err(err))
			return resp(wire.Fail(wire.Failed))
		}
		var sb wire.PB
		for _, id := range currencies[:limit] {
			sb = sb.Bytes(1, wire.PB{}.Varint(1, id).Signed(2, w.Balance[id]).Varint(3, 0))
		}
		return resp(wire.StructReply(r.Task, sb))
	case 246: // eShop reconciliation: nothing is sold for money here
		return resp(wire.StructReply(r.Task, nil))
	case 165: // inventory
		ctx, e1 := r.Args.String()
		page, e2 := r.Args.U32()
		size, e3 := r.Args.U32()
		if e1 != nil || e2 != nil || e3 != nil || ctx != marketContext || size == 0 || size > 4096 {
			return resp(wire.Fail(wire.Failed))
		}
		w, err := t.Wallets.view(pid)
		if err != nil {
			return resp(wire.Fail(wire.Failed))
		}
		items := w.inventory()
		var pageItems []uint32
		if page > 0 {
			from := min(uint64(page-1)*uint64(size), uint64(len(items)))
			pageItems = items[from:min(from+uint64(size), uint64(len(items)))]
		}
		owner := c.OnlineID()
		return resp(wire.Rows(r.Task, func(w *wire.Writer) uint32 {
			for _, it := range pageItems {
				w.U64(owner).String("nintendo").U32(it).U32(1).U32(0).Blob(nil).U32(0).I64(0).U16(0).U32(0)
			}
			return uint32(len(pageItems))
		}))
	case 154: // purchase
		skus, ok := purchaseArgs(r.Args)
		if !ok {
			return resp(wire.Fail(wire.Failed))
		}
		debit, err := t.Wallets.Buy(pid, skus)
		if err != nil {
			c.Log().Info("ctr: purchase refused", "skus", skus, "err", redact.Err(err))
			return resp(wire.Fail(wire.Failed))
		}
		c.Log().Info("ctr: purchase", "skus", skus, "coins", debit)
		return lobby.Response{Body: wire.Empty(r.Task), Then: func() { t.pushBalance(c) }}
	}
	c.Log().Info("ctr: marketplace task not implemented", "task", r.Task)
	return resp(wire.Empty(r.Task))
}

// purchaseArgs reads 154's arguments: context, n SKUs, n quantities (all 1).
func purchaseArgs(a *wire.Reader) ([]uint32, bool) {
	if ctx, err := a.String(); err != nil || ctx != marketContext {
		return nil, false
	}
	n, err := a.U32()
	if err != nil || n == 0 || n > 255 {
		return nil, false
	}
	skus := make([]uint32, n)
	for i := range skus {
		if skus[i], err = a.U32(); err != nil {
			return nil, false
		}
	}
	if q, err := a.U32(); err != nil || q != n {
		return nil, false
	}
	for range n {
		if q, err := a.U32(); err != nil || q != 1 {
			return nil, false
		}
	}
	return skus, true
}

func (t *Title) achievements(c *lobby.Conn, r *lobby.Request) lobby.Response {
	pid := c.Player().PID
	now := t.now()
	switch r.Task {
	case 1: // reportEvents
		events, err := parseEvents(structArg(r))
		if err != nil {
			c.Log().Info("ctr: events rejected", "err", redact.Err(err))
			return resp(wire.Fail(wire.Failed))
		}
		credit, changed, err := t.Wallets.report(pid, events, now)
		if err != nil {
			c.Log().Error("ctr: wallet", "err", redact.Err(err))
			return resp(wire.Fail(wire.Failed))
		}
		if credit != 0 {
			c.Log().Info("ctr: coins credited", "coins", credit)
		}
		return lobby.Response{Body: wire.StructReply(r.Task, nil), Then: func() {
			if credit != 0 {
				t.pushBalance(c)
			}
			if len(changed) == 0 {
				return
			}
			var sb wire.PB
			for _, s := range changed {
				sb = sb.Bytes(1, s.encode())
			}
			if err := c.Push(wire.Push(pushAchievements, func(w *wire.Writer) { w.Struct(sb) })); err != nil {
				c.Log().Info("ctr: achievements push failed", "err", redact.Err(err))
			}
		}}
	case 3: // getAchievementStates
		w, err := t.Wallets.view(pid)
		if err != nil {
			return resp(wire.Fail(wire.Failed))
		}
		filter, _ := wire.ParsePB(structArg(r))
		var sb wire.PB
		if s := refreshState(w, now); wanted(filter, s) {
			sb = sb.Bytes(1, s.encode())
		}
		if s, active := windowState(w, now); active && wanted(filter, s) {
			sb = sb.Bytes(1, s.encode())
		}
		return resp(wire.StructReply(r.Task, sb.Str(2, "")))
	case 9: // getUserState
		return resp(wire.StructReply(r.Task, wire.PB{}.Str(1, "{}")))
	}
	return resp(wire.StructReply(r.Task, nil))
}
