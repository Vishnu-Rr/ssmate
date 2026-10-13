package ui

import (
	"reflect"
	"testing"
	"unicode"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestASCIISymbolsAreAllASCII(t *testing.T) {
	s := NewSymbols(false)
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		val := v.Field(i).String()
		if val == "" {
			t.Errorf("symbol %s is empty in ASCII mode", name)
		}
		for _, r := range val {
			if r > unicode.MaxASCII {
				t.Errorf("symbol %s = %q contains non-ASCII rune %q in ASCII mode", name, val, r)
			}
		}
	}
}

func TestUnicodeSymbolsAreAllNonEmpty(t *testing.T) {
	s := NewSymbols(true)
	v := reflect.ValueOf(s)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).String() == "" {
			t.Errorf("symbol %s is empty in unicode mode", v.Type().Field(i).Name)
		}
	}
}

func TestFailMarkerIsCapitalXNotBallotGlyph(t *testing.T) {
	if got := NewSymbols(false).Fail; got != "X" {
		t.Fatalf("ASCII Fail = %q, want %q", got, "X")
	}
}

func TestSupportsUnicodeFalseWhenTermIsDumb(t *testing.T) {
	if SupportsUnicode(envFrom(map[string]string{"TERM": "dumb", "LANG": "en_US.UTF-8"})) {
		t.Fatal("TERM=dumb should not support unicode")
	}
}

func TestSupportsUnicodeTrueForUTF8Locale(t *testing.T) {
	if !SupportsUnicode(envFrom(map[string]string{"TERM": "xterm-256color", "LC_ALL": "en_US.UTF-8"})) {
		t.Fatal("UTF-8 locale should support unicode")
	}
}

func TestSupportsUnicodeFalseForNonUTF8Locale(t *testing.T) {
	if SupportsUnicode(envFrom(map[string]string{"TERM": "xterm", "LANG": "C"})) {
		t.Fatal("LANG=C should not support unicode")
	}
}

func TestColorDisabledByNoColor(t *testing.T) {
	if ColorEnabled(envFrom(map[string]string{"NO_COLOR": "1"}), true) {
		t.Fatal("NO_COLOR set should disable colour")
	}
}

func TestEmptyNoColorDoesNotDisableColor(t *testing.T) {
	// The NO_COLOR spec is explicit: the variable must be present AND
	// non-empty. An empty value is not an opt-out.
	if !ColorEnabled(envFrom(map[string]string{"NO_COLOR": ""}), true) {
		t.Fatal("empty NO_COLOR should not disable colour")
	}
}

func TestColorDisabledWhenNotATTY(t *testing.T) {
	if ColorEnabled(envFrom(map[string]string{}), false) {
		t.Fatal("non-TTY should disable colour")
	}
}
