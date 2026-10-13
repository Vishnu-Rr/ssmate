package ui

import (
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// Action is what the user asked to do with the machine they picked.
//
// The list returns an action rather than only a machine because everything you
// want to do to a machine starts by finding it. Making the user quit, read an
// identifier off the screen and retype it as another command would waste the
// work the list just did.
type Action int

const (
	ActionConnect Action = iota
	ActionForward
	ActionDoctor
	ActionBookmark
)

func (a Action) String() string {
	switch a {
	case ActionForward:
		return "forward"
	case ActionDoctor:
		return "doctor"
	case ActionBookmark:
		return "bookmark"
	default:
		return "connect"
	}
}

// Choice is the result of browsing.
type Choice struct {
	Node   inventory.Node
	Action Action
}

// mode is which keystrokes mean what right now.
type mode int

const (
	modeList mode = iota
	modeSearch
	modeHelp
)

// BrowserModel is the list, the search, the detail pane and the actions.
type BrowserModel struct {
	all     []inventory.Node
	visible []Hit
	clouds  []string
	query   Query
	recent  func(string) (float64, time.Time)

	cursor int
	top    int
	mode   mode
	detail bool

	width  int
	height int

	sym   Symbols
	theme Theme

	choice    *Choice
	cancelled bool
	notice    string
}

// NewBrowser builds the list over nodes.
func NewBrowser(all []inventory.Node, sym Symbols, theme Theme, recent func(string) (float64, time.Time)) BrowserModel {
	m := BrowserModel{
		all:    all,
		clouds: Clouds(all),
		recent: recent,
		sym:    sym,
		theme:  theme,
		width:  80,
		height: 24,
	}
	m.refilter()
	return m
}

func (m BrowserModel) Init() tea.Cmd { return nil }

func (m *BrowserModel) refilter() {
	m.visible = Apply(m.all, m.query, m.score())
	m.clampCursor()
}

// clampCursor keeps the selection inside the list and inside the window after
// anything that can change either.
// score adapts the recency accessor for the sort, which only needs strength.
func (m BrowserModel) score() func(string) float64 {
	if m.recent == nil {
		return nil
	}
	return func(id string) float64 {
		v, _ := m.recent(id)
		return v
	}
}

// lastUsed reports when a machine was last reached, for the column that
// explains why the default order is what it is.
func (m BrowserModel) lastUsed(id string) time.Time {
	if m.recent == nil {
		return time.Time{}
	}
	_, when := m.recent(id)
	return when
}

func (m *BrowserModel) clampCursor() {
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	rows := m.rows()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m BrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A terminal can report no size at all - under script, inside some
		// multiplexers before attach, in CI - and honouring a zero collapses
		// the list to a single row on a screen that is not actually that
		// small. Keep the last size that made sense.
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clampCursor()
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m BrowserModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	m.notice = ""

	if m.mode == modeHelp {
		// Any key leaves help: a help screen you have to work out how to
		// close is its own small insult.
		m.mode = modeList
		return m, nil
	}

	if m.mode == modeSearch {
		switch k {
		case "esc":
			m.mode = modeList
			return m, nil
		case "enter":
			m.mode = modeList
			return m, nil
		case "backspace":
			if n := len(m.query.Text); n > 0 {
				m.query.Text = m.query.Text[:n-1]
				m.refilter()
			}
			return m, nil
		case "ctrl+u":
			m.query.Text = ""
			m.refilter()
			return m, nil
		case "tab":
			m.query.Field = m.query.Field.Next()
			m.refilter()
			return m, nil
		case "up", "ctrl+p", "down", "ctrl+n":
			// Moving while searching is normal, so navigation stays live
			// rather than forcing a mode change to look at a result.
			return m.move(k)
		}
		if msg.Text != "" {
			m.query.Text += msg.Text
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}

	switch k {
	case "q", "esc", "ctrl+c":
		m.cancelled = true
		return m, tea.Quit

	case "/":
		m.mode = modeSearch
		return m, nil

	case "enter":
		return m.choose(ActionConnect)
	case "f":
		return m.choose(ActionForward)
	case "d":
		return m.choose(ActionDoctor)
	case "b":
		return m.choose(ActionBookmark)

	case "c":
		m.query.Cloud = NextCloud(m.query.Cloud, m.clouds)
		m.cursor = 0
		m.refilter()
		return m, nil

	case "s":
		m.query.Sort = m.query.Sort.Next()
		m.cursor = 0
		m.refilter()
		return m, nil

	case "tab":
		m.query.Field = m.query.Field.Next()
		m.refilter()
		return m, nil

	case "r":
		m.query.ReachableOnly = !m.query.ReachableOnly
		m.cursor = 0
		m.refilter()
		return m, nil

	case "i":
		m.detail = !m.detail
		m.clampCursor()
		return m, nil

	case "?":
		m.mode = modeHelp
		return m, nil

	case "x":
		// Clearing every narrowing at once, because getting back to the whole
		// estate should not mean undoing four separate choices.
		m.query = Query{Sort: m.query.Sort}
		m.cursor = 0
		m.refilter()
		return m, nil
	}
	return m.move(k)
}

// move handles navigation, shared by list and search modes.
func (m BrowserModel) move(k string) (tea.Model, tea.Cmd) {
	rows := m.rows()
	switch k {
	case "up", "k", "ctrl+p":
		m.cursor--
	case "down", "j", "ctrl+n":
		m.cursor++
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.visible) - 1
	case "ctrl+d", "pgdown":
		m.cursor += rows / 2
	case "ctrl+u", "pgup":
		m.cursor -= rows / 2
	default:
		return m, nil
	}
	m.clampCursor()
	return m, nil
}

// choose records the action, refusing the ones that need a reachable machine.
func (m BrowserModel) choose(a Action) (tea.Model, tea.Cmd) {
	if len(m.visible) == 0 {
		return m, nil
	}
	n := m.visible[m.cursor].Node
	// Diagnosis is the one action that is for machines that do not work, so
	// it is the one action never refused.
	if !n.Reachable && a != ActionDoctor && a != ActionBookmark {
		m.notice = fmt.Sprintf("%s is %s — press d to find out why", n.DisplayName(), n.Status)
		return m, nil
	}
	m.choice = &Choice{Node: n, Action: a}
	return m, tea.Quit
}

// Result reports what the user chose.
func (m BrowserModel) Result() (Choice, error) {
	if m.choice != nil {
		return *m.choice, nil
	}
	return Choice{}, ErrCancelled
}

// Selected is the machine under the cursor, for tests and for callers that
// want to inspect without quitting.
func (m BrowserModel) Selected() (inventory.Node, bool) {
	if len(m.visible) == 0 {
		return inventory.Node{}, false
	}
	return m.visible[m.cursor].Node, true
}

// Visible exposes the filtered machines for tests.
func (m BrowserModel) Visible() []inventory.Node { return Nodes(m.visible) }

// Browse runs the list and returns what the user chose to do.
//
// It replaces the fuzzy picker this tool started with. That picker could only
// answer "which machine", which meant every other question - why will this not
// connect, forward a port from it, give it a shorter name - sent the user back
// to the shell to retype an identifier they had just been looking at.
//
// The list is hand-rolled rather than built on the Bubbles list component,
// because that component's performance degrades past roughly eight thousand
// items and real multi-account estates exceed that.
func Browse(nodes []inventory.Node, sym Symbols, theme Theme, recent func(string) (float64, time.Time)) (Choice, error) {
	final, err := tea.NewProgram(NewBrowser(nodes, sym, theme, recent)).Run()
	if err != nil {
		return Choice{}, fmt.Errorf("run the machine list: %w", err)
	}
	m, ok := final.(BrowserModel)
	if !ok {
		return Choice{}, errors.New("the machine list returned an unexpected model")
	}
	return m.Result()
}

// ErrCancelled means the user dismissed the list without choosing.
var ErrCancelled = errors.New("cancelled")
