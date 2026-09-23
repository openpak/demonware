package diablo3

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Settings drive the publisher files (docs/protocol.md §7). They are read from
// DEMONWARE_D3_SETTINGS (JSON) on every request, so an edit reaches players the next time they
// connect; a missing file means the defaults. Every field is optional.
type Settings struct {
	Season      int    `json:"season"`
	SeasonStart string `json:"season_start"`
	SeasonEnd   string `json:"season_end"`
	BuffStart   string `json:"buff_start"`
	BuffEnd     string `json:"buff_end"`

	// Theme switches on the served season's theme events. Events are extras on top.
	Theme  *bool    `json:"theme"`
	Events []string `json:"events"`

	XP            string `json:"xp"`
	GoldFind      string `json:"gold_find"`
	LegendaryFind string `json:"legendary_find"`

	HeroPublishMinutes   string `json:"hero_publish_minutes"`
	SaveMigration        *bool  `json:"cross_platform_save_migration"`
	SeasonalLeaderboards *bool  `json:"seasonal_leaderboards"`
	Diablo4Ad            *bool  `json:"diablo4_advertisement"`
	UpdateVersion        string `json:"update_version"`

	// Themes overrides or extends the season theme table below.
	Themes map[int][]string `json:"themes"`
}

// events is every community event the game knows, in the order Config.txt lists them.
var events = []string{
	"DoubleGoblins", "DoubleBountyBags", "RoyalGrandeur", "LegacyOfNightmares", "TriunesWill",
	"Pandemonium", "KanaiPowers", "TrialsOfTempests", "SeasonOnly", "ShadowClones",
	"FourthKanaisCubeSlot", "EtherealItems", "SoulShards", "SwarmRifts", "SanctifiedItems",
	"DarkAlchemy", "ParagonCap", "NestingPortals", "EasterEggWorld", "DoubleRiftKeystones",
	"DoubleBloodShards",
}

// themes lists the events each themed season switched on. Seasons 1-13 had none.
var themes = map[int][]string{
	14: {"DoubleGoblins"}, 15: {"DoubleBountyBags"}, 16: {"RoyalGrandeur"}, 17: {"LegacyOfNightmares"},
	18: {"TriunesWill"}, 19: {"Pandemonium"}, 20: {"KanaiPowers"}, 21: {"TrialsOfTempests"},
	22: {"ShadowClones", "FourthKanaisCubeSlot"}, 23: {}, 24: {"EtherealItems"}, 25: {"SoulShards"},
	26: {"SwarmRifts"}, 27: {"SanctifiedItems"}, 28: {"DarkAlchemy"}, 29: {"NestingPortals"},
	30: {"SoulShards"}, 31: {"KanaiPowers"}, 32: {"EtherealItems"},
	33: {"ShadowClones", "FourthKanaisCubeSlot"}, 34: {"SanctifiedItems"}, 35: {"Pandemonium"},
	36: {"SoulShards"}, 37: {"KanaiPowers"}, 38: {"EtherealItems"},
	39: {"ShadowClones", "FourthKanaisCubeSlot"},
}

const dateLayout = "Mon, 02 Jan 2006 15:04:05 GMT"

// lastDate is the latest date written: the game appears to keep these in 32 bits.
var lastDate = time.Date(2038, 1, 1, 0, 0, 0, 0, time.UTC)

func defaults() Settings {
	// One pointer per field: json.Unmarshal writes through an existing pointer.
	yes := func() *bool { v := true; return &v }
	no := func() *bool { v := false; return &v }
	return Settings{
		Season: 37, SeasonStart: "Wed, 01 Jan 2020 00:00:00 GMT", SeasonEnd: "Tue, 01 Jan 2036 00:00:00 GMT",
		BuffStart: "Sat, 16 Sep 2023 00:00:00 GMT", BuffEnd: "Wed, 01 Dec 2027 01:00:00 GMT",
		Theme: yes(), XP: "1.0", GoldFind: "1.0", LegendaryFind: "1.0",
		HeroPublishMinutes: "30", SaveMigration: yes(), SeasonalLeaderboards: yes(), Diablo4Ad: no(),
		UpdateVersion: "1",
	}
}

// Load reads path over the defaults. An empty path or a missing file is the defaults.
func Load(path string) (Settings, error) {
	s := defaults()
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return defaults(), fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range s.Events {
		if !Known(e) {
			return defaults(), fmt.Errorf("%s: unknown event %q", path, e)
		}
	}
	for _, d := range []*string{&s.SeasonStart, &s.SeasonEnd, &s.BuffStart, &s.BuffEnd} {
		t, err := time.Parse(dateLayout, *d)
		if err != nil {
			return defaults(), fmt.Errorf("%s: date %q: want %q with a two-digit day", path, *d, dateLayout)
		}
		if t.After(lastDate) {
			*d = lastDate.Format(dateLayout)
		}
	}
	return s, nil
}

func flag(b *bool) string {
	if b != nil && *b {
		return "1"
	}
	return "0"
}

// active is the set of events switched on: the season's theme plus the extras.
func (s Settings) active() map[string]bool {
	on := map[string]bool{}
	if s.Theme == nil || *s.Theme {
		list, ok := s.Themes[s.Season]
		if !ok {
			list = themes[s.Season]
		}
		for _, e := range list {
			on[e] = true
		}
	}
	for _, e := range s.Events {
		on[e] = true
	}
	return on
}

// Config is Config.txt.
func (s Settings) Config() string {
	var b strings.Builder
	kv := func(k, v string) { fmt.Fprintf(&b, "%s \"%s\"\n", k, v) }
	kv("HeroPublishFrequencyMinutes", s.HeroPublishMinutes)
	kv("EnableCrossPlatformSaveMigration", flag(s.SaveMigration))
	kv("SeasonalGlobalLeaderboardsEnabled", flag(s.SeasonalLeaderboards))
	kv("EnableDiablo4Advertisement", flag(s.Diablo4Ad))
	kv("CommunityBuffStart", s.BuffStart)
	kv("CommunityBuffEnd", s.BuffEnd)
	on := s.active()
	for _, e := range events {
		v := on[e]
		kv("CommunityBuff"+e, flag(&v))
	}
	kv("CommunityBuffLegendaryFind", s.LegendaryFind)
	kv("CommunityBuffGoldFind", s.GoldFind)
	kv("CommunityBuffXP", s.XP)
	kv("UpdateVersion", s.UpdateVersion)
	return b.String()
}

// Seasons is Seasons.txt.
func (s Settings) Seasons() string {
	return fmt.Sprintf("[Season %d]\nStart \"%s\"\nEnd \"%s\"\n", s.Season, s.SeasonStart, s.SeasonEnd)
}

// Blacklist is Blacklist.txt: both sections, empty.
func (s Settings) Blacklist() string { return "[GBID]\n\n[SNO]\n" }

// File returns a publisher file by the name the game asks for, or nil when it does not exist.
func (s Settings) File(name string) []byte {
	switch strings.ToLower(name) {
	case "config.txt":
		return []byte(s.Config())
	case "seasons.txt":
		return []byte(s.Seasons())
	case "blacklist.txt":
		return []byte(s.Blacklist())
	}
	return nil
}

// Known reports whether name is an event the game knows.
func Known(name string) bool { return slices.Contains(events, name) }
