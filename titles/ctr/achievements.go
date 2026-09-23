package ctr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openpak/demonware/wire"
)

// The achievements engine (service 125) carries race results, Pit Stop refreshes and the
// Wumpa challenges (docs/protocol.md §8).

type event struct {
	name   string
	at     uint64 // unix seconds
	id     uint64
	values map[string]uint64
}

// receipt identifies an event independently of field order, so a retried report pays once.
func (e event) receipt() string {
	keys := make([]string, 0, len(e.values))
	for k := range e.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%d", e.name, e.at, e.id)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%d", k, e.values[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// parseEvents reads a reportEvents struct: field 2 is repeated event {1 name, 2 time,
// 3 repeated {1 key, 2 value}, 4 id}.
func parseEvents(sb []byte) ([]event, error) {
	top, err := wire.ParsePB(sb)
	if err != nil {
		return nil, err
	}
	var out []event
	for _, f := range top {
		if f.Num != 2 || f.Data == nil {
			continue
		}
		parts, err := wire.ParsePB(f.Data)
		if err != nil {
			return nil, err
		}
		e := event{values: map[string]uint64{}}
		for _, p := range parts {
			switch p.Num {
			case 1:
				e.name = string(p.Data)
			case 2:
				e.at = p.Value
			case 4:
				e.id = p.Value
			case 3:
				kv, err := wire.ParsePB(p.Data)
				if err != nil {
					return nil, err
				}
				var key string
				var val uint64
				for _, x := range kv {
					switch x.Num {
					case 1:
						key = string(x.Data)
					case 2:
						val = x.Value
					}
				}
				if key == "" || len(key) > 100 {
					return nil, errors.New("event value without a key")
				}
				e.values[key] = val
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// state is one achievement's progress as the game reads it.
type state struct {
	name                                                 string
	id, kind, progress, target, status, done, start, end uint64
}

const (
	statusActive    = 2
	statusCompleted = 3
)

func (s state) encode() []byte {
	var flag uint64
	if s.done != 0 {
		flag = 1
	}
	return wire.PB{}.Str(1, s.name).Varint(2, s.kind).Varint(3, s.progress).Varint(4, s.target).
		Varint(5, s.status).Varint(6, s.done).Varint(7, s.start).Varint(8, s.end).Varint(9, flag).
		Varint(11, s.id)
}

// day is the Pit Stop day containing now: it starts at 15:00 UTC.
func day(now time.Time) (name string, start, end uint64) {
	t := now.UTC().Add(-15 * time.Hour)
	s := time.Date(t.Year(), t.Month(), t.Day(), 15, 0, 0, 0, time.UTC)
	return s.Format(time.DateOnly), uint64(s.Unix()), uint64(s.Add(24 * time.Hour).Unix())
}

func inPeriod(e event, start, end uint64, now time.Time) bool {
	return e.at >= start && e.at < end && e.at <= uint64(now.Add(5*time.Minute).Unix())
}

// Pit Stop daily refresh: two a day.
const refreshName = "pitstop_daily_refresh"

func refreshState(w *wallet, now time.Time) state {
	d, start, end := day(now)
	s := state{name: refreshName, id: 255, kind: 1, target: 2, status: statusActive, start: start, end: end}
	if w.Refresh != nil && w.Refresh.Day == d {
		s.progress = min(w.Refresh.Count, s.target)
	}
	if s.progress >= s.target {
		s.status = statusCompleted
	}
	return s
}

// Window Shopping ("visit_pit_stop"): 25 coins once per active day of its 84-day cycle.
var visitChallenge = struct {
	name                 string
	id, kind, target     uint64
	reward               int64
	cycleStart, cycleLen int64 // unix seconds, days
	activeDays           map[int64]bool
}{name: "visit_pit_stop", id: 11, kind: 3, target: 1, reward: 25, cycleStart: 1585234800, cycleLen: 84,
	activeDays: days(0, 1, 4, 5, 6, 7, 8, 11, 12, 13, 14, 15, 18, 19, 20, 21, 22, 25, 26, 27, 28, 29,
		32, 33, 34, 35, 36, 39, 40, 41)}

func days(d ...int64) map[int64]bool {
	m := map[int64]bool{}
	for _, v := range d {
		m[v] = true
	}
	return m
}

func windowState(w *wallet, now time.Time) (state, bool) {
	ws := visitChallenge
	_, start, end := day(now)
	if int64(start) < ws.cycleStart {
		return state{}, false
	}
	n := (int64(start) - ws.cycleStart) / 86400 % ws.cycleLen
	s := state{name: ws.name, id: ws.id, kind: ws.kind, target: ws.target, status: statusActive, start: start, end: end}
	if done, ok := w.Challenges[fmt.Sprintf("%s@%d", ws.name, start)]; ok {
		s.progress, s.status, s.done = ws.target, statusCompleted, done
	}
	return s, ws.activeDays[n]
}

// raceCoins is what one end_race event pays, before it is recorded.
func raceCoins(w *wallet, e event, now time.Time) (int64, bool) {
	table, ok := raceRewards[e.values["Track"]]
	rank, secs, laps := e.values["Rank"], e.values["Time"], e.values["CompletedLap"]
	if !ok || rank < 1 || rank > 8 || secs == 0 || secs > 86400 || laps == 0 || laps > 100 || e.at == 0 {
		return 0, false
	}
	at := time.Unix(int64(e.at), 0).UTC()
	if at.After(now.Add(5*time.Minute)) || at.Before(now.Add(-7*24*time.Hour)) {
		return 0, false
	}
	amount := table[min(rank-1, 3)]
	if e.values["RaceMode"] == 15 {
		amount = amount * int64(min(laps, 8)) / 3
	}
	if e.values["Online"] != 0 {
		d := at.Format(time.DateOnly)
		if w.Online[d] < 1800 {
			amount *= 5
		}
		w.Online[d] += secs
	}
	if weekend(at) {
		amount *= 2
	}
	if e.values["RewardGoldenWumpa"] == 1 {
		amount += 200
	}
	return amount, true
}

// weekend is Friday 17:00 UTC to Monday 08:00 UTC.
func weekend(t time.Time) bool {
	t = t.UTC()
	h := t.Hour()
	switch t.Weekday() {
	case time.Friday:
		return h >= 17
	case time.Saturday, time.Sunday:
		return true
	case time.Monday:
		return h < 8
	}
	return false
}

// report applies a reportEvents batch: race rewards, refreshes, Window Shopping. It returns
// the coins credited and the achievement states that changed (for push 75).
func (s *Wallets) report(pid uint64, events []event, now time.Time) (int64, []state, error) {
	var credit int64
	var changed []state
	err := s.update(pid, func(w *wallet) error {
		credit, changed = 0, nil
		if w.Races == nil {
			w.Races = map[string]int64{}
		}
		if w.Online == nil {
			w.Online = map[string]uint64{}
		}
		if w.Challenges == nil {
			w.Challenges = map[string]uint64{}
		}
		w.prune(now)
		d, start, end := day(now)
		refreshed, visited := false, false
		for _, e := range events {
			switch e.name {
			case "end_race":
				r := e.receipt()
				if _, paid := w.Races[r]; paid {
					continue
				}
				if amount, ok := raceCoins(w, e, now); ok {
					w.Races[r] = now.Unix()
					w.Balance[coins] += amount
					credit += amount
				}
			case refreshName:
				if e.values["value"] != 1 || !inPeriod(e, start, end, now) {
					continue
				}
				if w.Refresh == nil || w.Refresh.Day != d {
					w.Refresh = &refresh{Day: d, Receipts: map[string]bool{}}
				}
				if r := e.receipt(); !w.Refresh.Receipts[r] && w.Refresh.Count < 2 {
					w.Refresh.Count++
					w.Refresh.Receipts[r] = true
				}
				refreshed = true
			case visitChallenge.name:
				st, active := windowState(w, now)
				if e.values["value"] != 1 || !active || st.status == statusCompleted || !inPeriod(e, st.start, st.end, now) {
					continue
				}
				w.Challenges[fmt.Sprintf("%s@%d", visitChallenge.name, st.start)] = e.at
				w.Balance[coins] += visitChallenge.reward
				credit += visitChallenge.reward
				visited = true
			}
		}
		if refreshed {
			changed = append(changed, refreshState(w, now))
		}
		if visited {
			st, _ := windowState(w, now)
			changed = append(changed, st)
		}
		return nil
	})
	return credit, changed, err
}

// wanted applies a getAchievementStates filter (2 kinds, 3 limit, 4 page token, 5 statuses,
// 6 names, 7 ids) to one state.
func wanted(filter []wire.Field, s state) bool {
	var kinds, statuses, names []bool
	for _, f := range filter {
		switch f.Num {
		case 2:
			kinds = append(kinds, f.Value == s.kind)
		case 3:
			if f.Value == 0 {
				return false
			}
		case 4:
			if len(f.Data) != 0 {
				return false // no second page
			}
		case 5:
			statuses = append(statuses, f.Value == s.status)
		case 6:
			names = append(names, string(f.Data) == s.name)
		case 7:
			return false
		}
	}
	match := func(l []bool) bool {
		if len(l) == 0 {
			return true
		}
		for _, v := range l {
			if v {
				return true
			}
		}
		return false
	}
	return match(kinds) && match(statuses) && match(names)
}
