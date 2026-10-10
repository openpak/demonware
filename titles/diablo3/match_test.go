package diablo3

import (
	"testing"

	"github.com/openpak/demonware/wire"
)

type d3game struct {
	level, act, quest, mode int32
	free                    uint32
	version, region, diff   int32
	option, open, flag      int32
}

func (g d3game) attrs() []byte {
	w := &wire.Writer{}
	w.I32(g.level).I32(g.act).I32(g.quest).Blob(make([]byte, 8)).Blob(make([]byte, 16)).I32(g.mode).U32(g.free).
		I32(g.version).I32(g.region).I32(g.diff).I32(g.option).I32(g.open).I32(g.flag)
	return w.B
}

func search(g d3game, party uint32, lo, hi int32) []byte {
	w := &wire.Writer{}
	w.I32(g.level).I32(lo).I32(hi).I32(g.act).I32(g.quest).I32(g.mode).U32(party).
		I32(g.version).I32(g.region).I32(g.diff).I32(g.option).I32(g.flag)
	return append(w.B, 0)
}

func TestMatch(t *testing.T) {
	host := d3game{level: 70, act: 0, quest: 87700, mode: 2, free: 3, version: 1441792, region: 2, diff: 6, option: 1, open: 1, flag: 0}
	me := host
	me.level = 68
	if _, ok, why := Match(queryActQuest, search(me, 1, 66, 72), host.attrs()); !ok {
		t.Fatalf("same settings refused: %s", why)
	}
	cases := []struct {
		name  string
		query uint32
		edit  func(*d3game)
		lo    int32
		ok    bool
		why   string
	}{
		{"hardcore vs softcore", queryAnyAct, func(g *d3game) { g.mode = 3 }, 66, false, "mode"},
		{"difficulty", queryAnyAct, func(g *d3game) { g.diff = 7 }, 66, false, "difficulty"},
		{"level out of range", queryAnyAct, nil, 71, false, "level"},
		{"other act, act query", queryAct, func(g *d3game) { g.act = 100 }, 66, false, "act"},
		{"other act, any-act query", queryAnyAct, func(g *d3game) { g.act = 100 }, 66, true, ""},
		{"other quest, act-only query", queryAct, func(g *d3game) { g.quest = 1 }, 66, true, ""},
		{"other quest, act+quest query", queryActQuest, func(g *d3game) { g.quest = 1 }, 66, false, "quest"},
		{"version", queryAnyAct, func(g *d3game) { g.version = 1 }, 66, false, "version"},
		{"flag", queryAnyAct, func(g *d3game) { g.flag = 1 }, 66, false, "flag"},
	}
	for _, tc := range cases {
		s := me
		if tc.edit != nil {
			tc.edit(&s)
		}
		_, ok, why := Match(tc.query, search(s, 1, tc.lo, 72), host.attrs())
		if ok != tc.ok || why != tc.why {
			t.Errorf("%s: ok=%v why=%q, want ok=%v why=%q", tc.name, ok, why, tc.ok, tc.why)
		}
	}
	full := host
	full.free = 1
	if _, ok, why := Match(queryAnyAct, search(me, 2, 66, 72), full.attrs()); ok || why != "room" {
		t.Errorf("party of two into one free slot: ok=%v why=%q", ok, why)
	}
	near, far := host, host
	far.level = 72
	rn, _, _ := Match(queryAnyAct, search(me, 1, 66, 72), near.attrs())
	rf, _, _ := Match(queryAnyAct, search(me, 1, 66, 72), far.attrs())
	if rn >= rf {
		t.Errorf("closer level should rank first: %d vs %d", rn, rf)
	}
	if _, ok, _ := Match(queryAnyAct, []byte{0x08, 1, 0, 0, 0, 0}, host.attrs()); !ok {
		t.Error("an unknown filter layout must not refuse games")
	}
}
