package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Column layout, kept separate from drawing so the arithmetic can be tested.
//
// The problem this solves is not cosmetic. Identifiers differ in length by an
// order of magnitude between clouds - an EC2 instance ID is 19 characters and
// an OCI OCID is over a hundred - so a table that sizes columns to their
// widest value gives almost the whole terminal to a string nobody reads. The
// answer is to budget: fixed columns take what they need, and the name, which
// is the only column people actually scan, takes everything left.

// Col is one column's computed width.
type Col struct {
	Name  string
	Width int
}

// Layout is the resolved widths for one box.
type Layout struct {
	Name    int
	Cloud   int
	Region  int
	Account int
	Via     int
	Status  int
	Used    int
	// Narrow drops the columns that are context rather than identity, so a
	// small terminal still shows a usable list instead of a smear.
	Narrow bool
}

// Widths of the fixed columns. Each is the longest value it has to hold:
// "azure", "ap-southeast-2", "bastion-fwd", "no gateway". The account is
// clipped because an account is recognised from its first few characters.
const (
	cloudWidth   = 5
	regionWidth  = 14
	accountWidth = 12
	viaWidth     = 11
	statusWidth  = 10
	usedWidth    = 4
	gutter       = 2
	minNameWidth = 12

	// lead is the space, the reachability marker and the space after it.
	lead = 3
	// trail is the space before the scrollbar and the scrollbar itself.
	trail = 2
	// MarkerWidth is the reachability marker, one cell in every symbol set.
	MarkerWidth = 1
)

// wideFixed is everything on a full row except the name.
const wideFixed = lead + trail + gutter*6 + cloudWidth + regionWidth + accountWidth + viaWidth + statusWidth + usedWidth

// Resolve computes column widths for a box whose content is inner columns wide.
//
// Identifiers differ in length by an order of magnitude between clouds - an
// EC2 instance ID is 19 characters and an OCI OCID over a hundred - so a table
// sized to its widest value gives the screen to a string nobody reads. The
// answer is to budget: fixed columns take what they need and the name, the
// only column people actually scan, takes everything left.
func Resolve(inner int) Layout {
	if inner <= 0 {
		inner = 72
	}
	if name := inner - wideFixed; name >= minNameWidth+4 {
		return Layout{
			Name: name, Cloud: cloudWidth, Region: regionWidth,
			Account: accountWidth, Via: viaWidth, Status: statusWidth, Used: usedWidth,
		}
	}
	name := inner - lead - trail - gutter*2 - statusWidth - usedWidth
	if name < minNameWidth {
		name = minNameWidth
	}
	return Layout{Name: name, Status: statusWidth, Used: usedWidth, Narrow: true}
}

// Width is how many columns a row laid out this way occupies.
func (l Layout) Width() int {
	if l.Narrow {
		return lead + l.Name + gutter + l.Status + gutter + l.Used + trail
	}
	return lead + l.Name + gutter*6 + l.Cloud + l.Region + l.Account + l.Via + l.Status + l.Used + trail
}

// Truncate shortens a string to width, marking that it was cut.
//
// It counts runes rather than bytes: a name with any non-ASCII character in it
// would otherwise be cut mid-character and render as a replacement glyph. The
// marker comes from the caller's symbol set rather than being fixed, because a
// terminal that cannot draw an ellipsis is exactly where a truncated name is
// hardest to read.
func Truncate(s string, width int, marker string) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if marker == "" {
		marker = "~"
	}
	mark := utf8.RuneCountInString(marker)
	if width <= mark {
		return string([]rune(marker)[:width])
	}
	out := make([]rune, 0, width)
	for _, r := range s {
		if len(out) == width-mark {
			break
		}
		out = append(out, r)
	}
	return string(out) + marker
}

// Pad right-pads to width, after truncating anything longer.
func Pad(s string, width int, marker string) string {
	s = Truncate(s, width, marker)
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// ShortCloud abbreviates a cloud name to fit the column while staying
// unmistakable.
func ShortCloud(cloud string) string {
	switch cloud {
	case "aws":
		return "aws"
	case "azure":
		return "azure"
	case "oci":
		return "oci"
	case "":
		return "?"
	default:
		return Truncate(cloud, cloudWidth, "")
	}
}

// ShortAccount clips an account for display.
//
// An unnamed account is the ambient default, and rendering it as an empty
// column looks like missing data rather than an answer, so it says so.
func ShortAccount(account string) string {
	if account == "" {
		return "default"
	}
	return Truncate(account, accountWidth, "")
}

// Ago renders how long ago something happened, short enough for a column.
//
// A machine never reached reads as a dash rather than "never": the column is
// about finding the ones you use, and a column of the word "never" would draw
// the eye to exactly the machines it is not about.
func Ago(then, now time.Time) string {
	if then.IsZero() {
		return "-"
	}
	d := now.Sub(then)
	switch {
	case d < 0:
		return "-"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}
