package diablo3

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	s, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Config()
	lines := strings.Split(strings.TrimSpace(cfg), "\n")
	if len(lines) != 6+len(events)+4 {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0] != `HeroPublishFrequencyMinutes "30"` || lines[len(lines)-1] != `UpdateVersion "1"` {
		t.Fatalf("order: %q .. %q", lines[0], lines[len(lines)-1])
	}
	// Season 37's theme is KanaiPowers; every other event is present and off.
	if !strings.Contains(cfg, "CommunityBuffKanaiPowers \"1\"\n") || !strings.Contains(cfg, "CommunityBuffDoubleGoblins \"0\"\n") {
		t.Fatal(cfg)
	}
	if got := s.Seasons(); got != "[Season 37]\nStart \"Wed, 01 Jan 2020 00:00:00 GMT\"\nEnd \"Tue, 01 Jan 2036 00:00:00 GMT\"\n" {
		t.Fatalf("%q", got)
	}
	if s.File("Blacklist.txt") == nil || s.File("update-1.cpk") != nil {
		t.Fatal("file lookup")
	}
}

func TestSettingsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d3.json")
	os.WriteFile(path, []byte(`{"season": 22, "events": ["DoubleGoblins"], "xp": "2.0", "theme": true,
		"season_end": "Mon, 01 Jan 2050 00:00:00 GMT", "diablo4_advertisement": true}`), 0o600)
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Config()
	for _, want := range []string{`CommunityBuffShadowClones "1"`, `CommunityBuffFourthKanaisCubeSlot "1"`,
		`CommunityBuffDoubleGoblins "1"`, `CommunityBuffXP "2.0"`, `EnableDiablo4Advertisement "1"`,
		`EnableCrossPlatformSaveMigration "1"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(s.Seasons(), "2038") {
		t.Fatal("a date past 2038 was not clamped")
	}

	for _, bad := range []string{`{"season_start": "Wed, 1 Jan 2020 00:00:00 GMT"}`, `{"events": ["Nope"]}`, `{`} {
		os.WriteFile(path, []byte(bad), 0o600)
		if _, err := Load(path); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if s, err := Load(filepath.Join(dir, "missing.json")); err != nil || s.Season != 37 {
		t.Fatal("a missing file is not the defaults")
	}
}
