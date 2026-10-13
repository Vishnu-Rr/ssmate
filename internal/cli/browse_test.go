package cli

import (
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
)

// The prompt has to read a whole line. Reading one whitespace-delimited word
// would silently keep the first half of an answer, which is worse than
// refusing it.
func TestAskLineReadsAWholeLine(t *testing.T) {
	restore := askLine
	t.Cleanup(func() { askLine = restore })

	for _, tc := range []struct {
		name  string
		input string
		want  string
		fails bool
	}{
		{"a plain answer", "5432:5432\n", "5432:5432", false},
		{"surrounding space is trimmed", "  db-1  \n", "db-1", false},
		{"words are kept together", "my machine\n", "my machine", false},
		{"a blank line is a refusal", "\n", "", true},
		{"end of input is a refusal", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readLineFrom(strings.NewReader(tc.input))
			if tc.fails {
				if err == nil {
					t.Fatalf("want a refusal, got %q", got)
				}
				// A refusal must be distinguishable from a failure, so a
				// script can tell "they said no" from "it broke".
				if code := exitcode.From(err); code != exitcode.Cancelled {
					t.Errorf("exit code %v, want cancelled", code)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
