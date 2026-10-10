package diablo3

import (
	"github.com/openpak/demonware/wire"
)

// Public-game matching (service 21 task 5), docs/protocol.md §7 "Quick Match". Positions in the
// game's attributes (after maxPlayers) and in a search's filters (after query, start, max):
const (
	aLevel      = 0  // i32, compared with the filter's level range
	aAct        = 1  // i32: 0, 100, 200, 300, 400 for Acts I-V, 3000 for Adventure Mode
	aQuest      = 2  // i32: the quest, -1 when none
	aMode       = 5  // i32: 0 normal, 1 hardcore, 2 seasonal, 3 seasonal hardcore, 4 the special game type
	aFreeSlots  = 6  // u32: players the game still takes
	aVersion    = 7  // i32
	aRegion     = 8  // i32
	aDifficulty = 9  // i32
	aOption     = 10 // i32
	aFlag       = 12 // i32, 0 or 1
	attrCount   = 13

	fLevel      = 0 // i32, the searcher's own
	fLevelMin   = 1 // i32
	fLevelMax   = 2 // i32
	fAct        = 3 // i32, -1 = any act
	fQuest      = 4 // i32, -1 = any quest
	fMode       = 5 // i32
	fPartySize  = 6 // u32
	fVersion    = 7 // i32
	fRegion     = 8 // i32
	fDifficulty = 9 // i32
	fOption     = 10
	fFlag       = 11
	filterCount = 12
)

// Query ids the client picks: 1 act and quest, 2 act only (Adventure Mode, or any quest),
// 3 any act.
const (
	queryActQuest = 1
	queryAct      = 2
	queryAnyAct   = 3
)

// Match keeps a public game for a search the way the client's own query asks: same mode
// (hardcore, seasonal), difficulty, version, region and option flags, a level inside the
// searcher's range, room for the searcher's party, and the act (and quest) the query names.
// Closest level first.
func Match(query uint32, filters, attrs []byte) (rank int, ok bool, why string) {
	f, okF := wire.Values(filters)
	a, okA := wire.Values(attrs)
	if !okF || !okA || len(f) < filterCount || len(a) < attrCount {
		// A layout this matcher was not written for: do not refuse a game on a guess.
		return 1 << 30, true, ""
	}
	eq := func(ai, fi int, name string) bool {
		if a[ai].N != f[fi].N {
			why = name
			return false
		}
		return true
	}
	if !eq(aMode, fMode, "mode") || !eq(aDifficulty, fDifficulty, "difficulty") ||
		!eq(aVersion, fVersion, "version") || !eq(aRegion, fRegion, "region") ||
		!eq(aOption, fOption, "option") || !eq(aFlag, fFlag, "flag") {
		return 0, false, why
	}
	if lvl := a[aLevel].N; lvl < f[fLevelMin].N || lvl > f[fLevelMax].N {
		return 0, false, "level"
	}
	if a[aFreeSlots].N < f[fPartySize].N {
		return 0, false, "room"
	}
	switch query {
	case queryActQuest:
		if !eq(aAct, fAct, "act") || !eq(aQuest, fQuest, "quest") {
			return 0, false, why
		}
	case queryAct:
		if !eq(aAct, fAct, "act") {
			return 0, false, why
		}
	}
	d := a[aLevel].N - f[fLevel].N
	if d < 0 {
		d = -d
	}
	return int(d), true, ""
}
