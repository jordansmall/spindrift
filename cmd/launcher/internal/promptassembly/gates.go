package promptassembly

// Gates reproduces, as a pure function with no I/O, every computed gate
// agent/entrypoint.sh's phase_prompt_assembly derives from e before
// rendering the prompt fragment registry (lib/fragments.nix). The returned
// map is keyed by the exact bash local-variable name each gate corresponds
// to (e.g. "CAVEMAN_BAKED", "ORCHESTRATOR") — the same name the fragment
// registry's gate column names and the bash loop reads via "${!_fgate}" —
// so the mapping back to entrypoint.sh stays traceable one key at a time.
func Gates(e Env) map[string]bool {
	g := map[string]bool{}

	// Skill-baking gates (entrypoint.sh: 733-739): each fires exactly when
	// its corresponding skill was baked at DRIVER_SKILLS_DIR/<name>/SKILL.md.
	// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT
	g["CAVEMAN_BAKED"] = e.CavemanSkillBaked
	g["TDD_BAKED"] = e.TDDSkillBaked
	g["COMMIT_BAKED"] = e.CommitSkillBaked
	g["CODE_REVIEW_BAKED"] = e.CodeReviewSkillBaked
	g["AUTO_FORMAT_BAKED"] = e.AutoFormatSkillBaked
	g["AUTO_LINT_BAKED"] = e.AutoLintSkillBaked
	// END GENERATED SKILL-BAKED GATES

	// ORCHESTRATOR (entrypoint.sh: 761-762): the single canonical
	// master-switch gate every orchestrator-conditioned fork reads.
	orchestrator := e.OrchestratorEnabled
	g["ORCHESTRATOR"] = orchestrator

	// REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR (entrypoint.sh: 771-779):
	// exactly one is ever on, picked by $ORCHESTRATOR alone -- nix already
	// resolves the pairing at eval time (issue #2533), so this is now a
	// plain passthrough of Env.ReviewLoopInline/Env.ReviewLoopOrchestrator
	// rather than Gates negating/copying ORCHESTRATOR itself.
	//
	// BOX_REVIEW_LOOP_INLINE/ORCHESTRATOR are dispatch-time-only forwards
	// with no baked preamble default, so an older host launcher binary that
	// predates issue #2533 -- and therefore never sets either env var at
	// all -- dispatching against a newer box image leaves both false here.
	// ORCHESTRATOR_ENABLED itself is a pre-existing boxEnv knob issue #2533
	// left untouched, so `orchestrator` above still arrives correctly even
	// under that version skew. The two forwarded fields cross a process
	// boundary independently of $ORCHESTRATOR, so version skew can leave
	// them agreeing with each other instead of only ever both-false: an
	// older host launcher that never forwards either field leaves both
	// false, while a forward that's stuck/duplicated could in principle
	// leave both true. Either way the exactly-one-true invariant
	// (env.go: 78-91) is broken, so repair both agreeing cases the same
	// way, by deriving the pairing from the live ORCHESTRATOR gate the same
	// way entrypoint.sh's old bash negation did, rather than repairing only
	// the both-false arm and rendering both review-loop sections on a
	// both-true forward.
	reviewLoopInline, reviewLoopOrchestrator := e.ReviewLoopInline, e.ReviewLoopOrchestrator
	if reviewLoopInline == reviewLoopOrchestrator {
		reviewLoopInline, reviewLoopOrchestrator = !orchestrator, orchestrator
	}
	g["REVIEW_LOOP_INLINE"] = reviewLoopInline
	g["REVIEW_LOOP_ORCHESTRATOR"] = reviewLoopOrchestrator

	// FILER_ENABLED/WORKER_PROVISIONED (entrypoint.sh: 781-799): each fires
	// when the roster nix also bakes into AgentsJSONTemplate carries the
	// corresponding top-level key. nix already resolves this presence fact
	// at eval time (issue #2533), so this is now a plain passthrough of
	// Env.FilerEnabled/Env.WorkerProvisioned rather than Gates reparsing
	// AgentsJSONTemplate's JSON for the same answer.
	g["FILER_ENABLED"] = e.FilerEnabled
	g["WORKER_PROVISIONED"] = e.WorkerProvisioned

	// Issue-Tracker gate family (entrypoint.sh: 801-814, 816-860, 862-938):
	// the tracker read/write/filer descriptor gates and the PR-body
	// ticket-reference gates, computed in gates_tracker.go and merged in.
	for k, v := range trackerGates(e, orchestrator) {
		g[k] = v
	}

	for k, v := range accessForgeGates(e) {
		g[k] = v
	}

	// AUTO_FORMAT/AUTO_LINT (lib/fragments.nix ~152-159): plain passthrough
	// presence gates, not derived from any other state -- Env.AutoFormat/
	// Env.AutoLint already carry entrypoint.sh's old
	// "[ -n "${AUTO_FORMAT:-}" ]"/"[ -n "${AUTO_LINT:-}" ]" checks verbatim.
	g["AUTO_FORMAT"] = e.AutoFormat
	g["AUTO_LINT"] = e.AutoLint

	// CI_FAILURE_SUMMARY (lib/fragments.nix ~162-166): fires exactly when
	// the launcher forwarded a non-empty CI_FAILURE_SUMMARY (issue #426),
	// mirroring entrypoint.sh's old "[ -n "${CI_FAILURE_SUMMARY:-}" ]"
	// presence check on the value itself, not a separate boolean knob.
	g["CI_FAILURE_SUMMARY"] = e.CIFailureSummary != ""

	return g
}
