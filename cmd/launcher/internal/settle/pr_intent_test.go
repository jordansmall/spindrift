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
// Pins closeOnlyOwnIssue's ClosesRefTracker allow-list (issue #2341): only
// the github row gets a "Closes #<num>" reference. The jira, local, and forgejo
// rows get none, forgejo too though it is non-local; the ClosesRefTracker field
// doc gives each row's reason.
func TestInternalSeam_CloseOnlyOwnIssue(t *testing.T) {
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
		{"github tracker, other closes defused, own stays live, no duplicate", "Fixes #1919. Closes #999", "github", "Fixes #1919. Closes #\u200b999"},
		{"github tracker, cross-repo ref to own number defused, appends", "Closes o/r#1919", "github", "Closes o/r#\u200b1919\n\nCloses #1919"},
		{"github tracker, URL ref to own number defused, appends", "Closes https://github.com/o/r/issues/1919", "github", "Closes https://github.com/o/r/issues/\u200b1919\n\nCloses #1919"},
		{"github tracker, closes references a different number, appends", "Adds a widget.\n\nCloses #191", "github", "Adds a widget.\n\nCloses #\u200b191\n\nCloses #1919"},
		{"github tracker, closes references a longer number, appends", "Adds a widget.\n\nCloses #19195", "github", "Adds a widget.\n\nCloses #\u200b19195\n\nCloses #1919"},
		{"github tracker, empty body, becomes exactly Closes", "", "github", "Closes #1919"},
		{"local tracker, no closes reference, unchanged", "Adds a widget.", "local", "Adds a widget."},
		{"local tracker, closing keywords defused, nothing appended", "Fixes #1919. Closes #999", "local", "Fixes #\u200b1919. Closes #\u200b999"},
		{"forgejo tracker, closing keywords defused, nothing appended", "Fixes #1919. Closes #999", "forgejo", "Fixes #\u200b1919. Closes #\u200b999"},
		{"forgejo tracker, no closes reference, unchanged", "Adds a widget.", "forgejo", "Adds a widget."},
		{"jira tracker, no closes reference, unchanged", "Adds a widget.", "jira", "Adds a widget."},
		{"jira tracker, closing keyword defused, nothing appended", "Fixes #1919", "jira", "Fixes #\u200b1919"},
		{"zero descriptor fails closed, unchanged", "Adds a widget.", "", "Adds a widget."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracker backend.Descriptor
			if tt.tracker != "" {
				tracker = row(t, tt.tracker)
			}
			got := closeOnlyOwnIssue(tt.body, num, tracker)
			if got != tt.want {
				t.Errorf("closeOnlyOwnIssue(%q, %q) = %q, want %q", tt.body, num, got, tt.want)
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

// Box-authored PR-intent text must never close an issue but the dispatched one
// (issue #7079): a prompt-injected Box could otherwise name another issue, in
// any of the three reference forms, and auto-close it on merge.
func TestMediation_Open_IntentClosingKeywordsDefused(t *testing.T) {
	const intent = "feat: add widget\n\nFixes #42. Closes #999\nCloses o/r#998\nCloses https://github.com/o/r/issues/997"
	const zwsp = "\u200b"
	for _, tc := range []struct {
		tracker string
		want    []string
		notWant []string
	}{
		// Own #42 stays live and is not re-appended.
		{"github", []string{"Fixes #42.", "Closes #" + zwsp + "999", "o/r#" + zwsp + "998", "issues/" + zwsp + "997"}, []string{"Closes #42", "Closes #999", "o/r#998", "issues/997"}},
		{"jira", []string{"Fixes #" + zwsp + "42", "Closes #" + zwsp + "999", "o/r#" + zwsp + "998", "issues/" + zwsp + "997"}, []string{"Fixes #42", "Closes #42", "Closes #999", "o/r#998", "issues/997"}},
		{"forgejo", []string{"Fixes #" + zwsp + "42", "Closes #" + zwsp + "999", "o/r#" + zwsp + "998", "issues/" + zwsp + "997"}, []string{"Fixes #42", "Closes #42", "Closes #999", "o/r#998", "issues/997"}},
		{"local", []string{"Fixes #" + zwsp + "42", "Closes #" + zwsp + "999", "o/r#" + zwsp + "998", "issues/" + zwsp + "997"}, []string{"Fixes #42", "Closes #42", "Closes #999", "o/r#998", "issues/997"}},
	} {
		t.Run(tc.tracker+"+github", func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.CreateDraftPRURL = "https://example.test/pull/42"
			it := fc.AsNoLandingRecorder()
			caps := forge.ResolveCapabilities(fc.AsGithubReadOnly(), it, row(t, "github"), row(t, tc.tracker))
			m := NewMediation(caps, it, func(num string) string { return "/outbox/" + num }, "main")
			result := dispatch.Result{PRIntent: intent, PRIntentFound: true}
			if _, _, _, err := m.Open("42", "agent/issue-42", result, FallbackNone); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if len(fc.CreateDraftPRCalls) != 1 {
				t.Fatalf("CreateDraftPRCalls = %+v, want 1", fc.CreateDraftPRCalls)
			}
			body := fc.CreateDraftPRCalls[0].Body
			for _, s := range tc.want {
				if !strings.Contains(body, s) {
					t.Errorf("body %q: missing %q", body, s)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(body, s) {
					t.Errorf("body %q: contains live %q", body, s)
				}
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
		{
			name:    "cross-repo reference",
			subject: "Closes o/r#998",
		},
		{
			name:    "issue URL reference",
			subject: "Closes https://github.com/o/r/issues/997",
		},
		{
			name:    "issue URL under a forge subpath",
			subject: "Closes https://git.example/forgejo/o/r/issues/999",
		},
		{
			name:    "Forgejo pulls URL reference",
			subject: "Closes https://git.example/o/r/pulls/996",
		},
		{
			name:    "comma-joined issue URLs",
			subject: "Closes https://github.com/o/r/issues/999,https://github.com/o/r/issues/1",
		},
		{
			name:    "issue URL with a query holding a second issue path",
			subject: "Closes https://github.com/o/r/issues/999?x=/issues/1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defuseClosingKeywords(tt.subject)
			if closingKeywordPattern.MatchString(got) {
				t.Errorf("defuseClosingKeywords(%q) = %q, still matches closingKeywordPattern", tt.subject, got)
			}
			// Not matching is vacuous for a reference the pattern never saw.
			if got == tt.subject {
				t.Errorf("defuseClosingKeywords(%q) left the reference live", tt.subject)
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
