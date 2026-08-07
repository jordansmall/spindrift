package promptassembly

import "testing"

// TestGatesSkillsBaking covers the CAVEMAN_BAKED/TDD_BAKED/COMMIT_BAKED/
// CODE_REVIEW_BAKED gates (entrypoint.sh phase_prompt_assembly, lines
// 727-747): each fires only when the corresponding skill was actually baked
// at DRIVER_SKILLS_DIR/<name>/SKILL.md — a per-skill presence flag the CLI
// boundary resolves via a filesystem stat before ever reaching this pure
// Env, so Gates itself only branches on the already-resolved bool.
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
			},
		},
		{
			name: "every skill baked",
			env: Env{
				CavemanSkillBaked:    true,
				TDDSkillBaked:        true,
				CommitSkillBaked:     true,
				CodeReviewSkillBaked: true,
			},
			want: map[string]bool{
				"CAVEMAN_BAKED":     true,
				"TDD_BAKED":         true,
				"COMMIT_BAKED":      true,
				"CODE_REVIEW_BAKED": true,
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
// WORKER_PROVISIONED (entrypoint.sh: 781-799), each derived from whether
// AgentsJSONTemplate carries a "filer"/"worker" key — orthogonal to
// ORCHESTRATOR, so every combination of the two axes is exercised.
func TestGatesOrchestratorReviewLoop(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want map[string]bool
	}{
		{
			name: "orchestrator off, no agents template",
			env:  Env{},
			want: map[string]bool{
				"ORCHESTRATOR":             false,
				"REVIEW_LOOP_INLINE":       true,
				"REVIEW_LOOP_ORCHESTRATOR": false,
				"FILER_ENABLED":            false,
				"WORKER_PROVISIONED":       false,
			},
		},
		{
			name: "orchestrator on, no agents template",
			env:  Env{OrchestratorEnabled: true},
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
				AgentsJSONTemplate: `{"filer":{"model":"m"},"worker":{"model":"m"}}`,
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
				OrchestratorEnabled: true,
				AgentsJSONTemplate:  `{"filer":{"model":"m"}}`,
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
			name: "agents template present but neither filer nor worker keyed",
			env: Env{
				AgentsJSONTemplate: `{"reviewer":{"model":"m"}}`,
			},
			want: map[string]bool{
				"FILER_ENABLED":      false,
				"WORKER_PROVISIONED": false,
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
// (entrypoint.sh: 958-989): CODE_FORGE (defaulting to "github" when empty)
// resolves to a GH or FORGEJO backend suffix, only forgejo diverging from
// the shared gh-flavored path. OPEN_PR_CREATE_RW_<suffix> forks further on
// BOX_ACCESS_READ_WRITE (only the read-write create step splits on
// CODE_FORGE); FIX_CI_READ_<suffix> fires unconditionally on the resolved
// backend, regardless of box access.
func TestGatesCodeForgeBackend(t *testing.T) {
	cases := []struct {
		name            string
		codeForge       string
		boxWriteEnabled bool
		want            map[string]bool
	}{
		{
			name:            "empty defaults to github, read-write",
			codeForge:       "",
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
			codeForge:       "github",
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
			codeForge:       "github",
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
			codeForge:       "forgejo",
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
			codeForge:       "forgejo",
			boxWriteEnabled: false,
			want: map[string]bool{
				"OPEN_PR_CREATE_RW_GH":      false,
				"OPEN_PR_CREATE_RW_FORGEJO": false,
				"FIX_CI_READ_GH":            false,
				"FIX_CI_READ_FORGEJO":       true,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{CodeForge: tc.codeForge, BoxWriteEnabled: tc.boxWriteEnabled})
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("Gates(CodeForge=%q, BoxWriteEnabled=%v)[%q] = %v, want %v", tc.codeForge, tc.boxWriteEnabled, k, got[k], want)
				}
			}
		})
	}
}
