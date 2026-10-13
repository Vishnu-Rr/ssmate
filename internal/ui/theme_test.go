package ui

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

func TestDisabledThemeEmitsNoEscapes(t *testing.T) {
	th := NewTheme(false)
	for name, fn := range map[string]func(string) string{
		"Name": th.Name, "Dim": th.Dim, "Selected": th.Selected, "Match": th.Match,
		"Online": th.Online, "Degraded": th.Degraded, "Offline": th.Offline, "Heading": th.Heading,
	} {
		got := fn("hello")
		if got != "hello" {
			t.Errorf("%s(%q) = %q with colour off; must be the identity", name, "hello", got)
		}
		if strings.Contains(got, "\x1b") {
			t.Errorf("%s emitted an ANSI escape with colour off", name)
		}
	}
	if th.Enabled() {
		t.Error("Enabled() true on a disabled theme")
	}
}

func TestStatusStyleCoversEveryStatusNodeCanReport(t *testing.T) {
	th := NewTheme(false)
	// Every value Node.Status can return must map to a style, or a node
	// silently renders unstyled and the colour carries no meaning.
	for _, s := range []string{"online", "agent lost", "unmanaged", "stopped", "terminated", "unknown"} {
		if th.StatusStyle(s) == nil {
			t.Errorf("no style for status %q", s)
		}
	}
}

func TestPickerViewStaysASCIIAndEscapeFreeWithColourOff(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	out := m.View().Content
	if strings.Contains(out, "\x1b") {
		t.Fatal("picker emitted ANSI escapes with colour disabled")
	}
	for _, r := range out {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q in ASCII mode", r)
		}
	}
}

func TestPickerViewDoesEmitColourWhenEnabled(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(true), NewTheme(true), nil)
	if !strings.Contains(m.View().Content, "\x1b") {
		t.Fatal("picker emitted no colour with colour enabled")
	}
}

func TestColumnsAlignRegardlessOfColour(t *testing.T) {
	// Padding must happen before styling. A padded verb applied to an
	// already-styled string counts the invisible escape bytes as width and
	// the columns drift apart.
	plainOut := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil).View().Content
	colourOut := NewBrowser(nodes(), NewSymbols(false), NewTheme(true), nil).View().Content

	strip := func(s string) string {
		var b strings.Builder
		in := false
		for _, r := range s {
			switch {
			case r == '\x1b':
				in = true
			case in && (r == 'm'):
				in = false
			case !in:
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	// The one intended difference: without colour the selected row carries a
	// ">" cursor where the colour view has a space, because there is no bar to
	// show the selection. Normalise it; every column must still line up.
	norm := func(s string) string { return strings.ReplaceAll(s, ">", " ") }
	if norm(strip(colourOut)) != norm(plainOut) {
		t.Errorf("stripping colour does not reproduce the plain layout:\nplain:\n%s\nstripped:\n%s",
			plainOut, strip(colourOut))
	}
}

func TestNoTwoMeaningsShareAColour(t *testing.T) {
	// Regression guard. "degraded" and "your search matched here" once used
	// the same amber, so a filtered list looked like a list of warnings.
	th := NewTheme(true)
	seen := map[string]string{}
	for name, fn := range map[string]func(string) string{
		"Dim": th.Dim, "Selected": th.Selected, "Match": th.Match,
		"Online": th.Online, "Degraded": th.Degraded, "Offline": th.Offline,
	} {
		code := fn("x")
		if prev, dup := seen[code]; dup {
			t.Errorf("%s and %s render identically; each colour must mean one thing", prev, name)
		}
		seen[code] = name
	}
}

// The unreachable states Azure and OCI introduced have to mean what they look
// like. A machine with no bastion in its network is healthy and has no way in,
// which is the same thing amber already meant for an agent that stopped
// reporting - not grey, which is for a state nothing can interpret.
func TestStatusStyleCoversEveryCloudsUnreachableStates(t *testing.T) {
	th := NewTheme(true)
	sample := "x"
	amber := th.Degraded(sample)
	grey := th.Dim(sample)
	if amber == grey {
		t.Fatal("this test cannot tell the two styles apart")
	}
	for _, status := range []string{"agent lost", "unmanaged", "no gateway", "no tunnel"} {
		if got := th.StatusStyle(status)(sample); got != amber {
			t.Errorf("%q should read as reachable-but-not-right", status)
		}
	}
	for _, status := range []string{"stopped", "terminated"} {
		if got := th.StatusStyle(status)(sample); got != th.Offline(sample) {
			t.Errorf("%q should read as not running", status)
		}
	}
	if got := th.StatusStyle("online")(sample); got != th.Online(sample) {
		t.Error("online should read as reachable")
	}
	// Anything a provider invents falls to grey, which is the signal that
	// nothing here knows what it means.
	if got := th.StatusStyle("something new")(sample); got != grey {
		t.Error("an unknown status should be grey")
	}
}

// Colour is applied after padding, so escape sequences never count towards a
// column's width and the table cannot drift.
func TestColourDoesNotChangeLayout(t *testing.T) {
	plain := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	colour := NewBrowser(nodes(), NewSymbols(false), NewTheme(true), nil)
	lay := Resolve(100)
	n := nodes()[0]
	if visibleWidth(plain.row(Hit{Node: n}, lay, false)) != visibleWidth(colour.row(Hit{Node: n}, lay, false)) {
		t.Error("a coloured row occupies a different width from a plain one")
	}
}

// visibleWidth counts runes a terminal would draw, ignoring escape sequences.
func visibleWidth(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm'):
			inEscape = false
		case !inEscape:
			n++
		}
	}
	return n
}

// The selected row is a solid bar. Every character of it has to sit on the
// accent: a single reset in the middle leaves a hole in the bar, which is
// exactly how the previous design went wrong.
func TestSelectedRowIsOneContinuousBar(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(true), NewTheme(true), nil)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = out.(BrowserModel)
	lay := Resolve(m.inner())
	row := m.row(m.visible[0], lay, true)

	// Walk the row, tracking whether the background is set, and fail on any
	// drawn character that is not on it.
	onBar, inEsc, esc := false, false, ""
	for _, r := range row {
		switch {
		case r == 0x1b:
			inEsc, esc = true, ""
		case inEsc:
			esc += string(r)
			if r == 'm' {
				inEsc = false
				switch {
				case strings.Contains(esc, "48;2;"), strings.Contains(esc, "48;5;"):
					onBar = true
				case esc == "[m", esc == "[0m":
					onBar = false
				}
			}
		default:
			if !onBar {
				t.Fatalf("a character of the selected row is off the bar: %q", row)
			}
		}
	}
}

// Colour off means colour off, everywhere.
func TestThemeIsInertWithoutColour(t *testing.T) {
	th := NewTheme(false)
	for name, f := range map[string]func(string) string{
		"Selected": th.Selected, "Chip": th.Chip, "Accent": th.Accent,
		"Match": th.Match, "Faint": th.Faint, "Key": th.Key,
	} {
		if got := f("x"); got != "x" {
			t.Errorf("%s altered its input with colour off: %q", name, got)
		}
	}
}
