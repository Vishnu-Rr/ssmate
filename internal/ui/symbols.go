// Package ui holds terminal presentation concerns: symbols, colour gating
// and the machine list.
package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Symbols is the marker set used in human-readable output. Every field must
// have a meaningful ASCII form, because a terminal that cannot render the
// unicode form is exactly where a user most needs to read a diagnostic.
type Symbols struct {
	Pass   string
	Fail   string
	Warn   string
	Info   string
	Bullet string
	// Ellipsis marks a value the column was too narrow to show.
	Ellipsis string
	// Rule draws a horizontal divider.
	Rule string
	// Up and Down is how the list reports it has more above or below.
	Up, Down string
	// Online and Offline mark a machine's row. They differ in shape as well
	// as colour - filled against hollow - so the distinction survives a
	// terminal with no colour and a reader who cannot tell green from amber.
	Online, Offline string
	// Search marks the search box.
	Search string
	// Frame is the border drawn around a box.
	Frame lipgloss.Border
	// Track and Thumb draw the scrollbar down the list's right edge.
	Track, Thumb string
}

// NewSymbols returns the unicode set when the terminal can render it, and a
// pure-ASCII set otherwise.
//
// The failure marker is a capital letter X rather than the ballot glyph. The
// glyph renders poorly or not at all in several common terminals, and the
// GitHub CLI reached the same conclusion.
func NewSymbols(unicode bool) Symbols {
	if unicode {
		return Symbols{
			Pass:     "✓", // check mark
			Fail:     "✗", // ballot X
			Warn:     "!",
			Info:     "•", // bullet
			Bullet:   "•",
			Ellipsis: "…",
			Rule:     "─",
			Up:       "▲",
			Down:     "▼",
			Online:   "●",
			Offline:  "○",
			Search:   "›",
			Frame:    lipgloss.RoundedBorder(),
			Track:    "│",
			Thumb:    "┃",
		}
	}
	return Symbols{
		Pass:     "ok",
		Fail:     "X",
		Warn:     "!",
		Info:     "-",
		Bullet:   "-",
		Ellipsis: "~",
		Rule:     "-",
		Up:       "^",
		Down:     "v",
		Online:   "*",
		Offline:  "o",
		Search:   ">",
		Frame:    lipgloss.ASCIIBorder(),
		Track:    ":",
		Thumb:    "#",
	}
}

// SupportsUnicode reports whether the terminal is likely to render non-ASCII
// glyphs. env is an accessor such as os.Getenv, injected so this is testable.
func SupportsUnicode(env func(string) string) bool {
	if env("TERM") == "dumb" {
		return false
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := env(key)
		if v == "" {
			continue
		}
		return strings.Contains(strings.ToUpper(v), "UTF-8") ||
			strings.Contains(strings.ToUpper(v), "UTF8")
	}
	return false
}

// ColorEnabled reports whether ANSI colour should be emitted.
//
// NO_COLOR is honoured per its specification: the variable disables colour
// when it is present and non-empty. An empty value is not an opt-out.
func ColorEnabled(env func(string) string, isTTY bool) bool {
	if env("NO_COLOR") != "" {
		return false
	}
	if env("TERM") == "dumb" {
		return false
	}
	return isTTY
}
