package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// The screen, top to bottom:
//
//	  █▀▀ █▀▀ █▀▄▀█ ▄▀█ ▀█▀ █▀▀   42 machines
//	  ▄▄█ ▄▄█ █ ▀ █ █▀█  █  ██▄   aws 354 · azure 118 · oci 36
//
//	  ╭──────────────────────────────────────────────────────╮
//	  │ ⌕  search machines                        42 of 42 │
//	  ╰──────────────────────────────────────────────────────╯
//	  ╭──────────────────────────────────────────────────────╮
//	  │   MACHINE        CLOUD  REGION  ...  VIA  STATUS USED │
//	  │   ────────────────────────────────────────────────── │
//	  │ ● web-1          aws    eu-west-2 ... ssm  online  2h ┃│
//	  │ ○ db-1           oci    ashburn   ... -    no gate  - ││
//	  ╰──────────────────────────────────────────────────────╯
//	   enter connect  f forward  d doctor  / search  ? help    1/42
//
// The search box and the table are two boxes, and whichever one your keys are
// going to has its border in the accent. That is the whole focus model: you
// can see where typing will land without being told.

// Page margins. The screen breathes: a list hard against the terminal's edge
// reads as output, one set in from it reads as an application.
const (
	padX = 2
	padY = 1
)

// logoMinHeight is the height below which the wordmark is dropped. It costs
// three lines, and on a short terminal three lines is three machines.
const logoMinHeight = 26

// detailRows is the content height of the detail box.
const detailRows = 4

// wordmark is the tool's name in a two-line block face.
var wordmark = [2]string{
	"█▀▀ █▀▀ █▀▄▀█ ▄▀█ ▀█▀ █▀▀",
	"▄▄█ ▄▄█ █ ▀ █ █▀█  █  ██▄",
}

// boxWidth is the outer width of every box on the page.
func (m BrowserModel) boxWidth() int {
	return maxInt(20, m.width-padX*2)
}

// inner is the content width inside a box: its border and one column of
// padding on each side.
func (m BrowserModel) inner() int {
	return m.boxWidth() - 4
}

func (m BrowserModel) showLogo() bool { return m.height >= logoMinHeight }

// rows is how many machines fit in the table.
func (m BrowserModel) rows() int {
	chrome := padY + // top margin
		3 + // search box
		2 + 2 + // table border and its heading plus rule
		1 // footer
	if m.showLogo() {
		chrome += 3 // two lines of wordmark and a gap
	}
	if m.detail {
		chrome += detailRows + 2
	}
	return maxInt(1, m.height-chrome)
}

func (m BrowserModel) View() tea.View {
	var body string
	if m.mode == modeHelp {
		body = m.renderHelp()
	} else {
		parts := []string{}
		if m.showLogo() {
			parts = append(parts, m.renderLogo(), "")
		}
		parts = append(parts, m.renderSearch(), m.renderTable())
		if m.detail {
			parts = append(parts, m.renderDetail())
		}
		parts = append(parts, m.renderFooter())
		body = lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	page := lipgloss.NewStyle().Padding(padY, padX, 0, padX).Render(body)
	v := tea.NewView(page)
	// The list takes the whole screen and gives it back on exit, the way an
	// editor does. Drawn inline it scrolls your history away and leaves a
	// copy of itself behind in the scrollback.
	v.AltScreen = true
	return v
}

// renderLogo sets the wordmark beside the shape of the estate, so the two
// things you want before reading any row share the same two lines.
func (m BrowserModel) renderLogo() string {
	c := Summarise(m.all)
	clouds := make([]string, 0, len(c.ByCloud))
	for cloud := range c.ByCloud {
		clouds = append(clouds, cloud)
	}
	sort.Strings(clouds)
	byCloud := make([]string, 0, len(clouds))
	for _, cloud := range clouds {
		byCloud = append(byCloud, m.theme.Heading(fmt.Sprint(c.ByCloud[cloud]))+" "+m.theme.Dim(cloud))
	}
	sep := m.theme.Faint(" " + m.sym.Bullet + " ")

	top := m.theme.Heading(fmt.Sprintf("%d", c.Total)) + m.theme.Dim(" machines  ") +
		m.theme.Online(fmt.Sprintf("%d", c.Reachable)) + m.theme.Dim(" reachable")
	bottom := strings.Join(byCloud, sep)

	mark := wordmark
	if m.sym.Online != "●" {
		// A terminal that cannot draw block elements gets the name plainly
		// rather than a smear of replacement characters.
		mark = [2]string{"ssmate", ""}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.theme.Accent(mark[0]+"\n"+mark[1]),
		"   ",
		top+"\n"+bottom,
	)
}

// box draws content in a rounded frame whose colour says whether it has focus.
func (m BrowserModel) box(content string, focused bool, height int) string {
	style := m.theme.BorderBlur
	if focused {
		style = m.theme.BorderFocus
	}
	style = style.Border(m.sym.Frame).Width(m.boxWidth()).Padding(0, 1)
	if height > 0 {
		style = style.Height(height + 2)
	}
	return style.Render(content)
}

// renderSearch draws the search box. It is always on screen, focused or not,
// because a filter you cannot see is a filter you forget is on.
func (m BrowserModel) renderSearch() string {
	searching := m.mode == modeSearch
	icon := m.theme.Dim(m.sym.Search)
	if searching {
		icon = m.theme.Accent(m.sym.Search)
	}

	var field string
	switch {
	case searching:
		field = m.theme.Heading(m.query.Text) + m.theme.Accent("▏")
	case m.query.Text != "":
		field = m.query.Text
	default:
		field = m.theme.Dim("press / to search")
	}

	// What the search matches is named because tab silently changes it, and
	// an unlabelled filter that stops matching looks like a broken search.
	var right []string
	if searching || m.query.Text != "" {
		right = append(right, m.theme.Dim("by "+m.query.Field.String()))
	}
	for _, chip := range m.filterChips() {
		right = append(right, m.theme.Accent(chip))
	}
	right = append(right, m.theme.Heading(fmt.Sprint(len(m.visible)))+m.theme.Dim(fmt.Sprintf(" of %d", len(m.all))))
	tail := strings.Join(right, m.theme.Faint("  "+m.sym.Bullet+"  "))

	left := icon + "  " + field
	gap := m.inner() - lipgloss.Width(left) - lipgloss.Width(tail)
	line := left + strings.Repeat(" ", maxInt(1, gap)) + tail
	if gap < 1 {
		line = truncateVisible(left, m.inner(), m.sym.Ellipsis)
	}
	return m.box(line, searching, 1)
}

// filterChips names the narrowing in force, so a short list is never taken
// for a small estate.
func (m BrowserModel) filterChips() []string {
	var out []string
	if m.query.Cloud != "" {
		out = append(out, m.query.Cloud+" only")
	}
	if m.query.ReachableOnly {
		out = append(out, "reachable only")
	}
	if m.query.Sort != SortRecent {
		out = append(out, "by "+m.query.Sort.String())
	}
	return out
}

// renderTable draws the machines.
func (m BrowserModel) renderTable() string {
	lay := Resolve(m.inner())
	lines := []string{m.columnHeads(lay), m.theme.Faint(strings.Repeat(m.sym.Rule, m.inner()))}
	lines = append(lines, m.renderRows(lay)...)
	return m.box(strings.Join(lines, "\n"), m.mode != modeSearch, 0)
}

// columnHeads names what each column holds. The facts in this table look
// alike at a glance - a cloud, a region, an account and a status are all short
// lowercase words - so without headings the table is a guessing game.
func (m BrowserModel) columnHeads(lay Layout) string {
	cells := []string{Pad("MACHINE", lay.Name, "")}
	if !lay.Narrow {
		cells = append(cells, Pad("CLOUD", lay.Cloud, ""), Pad("REGION", lay.Region, ""),
			Pad("ACCOUNT", lay.Account, ""), Pad("VIA", lay.Via, ""))
	}
	cells = append(cells, Pad("STATUS", lay.Status, ""), Pad("USED", lay.Used, ""))
	return m.theme.Dim(strings.Repeat(" ", lead) + strings.Join(cells, strings.Repeat(" ", gutter)))
}

func (m BrowserModel) renderRows(lay Layout) []string {
	rows := m.rows()
	if len(m.visible) == 0 {
		out := m.nothingMatches()
		for len(out) < rows {
			out = append(out, "")
		}
		return out[:rows]
	}
	out := make([]string, 0, rows)
	end := minInt(len(m.visible), m.top+rows)
	for i := m.top; i < end; i++ {
		sel := i == m.cursor
		gap := " "
		if sel {
			// The bar runs right up to the scrollbar; stopping a column short
			// of it looks like the row was measured wrong.
			gap = m.theme.Selected(" ")
		}
		out = append(out, m.row(m.visible[i], lay, sel)+gap+m.scrollbar(i-m.top, rows))
	}
	for len(out) < rows {
		out = append(out, strings.Repeat(" ", lay.Width()-1)+m.scrollbar(len(out), rows))
	}
	return out
}

// row draws one machine.
//
// Colour is confined to two places: the marker and the status word, which say
// the same thing, and the name on the selected row. Everything else is grey,
// so the eye runs down the names and the colour of the markers and nowhere
// else. The selected row is a solid bar in the accent - found at a glance,
// rather than hunted for on a list hundreds long.
func (m BrowserModel) row(h Hit, lay Layout, selected bool) string {
	n := h.Node
	marker := m.sym.Offline
	if n.Reachable {
		marker = m.sym.Online
	}
	status := m.theme.StatusStyle(n.Status)

	plainCells := []string{Pad(n.DisplayName(), lay.Name, m.sym.Ellipsis)}
	if !lay.Narrow {
		plainCells = append(plainCells,
			Pad(ShortCloud(n.Cloud), lay.Cloud, m.sym.Ellipsis),
			Pad(n.Region, lay.Region, m.sym.Ellipsis),
			Pad(ShortAccount(n.Account), lay.Account, m.sym.Ellipsis),
			Pad(viaOf(n), lay.Via, m.sym.Ellipsis))
	}
	plainCells = append(plainCells,
		Pad(n.Status, lay.Status, m.sym.Ellipsis),
		Pad(Ago(m.lastUsed(n.ID), timeNow()), lay.Used, ""))
	gap := strings.Repeat(" ", gutter)

	if selected {
		// On the bar every cell is drawn in one style, so there are no
		// nested resets to punch holes in the background. The marker keeps
		// its own colour: on the selected row it is the only status cue
		// left, and it reads clearly against the accent.
		name := m.highlight(n.DisplayName(), h.Matched, lay.Name, true)
		rest := strings.Join(plainCells[1:], gap)
		// Without colour there is no bar, so the cursor needs a mark of its
		// own - otherwise the selected row is simply invisible, which is
		// exactly the terminal where it matters most.
		cursor := " "
		if !m.theme.Enabled() {
			cursor = ">"
		}
		return m.theme.Selected(cursor) + m.markerOnBar(marker, status) + m.theme.Selected(" ") +
			name + m.theme.Selected(gap+rest)
	}

	cells := []string{m.highlight(n.DisplayName(), h.Matched, lay.Name, false)}
	if !lay.Narrow {
		for _, c := range plainCells[1:5] {
			cells = append(cells, m.theme.Dim(c))
		}
	}
	cells = append(cells, status(plainCells[len(plainCells)-2]), m.theme.Dim(plainCells[len(plainCells)-1]))
	return " " + status(marker) + " " + strings.Join(cells, gap)
}

// markerOnBar draws the reachability marker on the selected row's background.
func (m BrowserModel) markerOnBar(marker string, status func(string) string) string {
	if !m.theme.Enabled() {
		return marker
	}
	return lipgloss.NewStyle().Background(lipgloss.Color(accent)).Render(status(marker))
}

// highlight draws a name with the characters the search matched picked out.
//
// A fuzzy search matches letters scattered through a name - "strm" finds
// production-storm - and a list that does not show which ones is a list you
// squint at, wondering why a row is there. Padding happens first, so a
// highlight can never change a column's width, and runs of one style are
// drawn together rather than one escape sequence per character.
func (m BrowserModel) highlight(name string, matched []int, width int, selected bool) string {
	base, hitStyle := m.theme.Name, m.theme.Match
	if selected {
		base, hitStyle = m.theme.Selected, m.theme.MatchSelected
	}
	padded := []rune(Pad(name, width, m.sym.Ellipsis))
	if len(matched) == 0 {
		return base(string(padded))
	}
	hit := make(map[int]bool, len(matched))
	for _, i := range matched {
		hit[i] = true
	}
	var b strings.Builder
	for i := 0; i < len(padded); {
		on := hit[i]
		j := i
		for j < len(padded) && hit[j] == on {
			j++
		}
		if on {
			b.WriteString(hitStyle(string(padded[i:j])))
		} else {
			b.WriteString(base(string(padded[i:j])))
		}
		i = j
	}
	return b.String()
}

// scrollbar draws one cell of the scrollbar beside the given visible row.
//
// A list of five hundred machines with no sense of position cannot be
// navigated: every screen looks like every other. The thumb is at least one
// row tall so it cannot vanish, and the bar is blank when the list fits,
// because a scrollbar with nothing to scroll is furniture.
func (m BrowserModel) scrollbar(row, rows int) string {
	total := len(m.visible)
	if total <= rows {
		return " "
	}
	height := maxInt(1, rows*rows/total)
	top := m.top * (rows - height) / maxInt(1, total-rows)
	if row >= top && row < top+height {
		return m.theme.Accent(m.sym.Thumb)
	}
	return m.theme.Faint(m.sym.Track)
}

// nothingMatches explains an empty list in terms of what is narrowing it, so
// the way out is on screen rather than in the help.
func (m BrowserModel) nothingMatches() []string {
	out := []string{"", "   " + m.theme.Heading("Nothing matches.")}
	var why []string
	if m.query.Text != "" {
		why = append(why, fmt.Sprintf("%q by %s", m.query.Text, m.query.Field))
	}
	if m.query.Cloud != "" {
		why = append(why, "cloud "+m.query.Cloud)
	}
	if m.query.ReachableOnly {
		why = append(why, "reachable only")
	}
	if len(why) == 0 {
		return append(out, "   "+m.theme.Dim("No machines cached yet. Run ssmate ls --refresh."))
	}
	return append(out,
		"   "+m.theme.Dim("Filtering by "+strings.Join(why, ", ")+"."),
		"",
		"   "+m.theme.Key("x")+m.theme.Dim(" clears every filter    ")+
			m.theme.Key("tab")+m.theme.Dim(" changes what the search matches"))
}

// renderDetail shows everything worth knowing about the selected machine.
func (m BrowserModel) renderDetail() string {
	n, ok := m.Selected()
	if !ok {
		return m.box("", false, detailRows)
	}
	// One wider than the longest label, so no value ever touches its label.
	label := func(s string) string { return m.theme.Dim(Pad(s, 9, "")) }

	head := "   " + n.Cloud + " " + m.sym.Bullet + " " + n.Where()
	if n.Via != "" {
		head += " " + m.sym.Bullet + " via " + n.Via
		if guessedLink(n) {
			head += ", matched by address"
		}
	}
	lines := []string{m.theme.Accent(n.DisplayName()) + m.theme.Dim(head)}
	if n.Hint != "" {
		lines = append(lines, m.theme.StatusStyle(n.Status)(n.Status+": ")+n.Hint)
	}
	facts := []string{label("id") + n.ID}
	if n.PrivateIP != "" {
		addr := n.PrivateIP
		if n.PublicIP != "" {
			addr += m.theme.Dim("  public ") + n.PublicIP
		}
		facts = append(facts, label("address")+addr)
	}
	for _, k := range detailKeys(n) {
		v, _ := n.Detailed(k)
		facts = append(facts, label(detailLabel(k))+v)
	}
	if tags := tagChips(n, m.theme); tags != "" {
		facts = append(facts, label("tags")+tags)
	}
	lines = append(lines, facts...)
	if len(lines) > detailRows {
		lines = lines[:detailRows]
	}
	for i, l := range lines {
		lines[i] = truncateVisible(l, m.inner(), m.sym.Ellipsis)
	}
	return m.box(strings.Join(lines, "\n"), false, detailRows)
}

// tagChips renders a machine's tags compactly. Keys and values both appear,
// because people search for "production" as readily as "Environment".
func tagChips(n inventory.Node, t Theme) string {
	if len(n.Tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(n.Tags))
	for k := range n.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := n.Tags[k]; v != "" {
			out = append(out, t.Dim(k+"=")+v)
		}
		if len(out) == 4 {
			break
		}
	}
	return strings.Join(out, "   ")
}

// detailKeys picks the few provider facts worth the space, in a fixed order,
// so the box does not reshuffle as the cursor moves between clouds.
//
// The bastion is one slot whichever cloud names it: Azure records its name
// under "bastion", while OCI keeps an OCID there and the name under
// "bastion_name". Showing both would spend a line of a small box on a hundred
// characters of identifier.
func detailKeys(n inventory.Node) []string {
	want := []string{"bastion_name", "bastion", "bastion_via", "bastion_sku", "vcn_name", "vnet",
		"login_user", "resource_group", "compartment", "platform_name", "shape", "size"}
	var out []string
	haveBastion := false
	for _, k := range want {
		v, ok := n.Detailed(k)
		if !ok || v == "" {
			continue
		}
		if k == "bastion_name" || k == "bastion" {
			if haveBastion {
				continue
			}
			haveBastion = true
		}
		out = append(out, k)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// detailLabel turns a provider's key into a short word a person reads. A
// truncated key such as "platf~" is worse than no label at all.
func detailLabel(key string) string {
	switch key {
	case "bastion_name", "bastion":
		return "via"
	case "bastion_via":
		return "peered"
	case "bastion_sku":
		return "sku"
	case "vcn_name", "vnet":
		return "network"
	case "login_user":
		return "user"
	case "resource_group":
		return "group"
	case "compartment":
		return "compart"
	case "platform_name":
		return "os"
	case "shape", "size":
		return "size"
	default:
		return Truncate(key, 7, "")
	}
}

// renderFooter lists the keys that matter now, and where in the list you are.
//
// Keys are drawn brighter than their descriptions so the eye finds the key
// first, which is the order a person reads a hint in. Hints drop from the
// right as the terminal narrows rather than wrapping, because a wrapped footer
// pushes a row of the list off screen.
func (m BrowserModel) renderFooter() string {
	if m.notice != "" {
		return " " + m.theme.Degraded(truncateVisible(m.notice, m.boxWidth()-1, m.sym.Ellipsis))
	}
	type hint struct{ key, what string }
	var hints []hint
	if m.mode == modeSearch {
		hints = []hint{{"type", "to filter"}, {"tab", "match " + m.query.Field.Next().String()},
			{"↑↓", "move"}, {"enter", "done"}, {"esc", "stop searching"}}
	} else {
		hints = []hint{{"enter", "connect"}, {"f", "forward"}, {"d", "doctor"},
			{"/", "search"}, {"c", "cloud"}, {"i", "info"}, {"?", "help"}, {"q", "quit"}}
	}

	pos := ""
	if len(m.visible) > 0 {
		pos = m.theme.Dim(fmt.Sprintf("%d/%d", m.cursor+1, len(m.visible)))
	}
	room := m.boxWidth() - lipgloss.Width(pos) - 2
	for len(hints) > 0 {
		parts := make([]string, len(hints))
		for i, h := range hints {
			parts[i] = m.theme.Key(h.key) + " " + m.theme.Dim(h.what)
		}
		line := " " + strings.Join(parts, "   ")
		if lipgloss.Width(line) <= room {
			return line + strings.Repeat(" ", maxInt(1, m.boxWidth()-lipgloss.Width(line)-lipgloss.Width(pos))) + pos
		}
		hints = hints[:len(hints)-1]
	}
	return " " + pos
}

// renderHelp is the whole-screen reference, grouped by intent.
func (m BrowserModel) renderHelp() string {
	type key struct{ keys, what string }
	groups := []struct {
		name string
		keys []key
	}{
		{"Move", []key{
			{"↑ ↓  j k", "one machine"},
			{"ctrl+d ctrl+u", "half a screen"},
			{"g G", "first and last"},
		}},
		{"Narrow", []key{
			{"/", "search as you type"},
			{"tab", "search by name, tag, or anything"},
			{"c", "one cloud at a time, then all"},
			{"r", "only what can be reached now"},
			{"s", "order by recent, name, cloud, status"},
			{"x", "clear every filter"},
		}},
		{"Act", []key{
			{"enter", "open a shell"},
			{"f", "forward a port"},
			{"d", "diagnose why it will not connect"},
			{"b", "bookmark under a shorter name"},
			{"i", "show everything known about it"},
		}},
		{"Leave", []key{
			{"q  esc", "quit"},
		}},
	}
	widest := 0
	for _, g := range groups {
		for _, k := range g.keys {
			widest = maxInt(widest, lipgloss.Width(k.keys))
		}
	}
	var lines []string
	for gi, g := range groups {
		if gi > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.theme.Accent(g.name))
		for _, k := range g.keys {
			lines = append(lines, "  "+m.theme.Key(Pad(k.keys, widest, ""))+"    "+m.theme.Dim(k.what))
		}
	}

	parts := []string{}
	if m.showLogo() {
		parts = append(parts, m.renderLogo(), "")
	}
	parts = append(parts,
		m.box(strings.Join(lines, "\n"), true, 0),
		" "+m.theme.Dim("any key returns to the list"))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// timeNow is a variable so the used column can be tested against a fixed
// clock rather than whatever today happens to be.
var timeNow = time.Now

// truncateVisible shortens a string that already carries colour.
//
// Truncate counts runes, and an escape sequence is several runes that occupy
// no columns, so using it on a coloured string cuts far too early and can
// leave the terminal mid-sequence. This counts only what is drawn, keeps every
// escape it passes, and closes with a reset so colour cannot leak onward.
func truncateVisible(s string, width int, marker string) string {
	if width <= 0 {
		return ""
	}
	if visibleLen(s) <= width {
		return s
	}
	mark := len([]rune(marker))
	var b strings.Builder
	drawn, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
			b.WriteRune(r)
		case inEscape:
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
		default:
			if drawn >= width-mark {
				return b.String() + marker + "\x1b[0m"
			}
			b.WriteRune(r)
			drawn++
		}
	}
	return b.String()
}

// visibleLen counts the columns a string occupies, ignoring escape sequences.
func visibleLen(s string) int { return lipgloss.Width(s) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// viaOf is what the VIA column shows: the way a session would go in, or a
// dash for a machine with none.
func viaOf(n inventory.Node) string {
	if n.Via == "" {
		return "-"
	}
	return n.Via
}

// guessedLink reports whether the way in the machine is shown with came from
// another inventory on the strength of its address and hostname alone. That
// is a judgement rather than a fact, so the detail says so.
func guessedLink(n inventory.Node) bool {
	for _, r := range n.Routes {
		if inventory.RouteLabel(r.Kind) == n.Via && r.Link == inventory.LinkAddress {
			return true
		}
	}
	return false
}
