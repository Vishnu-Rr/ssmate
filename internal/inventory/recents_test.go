package inventory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func TestScoreFavoursRecentUse(t *testing.T) {
	hot := &Recent{Count: 1, Last: base.Add(-10 * time.Minute)}
	cold := &Recent{Count: 1, Last: base.Add(-30 * 24 * time.Hour)}
	if hot.Score(base) <= cold.Score(base) {
		t.Fatal("a host used ten minutes ago must outrank one used a month ago")
	}
}

func TestScoreFavoursRepeatUseOverASingleRecentOne(t *testing.T) {
	// The whole reason for frecency rather than recency: the host you open
	// every day should beat one you opened once, an hour ago.
	daily := &Recent{Count: 40, Last: base.Add(-20 * time.Hour)}
	once := &Recent{Count: 1, Last: base.Add(-30 * time.Minute)}
	if daily.Score(base) <= once.Score(base) {
		t.Fatal("a daily host must outrank a single recent visit")
	}
}

func TestScoreOfUnusedIsZero(t *testing.T) {
	if (&Recent{}).Score(base) != 0 {
		t.Error("an unused entry must score zero")
	}
	var nilEntry *Recent
	if nilEntry.Score(base) != 0 {
		t.Error("a nil entry must score zero, not panic")
	}
}

func TestRecordAccumulates(t *testing.T) {
	r := &Recents{}
	r.Record("i-1", base)
	r.Record("i-1", base.Add(time.Minute))
	if r.Entries["i-1"].Count != 2 {
		t.Fatalf("count = %d, want 2", r.Entries["i-1"].Count)
	}
}

func TestSortPutsUsedFirstAndLeavesTheRestAlone(t *testing.T) {
	nodes := []Node{
		{ID: "i-a"}, {ID: "i-b"}, {ID: "i-c"}, {ID: "i-d"},
	}
	r := &Recents{Entries: map[string]*Recent{
		"i-c": {Count: 10, Last: base.Add(-time.Minute)},
		"i-a": {Count: 1, Last: base.Add(-time.Minute)},
	}}
	got := r.Sort(nodes, base)
	if got[0].ID != "i-c" || got[1].ID != "i-a" {
		t.Fatalf("used nodes wrong or misordered: %v %v", got[0].ID, got[1].ID)
	}
	// Unused entries must keep their original relative order, so the list
	// stays predictable.
	if got[2].ID != "i-b" || got[3].ID != "i-d" {
		t.Fatalf("unused nodes were reordered: %v %v", got[2].ID, got[3].ID)
	}
}

func TestSortIsANoOpWithNoHistory(t *testing.T) {
	nodes := []Node{{ID: "i-a"}, {ID: "i-b"}}
	for _, r := range []*Recents{nil, {}, {Entries: map[string]*Recent{}}} {
		got := r.Sort(nodes, base)
		if len(got) != 2 || got[0].ID != "i-a" {
			t.Fatalf("empty history should not reorder: %+v", got)
		}
	}
}

func TestRecentsSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recents.json")
	r := &Recents{}
	r.Record("i-1", base)
	r.Record("i-1", base)
	if err := r.Save(path, base); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := LoadRecents(path)
	if got.Entries["i-1"].Count != 2 {
		t.Fatalf("count after round trip = %d, want 2", got.Entries["i-1"].Count)
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	// Losing usage history must never block a connection.
	got := LoadRecents(filepath.Join(t.TempDir(), "absent.json"))
	if got == nil || len(got.Entries) != 0 {
		t.Fatal("a missing recents file should yield an empty, usable value")
	}
}

func TestLoadCorruptFileIsEmptyNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recents.json")
	if err := writeFile(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := LoadRecents(path); len(got.Entries) != 0 {
		t.Fatal("a corrupt recents file should yield an empty, usable value")
	}
}

func TestTrimBoundsTheFile(t *testing.T) {
	r := &Recents{Entries: map[string]*Recent{}}
	for i := 0; i < maxRecents+50; i++ {
		r.Record(string(rune('a'+i%26))+string(rune('0'+i/26)), base)
	}
	path := filepath.Join(t.TempDir(), "recents.json")
	if err := r.Save(path, base); err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) > maxRecents {
		t.Fatalf("kept %d entries, want at most %d", len(r.Entries), maxRecents)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
