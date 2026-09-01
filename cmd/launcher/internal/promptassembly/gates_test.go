package promptassembly

import "testing"

// TestGatesSkillsBaking covers the CAVEMAN_BAKED/TDD_BAKED/COMMIT_BAKED/
// CODE_REVIEW_BAKED/AUTO_FORMAT_BAKED/AUTO_LINT_BAKED gates (entrypoint.sh
// phase_prompt_assembly): each fires only when the
// corresponding skill was actually baked at DRIVER_SKILLS_DIR/<name>/
// SKILL.md — a per-skill presence flag the CLI boundary resolves via a
// filesystem stat before ever reaching this pure Env, so Gates itself only
// branches on the already-resolved bool. AUTO_FORMAT_BAKED/AUTO_LINT_BAKED
// exist for consistency/completeness of the generated skill-baked family,
// not because any fragment row gates on them yet.
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
				"CAVEMAN_BAKED":     false,
				"TDD_BAKED":         false,
				"COMMIT_BAKED":      false,
				"CODE_REVIEW_BAKED": false,
				"AUTO_FORMAT_BAKED": false,
				"AUTO_LINT_BAKED":   false,
			},
		},
		{
			name: "every skill baked",
			env: Env{
				CavemanSkillBaked:    true,
				TDDSkillBaked:        true,
				CommitSkillBaked:     true,
				CodeReviewSkillBaked: true,
				AutoFormatSkillBaked: true,
				AutoLintSkillBaked:   true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     true,
				"TDD_BAKED":         true,
				"COMMIT_BAKED":      true,
				"CODE_REVIEW_BAKED": true,
				"AUTO_FORMAT_BAKED": true,
				"AUTO_LINT_BAKED":   true,
			},
		},
		{
			name: "only caveman baked",
			env: Env{
				CavemanSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     true,
				"TDD_BAKED":         false,
				"COMMIT_BAKED":      false,
				"CODE_REVIEW_BAKED": false,
				"AUTO_FORMAT_BAKED": false,
				"AUTO_LINT_BAKED":   false,
			},
		},
		{
			name: "only auto-format baked",
			env: Env{
				AutoFormatSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     false,
				"TDD_BAKED":         false,
				"COMMIT_BAKED":      false,
				"CODE_REVIEW_BAKED": false,
				"AUTO_FORMAT_BAKED": true,
				"AUTO_LINT_BAKED":   false,
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

// TestGatesCodeCommentsMandatory covers CODE_COMMENTS_MANDATORY (issue
// #2880): unlike WORKER_PROVISIONED, this gate carries no Env field and is
// always true, regardless of what else Env sets -- so the code-comments
// rule reaches worker-prompt.md on every Driver, including opencode, where
// WorkerProvisioned stays false by design even when a worker exists.
func TestGatesCodeCommentsMandatory(t *testing.T) {
	for name, env := range map[string]Env{
		"zero-value Env{}": {},
		"coveredEnv()":     coveredEnv(),
	} {
		t.Run(name, func(t *testing.T) {
			if got := Gates(env)["CODE_COMMENTS_MANDATORY"]; !got {
				t.Errorf("Gates(%s)[%q] = %v, want true", name, "CODE_COMMENTS_MANDATORY", got)
			}
		})
	}
}

// TestGatesOrchestratorReviewLoop covers ORCHESTRATOR (entrypoint.sh),
// the REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR exactly-one-on
// pairing it drives (entrypoint.sh), and FILER_ENABLED/
// WORKER_PROVISIONED (entrypoint.sh) -- since issue #2533, all four
// gates are plain passthroughs of nix-precomputed Env fields
// (ReviewLoopInline/ReviewLoopOrchestrator/FilerEnabled/WorkerProvisioned)
// rather than derived in-box from OrchestratorEnabled/AgentsJSONTemplate, so
// each case sets its Env fields explicitly rather than relying on Gates to
// re-derive them.
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
			// ReviewLoopInline/ReviewLoopOrchestrator's zero value: the
			// shape a version-skew dispatch leaves behind, not just a
			// stray `Env{}` literal. BOX_REVIEW_LOOP_INLINE/ORCHESTRATOR
			// are dispatch-time-only forwards (issue #2533) with no baked
			// preamble default, so an older host launcher binary that
			// predates issue #2533 (and therefore never sets either env
			// var) dispatching against a newer box image leaves both false
			// here even though ORCHESTRATOR_ENABLED itself -- a
			// pre-existing knob issue #2533 left untouched -- still arrives
			// correctly. Gates falls back to negating the live
			// ORCHESTRATOR gate as a version-skew safety net rather
			// than leaving both off and breaking the exactly-one-true
			// invariant.
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
			// Both fields forwarded true (issue #2533 review): the two
			// fields cross a process boundary independently of each other,
			// so a stuck/duplicated forward can in principle leave both
			// true rather than only ever both-false. The same repair as
			// the both-false case above must apply here too, or Gates
			// renders both review-loop prompt sections at once.
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

// TestGatesBoxAccess covers the OPEN A PULL REQUEST push step gate
// (entrypoint.sh): exactly one of BOX_ACCESS_READ_WRITE/
// BOX_ACCESS_READ_ONLY is ever on, selected solely by BOX_WRITE_ENABLED --
// independent of ISSUE_TRACKER/CODE_FORGE.
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

// TestGatesCodeForgeBackend covers the CODE_FORGE-backend gate family
// (entrypoint.sh): ForgeBackend -- nix's precomputed equivalent of
// CODE_FORGE (defaulting to "github" when empty), resolved upstream rather
// than re-derived by Gates itself (issue #2533) -- is a GH or FORGEJO
// backend suffix, only forgejo diverging from the shared gh-flavored path.
// OPEN_PR_CREATE_RW_<suffix> forks further on BOX_ACCESS_READ_WRITE (only
// the read-write create step splits on the backend); FIX_CI_READ_<suffix>
// fires unconditionally on the resolved backend, regardless of box access.
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
			// ForgeBackend's zero value: the shape a version-skew dispatch
			// leaves behind, not just a stray `Env{}` literal. BOX_FORGE_
			// BACKEND is a dispatch-time-only forward (issue #2533) with no
			// baked preamble default, so an older host launcher binary that
			// predates issue #2533 (and therefore never sets that env var
			// at all) dispatching against a newer box image leaves
			// ForgeBackend empty here even though the access-forge gate
			// family is fully wired up. Gates defaults to the GH arm as a
			// version-skew safety net so an old-launcher/new-box pairing
			// renders it instead of silently dropping every
			// PR-create/CI-read instruction for the run.
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
			// Same version-skew shape as above, but CodeForge itself --
			// still forwarded on Env for exactly this fallback (env.go)
			// -- says "forgejo". The fallback must re-derive from
			// CodeForge, not hardcode the GH arm regardless of it (issue
			// #2533 review): hardcoding GH here would instruct the agent to
			// drive `gh` against a Forgejo forge.
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
