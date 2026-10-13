package ui

import (
	"strings"
	"testing"
	"time"
)

// The whole point of budgeting columns: identifiers differ in length by an
// order of magnitude between clouds, so a table sized to its widest value
// gives most of the terminal to a string nobody reads.
func TestLayoutNeverExceedsTheBox(t *testing.T) {
	for _, inner := range []int{0, 30, 50, 60, 70, 80, 100, 140, 200} {
		lay := Resolve(inner)
		effective := inner
		if effective <= 0 {
			effective = 72
		}
		// A box too small for the minimum name may overflow; anything that
		// has room must fit exactly.
		if lay.Name > minNameWidth && lay.Width() > effective {
			t.Errorf("inner %d: row is %d wide", inner, lay.Width())
		}
		if lay.Name < minNameWidth {
			t.Errorf("inner %d: name column %d is below the minimum", inner, lay.Name)
		}
	}
}

func TestNarrowLayoutDropsContextColumns(t *testing.T) {
	if lay := Resolve(40); !lay.Narrow || lay.Cloud != 0 {
		t.Errorf("40 columns should be narrow, got %+v", lay)
	}
	if lay := Resolve(110); lay.Narrow || lay.Cloud == 0 {
		t.Errorf("110 columns should show context, got %+v", lay)
	}
}

// The name is the only column people scan, so it takes whatever the fixed
// columns leave rather than a fixed share.
func TestNameTakesTheSpareWidth(t *testing.T) {
	a, b := Resolve(100), Resolve(140)
	if b.Name-a.Name != 40 {
		t.Errorf("forty more columns gave the name %d more", b.Name-a.Name)
	}
	if a.Cloud != b.Cloud || a.Status != b.Status {
		t.Error("fixed columns should not grow with the box")
	}
}

// Truncation counts runes, not bytes: cutting mid-character renders as a
// replacement glyph.
func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		in     string
		width  int
		marker string
		want   string
	}{
		{"short", 10, "…", "short"},
		{"exactly-10", 10, "…", "exactly-10"},
		{"truncate-me", 6, "…", "trunc…"},
		{"truncate-me", 6, "~", "trunc~"},
		{"anything", 0, "…", ""},
		{"anything", 1, "…", "…"},
		{"héllo wörld", 7, "…", "héllo …"},
	} {
		if got := Truncate(tc.in, tc.width, tc.marker); got != tc.want {
			t.Errorf("Truncate(%q, %d, %q) = %q, want %q", tc.in, tc.width, tc.marker, got, tc.want)
		}
		if got := Truncate(tc.in, tc.width, tc.marker); len([]rune(got)) > tc.width {
			t.Errorf("Truncate(%q, %d) produced %d runes", tc.in, tc.width, len([]rune(got)))
		}
	}
}

// An OCID is over a hundred characters. It must not be allowed to set a
// column's width.
func TestPadClipsAnOCID(t *testing.T) {
	ocid := "ocid1.instance.oc1.us-ashburn-1." + strings.Repeat("a", 90)
	got := Pad(ocid, 20, "…")
	if len([]rune(got)) != 20 {
		t.Errorf("padded to %d runes, want 20", len([]rune(got)))
	}
}

func TestPadFillsShortValues(t *testing.T) {
	if got := Pad("ab", 5, "…"); got != "ab   " {
		t.Errorf("got %q", got)
	}
}

// An unnamed account is the ambient default; an empty column reads as missing
// data rather than an answer.
func TestShortAccountNamesTheDefault(t *testing.T) {
	if got := ShortAccount(""); got != "default" {
		t.Errorf("got %q", got)
	}
	if got := ShortAccount("prod"); got != "prod" {
		t.Errorf("got %q", got)
	}
}

// Truncating a string that already carries colour cannot count runes: an
// escape sequence is several runes that occupy no columns, so rune counting
// cuts far too early and can leave the terminal mid-sequence.
func TestTruncateVisibleCountsOnlyWhatIsDrawn(t *testing.T) {
	th := NewTheme(true)
	coloured := th.Dim("hello") + " " + th.Online("world")

	if got := truncateVisible(coloured, 20, "…"); got != coloured {
		t.Error("something that already fits must come back unchanged")
	}
	got := truncateVisible(coloured, 8, "…")
	if visibleLen(got) > 8 {
		t.Errorf("drew %d columns, want at most 8", visibleLen(got))
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Error("a cut string must close its colour, or it leaks into the next line")
	}
	// Rune counting would have cut inside the first escape sequence and
	// produced almost nothing visible.
	if visibleLen(got) < 4 {
		t.Errorf("cut far too early: %d columns drawn", visibleLen(got))
	}
}

// The relative time column explains why the default order is what it is, so
// it has to stay short enough to fit beside everything else.
func TestAgo(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		when time.Time
		want string
	}{
		{time.Time{}, "-"},
		{now.Add(-30 * time.Second), "now"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-2 * 24 * time.Hour), "2d"},
		{now.Add(-90 * 24 * time.Hour), "3mo"},
		{now.Add(time.Hour), "-"},
	} {
		got := Ago(tc.when, now)
		if got != tc.want {
			t.Errorf("Ago(%v) = %q, want %q", tc.when, got, tc.want)
		}
		if len([]rune(got)) > usedWidth {
			t.Errorf("%q does not fit the column", got)
		}
	}
}
