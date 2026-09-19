package dispatch

import (
	"testing"

	"spindrift.dev/launcher/internal/daemon"
)

// TestAnnounceLine_ParsesBack guards the pairing announce.go's doc comment
// describes: internal/daemon's ParseAnnouncedIssue has no machine-readable
// channel other than this exact line shape, so feeding announceLine's own
// output through the real parser (rather than a hand-copied literal) means a
// future re-indent or reformat of announceLine fails here, in the package
// that owns the format, instead of silently in the daemon.
func TestAnnounceLine_ParsesBack(t *testing.T) {
	cases := []struct {
		name string
		kind string
	}{
		{"run", ""},
		{"fix pass", "fix-pass-2"},
		{"conflict resolve", "conflict-resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := announceLine("42", tc.kind, "fix the thing")
			got, ok := daemon.ParseAnnouncedIssue(line)
			if !ok || got != "42" {
				t.Fatalf("ParseAnnouncedIssue(%q) = (%q, %v), want (%q, true)", line, got, ok, "42")
			}
		})
	}
}
