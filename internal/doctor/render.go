package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/ui"
)

// Render writes the human-readable report.
//
// Each remedy appears on the line directly beneath the check that produced
// it, so a reader never has to match a fix at the bottom of the output to a
// failure at the top. The report ends with a count, because otherwise the
// reader is left counting symbols themselves.
func Render(w io.Writer, results []Result, sym ui.Symbols) error {
	return RenderThemed(w, results, sym, ui.NewTheme(false))
}

// RenderThemed is Render with an explicit theme.
func RenderThemed(w io.Writer, results []Result, sym ui.Symbols, t ui.Theme) error {
	for _, r := range results {
		marker, colour := sym.Pass, t.Online
		switch r.Severity {
		case Warn:
			marker, colour = sym.Warn, t.Degraded
		case Fail:
			marker, colour = sym.Fail, t.Offline
		}

		line := colour(marker) + " " + r.Name
		if r.Detail != "" {
			line += t.Dim(fmt.Sprintf(" (%s)", r.Detail))
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
		if r.Remedy != "" {
			// The remedy is a command to copy, so it gets the attention
			// colour rather than being dimmed as secondary text. Multi-line
			// remedies keep their indentation so they stay visually attached
			// to the check that produced them.
			for _, line := range strings.Split(r.Remedy, "\n") {
				if _, err := fmt.Fprintf(w, "    %s\n", t.Match(line)); err != nil {
					return err
				}
			}
		}
	}

	passed, warned, failed := Summarize(results)
	total := passed + warned + failed
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if failed == 0 && warned == 0 {
		_, err := fmt.Fprintf(w, "%s %s\n", t.Online(sym.Info),
			t.Dim(fmt.Sprintf("%d %s, no problems found.", total, plural(total, "check", "checks"))))
		return err
	}
	summary := fmt.Sprintf("%d %s", total, plural(total, "check", "checks"))
	if failed > 0 {
		summary += ", " + t.Offline(fmt.Sprintf("%d failed", failed))
	}
	if warned > 0 {
		summary += fmt.Sprintf(", %d %s", warned, plural(warned, "warning", "warnings"))
	}
	_, err := fmt.Fprintf(w, "%s %s.\n", t.Degraded(sym.Warn), summary)
	return err
}

// RenderJSON writes the machine-readable report. It is shaped to be pasted
// into a bug report, so it carries the detail and remedy of every check.
func RenderJSON(w io.Writer, results []Result) error {
	type jsonResult struct {
		Name     string `json:"name"`
		Severity string `json:"severity"`
		Detail   string `json:"detail,omitempty"`
		Remedy   string `json:"remedy,omitempty"`
	}
	passed, warned, failed := Summarize(results)

	payload := struct {
		Checks  []jsonResult `json:"checks"`
		Summary struct {
			Total  int `json:"total"`
			Passed int `json:"passed"`
			Warned int `json:"warned"`
			Failed int `json:"failed"`
		} `json:"summary"`
	}{Checks: make([]jsonResult, 0, len(results))}

	for _, r := range results {
		payload.Checks = append(payload.Checks, jsonResult{
			Name: r.Name, Severity: r.Severity.String(), Detail: r.Detail, Remedy: r.Remedy,
		})
	}
	payload.Summary.Total = passed + warned + failed
	payload.Summary.Passed = passed
	payload.Summary.Warned = warned
	payload.Summary.Failed = failed

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
