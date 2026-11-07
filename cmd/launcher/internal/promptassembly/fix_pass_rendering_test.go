package promptassembly

import (
	"strings"
	"testing"
)

const (
	freshMarker = "SYN-FRESH-TEMPLATE"
	fixMarker   = "SYN-FIX-TEMPLATE"
)

// FIX_PASS picks the warm fix template over the cold issue template, and the
// CI failure the launcher captured rides only the fix pass (issues #425,
// #426). The fix template alone references the CI step var, as the real one
// does.
func TestAssembleFixPassRouting(t *testing.T) {
	reg, env0 := syntheticRegistry(t,
		[]FragmentRow{{Gate: "CI_FAILURE_SUMMARY", Fragment: "ci.md", Var: "CI_STEP", ExtraSubstVars: []string{"CI_FAILURE_SUMMARY"}}},
		map[string]string{
			"issue-prompt.md": freshMarker + "\n",
			"fix-prompt.md":   fixMarker + "\n${CI_STEP}\n",
			"fragments/ci.md": "SYN-CI-STEP ${CI_FAILURE_SUMMARY}",
		})

	cases := []struct {
		name    string
		fixPass int
		summary string
		fix     bool
		want    []string
		wantNot []string
	}{
		{name: "FIX_PASS unset drives issue-prompt.md", fixPass: 0},
		{name: "FIX_PASS>0 drives fix-prompt.md", fixPass: 2, fix: true},
		{
			name: "CI_FAILURE_SUMMARY on a fix pass is rendered", fixPass: 2, fix: true,
			summary: "lint: FAILURE\n2 errors in main.go",
			want:    []string{"SYN-CI-STEP lint: FAILURE", "2 errors in main.go"},
		},
		{
			name: "CI_FAILURE_SUMMARY unset on a fix pass leaves no unexpanded token", fixPass: 2, fix: true,
			wantNot: []string{"${CI_FAILURE_SUMMARY}", "${CI_STEP}", "SYN-CI-STEP"},
		},
		{
			name: "CI_FAILURE_SUMMARY is ignored on a fresh run", fixPass: 0, summary: "lint: FAILURE",
			wantNot: []string{"lint: FAILURE", "SYN-CI-STEP"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := env0
			env.FixPass = tc.fixPass
			env.CIFailureSummary = tc.summary

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			marker, other := freshMarker, fixMarker
			if tc.fix {
				marker, other = fixMarker, freshMarker
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

// The gh CI commands do not exist against a Forgejo remote, so the fix pass's
// CI-read step forks on the forge (issue #1963): the forge axis row reads the
// active forge's own folder.
func TestAssembleFixPassCIReadFollowsTheForge(t *testing.T) {
	reg, env0 := syntheticRegistry(t,
		[]FragmentRow{{Axis: "forge", Fragment: "ci-read.md", Var: "CI_READ"}},
		map[string]string{
			"fix-prompt.md":                "SYN-FIX\n${CI_READ}\n",
			"fragments/github/ci-read.md":  "SYN-GITHUB-CI-READ",
			"fragments/forgejo/ci-read.md": "SYN-FORGEJO-CI-READ",
		})

	cases := []struct {
		name    string
		mutate  []func(*Env)
		want    string
		wantNot string
	}{
		{"forgejo reads CI from the forgejo folder", []func(*Env){fragForgejoTracker, fragForgejoForge}, "SYN-FORGEJO-CI-READ", "SYN-GITHUB-CI-READ"},
		{"github reads CI from the github folder", nil, "SYN-GITHUB-CI-READ", "SYN-FORGEJO-CI-READ"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := env0
			env.FixPass = 2
			for _, m := range tc.mutate {
				m(&env)
			}
			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, tc.want) {
				t.Errorf("prompt lacks %q:\n%s", tc.want, result.Prompt)
			}
			if strings.Contains(result.Prompt, tc.wantNot) {
				t.Errorf("prompt has %q, want it absent:\n%s", tc.wantNot, result.Prompt)
			}
		})
	}
}

// The issue placeholders are substituted into the rendered prompt.
func TestAssembleRendersIssuePlaceholders(t *testing.T) {
	reg, env := syntheticRegistry(t, nil, map[string]string{
		"issue-prompt.md": "issue #${ISSUE_NUMBER}: ${ISSUE_TITLE} on ${BRANCH} from ${BASE_BRANCH}; labels ${IN_PROGRESS_LABEL}/${COMPLETE_LABEL}\n",
	})
	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	const want = "issue #2349: Add promptassembly.Assemble on agent/issue-2349 from main; labels agent-in-progress/agent-complete"
	if !strings.Contains(result.Prompt, want) {
		t.Errorf("prompt lacks %q:\n%s", want, result.Prompt)
	}
}

// RUN_NONCE reaches the prompt only where a log-carried PR-intent line
// needs it (issues #1937, #5131): the read-only log carrier substitutes it;
// the socket carrier and read-write runs, which have no nonce-bearing line,
// never mention it.
func TestAssembleRunNonceRendersOnlyOnReadOnlyLogCarrier(t *testing.T) {
	const nonce = "deadbeefcafe1234"
	reg, env0 := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "BOX_ACCESS_READ_ONLY_LOG", Fragment: "ro-log.md", Var: "RO_LOG_STEP"},
			{Gate: "BOX_ACCESS_READ_ONLY_SOCKET", Fragment: "ro-socket.md", Var: "RO_SOCKET_STEP"},
			{Gate: "BOX_ACCESS_READ_WRITE", Fragment: "rw.md", Var: "RW_STEP"},
		},
		map[string]string{
			"issue-prompt.md":        "${RO_LOG_STEP}${RO_SOCKET_STEP}${RW_STEP}\n",
			"fragments/ro-log.md":    "SYN-RO-LOG nonce=${RUN_NONCE}",
			"fragments/ro-socket.md": "SYN-RO-SOCKET",
			"fragments/rw.md":        "SYN-RW",
		})

	for _, tc := range []struct {
		name      string
		readOnly  bool
		carrier   string
		marker    string
		wantNonce bool
	}{
		{"read-only log", true, "log", "SYN-RO-LOG", true},
		{"read-only socket", true, "socket", "SYN-RO-SOCKET", false},
		{"read-write log", false, "log", "SYN-RW", false},
		{"read-write socket", false, "socket", "SYN-RW", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := env0
			env.BoxWriteEnabled = !tc.readOnly
			env.SignalCarrier = tc.carrier
			env.RunNonce = nonce
			got, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(got.Prompt, tc.marker) {
				t.Errorf("prompt lacks %q:\n%s", tc.marker, got.Prompt)
			}
			if has := strings.Contains(got.Prompt, nonce); has != tc.wantNonce {
				t.Errorf("prompt contains the nonce = %v, want %v:\n%s", has, tc.wantNonce, got.Prompt)
			}
		})
	}
}

// A warm fix pass gets the baked-skill steps through the shared-block
// injection (issue #455) rather than its own copy: CAVEMAN_STEP inside the
// COMMS block and the commit anchor inside the CHECK/COMMIT block (issue
// #487) must substitute at render time, and render nothing when unbaked.
func TestAssembleFixPassInjectedBlocksCarryBakedSkillSteps(t *testing.T) {
	reg, env0 := syntheticRegistry(t,
		[]FragmentRow{
			{Gate: "CAVEMAN_BAKED", Fragment: "caveman.md", Var: "CAVEMAN_STEP"},
			{Gate: "COMMIT_BAKED", Fragment: "commit-baked.md", Var: "COMMIT_BAKED_STEP"},
			{Gate: "COMMIT_UNBAKED", Fragment: "commit-unbaked.md", Var: "COMMIT_UNBAKED_STEP"},
		},
		map[string]string{
			"fix-prompt.md":               "SYN-FIX\n",
			"fragments/caveman.md":        "SYN-CAVEMAN",
			"fragments/commit-baked.md":   "SYN-COMMIT-BAKED",
			"fragments/commit-unbaked.md": "SYN-COMMIT-UNBAKED",
		})

	for _, baked := range []bool{true, false} {
		name := "unbaked"
		if baked {
			name = "baked"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			env := env0
			env.FixPass = 2
			env.CavemanSkillBaked = baked
			env.CommitSkillBaked = baked
			env.CommsContractFile = writeContractFile(t, dir, "comms-contract.md", "# COMMS\n\n${CAVEMAN_STEP}body text\n")
			env.CheckContractFile = writeContractFile(t, dir, "check-contract.md", "# CHECK\n\n${COMMIT_BAKED_STEP}${COMMIT_UNBAKED_STEP}tail text\n")

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			for s, want := range map[string]bool{
				"SYN-CAVEMAN":        baked,
				"SYN-COMMIT-BAKED":   baked,
				"SYN-COMMIT-UNBAKED": !baked,
				"body text":          true,
				"tail text":          true,
			} {
				if got := strings.Contains(result.Prompt, s); got != want {
					t.Errorf("Prompt contains %q = %v, want %v:\n%s", s, got, want, result.Prompt)
				}
			}
			for _, s := range []string{"${CAVEMAN_STEP}", "${COMMIT_BAKED_STEP}", "${COMMIT_UNBAKED_STEP}"} {
				if strings.Contains(result.Prompt, s) {
					t.Errorf("Prompt still contains literal %s", s)
				}
			}
		})
	}
}

// Recover runs no Box and its row names no prompt: assembling for it must say
// so, not fail reading the prompts directory itself.
func TestAssembleKindWithoutPromptErrors(t *testing.T) {
	reg, env0 := syntheticRegistry(t, nil, nil)
	for _, fixPass := range []int{0, 2} {
		env := env0
		env.DispatchKind = "recover"
		env.FixPass = fixPass
		_, err := Assemble(env, reg)
		if err == nil || !strings.Contains(err.Error(), "kind recover has no prompt") {
			t.Errorf("FixPass=%d: Assemble err = %v, want \"kind recover has no prompt\"", fixPass, err)
		}
	}
}
