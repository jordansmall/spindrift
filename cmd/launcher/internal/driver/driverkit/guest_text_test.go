package driverkit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapLineStripsAndSpaces(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"csi":       {"a\x1b[31mred\x1b[0mb", "aredb"},
		"osc":       {"a\x1b]0;title\x07b", "ab"},
		"cr":        {"a\rb", "a b"},
		"nul":       {"a\x00b", "ab"},
		"bidi":      {"a\u202eb", "ab"},
		"separator": {"a\u2028b", "ab"},
		"tab":       {"a\tb", "a b"},
		"newline":   {"a\nb", "a b"},
		"under cap": {"plain é text", "plain é text"},
	} {
		if got := CapLine(tc.in, 100); got != tc.want {
			t.Errorf("%s: CapLine(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

func TestCapLineCapsOnRuneBoundaryWithMarker(t *testing.T) {
	const limit = 40
	for _, unit := range []string{"a", "é", "世"} {
		for pad := 0; pad < 4; pad++ {
			in := strings.Repeat("x", pad) + strings.Repeat(unit, 1<<20/len(unit))
			got := CapLine(in, limit)
			if len(got) > limit || !utf8.ValidString(got) || !strings.HasSuffix(got, CapMarker) {
				t.Errorf("unit %q pad %d: bad result %q (len %d)", unit, pad, got, len(got))
			}
		}
	}
	if got := CapLine("ab   cd efgh ijkl mnop qrst uvwx yzyz", 20); got != "ab   cd"+CapMarker {
		t.Errorf("trailing space not trimmed before marker: %q", got)
	}
}
