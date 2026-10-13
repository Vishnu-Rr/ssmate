package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// press drives the model the way a terminal would.
func press(m BrowserModel, keys ...string) BrowserModel {
	for _, k := range keys {
		var out tea.Model
		if len(k) == 1 && k != "/" && k != "?" {
			out, _ = m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		} else {
			out, _ = m.Update(tea.KeyPressMsg{Code: keyCodeFor(k)})
		}
		m = out.(BrowserModel)
	}
	return m
}

func keyCodeFor(k string) rune {
	switch k {
	case "enter":
		return tea.KeyEnter
	case "esc":
		return tea.KeyEscape
	case "up":
		return tea.KeyUp
	case "down":
		return tea.KeyDown
	case "tab":
		return tea.KeyTab
	case "backspace":
		return tea.KeyBackspace
	case "/":
		return '/'
	case "?":
		return '?'
	default:
		return rune(k[0])
	}
}

func browser() BrowserModel {
	m := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return out.(BrowserModel)
}

func TestNavigationStaysInsideTheList(t *testing.T) {
	m := browser()
	// Up from the top must not run off the start.
	m = press(m, "k", "k", "k")
	if got, _ := m.Selected(); got.DisplayName() != m.Visible()[0].DisplayName() {
		t.Errorf("cursor left the top, got %s", got.DisplayName())
	}
	// Down past the end must stop at the last row.
	for i := 0; i < 20; i++ {
		m = press(m, "j")
	}
	last := m.Visible()[len(m.Visible())-1]
	if got, _ := m.Selected(); got.ID != last.ID {
		t.Errorf("cursor left the bottom, got %s", got.DisplayName())
	}
	// g and G are the vim habits people bring with them.
	m = press(m, "g")
	if got, _ := m.Selected(); got.ID != m.Visible()[0].ID {
		t.Errorf("g should go to the first row, got %s", got.DisplayName())
	}
	m = press(m, "G")
	if got, _ := m.Selected(); got.ID != last.ID {
		t.Errorf("G should go to the last row, got %s", got.DisplayName())
	}
}

// Typing in search narrows the list, and backspace widens it again.
func TestSearchNarrowsAndBackspaceRestores(t *testing.T) {
	m := press(browser(), "/", "w", "e", "b")
	if len(m.Visible()) != 1 {
		t.Fatalf("searching web gave %v", shown(m.Visible()))
	}
	m = press(m, "backspace", "backspace", "backspace")
	if len(m.Visible()) != 4 {
		t.Errorf("clearing the search gave %v", shown(m.Visible()))
	}
}

// A search that matches nothing must not leave a stale selection pointing at
// a machine that is no longer on screen.
func TestEmptyResultHasNoSelection(t *testing.T) {
	m := press(browser(), "/", "z", "z", "z", "z")
	if len(m.Visible()) != 0 {
		t.Fatalf("expected no matches, got %v", shown(m.Visible()))
	}
	if _, ok := m.Selected(); ok {
		t.Error("nothing is visible, so nothing can be selected")
	}
	// Choosing with nothing selected must be a no-op, not a crash.
	m = press(m, "enter")
	if _, err := m.Result(); err == nil {
		t.Error("enter on an empty list should not choose anything")
	}
}

// The cloud filter is a key rather than a command prefix.
func TestCloudKeyCyclesAndClears(t *testing.T) {
	m := press(browser(), "c")
	for _, n := range m.Visible() {
		if n.Cloud != inventory.CloudAWS {
			t.Fatalf("after one press expected aws only, got %v", shown(m.Visible()))
		}
	}
	m = press(m, "c", "c", "c")
	if len(m.Visible()) != 4 {
		t.Errorf("cycling past the last cloud should show everything, got %v", shown(m.Visible()))
	}
}

// One key clears every narrowing, because getting back to the whole estate
// should not mean undoing four separate choices.
func TestClearAllFilters(t *testing.T) {
	m := press(browser(), "c", "r", "/", "a")
	m = press(m, "esc") // leave search
	m = press(m, "x")
	if len(m.Visible()) != 4 {
		t.Errorf("x should restore everything, got %v", shown(m.Visible()))
	}
}

// Actions that need a working machine are refused with an explanation;
// diagnosis, which exists for broken machines, is not.
func TestUnreachableMachineRefusesConnectButAllowsDoctor(t *testing.T) {
	m := browser()
	m.query.Sort = SortName
	m.refilter()
	// db-1 is the unreachable one in the fixture.
	for {
		got, _ := m.Selected()
		if got.DisplayName() == "db-1" {
			break
		}
		m = press(m, "j")
	}

	after := press(m, "enter")
	if _, err := after.Result(); err == nil {
		t.Error("connect to an unreachable machine should be refused")
	}
	if !strings.Contains(after.View().Content, "press d") {
		t.Error("the refusal should point at the diagnosis")
	}

	after = press(m, "d")
	choice, err := after.Result()
	if err != nil {
		t.Fatalf("doctor must work on an unreachable machine: %v", err)
	}
	if choice.Action != ActionDoctor || choice.Node.DisplayName() != "db-1" {
		t.Errorf("got %v on %s", choice.Action, choice.Node.DisplayName())
	}
}

// Bookmarking is naming, which is worth doing before a machine works.
func TestBookmarkAllowedOnUnreachable(t *testing.T) {
	m := browser()
	m.query = Query{Sort: SortName, Text: "db-1"}
	m.refilter()
	choice, err := press(m, "b").Result()
	if err != nil {
		t.Fatalf("bookmark should be allowed: %v", err)
	}
	if choice.Action != ActionBookmark {
		t.Errorf("got %v", choice.Action)
	}
}

func TestQuitCancels(t *testing.T) {
	if _, err := press(browser(), "q").Result(); err != ErrCancelled {
		t.Errorf("q should cancel, got %v", err)
	}
}

// Help has to be dismissible by any key: a help screen you must work out how
// to close is its own small insult.
func TestAnyKeyLeavesHelp(t *testing.T) {
	m := press(browser(), "?")
	if !strings.Contains(m.View().Content, "Narrow") {
		t.Fatal("? should show help")
	}
	m = press(m, "z")
	if strings.Contains(m.View().Content, "Narrow") {
		t.Error("any key should return to the list")
	}
}

// The detail pane must not push the list off the screen.
func TestDetailPaneKeepsRowsOnScreen(t *testing.T) {
	m := browser()
	before := m.rows()
	m = press(m, "i")
	after := m.rows()
	if after >= before {
		t.Errorf("rows %d -> %d, the pane should take space from the list", before, after)
	}
	if after < 1 {
		t.Error("the list must keep at least one row")
	}
	if !strings.Contains(m.View().Content, m.Visible()[0].ID) {
		t.Error("the pane should show the selected machine's identifier")
	}
}

// A machine that cannot be reached carries the reason; the pane is where
// there is room to say it.
func TestDetailPaneShowsTheHint(t *testing.T) {
	m := browser()
	m.query = Query{Text: "db-1"}
	m.refilter()
	m = press(m, "i")
	if !strings.Contains(m.View().Content, "no bastion in app-vcn") {
		t.Error("the pane should show why the machine cannot be reached")
	}
}

// A terminal too narrow for context columns must still show a usable list.
func TestNarrowTerminalDropsContextNotTheName(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 44, Height: 20})
	m = out.(BrowserModel)
	view := m.View().Content
	if !strings.Contains(view, "web-1") {
		t.Error("the name must survive a narrow terminal")
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 44 {
			t.Errorf("line overflows the terminal (%d cols): %q", len([]rune(line)), line)
		}
	}
}

// A terminal may not report its size before the first frame, and the list has
// to draw something rather than an empty screen.
func TestRendersBeforeAnyWindowSizeMessage(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	out := m.View().Content
	if !strings.Contains(out, "web-1") {
		t.Errorf("the first frame drew no machines:\n%q", out)
	}
}

// Some terminals report no size at all - under script, inside a multiplexer
// before attach, in CI. Honouring a zero would collapse the list to one row on
// a screen that is not that small.
func TestZeroSizedReportIsIgnored(t *testing.T) {
	m := browser() // sized 120x30
	rowsBefore := m.rows()
	out, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m = out.(BrowserModel)
	if m.rows() != rowsBefore {
		t.Errorf("rows %d -> %d, a zero report should change nothing", rowsBefore, m.rows())
	}
	if !strings.Contains(m.View().Content, "web-1") {
		t.Error("a zero-sized report blanked the list")
	}
	// A real resize still applies.
	out, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = out.(BrowserModel)
	if m.rows() >= rowsBefore {
		t.Errorf("a genuine resize should still take effect, rows = %d", m.rows())
	}
}

// A fuzzy search matches letters scattered through a name. Showing which ones
// is the difference between trusting the result and wondering why a row is
// there at all.
func TestSearchHighlightsTheMatchedCharacters(t *testing.T) {
	m := NewBrowser(nodes(), NewSymbols(false), NewTheme(true), nil)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	m = out.(BrowserModel)
	m = press(m, "/", "w", "b")

	if len(m.Visible()) == 0 {
		t.Fatal("wb should fuzzy-match web-1")
	}
	view := m.View().Content
	// Compare against the style the theme actually uses for a match, rather
	// than a hard-coded escape: an earlier version of this test checked for
	// the previous palette's code, skipped itself when that stopped
	// appearing, and so tested nothing for a while.
	th := NewTheme(true)
	// "wb" matches only web-1, which is the selected row, and on the selected
	// row a match is drawn on the accent bar with its own style.
	marked := th.MatchSelected("w")
	if marked == "w" || marked == th.Selected("w") {
		t.Fatal("the theme draws a match on the bar no differently from the bar")
	}
	if !strings.Contains(view, marked) {
		t.Errorf("the matched character is not picked out on the selected row")
	}
	// And on an ordinary row it uses the ordinary match style.
	m = press(m, "backspace", "backspace", "b") // "b" matches several rows
	if !strings.Contains(m.View().Content, th.Match("b")) {
		t.Error("a match on an unselected row is not picked out")
	}
	// Highlighting must not change how wide the row is.
	lay := Resolve(120)
	plain := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	if visibleLen(m.row(m.visible[0], lay, false)) != visibleLen(plain.row(Hit{Node: m.visible[0].Node}, lay, false)) {
		t.Error("a highlighted row is a different width from a plain one")
	}
}

// Matching a tag or every field searches text the row does not display, so
// there is nothing honest to underline.
func TestOnlyNameSearchesHighlight(t *testing.T) {
	hits := Apply(nodes(), Query{Text: "production", Field: FieldTag}, nil)
	if len(hits) == 0 {
		t.Fatal("expected a tag match")
	}
	for _, h := range hits {
		if len(h.Matched) != 0 {
			t.Errorf("%s carries match positions from a tag search", h.Node.DisplayName())
		}
	}
	named := Apply(nodes(), Query{Text: "web", Field: FieldName}, nil)
	if len(named) == 0 || len(named[0].Matched) == 0 {
		t.Error("a name search should report where it matched")
	}
}

// A list of five hundred machines with no sense of position cannot be
// navigated: every screen looks like every other.
func TestScrollbarTracksPosition(t *testing.T) {
	many := make([]inventory.Node, 200)
	for i := range many {
		many[i] = inventory.Node{
			Cloud: "aws", ID: fmt.Sprintf("i-%03d", i),
			Name: fmt.Sprintf("machine-%03d", i), Reachable: true, Status: "online",
		}
	}
	m := NewBrowser(many, NewSymbols(false), NewTheme(false), nil)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = out.(BrowserModel)

	rows := m.rows()
	atTop := m.scrollbar(0, rows)
	if atTop != m.sym.Thumb {
		t.Errorf("the thumb should be at the top, got %q", atTop)
	}
	m = press(m, "G")
	if got := m.scrollbar(rows-1, rows); got != m.sym.Thumb {
		t.Errorf("after G the thumb should be at the bottom, got %q", got)
	}
	// A list that fits needs no thumb at all.
	few := NewBrowser(nodes(), NewSymbols(false), NewTheme(false), nil)
	out, _ = few.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	few = out.(BrowserModel)
	if got := few.scrollbar(0, few.rows()); got != " " {
		t.Errorf("a list that fits should show no thumb, got %q", got)
	}
}

// An empty list should say what is narrowing it, so the way out is on screen
// rather than in the help.
func TestEmptyStateNamesTheFilters(t *testing.T) {
	m := browser()
	m = press(m, "c") // cloud aws
	m = press(m, "/", "z", "z", "z")
	view := m.View().Content
	for _, want := range []string{"Nothing matches", "zzz", "cloud aws", "x clears"} {
		if !strings.Contains(view, want) {
			t.Errorf("the empty state should mention %q", want)
		}
	}
}

// With colour off there is no selection bar, so the cursor has to be marked
// some other way - otherwise the selected row is invisible on exactly the
// terminal where a user most needs to see it.
func TestSelectionIsVisibleWithoutColour(t *testing.T) {
	m := browser() // colour off
	lay := Resolve(m.inner())
	sel := m.row(m.visible[0], lay, true)
	other := m.row(m.visible[1], lay, false)
	if !strings.HasPrefix(sel, ">") {
		t.Errorf("the selected row carries no cursor without colour: %q", sel)
	}
	if strings.HasPrefix(other, ">") {
		t.Error("an unselected row must not carry the cursor")
	}
	if visibleLen(sel) != visibleLen(other) {
		t.Error("the cursor must not change the row's width")
	}
}

// The bastion takes one line of the detail box whichever cloud names it. OCI
// keeps an OCID under "bastion" and the name under "bastion_name"; showing both
// would spend a line on a hundred characters of identifier.
func TestDetailShowsOneBastionLine(t *testing.T) {
	oci := inventory.Node{Detail: map[string]string{
		"bastion": "ocid1.bastion.oc1.us-ashburn-1.aaaaaaaa", "bastion_name": "app-bastion",
		"vcn_name": "app-vcn",
	}}
	got := detailKeys(oci)
	if len(got) != 2 || got[0] != "bastion_name" || got[1] != "vcn_name" {
		t.Errorf("oci detail keys = %v, want the name and the network", got)
	}
	azure := inventory.Node{Detail: map[string]string{
		"bastion": "hub-bastion", "bastion_via": "vnet-hub", "vnet": "vnet-spoke",
	}}
	if got := detailKeys(azure); got[0] != "bastion" || got[1] != "bastion_via" {
		t.Errorf("azure detail keys = %v, want the bastion and the peering", got)
	}
}
