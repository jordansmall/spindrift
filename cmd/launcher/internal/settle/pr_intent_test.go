package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestEnsureClosesReference covers ensureClosesReference's guarantee that a
// PR body carries a "Closes #<num>" reference — but only for non-local
// (GitHub-shaped) trackers, since a LandingRecorder-shaped (local) tracker
// closes issues through its own axis (ADR 0029), never GitHub's
// auto-close-on-merge keyword convention.
func TestEnsureClosesReference(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		num     string
		local   bool
		forgejo bool
		want    string
	}{
		{
			name: "non-local, no closes reference, appends",
			body: "Adds a widget.",
			num:  "1919",
			want: "Adds a widget.\n\nCloses #1919",
		},
		{
			name: "non-local, already has Closes, unchanged",
			body: "Adds a widget.\n\nCloses #1919",
			num:  "1919",
			want: "Adds a widget.\n\nCloses #1919",
		},
		{
			name: "non-local, already has Fixes, unchanged",
			body: "Adds a widget.\n\nFixes #1919",
			num:  "1919",
			want: "Adds a widget.\n\nFixes #1919",
		},
		{
			name: "non-local, empty body, becomes exactly Closes",
			body: "",
			num:  "1919",
			want: "Closes #1919",
		},
		{
			name:  "local tracker, no closes reference, unchanged",
			body:  "Adds a widget.",
			num:   "1919",
			local: true,
			want:  "Adds a widget.",
		},
		{
			name:    "forgejo tracker, no closes reference, unchanged",
			body:    "Adds a widget.",
			num:     "1919",
			forgejo: true,
			want:    "Adds a widget.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var it forge.IssueTracker
			switch {
			case tt.local:
				it = forge.NewFake(testDispatchLabels).AsLocalShaped()
			case tt.forgejo:
				it = forge.NewFake(testDispatchLabels).AsForgejoShaped()
			default:
				it = forge.NewFake(testDispatchLabels).AsNoLandingRecorder()
			}
			got := ensureClosesReference(tt.body, tt.num, it)
			if got != tt.want {
				t.Errorf("ensureClosesReference(%q, %q) = %q, want %q", tt.body, tt.num, got, tt.want)
			}
		})
	}
}
