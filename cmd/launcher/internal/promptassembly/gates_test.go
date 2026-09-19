package promptassembly

import "testing"

// Each *_BAKED gate (entrypoint.sh phase_prompt_assembly) fires only when the
// CLI boundary found the skill at DRIVER_SKILLS_DIR/<name>/SKILL.md, so Gates
// only branches on the already-resolved bool. AUTO_FORMAT_BAKED and
// AUTO_LINT_BAKED gate no fragment row and exist for family completeness;
// CHECK_HYGIENE_BAKED and CODE_COMMENTS_BAKED each gate one (issues #3220, #3221).
func TestGatesSkillsBaking(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want map[string]bool
	}{
		{
			name: "no skill baked",
			env:  Env{},
			want: map[string]bool{
				"CAVEMAN_BAKED":       false,
				"TDD_BAKED":           false,
				"COMMIT_BAKED":        false,
				"CODE_REVIEW_BAKED":   false,
				"AUTO_FORMAT_BAKED":   false,
				"AUTO_LINT_BAKED":     false,
				"CHECK_HYGIENE_BAKED": false,
				"CODE_COMMENTS_BAKED": false,
			},
		},
		{
			name: "every skill baked",
			env: Env{
				CavemanSkillBaked:      true,
				TDDSkillBaked:          true,
				CommitSkillBaked:       true,
				CodeReviewSkillBaked:   true,
				AutoFormatSkillBaked:   true,
				AutoLintSkillBaked:     true,
				CheckHygieneSkillBaked: true,
				CodeCommentsSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":       true,
				"TDD_BAKED":           true,
				"COMMIT_BAKED":        true,
				"CODE_REVIEW_BAKED":   true,
				"AUTO_FORMAT_BAKED":   true,
				"AUTO_LINT_BAKED":     true,
				"CHECK_HYGIENE_BAKED": true,
				"CODE_COMMENTS_BAKED": true,
			},
		},
		{
			name: "only caveman baked",
			env: Env{
				CavemanSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":       true,
				"TDD_BAKED":           false,
				"COMMIT_BAKED":        false,
				"CODE_REVIEW_BAKED":   false,
				"AUTO_FORMAT_BAKED":   false,
				"AUTO_LINT_BAKED":     false,
				"CHECK_HYGIENE_BAKED": false,
				"CODE_COMMENTS_BAKED": false,
			},
		},
		{
			name: "only auto-format baked",
			env: Env{
				AutoFormatSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":       false,
				"TDD_BAKED":           false,
				"COMMIT_BAKED":        false,
				"CODE_REVIEW_BAKED":   false,
				"AUTO_FORMAT_BAKED":   true,
				"AUTO_LINT_BAKED":     false,
				"CHECK_HYGIENE_BAKED": false,
				"CODE_COMMENTS_BAKED": false,
			},
		},
		{
			name: "only check-hygiene baked",
			env: Env{
				CheckHygieneSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":       false,
				"TDD_BAKED":           false,
				"COMMIT_BAKED":        false,
				"CODE_REVIEW_BAKED":   false,
				"AUTO_FORMAT_BAKED":   false,
				"AUTO_LINT_BAKED":     false,
				"CHECK_HYGIENE_BAKED": true,
				"CODE_COMMENTS_BAKED": false,
			},
		},
		{
			name: "only code-comments baked",
			env: Env{
				CodeCommentsSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":       false,
				"TDD_BAKED":           false,
				"COMMIT_BAKED":        false,
				"CODE_REVIEW_BAKED":   false,
				"AUTO_FORMAT_BAKED":   false,
				"AUTO_LINT_BAKED":     false,
				"CHECK_HYGIENE_BAKED": false,
				"CODE_COMMENTS_BAKED": true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(tc.env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", tc.env, k, got[k], want)
				}
			}
		})
	}
}

// Since issue #2533, ORCHESTRATOR, the REVIEW_LOOP_INLINE/ORCHESTRATOR
// exactly-one-on pair, FILER_ENABLED and WORKER_PROVISIONED (entrypoint.sh:
// 761-799) are passthroughs of nix-precomputed Env fields rather than derived
// in-box, so each case sets those fields explicitly instead of relying on
// Gates to re-derive them.
func TestGatesOrchestratorReviewLoop(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want map[string]bool
	}{
		{
			name: "orchestrator off, no roster",
			env: Env{
				ReviewLoopInline: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "orchestrator on, no roster",
			env: Env{
				OrchestratorEnabled:    true,
				ReviewLoopOrchestrator: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "orchestrator off, filer and worker both provisioned",
			env: Env{
				ReviewLoopInline:  true,
				FilerEnabled:      true,
				WorkerProvisioned: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
				"FILER_ENABLED":            true,
				"WORKER_PROVISIONED":       true,
			},
		},
		{
			name: "orchestrator on, only filer provisioned",
			env: Env{
				OrchestratorEnabled:    true,
				ReviewLoopOrchestrator: true,
				FilerEnabled:           true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
				"FILER_ENABLED":            true,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "roster present but neither filer nor worker provisioned",
			env: Env{
				AgentsJSONTemplate: `{"reviewer":{"model":"m"}}`,
				ReviewLoopInline:   true,
			},
			want: map[string]bool{
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
			},
		},
		{
			// The zero value here is what a version-skew dispatch leaves
			// behind, not a stray `Env{}`: a host launcher predating issue
			// #2533 never sets BOX_REVIEW_LOOP_INLINE/ORCHESTRATOR, which
			// have no baked default. Gates reproduces entrypoint.sh's old
			// bash negation so exactly one of the pair stays on (env.go: 78-91).
			name: "both review-loop fields empty, orchestrator off: falls open to inline",
			env: Env{
				OrchestratorEnabled: false,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
			},
		},
		{
			// Same version-skew scenario as above, but with
			// ORCHESTRATOR_ENABLED itself on: the fallback must track the
			// live ORCHESTRATOR gate, not hardcode inline regardless of it.
			name: "both review-loop fields empty, orchestrator on: falls open to orchestrator",
			env: Env{
				OrchestratorEnabled: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
			},
		},
		{
			// The two fields cross a process boundary independently, so a
			// stuck or duplicated forward can leave both true, not only both
			// false (issue #2533 review). The both-false repair must apply
			// here too, or Gates renders both review-loop sections at once.
			name: "both review-loop fields true, orchestrator off: repairs to inline",
			env: Env{
				OrchestratorEnabled:    false,
				ReviewLoopInline:       true,
				ReviewLoopOrchestrator: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
			},
		},
		{
			name: "both review-loop fields true, orchestrator on: repairs to orchestrator",
			env: Env{
				OrchestratorEnabled:    true,
				ReviewLoopInline:       true,
				ReviewLoopOrchestrator: true,
			},
			want: map[string]bool{
				"ORCHESTRATOR":             true,
				"REVIEW_LOOP_INLINE":       false,
				"REVIEW_LOOP_ORCHESTRATOR": true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(tc.env)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(%+v)[%q] = %v, want %v", tc.env, k, got[k], want)
				}
			}
		})
	}
}

// SCOUT_PROVISIONED is a plain passthrough of the nix-resolved
// Env.ScoutProvisioned roster fact (issue #3157), the same shape as
// FILER_ENABLED and WORKER_PROVISIONED above.
func TestGatesScoutProvisioned(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want bool
	}{
		{name: "scout not provisioned", env: Env{}, want: false},
		{name: "scout provisioned", env: Env{ScoutProvisioned: true}, want: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := Gates(tc.env)["SCOUT_PROVISIONED"]; got != tc.want {
				t.Errorf("Gates(%+v)[%q] = %v, want %v", tc.env, "SCOUT_PROVISIONED", got, tc.want)
			}
		})
	}
}

// SCOUT_ABSENT is the exact complement of SCOUT_PROVISIONED (issue #3157). The
// `# SCOUT` section's body concatenates both arms' vars, so exactly one gate
// must be on or the section renders both arms or neither.
func TestGatesScoutAbsent(t *testing.T) {
	cases := []struct {
		name             string
		scoutProvisioned bool
	}{
		{name: "scout not provisioned", scoutProvisioned: false},
		{name: "scout provisioned", scoutProvisioned: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{ScoutProvisioned: tc.scoutProvisioned}
			got := Gates(env)
			if got["SCOUT_ABSENT"] == got["SCOUT_PROVISIONED"] {
				t.Fatalf("Gates(%+v)[\"SCOUT_ABSENT\"] = %v, [\"SCOUT_PROVISIONED\"] = %v, want exact complements", env, got["SCOUT_ABSENT"], got["SCOUT_PROVISIONED"])
			}
			if got["SCOUT_ABSENT"] != !tc.scoutProvisioned {
				t.Errorf("Gates(%+v)[\"SCOUT_ABSENT\"] = %v, want %v", env, got["SCOUT_ABSENT"], !tc.scoutProvisioned)
			}
		})
	}
}

// TDD_UNBAKED is the exact complement of TDD_BAKED (issue #3219). The IMPLEMENT
// section's test-first body concatenates both arms' vars, so exactly one of the
// anchor line and the inline red/green/refactor fallback ever renders.
func TestGatesTDDUnbaked(t *testing.T) {
	cases := []struct {
		name          string
		tddSkillBaked bool
	}{
		{name: "tdd skill not baked", tddSkillBaked: false},
		{name: "tdd skill baked", tddSkillBaked: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{TDDSkillBaked: tc.tddSkillBaked}
			got := Gates(env)
			if got["TDD_UNBAKED"] == got["TDD_BAKED"] {
				t.Fatalf("Gates(%+v)[\"TDD_UNBAKED\"] = %v, [\"TDD_BAKED\"] = %v, want exact complements", env, got["TDD_UNBAKED"], got["TDD_BAKED"])
			}
			if got["TDD_UNBAKED"] != !tc.tddSkillBaked {
				t.Errorf("Gates(%+v)[\"TDD_UNBAKED\"] = %v, want %v", env, got["TDD_UNBAKED"], !tc.tddSkillBaked)
			}
		})
	}
}

// COMMIT_UNBAKED is the exact complement of COMMIT_BAKED (issue #3222). The
// COMMIT section's format-rules body concatenates both arms' vars, so exactly
// one of the anchor line and the inline Conventional Commits rules renders.
func TestGatesCommitUnbaked(t *testing.T) {
	cases := []struct {
		name             string
		commitSkillBaked bool
	}{
		{name: "commit skill not baked", commitSkillBaked: false},
		{name: "commit skill baked", commitSkillBaked: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{CommitSkillBaked: tc.commitSkillBaked}
			got := Gates(env)
			if got["COMMIT_UNBAKED"] == got["COMMIT_BAKED"] {
				t.Fatalf("Gates(%+v)[\"COMMIT_UNBAKED\"] = %v, [\"COMMIT_BAKED\"] = %v, want exact complements", env, got["COMMIT_UNBAKED"], got["COMMIT_BAKED"])
			}
			if got["COMMIT_UNBAKED"] != !tc.commitSkillBaked {
				t.Errorf("Gates(%+v)[\"COMMIT_UNBAKED\"] = %v, want %v", env, got["COMMIT_UNBAKED"], !tc.commitSkillBaked)
			}
		})
	}
}

// CODE_REVIEW_UNBAKED is the exact complement of CODE_REVIEW_BAKED (issue
// #3222). The review prompt's dimensions-hunting body concatenates both arms'
// vars, so exactly one of the anchor line and the inline SPEC/CORRECTNESS/
// SECURITY/STANDARDS & SMELLS coaching ever renders.
func TestGatesCodeReviewUnbaked(t *testing.T) {
	cases := []struct {
		name                 string
		codeReviewSkillBaked bool
	}{
		{name: "code-review skill not baked", codeReviewSkillBaked: false},
		{name: "code-review skill baked", codeReviewSkillBaked: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{CodeReviewSkillBaked: tc.codeReviewSkillBaked}
			got := Gates(env)
			if got["CODE_REVIEW_UNBAKED"] == got["CODE_REVIEW_BAKED"] {
				t.Fatalf("Gates(%+v)[\"CODE_REVIEW_UNBAKED\"] = %v, [\"CODE_REVIEW_BAKED\"] = %v, want exact complements", env, got["CODE_REVIEW_UNBAKED"], got["CODE_REVIEW_BAKED"])
			}
			if got["CODE_REVIEW_UNBAKED"] != !tc.codeReviewSkillBaked {
				t.Errorf("Gates(%+v)[\"CODE_REVIEW_UNBAKED\"] = %v, want %v", env, got["CODE_REVIEW_UNBAKED"], !tc.codeReviewSkillBaked)
			}
		})
	}
}

// COORDINATOR_SCOUT_BRIEF is a conjunction of WorkerProvisioned and
// ScoutProvisioned (issue #3157), so all four combinations are covered to pin
// that it is an AND rather than an OR or either field alone. It shares
// WORKER_SCOUT_BRIEF's work-only restriction: a research dispatch never writes
// a brief, so it gates false even with both fields on.
func TestGatesCoordinatorScoutBrief(t *testing.T) {
	cases := []struct {
		name              string
		dispatchKind      string
		workerProvisioned bool
		scoutProvisioned  bool
		want              bool
	}{
		{name: "neither provisioned", workerProvisioned: false, scoutProvisioned: false, want: false},
		{name: "only worker provisioned", workerProvisioned: true, scoutProvisioned: false, want: false},
		{name: "only scout provisioned", workerProvisioned: false, scoutProvisioned: true, want: false},
		{name: "both provisioned", workerProvisioned: true, scoutProvisioned: true, want: true},
		{name: "both provisioned, research dispatch", dispatchKind: "research", workerProvisioned: true, scoutProvisioned: true, want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{DispatchKind: tc.dispatchKind, WorkerProvisioned: tc.workerProvisioned, ScoutProvisioned: tc.scoutProvisioned}
			if got := Gates(env)["COORDINATOR_SCOUT_BRIEF"]; got != tc.want {
				t.Errorf("Gates(%+v)[%q] = %v, want %v", env, "COORDINATOR_SCOUT_BRIEF", got, tc.want)
			}
		})
	}
}

// WORKER_SCOUT_BRIEF is work-only (issue #3157): ScoutProvisioned alone is a
// roster-presence fact that holds on a research dispatch too, but
// research-prompt.md never delegates a scout or writes a brief, so the gate
// also checks DispatchKind, defaulting empty to "work" as gates_tracker.go and
// assemble.go do.
func TestGatesWorkerScoutBrief(t *testing.T) {
	cases := []struct {
		name          string
		dispatchKind  string
		scoutProvided bool
		want          bool
	}{
		{name: "research dispatch, scout provisioned", dispatchKind: "research", scoutProvided: true, want: false},
		{name: "work dispatch, scout provisioned", dispatchKind: "work", scoutProvided: true, want: true},
		{name: "default dispatch kind, scout provisioned", dispatchKind: "", scoutProvided: true, want: true},
		{name: "work dispatch, scout not provisioned", dispatchKind: "work", scoutProvided: false, want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := Env{DispatchKind: tc.dispatchKind, ScoutProvisioned: tc.scoutProvided}
			if got := Gates(env)["WORKER_SCOUT_BRIEF"]; got != tc.want {
				t.Errorf("Gates(%+v)[%q] = %v, want %v", env, "WORKER_SCOUT_BRIEF", got, tc.want)
			}
		})
	}
}

// Exactly one of BOX_ACCESS_READ_WRITE and BOX_ACCESS_READ_ONLY is ever on for
// the OPEN A PULL REQUEST push step (entrypoint.sh: 940-957), selected solely
// by BOX_WRITE_ENABLED, independent of ISSUE_TRACKER and CODE_FORGE.
func TestGatesBoxAccess(t *testing.T) {
	cases := []struct {
		name            string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "write-enabled",
			boxWriteEnabled: true,
			want: map[string]bool{
				"BOX_ACCESS_READ_WRITE": true,
				"BOX_ACCESS_READ_ONLY":  false,
			},
		},
		{
			name:            "read-only",
			boxWriteEnabled: false,
			want: map[string]bool{
				"BOX_ACCESS_READ_WRITE": false,
				"BOX_ACCESS_READ_ONLY":  true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(BoxWriteEnabled=%v)[%q] = %v, want %v", tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}

// ForgeBackend is nix's precomputed equivalent of CODE_FORGE (entrypoint.sh:
// 958-989), resolved upstream rather than re-derived by Gates (issue #2533).
// Only forgejo diverges from the shared gh-flavored path.
// OPEN_PR_CREATE_RW_<suffix> forks further on BOX_ACCESS_READ_WRITE, while
// FIX_CI_READ_<suffix> fires on the resolved backend regardless of box access.
func TestGatesCodeForgeBackend(t *testing.T) {
	cases := []struct {
		name            string
		forgeBackend    string
		codeForge       string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "empty CODE_FORGE resolves upstream to GH, read-write",
			forgeBackend:    "GH",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      true,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "github explicit, read-write",
			forgeBackend:    "GH",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      true,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "github explicit, read-only: OPEN_PR_CREATE_RW off, FIX_CI_READ still on",
			forgeBackend:    "GH",
			boxWriteEnabled: false,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			name:            "forgejo, read-write",
			forgeBackend:    "FORGEJO",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": true,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
		{
			name:            "forgejo, read-only: OPEN_PR_CREATE_RW off, FIX_CI_READ still on",
			forgeBackend:    "FORGEJO",
			boxWriteEnabled: false,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
		{
			// An empty ForgeBackend is what a version-skew dispatch leaves
			// behind: a host launcher predating issue #2533 never sets
			// BOX_FORGE_BACKEND, which has no baked default. Gates repeats
			// entrypoint.sh's old "${CODE_FORGE:-github}" default, so the run
			// gets the GH arm instead of no PR-create or CI-read step at all.
			name:            "empty ForgeBackend falls open to GH default",
			forgeBackend:    "",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      true,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            true,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
		{
			// Same version-skew shape, but CodeForge says "forgejo" (it is
			// still forwarded on Env for exactly this fallback, env.go:
			// 133-138). The fallback must re-derive from CodeForge: a
			// hardcoded GH arm would tell the agent to drive `gh` against a
			// Forgejo forge (issue #2533 review).
			name:            "empty ForgeBackend with CodeForge=forgejo falls open to FORGEJO",
			forgeBackend:    "",
			codeForge:       "forgejo",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": true,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{ForgeBackend: tc.forgeBackend, CodeForge: tc.codeForge, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(ForgeBackend=%q, CodeForge=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.forgeBackend, tc.codeForge, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}
