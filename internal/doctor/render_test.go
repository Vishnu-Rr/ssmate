package doctor

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/ui"
)

func ascii() ui.Symbols { return ui.NewSymbols(false) }

func TestRenderPutsRemedyDirectlyUnderItsCheck(t *testing.T) {
	results := []Result{
		{Name: "plugin installed", Severity: Pass, Detail: "1.2.835.0"},
		{Name: "agent online", Severity: Fail, Detail: "last seen 3 days ago",
			Remedy: "ssmate doctor i-0abc --deep"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, results, ascii()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	var failIdx, remedyIdx = -1, -1
	for i, l := range lines {
		if strings.Contains(l, "agent online") {
			failIdx = i
		}
		if strings.Contains(l, "ssmate doctor i-0abc --deep") {
			remedyIdx = i
		}
	}
	if failIdx == -1 || remedyIdx == -1 {
		t.Fatalf("check or remedy missing from output:\n%s", buf.String())
	}
	if remedyIdx != failIdx+1 {
		t.Fatalf("remedy at line %d but its check is at %d; remedies must sit directly beneath:\n%s",
			remedyIdx, failIdx, buf.String())
	}
}

func TestRenderEndsWithACountSummary(t *testing.T) {
	results := []Result{
		{Name: "a", Severity: Pass}, {Name: "b", Severity: Pass},
		{Name: "c", Severity: Fail, Detail: "broken"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, results, ascii()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			last = l
		}
	}
	if !strings.Contains(last, "3") || !strings.Contains(last, "1") {
		t.Fatalf("summary line %q should state 3 checks and 1 failure", last)
	}
}

func TestRenderSummaryIsSingularForOneFailure(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, []Result{{Name: "a", Severity: Fail}}, ascii())
	out := buf.String()
	if strings.Contains(out, "1 checks") {
		t.Errorf("summary should say '1 check', not '1 checks': %q", out)
	}
}

func TestRenderUsesOnlyASCIIWithASCIISymbols(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, []Result{
		{Name: "a", Severity: Pass}, {Name: "b", Severity: Warn}, {Name: "c", Severity: Fail},
	}, ascii())
	for _, r := range buf.String() {
		if r > 127 {
			t.Fatalf("non-ASCII rune %q in ASCII-mode output", r)
		}
	}
}

func TestRenderJSONIsValidAndCarriesEveryField(t *testing.T) {
	results := []Result{{Name: "agent online", Severity: Fail, Detail: "gone", Remedy: "fix it"}}
	var buf bytes.Buffer
	if err := RenderJSON(&buf, results); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var decoded struct {
		Checks []struct {
			Name     string `json:"name"`
			Severity string `json:"severity"`
			Detail   string `json:"detail"`
			Remedy   string `json:"remedy"`
		} `json:"checks"`
		Summary struct {
			Passed int `json:"passed"`
			Failed int `json:"failed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(decoded.Checks) != 1 || decoded.Checks[0].Severity != "fail" {
		t.Fatalf("decoded wrong: %+v", decoded)
	}
	if decoded.Summary.Failed != 1 {
		t.Errorf("summary.failed = %d, want 1", decoded.Summary.Failed)
	}
}
