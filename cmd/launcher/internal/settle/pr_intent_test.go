package settle

import (
	"regexp"
	"strings"
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
			name: "non-local, already has colon-form Closes, unchanged",
			body: "Adds a widget.\n\nCloses: #1919",
			num:  "1919",
			want: "Adds a widget.\n\nCloses: #1919",
		},
		{
			name: "non-local, closes references a different number, appends",
			body: "Adds a widget.\n\nCloses #191",
			num:  "1919",
			want: "Adds a widget.\n\nCloses #191\n\nCloses #1919",
		},
		{
			name: "non-local, closes references a longer number, appends",
			body: "Adds a widget.\n\nCloses #19195",
			num:  "1919",
			want: "Adds a widget.\n\nCloses #19195\n\nCloses #1919",
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

// TestDefuseClosingKeywords covers defuseClosingKeywords' guarantee that a
// box-authored string embedded verbatim in a reconstructed PR body (issue
// #2447) never carries a live GitHub closing-keyword reference: a
// prompt-injected Box controls its own commit subjects, and a subject
// shaped like "fix: closes #999" would otherwise auto-close an unrelated
// issue #999 on merge.
func TestDefuseClosingKeywords(t *testing.T) {
	tests := []struct {
		name    string
		subject string
	}{
		{
			name:    "closes keyword, no colon",
			subject: "fix: closes #999",
		},
		{
			name:    "colon-form Closes keyword",
			subject: "Closes: #1234",
		},
		{
			name:    "Fixes keyword",
			subject: "Fixes #42",
		},
		{
			name:    "Resolved keyword",
			subject: "resolved #7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defuseClosingKeywords(tt.subject)
			if closingKeywordPattern.MatchString(got) {
				t.Errorf("defuseClosingKeywords(%q) = %q, still matches closingKeywordPattern", tt.subject, got)
			}
			// The subject must still be visually recognizable — every digit
			// and the keyword text itself must survive, only made invisible
			// to the closing-keyword scanner via a zero-width space.
			digitsOnly := regexp.MustCompile(`\d+`).FindString(tt.subject)
			if digitsOnly == "" || !strings.Contains(got, digitsOnly) {
				t.Errorf("defuseClosingKeywords(%q) = %q, want it to still contain the digits %q", tt.subject, got, digitsOnly)
			}
		})
	}

	t.Run("no closing keyword, unchanged", func(t *testing.T) {
		subject := "feat: add a widget"
		got := defuseClosingKeywords(subject)
		if got != subject {
			t.Errorf("defuseClosingKeywords(%q) = %q, want unchanged", subject, got)
		}
	})
}
