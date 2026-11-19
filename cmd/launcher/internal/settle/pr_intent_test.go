package settle

import (
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

const zwsp = "\u200b" // U+200B ZERO WIDTH SPACE

// Internal seam: a pure body-text matrix; the Settle draft-PR rows pin only the
// append and already-closed cases.
// Pins closeOnlyOwnIssue's body handling (append, dedup, defuse, empty body)
// and that it appends only when backend.ClosesRef(forge, tracker) holds, the
// same predicate the prompt's PR_BODY_CLOSES gate uses (issue #7058). A zero
// Descriptor on either side fails closed.
func TestInternalSeam_CloseOnlyOwnIssue(t *testing.T) {
	const num = "1919"
	tests := []struct {
		name    string
		body    string
		forge   string // backend row name; "" is the zero Descriptor
		tracker string // backend row name; "" is the zero Descriptor
		want    string
	}{
		{"github pair, no closes reference, appends", "Adds a widget.", "github", "github", "Adds a widget.\n\nCloses #1919"},
		{"github pair, already has Closes, unchanged", "Adds a widget.\n\nCloses #1919", "github", "github", "Adds a widget.\n\nCloses #1919"},
		{"github pair, already has Fixes, unchanged", "Adds a widget.\n\nFixes #1919", "github", "github", "Adds a widget.\n\nFixes #1919"},
		{"github pair, already has colon-form Closes, unchanged", "Adds a widget.\n\nCloses: #1919", "github", "github", "Adds a widget.\n\nCloses: #1919"},
		{"github pair, other closes defused, own stays live, no duplicate", "Fixes #1919. Closes #999", "github", "github", "Fixes #1919. C" + zwsp + "loses #999"},
		{"github pair, cross-repo ref to own number defused, appends", "Closes o/r#1919", "github", "github", "C" + zwsp + "loses o/r#1919\n\nCloses #1919"},
		{"github pair, URL ref to own number defused, appends", "Closes https://github.com/o/r/issues/1919", "github", "github", "C" + zwsp + "loses https://github.com/o/r/issues/1919\n\nCloses #1919"},
		{"github pair, emphasised own keyword defused, appends", "**Closes** #1919", "github", "github", "**C" + zwsp + "loses** #1919\n\nCloses #1919"},
		{"github pair, closes references a different number, appends", "Adds a widget.\n\nCloses #191", "github", "github", "Adds a widget.\n\nC" + zwsp + "loses #191\n\nCloses #1919"},
		{"github pair, closes references a longer number, appends", "Adds a widget.\n\nCloses #19195", "github", "github", "Adds a widget.\n\nC" + zwsp + "loses #19195\n\nCloses #1919"},
		{"github pair, letter after own number, defused, appends", "Closes #1919abc", "github", "github", "C" + zwsp + "loses #1919abc\n\nCloses #1919"},
		{"github pair, underscore after own number, defused, appends", "Closes #1919_x", "github", "github", "C" + zwsp + "loses #1919_x\n\nCloses #1919"},
		{"github pair, period then letter after own number, defused, appends", "Closes #1919.x", "github", "github", "C" + zwsp + "loses #1919.x\n\nCloses #1919"},
		{"github pair, long-s lookalike keyword defused, appends", "cloſes #1919", "github", "github", "c" + zwsp + "loſes #1919\n\nCloses #1919"},
		{"github pair, two own closes both stay live, nothing appended", "Fixes #1919. Closes #1919", "github", "github", "Fixes #1919. Closes #1919"},
		{"github pair, double space before own number, defused, appends", "Closes  #1919", "github", "github", "C" + zwsp + "loses  #1919\n\nCloses #1919"},
		{"github pair, tab before own number, defused, appends", "Closes\t#1919", "github", "github", "C" + zwsp + "loses\t#1919\n\nCloses #1919"},
		{"github pair, underscore before own keyword, defused, appends", "x_Closes #1919", "github", "github", "x_C" + zwsp + "loses #1919\n\nCloses #1919"},
		{"github pair, asterisk before own keyword, defused, appends", "**Closes #1919**", "github", "github", "**C" + zwsp + "loses #1919**\n\nCloses #1919"},
		{"github pair, open bracket before own keyword, unchanged", "[Closes #1919]", "github", "github", "[Closes #1919]"},
		{"github pair, trailing period, unchanged", "Adds a widget.\n\nCloses #1919.", "github", "github", "Adds a widget.\n\nCloses #1919."},
		{"github pair, trailing close paren, unchanged", "(Closes #1919)", "github", "github", "(Closes #1919)"},
		{"github pair, own number then newline, unchanged", "Closes #1919\nAdds a widget.", "github", "github", "Closes #1919\nAdds a widget."},
		{"github pair, empty body, becomes exactly Closes", "", "github", "github", "Closes #1919"},
		{"forgejo pair, no closes reference, appends", "Adds a widget.", "forgejo", "forgejo", "Adds a widget.\n\nCloses #1919"},
		{"forgejo pair, other closes defused, own stays live", "Fixes #1919. Closes #999", "forgejo", "forgejo", "Fixes #1919. C" + zwsp + "loses #999"},
		{"github tracker on a forgejo forge, unchanged", "Adds a widget.", "forgejo", "github", "Adds a widget."},
		{"github tracker on a forgejo forge, closing keywords defused, nothing appended", "Fixes #1919. Closes #999", "forgejo", "github", "F" + zwsp + "ixes #1919. C" + zwsp + "loses #999"},
		{"local tracker, no closes reference, unchanged", "Adds a widget.", "github", "local", "Adds a widget."},
		{"local tracker, closing keywords defused, nothing appended", "Fixes #1919. Closes #999", "github", "local", "F" + zwsp + "ixes #1919. C" + zwsp + "loses #999"},
		{"forgejo tracker on a github forge, closing keywords defused, nothing appended", "Fixes #1919. Closes #999", "github", "forgejo", "F" + zwsp + "ixes #1919. C" + zwsp + "loses #999"},
		{"jira tracker, no closes reference, unchanged", "Adds a widget.", "github", "jira", "Adds a widget."},
		{"jira tracker, closing keyword defused, nothing appended", "Fixes #1919", "github", "jira", "F" + zwsp + "ixes #1919"},
		{"zero forge descriptor fails closed, unchanged", "Adds a widget.", "", "github", "Adds a widget."},
		{"zero tracker descriptor fails closed, unchanged", "Adds a widget.", "github", "", "Adds a widget."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var forgeRow, trackerRow backend.Descriptor
			if tt.forge != "" {
				forgeRow = row(t, tt.forge)
			}
			if tt.tracker != "" {
				trackerRow = row(t, tt.tracker)
			}
			got := closeOnlyOwnIssue(tt.body, num, forgeRow, trackerRow)
			if got != tt.want {
				t.Errorf("closeOnlyOwnIssue(%q, %q) = %q, want %q", tt.body, num, got, tt.want)
			}
		})
	}
}

// The gate is backend.ClosesRef(forge, tracker), the predicate the prompt's
// PR_BODY_CLOSES uses: a Closes keyword only auto-closes when the forge shares
// the tracker's issue namespace, so a github tracker on a forgejo, local, or
// git forge appends nothing (#7058).
func TestMediation_Open_ClosesGateFollowsClosesRef(t *testing.T) {
	tests := []struct {
		tracker, forge string
		wantCloses     bool
	}{
		{"github", "github", true},
		{"github", "forgejo", false},
		{"github", "local", false},
		{"github", "git", false},
		{"forgejo", "forgejo", true},
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
// (issue #7079): a prompt-injected Box could otherwise name another issue after
// a closing keyword, in any reference form, and auto-close it on merge.
func TestMediation_Open_IntentClosingKeywordsDefused(t *testing.T) {
	const intent = "feat: add widget\n\nFixes #42. Closes #999\nCloses o/r#998\nCloses https://github.com/o/r/issues/997"
	defused := []string{"C" + zwsp + "loses #999", "C" + zwsp + "loses o/r#998", "C" + zwsp + "loses https://github.com/o/r/issues/997"}
	live := []string{"Closes #999", "Closes o/r#998", "Closes https://github.com/o/r/issues/997"}
	for _, tc := range []struct {
		tracker string
		want    []string
		notWant []string
	}{
		// Own #42 stays live and is not re-appended.
		{"github", append([]string{"Fixes #42."}, defused...), append([]string{"Closes #42"}, live...)},
		{"jira", append([]string{"F" + zwsp + "ixes #42"}, defused...), append([]string{"Fixes #42", "Closes #42"}, live...)},
		{"forgejo", append([]string{"F" + zwsp + "ixes #42"}, defused...), append([]string{"Fixes #42", "Closes #42"}, live...)},
		{"local", append([]string{"F" + zwsp + "ixes #42"}, defused...), append([]string{"Fixes #42", "Closes #42"}, live...)},
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
// A Box-authored string embedded verbatim in a reconstructed PR body (issues
// #2447, #7248) must never carry a live closing keyword: the keyword set is
// closed while reference syntaxes are open-ended, so U+200B goes inside the
// keyword and every reference form dies with it.
func TestInternalSeam_DefuseClosingKeywords(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"GH- prefix", "Closes GH-9", "C" + zwsp + "loses GH-9"},
		{"no space after keyword", "Closes#9", "C" + zwsp + "loses#9"},
		{"colon no space", "Closes:#9", "C" + zwsp + "loses:#9"},
		{"plain hash ref", "Closes #9", "C" + zwsp + "loses #9"},
		{"bang ref", "Closes !9", "C" + zwsp + "loses !9"},
		{"cross-repo bang ref", "Closes o/r!9", "C" + zwsp + "loses o/r!9"},
		{"angle autolink", "Closes <https://github.com/o/r/issues/999>", "C" + zwsp + "loses <https://github.com/o/r/issues/999>"},
		{"markdown link", "Closes [x](https://github.com/o/r/issues/999)", "C" + zwsp + "loses [x](https://github.com/o/r/issues/999)"},
		{"bold keyword", "**Closes** #999", "**C" + zwsp + "loses** #999"},
		{"underscore emphasis", "_Closes #999_", "_C" + zwsp + "loses #999_"},
		{"scheme-less URL", "Closes github.com/o/r/issues/999", "C" + zwsp + "loses github.com/o/r/issues/999"},
		{"www URL", "Closes www.github.com/o/r/issues/999", "C" + zwsp + "loses www.github.com/o/r/issues/999"},
		{"multi-issues URL", "Closes https://github.com/o/r/issues/1/o/r/issues/999", "C" + zwsp + "loses https://github.com/o/r/issues/1/o/r/issues/999"},
		{"full issue URL", "Closes https://github.com/o/r/issues/999", "C" + zwsp + "loses https://github.com/o/r/issues/999"},
		{"NBSP after keyword", "Closes\u00a0#9", "C" + zwsp + "loses\u00a0#9"},
		{"parenthesised ref", "Closes (#999)", "C" + zwsp + "loses (#999)"},
		{"bold whole phrase", "**Closes #999**", "**C" + zwsp + "loses #999**"},
		{"tab after keyword", "Closes\t#9", "C" + zwsp + "loses\t#9"},
		{"double space after keyword", "Closes  #9", "C" + zwsp + "loses  #9"},
		{"two refs", "Closes #999 and #1000", "C" + zwsp + "loses #999 and #1000"},
		{"fix before closes", "fix: closes #999", "f" + zwsp + "ix: c" + zwsp + "loses #999"},
		{"colon then space", "Closes: #1234", "C" + zwsp + "loses: #1234"},
		{"fix fix adjacent", "fix fix", "f" + zwsp + "ix f" + zwsp + "ix"},
		{"close inflections", "close closes closed", "c" + zwsp + "lose c" + zwsp + "loses c" + zwsp + "losed"},
		{"fix inflections", "fix fixes fixed", "f" + zwsp + "ix f" + zwsp + "ixes f" + zwsp + "ixed"},
		{"resolve inflections", "resolve resolves resolved", "r" + zwsp + "esolve r" + zwsp + "esolves r" + zwsp + "esolved"},
		{"mixed case", "FIXES #1 Resolved #2 cLoSeD #3", "F" + zwsp + "IXES #1 R" + zwsp + "esolved #2 c" + zwsp + "LoSeD #3"},
		{"CJK before keyword", "见Closes #999", "见C" + zwsp + "loses #999"},
		{"accented letter before keyword", "éCloses #999", "éC" + zwsp + "loses #999"},
		{"CJK after keyword", "Closes见 #999", "C" + zwsp + "loses见 #999"},
		{"digit before keyword unchanged", "9Closes #999", "9Closes #999"},
		{"link target path segment", "[diff](https://x/docs/fix-login.md)", "[diff](https://x/docs/f" + zwsp + "ix-login.md)"},
		{"prefix unchanged", "prefix #9", "prefix #9"},
		{"fixture unchanged", "fixture #9", "fixture #9"},
		{"closest unchanged", "closest #9", "closest #9"},
		{"unresolved unchanged", "unresolved #9", "unresolved #9"},
		{"no keywords", "no keywords here", "no keywords here"},
		{"conventional subject", "feat: add a widget", "feat: add a widget"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defuseClosingKeywords(tt.in); got != tt.want {
				t.Errorf("defuseClosingKeywords(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// liveClosingRef reports whether body still holds a closing keyword naming
// "#num". It is an oracle independent of the production predicates: a plain
// regexp that a U+200B inside the keyword never matches.
func liveClosingRef(body, num string) bool {
	re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):? #` + regexp.QuoteMeta(num) + `(?:\s|$|[)\]'"]|[:;,.?!](?:\s|$))`)
	return re.MatchString(body)
}
