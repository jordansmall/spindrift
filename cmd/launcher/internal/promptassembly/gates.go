package promptassembly

// Gates computes, as a pure function with no I/O, every gate the prompt
// fragment registry (lib/fragments.nix) selects on. Keys are the exact
// variable names the registry's gate column uses (e.g. "CAVEMAN_BAKED",
// "ORCHESTRATOR").
func Gates(e Env) map[string]bool {
	g := map[string]bool{}

	// Each fires when its skill was baked at DRIVER_SKILLS_DIR/<name>/SKILL.md.
	// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT
	g["CAVEMAN_BAKED"] = e.CavemanSkillBaked
	g["TDD_BAKED"] = e.TDDSkillBaked
	g["COMMIT_BAKED"] = e.CommitSkillBaked
	g["CODE_REVIEW_BAKED"] = e.CodeReviewSkillBaked
	g["AUTO_FORMAT_BAKED"] = e.AutoFormatSkillBaked
	g["AUTO_LINT_BAKED"] = e.AutoLintSkillBaked
	// END GENERATED SKILL-BAKED GATES

	// The master-switch gate every orchestrator-conditioned fork reads.
	orchestrator := e.OrchestratorEnabled
	g["ORCHESTRATOR"] = orchestrator

	// Exactly one of REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR is ever on.
	// Both are dispatch-time-only forwards with no baked preamble default, so
	// they cross the process boundary independently of $ORCHESTRATOR and
	// version skew can leave them agreeing either way: an older host launcher
	// forwards neither, a stuck/duplicated forward could set both. Either
	// break of the exactly-one-true invariant (env.go) is repaired the same
	// way, by re-deriving the pairing from ORCHESTRATOR — a baked boxEnv knob
	// that arrives correctly even under skew.
	reviewLoopInline, reviewLoopOrchestrator := e.ReviewLoopInline, e.ReviewLoopOrchestrator
	if reviewLoopInline == reviewLoopOrchestrator {
		reviewLoopInline, reviewLoopOrchestrator = !orchestrator, orchestrator
	}
	g["REVIEW_LOOP_INLINE"] = reviewLoopInline
	g["REVIEW_LOOP_ORCHESTRATOR"] = reviewLoopOrchestrator

	// Presence of the corresponding roster key in AgentsJSONTemplate, already
	// resolved at nix eval time.
	g["FILER_ENABLED"] = e.FilerEnabled
	g["WORKER_PROVISIONED"] = e.WorkerProvisioned

	// Always true. worker-prompt.md's code-comments rule is unconditional, so
	// it can't reuse WORKER_PROVISIONED — that gate is false for the opencode
	// Driver by design even when a worker exists (see lib/mkHarness.nix's
	// workerProvisioned comment), which silently dropped the rule from
	// opencode workers. The gate exists only to route the rule through the
	// Conditional fragment registry's render/gate/substitute plumbing.
	g["CODE_COMMENTS_MANDATORY"] = true

	for k, v := range trackerGates(e, orchestrator) {
		g[k] = v
	}

	for k, v := range accessForgeGates(e) {
		g[k] = v
	}

	g["AUTO_FORMAT"] = e.AutoFormat
	g["AUTO_LINT"] = e.AutoLint

	// Fires on a non-empty forwarded value, not a separate boolean knob.
	g["CI_FAILURE_SUMMARY"] = e.CIFailureSummary != ""

	return g
}
