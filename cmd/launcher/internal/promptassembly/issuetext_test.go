package promptassembly

import (
	"strings"
	"testing"
)

// Issue #3445 requires that an empty Env.IssueText append nothing: no stray
// separator, no empty section, no zero-byte ISSUE_TEXT source.
func TestIssueTextSectionEmpty(t *testing.T) {
	if got := issueTextSection(Env{IssueNumber: "42"}); got != "" {
		t.Fatalf("issueTextSection with empty IssueText = %q, want empty", got)
	}
}

// The fence must widen past the issue text's own longest backtick run, so
// quoted issue or comment content can never close the section's fence and
// impersonate host-authored prompt structure (CLAUDE.md's comment-injection
// trust boundary).
func TestIssueTextSectionFencesContent(t *testing.T) {
	got := issueTextSection(Env{IssueNumber: "42", IssueText: "payload with ```escape``` attempt"})
	if !strings.Contains(got, "# ISSUE TEXT") {
		t.Fatalf("issueTextSection missing heading:\n%s", got)
	}
	if !strings.Contains(got, "Issue #42's body") {
		t.Fatalf("issueTextSection missing substituted issue number:\n%s", got)
	}
	if !strings.Contains(got, "````\npayload with ```escape``` attempt\n````") {
		t.Fatalf("issueTextSection did not widen the fence past the payload's own backtick run:\n%s", got)
	}
}

// In both the base and review body shapes, the rendered template body stays a
// byte-identical prefix, joined to the appended section by exactly "\n\n".
func TestAssembleAppendsIssueTextSection(t *testing.T) {
	reg, env0 := syntheticRegistry(t, nil, nil)

	baseEnv := env0
	baseEnv.IssueText = "issue body text"

	t.Run("base prompt", func(t *testing.T) {
		withoutText := env0
		without, err := Assemble(withoutText, reg)
		if err != nil {
			t.Fatalf("Assemble (without IssueText): %v", err)
		}
		templateBody := without.Prompt

		result, err := Assemble(baseEnv, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		section := issueTextSection(baseEnv)
		want := templateBody + "\n\n" + section
		if result.Prompt != want {
			t.Fatalf("Prompt mismatch:\n got: %q\nwant: %q", result.Prompt, want)
		}
	})

	t.Run("review prompt", func(t *testing.T) {
		reviewEnv := baseEnv

		withoutText := reviewEnv
		withoutText.IssueText = ""
		without, err := Assemble(withoutText, reg)
		if err != nil {
			t.Fatalf("Assemble (without IssueText): %v", err)
		}
		templateBody := without.ReviewPromptText

		result, err := Assemble(reviewEnv, reg)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}

		section := issueTextSection(reviewEnv)
		want := templateBody + "\n\n" + section
		if result.ReviewPromptText != want {
			t.Fatalf("ReviewPromptText mismatch:\n got: %q\nwant: %q", result.ReviewPromptText, want)
		}
	})
}

// A direct ${ISSUE_TEXT} reference must resolve to the same rendered section.
func TestAssembleIssueTextVarSubstitution(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{
		"issue-prompt.md": "# TASK\n\ninline: ${ISSUE_TEXT}\n",
	})
	env.IssueText = "issue body text"

	bodies, err := assemblePromptBodies(env, reg)
	if err != nil {
		t.Fatalf("assemblePromptBodies: %v", err)
	}

	section := issueTextSection(env)
	if got := bodies.vars["ISSUE_TEXT"].text(); got != section {
		t.Fatalf("vars[ISSUE_TEXT] = %q, want %q", got, section)
	}
	// The inline ${ISSUE_TEXT} reference resolves through vars, then
	// the section is appended again as the run-stable suffix, so the rendered
	// base carries two copies.
	want := "# TASK\n\ninline: " + section + "\n\n" + section
	if bodies.base.text() != want {
		t.Fatalf("base.text() = %q, want %q", bodies.base.text(), want)
	}
}
