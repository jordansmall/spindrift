package promptassembly

import "testing"

// TestGatesSkillsBaking covers the CAVEMAN_BAKED/TDD_BAKED/COMMIT_BAKED/
// CODE_REVIEW_BAKED/AUTO_FORMAT_BAKED/AUTO_LINT_BAKED gates (entrypoint.sh
// phase_prompt_assembly, lines 733-739): each fires only when the
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

// TestGatesOrchestratorReviewLoop covers ORCHESTRATOR (entrypoint.sh:
// 761-762), the REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR exactly-one-on
// pairing it drives (entrypoint.sh: 771-779), and FILER_ENABLED/
// WORKER_PROVISIONED (entrypoint.sh: 781-799) -- since issue #2533, all four
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
// (entrypoint.sh: 940-957): exactly one of BOX_ACCESS_READ_WRITE/
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
// (entrypoint.sh: 958-989): ForgeBackend -- nix's precomputed equivalent of
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
			// ForgeBackend's zero value: an Env built without populating it
			// (e.g. a stray `Env{}` literal, or an upstream caller that
			// forgot to thread nix's resolved backend through). Mirrors
			// TestGatesIssueTrackerReadAxis's "empty TrackerAxisRead fails
			// closed" case: nix is the sole source of truth for a non-empty
			// backend value, so Gates fails closed (every gate off,
			// including FIX_CI_READ's unconditional-on-backend fork) rather
			// than silently guessing GH -- an empty ForgeBackend must never
			// silently drop every PR-create/CI-read instruction without a
			// loud signal that nix's resolution never reached here (issue
			// #2533 review).
			name:            "empty ForgeBackend fails closed: no gate fires",
			forgeBackend:    "",
			boxWriteEnabled: true,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       false,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{ForgeBackend: tc.forgeBackend, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(ForgeBackend=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.forgeBackend, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}
