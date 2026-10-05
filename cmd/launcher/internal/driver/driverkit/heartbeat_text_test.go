package driverkit

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// SanitizeLine drops Unicode bidi controls and line/paragraph separators so
// agent text cannot reorder or visually break a heartbeat line (issue #4396),
// while ordinary non-ASCII text survives.
func TestSanitizeLineDropsBidiAndSeparators(t *testing.T) {
	for _, r := range []rune{
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // embeddings/overrides
		0x2066, 0x2067, 0x2068, 0x2069, // isolates
		0x200E, 0x200F, 0x061C, // LRM/RLM/ALM
		0x2028, 0x2029, // separators
		0x200B, 0xFEFF, // zero-width/BOM
	} {
		t.Run(fmt.Sprintf("%U", r), func(t *testing.T) {
			if got := SanitizeLine("a" + string(r) + "b"); got != "ab" {
				t.Errorf("SanitizeLine(a%Ub) = %q, want %q", r, got, "ab")
			}
		})
	}
	t.Run("ordinary non-ASCII survives", func(t *testing.T) {
		const in = "café ✓"
		if got := SanitizeLine(in); got != in {
			t.Errorf("SanitizeLine(%q) = %q, want unchanged", in, got)
		}
	})
}

func TestTrimNarration(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"first sentence", "Reading the file. Then more.", "Reading the file."},
		{"skips blank lines", "\n  \nhello", "hello"},
		{"lone CR is a line break", "ok\r#99 · x", "ok"},
		{"CSI stripped", "\x1b[2K\x1b[1Ago on", "go on"},
		{"BEL-terminated OSC stripped", "\x1b]0;a.b\x07go on", "go on"},
		{"ST-terminated OSC stripped", "\x1b]0;a.b\x1b\\go on", "go on"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimNarration(tc.in); got != tc.want {
				t.Errorf("TrimNarration(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTrimNarrationCapIsRuneSafe(t *testing.T) {
	got := TrimNarration(strings.Repeat("é", 200))
	if !utf8.ValidString(got) {
		t.Errorf("TrimNarration produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != NarrationMaxRunes {
		t.Errorf("rune count = %d, want %d", n, NarrationMaxRunes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("TrimNarration(%q) lacks ellipsis suffix", got)
	}
}
