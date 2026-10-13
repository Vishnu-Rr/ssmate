package ui

import (
	"os"

	"charm.land/lipgloss/v2"
)

// Theme carries every style the tool draws with.
//
// The design rule is restraint: one accent colour, two greys, and status
// colour confined to the one place status is shown. A list that uses six
// colours for six kinds of fact reads as noise, because nothing stands out
// when everything does. The accent marks what you are acting on - the
// focused box, the selected row, the tool's own name - and nothing else.
//
// A theme is always usable. When colour is off - NO_COLOR is set, the output
// is piped, or the terminal is dumb - every style is the identity function, so
// callers never branch on whether colour is available. That keeps the "does
// this work without colour" question answerable by a test rather than by
// reading every call site.
type Theme struct {
	// Name is a machine's name in an ordinary row: the thing people scan for.
	Name func(string) string
	// Dim is secondary detail - regions, accounts, descriptions of keys.
	Dim func(string) string
	// Faint is structure: borders of boxes that do not have focus, rules.
	Faint func(string) string
	// Accent is the one colour that means "this is what you are acting on".
	Accent func(string) string
	// Key names a key in a hint, so the eye finds keys before descriptions.
	Key func(string) string
	// Selected is the selected row: the accent as a solid bar, light text.
	Selected func(string) string
	// Match picks out the characters a search matched on an ordinary row.
	Match func(string) string
	// MatchSelected picks them out on the selected row, where the accent is
	// already the background and cannot also be the highlight.
	MatchSelected func(string) string
	// Online, Degraded and Offline colour status, and only status.
	Online   func(string) string
	Degraded func(string) string
	Offline  func(string) string
	// Heading is emphasis without colour, for column heads and labels.
	Heading func(string) string
	// Chip is a filled label: the accent as background.
	Chip func(string) string

	// BorderFocus and BorderBlur colour a box's frame by whether it has
	// focus. The difference is the whole of the focus model: you can see which
	// box your keys are going to without being told.
	BorderFocus lipgloss.Style
	BorderBlur  lipgloss.Style

	enabled bool
}

// Enabled reports whether this theme emits colour.
func (t Theme) Enabled() bool { return t.enabled }

func plain(s string) string { return s }

// The palette.
//
// The accent is a violet chosen to read on both dark and light terminals: it
// holds white text at a contrast a bold label can carry, and stays visible as
// a thin border on a light background, where paler violets vanish. Status
// colours are mid-range for the same reason - saturated primaries are harsh on
// a light terminal and dark ones disappear on a dark one.
const (
	accent    = "#8B5CF6"
	onAccent  = "#FFFFFF"
	muted     = "#8A8F98"
	faint     = "#4B505A"
	green     = "#4ADE80"
	amber     = "#F5B83D"
	red       = "#F26D6D"
	highlight = "#C4B5FD"
)

// NewTheme builds a theme, honouring NO_COLOR and non-terminal output.
func NewTheme(colour bool) Theme {
	border := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if !colour {
		return Theme{
			Name: plain, Dim: plain, Faint: plain, Accent: plain, Key: plain,
			Selected: plain, Match: plain, MatchSelected: plain,
			Online: plain, Degraded: plain, Offline: plain, Heading: plain, Chip: plain,
			BorderFocus: border, BorderBlur: border,
		}
	}

	style := func(s lipgloss.Style) func(string) string {
		return func(in string) string { return s.Render(in) }
	}
	fg := func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

	return Theme{
		Name:     plain,
		Dim:      style(fg(muted)),
		Faint:    style(fg(faint)),
		Accent:   style(fg(accent).Bold(true)),
		Key:      style(lipgloss.NewStyle().Bold(true)),
		Selected: style(lipgloss.NewStyle().Foreground(lipgloss.Color(onAccent)).Background(lipgloss.Color(accent))),
		Match:    style(fg(highlight).Bold(true).Underline(true)),
		MatchSelected: style(lipgloss.NewStyle().Foreground(lipgloss.Color(onAccent)).
			Background(lipgloss.Color(accent)).Bold(true).Underline(true)),
		Online:      style(fg(green)),
		Degraded:    style(fg(amber)),
		Offline:     style(fg(red)),
		Heading:     style(lipgloss.NewStyle().Bold(true)),
		Chip:        style(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(onAccent)).Background(lipgloss.Color(accent))),
		BorderFocus: border.BorderForeground(lipgloss.Color(accent)),
		BorderBlur:  border.BorderForeground(lipgloss.Color(faint)),
		enabled:     true,
	}
}

// ThemeFromEnv builds a theme from the environment and whether stdout is a
// terminal.
func ThemeFromEnv(isTTY bool) Theme {
	return NewTheme(ColorEnabled(os.Getenv, isTTY))
}

// StatusStyle picks the colour for a node's status word.
//
// The grouping is by what the user has to do about it, not by which cloud
// said it. Amber is "this machine is fine, the way in is not" - an agent that
// stopped reporting, a network with no bastion, a bastion that cannot carry a
// terminal session. Red is "the machine itself is not running". Grey is only
// for a state we genuinely cannot interpret, so a status arriving here in grey
// is a sign a provider invented a word nothing else knows.
func (t Theme) StatusStyle(status string) func(string) string {
	switch status {
	case "online":
		return t.Online
	case "agent lost", "unmanaged", "no gateway", "no tunnel", "blocked", "no route":
		return t.Degraded
	case "stopped", "terminated":
		return t.Offline
	default:
		return t.Dim
	}
}
