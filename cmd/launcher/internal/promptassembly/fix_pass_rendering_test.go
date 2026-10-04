package promptassembly

import (
	"strings"
	"testing"
)

const (
	freshPromptMarker = "Fresh clone, new branch"
	fixPromptMarker   = "already checked out"
)

// FIX_PASS picks the warm fix prompt over the cold issue prompt, and the CI
// failure the launcher captured rides only the fix pass (issues #425, #426).
func TestAssembleFixPassRouting(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name    string
		fixPass int
		summary string
		fix     bool
		want    []string
		wantNot []string
	}{
		{name: "FIX_PASS unset drives issue-prompt.md", fixPass: 0, wantNot: []string{fixPromptMarker}},
		{name: "FIX_PASS>0 drives fix-prompt.md", fixPass: 2, fix: true, wantNot: []string{freshPromptMarker}},
		{
			name: "CI_FAILURE_SUMMARY on a fix pass is rendered", fixPass: 2, fix: true,
			summary: "lint: FAILURE\n2 errors in main.go",
			want:    []string{"lint: FAILURE", "2 errors in main.go"},
		},
		{
			name: "CI_FAILURE_SUMMARY unset on a fix pass leaves no unexpanded token", fixPass: 2, fix: true,
			wantNot: []string{"${CI_FAILURE_SUMMARY}"},
		},
		{
			name: "CI_FAILURE_SUMMARY is ignored on a fresh run", fixPass: 0, summary: "lint: FAILURE",
			wantNot: []string{"lint: FAILURE"},
		},
		{
			// The branch's own history may be rewritten under either access
			// mode: a read-only Box never pushes, the host relays the branch
			// (issue #2462, #3225), so the clause naming the mechanism is gone.
			name: "the history-rewrite line does not presuppose the Box pushed", fixPass: 2, fix: true,
			want:    []string{"Rewriting the branch's own unmerged"},
			wantNot: []string{"The branch already force-pushes"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.FixPass = tc.fixPass
			env.CIFailureSummary = tc.summary

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			marker, other := freshPromptMarker, fixPromptMarker
			if tc.fix {
				marker, other = fixPromptMarker, freshPromptMarker
			}
			if !strings.Contains(result.Prompt, marker) {
				t.Errorf("prompt lacks %q:\n%s", marker, result.Prompt)
			}
			if strings.Contains(result.Prompt, other) {
				t.Errorf("prompt has %q, want the other template:\n%s", other, result.Prompt)
			}
			for _, s := range tc.want {
				if !strings.Contains(result.Prompt, s) {
					t.Errorf("prompt lacks %q:\n%s", s, result.Prompt)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(result.Prompt, s) {
					t.Errorf("prompt has %q, want it absent:\n%s", s, result.Prompt)
				}
			}
		})
	}
}

// The gh CI commands do not exist against a Forgejo remote, so the fix
// pass's CONTEXT CI-read step forks on the forge (issue #1963). Scoped to
// that section: the shared contract below keeps an unconditional gh pr view.
func TestAssembleFixPassCIReadFollowsTheForge(t *testing.T) {
	reg := loadTestRegistry(t)

	cases := []struct {
		name    string
		mutate  []func(*Env)
		want    string
		wantNot string
	}{
		{"forgejo reads CI via fj pr status", []func(*Env){fragForgejoTracker, fragForgejoForge}, "fj pr status", "gh pr view"},
		{"github reads CI via gh pr view", nil, "gh pr view", "fj pr status"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := coveredEnv()
			env.FixPass = 2
			for _, m := range tc.mutate {
				m(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			ctx := promptSection(t, result.Prompt, "# CONTEXT", "# FIX")
			if !strings.Contains(ctx, tc.want) {
				t.Errorf("CONTEXT lacks %q:\n%s", tc.want, ctx)
			}
			if strings.Contains(ctx, tc.wantNot) {
				t.Errorf("CONTEXT has %q, want it absent:\n%s", tc.wantNot, ctx)
			}
		})
	}
}

// RUN_NONCE reaches the rendered prompt (issue #1937) and the issue
// placeholders are substituted.
func TestAssembleRendersIssuePlaceholdersAndRunNonce(t *testing.T) {
	env := coveredEnv()
	env.RunNonce = "deadbeefcafe1234"
	result, err := Assemble(env, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, want := range []string{
		"Implement GitHub issue #2349: Add promptassembly.Assemble",
		"agent/issue-2349", "cut from", "deadbeefcafe1234",
	} {
		if !strings.Contains(result.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, result.Prompt)
		}
	}
}
