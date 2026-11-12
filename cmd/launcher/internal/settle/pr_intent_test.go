package settle

import (
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// Internal seam: a pure body-text matrix; the Settle draft-PR rows pin only the
// append and already-closed cases.
// Only a tracker row with ClosesRefTracker (github) gets a "Closes #<num>"
// reference. A local tracker closes issues through its own axis
// (ADR 0029), never through GitHub's auto-close-on-merge keyword.
func TestInternalSeam_EnsureClosesReference(t *testing.T) {
	const num = "1919"
	tests := []struct {
		name    string
		body    string
		tracker string // backend row name; "" is the zero Descriptor
		want    string
	}{
		{"github tracker, no closes reference, appends", "Adds a widget.", "github", "Adds a widget.\n\nCloses #1919"},
		{"github tracker, already has Closes, unchanged", "Adds a widget.\n\nCloses #1919", "github", "Adds a widget.\n\nCloses #1919"},
		{"github tracker, already has Fixes, unchanged", "Adds a widget.\n\nFixes #1919", "github", "Adds a widget.\n\nFixes #1919"},
		{"github tracker, already has colon-form Closes, unchanged", "Adds a widget.\n\nCloses: #1919", "github", "Adds a widget.\n\nCloses: #1919"},
		{"github tracker, closes references a different number, appends", "Adds a widget.\n\nCloses #191", "github", "Adds a widget.\n\nCloses #191\n\nCloses #1919"},
		{"github tracker, closes references a longer number, appends", "Adds a widget.\n\nCloses #19195", "github", "Adds a widget.\n\nCloses #19195\n\nCloses #1919"},
		{"github tracker, empty body, becomes exactly Closes", "", "github", "Closes #1919"},
		{"local tracker, no closes reference, unchanged", "Adds a widget.", "local", "Adds a widget."},
		{"forgejo tracker, no closes reference, unchanged", "Adds a widget.", "forgejo", "Adds a widget."},
		{"jira tracker, no closes reference, unchanged", "Adds a widget.", "jira", "Adds a widget."},
		{"zero descriptor fails closed, unchanged", "Adds a widget.", "", "Adds a widget."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracker backend.Descriptor
			if tt.tracker != "" {
				tracker = row(t, tt.tracker)
			}
			got := ensureClosesReference(tt.body, num, tracker)
			if got != tt.want {
				t.Errorf("ensureClosesReference(%q, %q) = %q, want %q", tt.body, num, got, tt.want)
			}
		})
	}
}

// The gate is a fact of the tracker row alone: the Code Forge row never
// changes it, so a github tracker with a forgejo forge still injects (#6880).
func TestMediation_Open_ClosesGateFollowsTrackerRow(t *testing.T) {
	tests := []struct {
		tracker, forge string
		wantCloses     bool
	}{
		{"github", "github", true},
		{"github", "forgejo", true},
		{"github", "local", true},
		{"github", "git", true},
		{"forgejo", "forgejo", false},
		{"forgejo", "local", false},
		{"forgejo", "github", false},
		{"jira", "github", false},
		{"jira", "forgejo", false},
		{"local", "github", false},
		{"local", "forgejo", false},
		{"local", "local", false},
		{"jira", "git", false},
	}
	for _, tt := range tests {
		t.Run(tt.tracker+"+"+tt.forge, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.CreateDraftPRURL = "https://example.test/pull/1919"
			it := fc.AsNoLandingRecorder()
			caps := forge.ResolveCapabilities(fc.AsGithubReadOnly(), it, row(t, tt.forge), row(t, tt.tracker))
			m := NewMediation(caps, it, func(num string) string { return "/outbox/" + num }, "main")
			result := dispatch.Result{PRIntent: "feat: add widget\n\nAdds a widget.", PRIntentFound: true}
			if _, _, _, err := m.Open("1919", "agent/issue-1919", result, FallbackNone); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if len(fc.CreateDraftPRCalls) != 1 {
				t.Fatalf("CreateDraftPRCalls = %+v, want 1", fc.CreateDraftPRCalls)
			}
			body := fc.CreateDraftPRCalls[0].Body
			if got := strings.Contains(body, "Closes #1919"); got != tt.wantCloses {
				t.Errorf("body %q: Closes injected = %v, want %v", body, got, tt.wantCloses)
			}
		})
	}
}

// Internal seam: a pure keyword matrix over commit subjects, too wide to drive
// through Settle row by row.
// A Box-authored string embedded verbatim in a reconstructed PR body (issue
// #2447) must never carry a live GitHub closing-keyword reference: a
// prompt-injected Box controls its own commit subjects, and a subject shaped
// like "fix: closes #999" would otherwise auto-close an unrelated issue #999
// on merge.
func TestInternalSeam_DefuseClosingKeywords(t *testing.T) {
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
			// The digits and keyword text must survive: defusing only hides
			// them from the scanner behind a zero-width space.
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
