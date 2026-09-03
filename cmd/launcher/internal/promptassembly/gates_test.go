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
			// correctly. Before issue #2533, entrypoint.sh's own bash
			// negation of $ORCHESTRATOR guaranteed exactly one of the pair
			// fired regardless; Gates now reproduces that same negation as
			// a version-skew safety net (falling back to the live
			// ORCHESTRATOR gate computed a few lines above) rather than
			// leaving both off and breaking the exactly-one-true invariant
			// (env.go: 78-91).
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

// TestGatesScoutProvisioned covers SCOUT_PROVISIONED (issue #3157): a plain
// passthrough of the nix-resolved Env.ScoutProvisioned roster fact, the same
// shape as FILER_ENABLED/WORKER_PROVISIONED above.
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

// TestGatesScoutAbsent covers SCOUT_ABSENT (issue #3157): the exact
// complement of SCOUT_PROVISIONED, the same paired shape as
// REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR -- exactly one of the two
// gates is ever on for a given Env, so the `# SCOUT` section's body (the
// concatenation of both arms' vars) never renders both or neither.
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

// TestGatesTDDUnbaked covers TDD_UNBAKED (issue #3219): the exact
// complement of TDD_BAKED, the same paired shape as SCOUT_PROVISIONED/
// SCOUT_ABSENT above -- the IMPLEMENT section's test-first body is the
// concatenation of both arms' vars, so exactly one of the anchor line and
// the full inline red/green/refactor fallback ever renders.
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

// TestGatesCoordinatorScoutBrief covers COORDINATOR_SCOUT_BRIEF (issue
// #3157): a computed conjunction of Env.WorkerProvisioned and
// Env.ScoutProvisioned, not a passthrough of either alone -- the
// coordinator's scout-brief guidance is only meaningful when there's both a
// worker to delegate to and a scout that wrote a brief, and the fragment
// registry allows only one gate per row, so all four combinations need
// covering to pin down that it's an AND, not an OR or either field alone.
// It also shares WORKER_SCOUT_BRIEF's work-only restriction: a research
// dispatch never writes a brief, so a research-kind env must gate false even
// with both fields on.
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

// TestGatesWorkerScoutBrief covers WORKER_SCOUT_BRIEF (issue #3157): a
// work-only conjunction of Env.ScoutProvisioned and dispatch kind --
// ScoutProvisioned alone is a roster-presence fact true on a research
// dispatch too, but research-prompt.md never delegates a scout or writes a
// brief, so the gate must also check DispatchKind, defaulting empty to
// "work" the same way gates_tracker.go/assemble.go already do.
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
			// family is fully wired up. Before issue #2533, entrypoint.sh's
			// own bash "${CODE_FORGE:-github}" defaulting guaranteed a real
			// gate fired regardless (including FIX_CI_READ's unconditional-
			// on-backend fork); Gates now reproduces that same default arm
			// as a version-skew safety net so an old-launcher/new-box
			// pairing renders the GH arm instead of silently dropping
			// every PR-create/CI-read instruction for the run. This pins
			// that fail-open contract so a future change can't silently
			// reintroduce the fail-closed regression.
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
			// still forwarded on Env for exactly this fallback (env.go:
			// 133-138) -- says "forgejo". The fallback must re-derive from
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
