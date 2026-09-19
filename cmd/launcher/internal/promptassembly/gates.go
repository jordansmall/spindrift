package promptassembly

// Gates computes every gate the prompt fragment registry (lib/fragments.nix)
// reads, with no I/O. Each key is the exact name of the bash local variable
// the gate had in agent/entrypoint.sh's phase_prompt_assembly, which is also
// the name the registry's gate column uses, so the two must stay in step.
func Gates(e Env) map[string]bool {
	g := map[string]bool{}

	// Each gate fires when its skill was baked at DRIVER_SKILLS_DIR/<name>/SKILL.md.
	// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT
	g["CAVEMAN_BAKED"] = e.CavemanSkillBaked
	g["TDD_BAKED"] = e.TDDSkillBaked
	g["COMMIT_BAKED"] = e.CommitSkillBaked
	g["CODE_REVIEW_BAKED"] = e.CodeReviewSkillBaked
	g["AUTO_FORMAT_BAKED"] = e.AutoFormatSkillBaked
	g["AUTO_LINT_BAKED"] = e.AutoLintSkillBaked
	g["CHECK_HYGIENE_BAKED"] = e.CheckHygieneSkillBaked
	g["CODE_COMMENTS_BAKED"] = e.CodeCommentsSkillBaked
	g["NIX_CHECKS_BAKED"] = e.NixChecksSkillBaked
	// END GENERATED SKILL-BAKED GATES

	// Baking the tdd skill subtracts the inline red/green/refactor fallback
	// rather than adding to it (issue #3219), so the off arm needs a gate of
	// its own to render.
	g["TDD_UNBAKED"] = !e.TDDSkillBaked

	// Baking the commit skill subtracts the inline Conventional Commits format
	// rules rather than deferring to the skill on top of them (issue #3222).
	g["COMMIT_UNBAKED"] = !e.CommitSkillBaked

	// The hunt dimensions render unconditionally (issue #3226), so this pair
	// only picks execution mode: fan out to the baked skill's two axes, or
	// hunt every dimension solo (issue #3222).
	g["CODE_REVIEW_UNBAKED"] = !e.CodeReviewSkillBaked

	orchestrator := e.OrchestratorEnabled
	g["ORCHESTRATOR"] = orchestrator

	// Nix resolves this pairing at eval time (issue #2533), but the two forwards
	// cross a process boundary independently of ORCHESTRATOR_ENABLED, so version
	// skew can leave them agreeing instead of exactly one true: an older host
	// launcher forwards neither, and a stuck forward could set both. Repair from
	// the live gate, whose knob predates issue #2533 and survives that skew.
	reviewLoopInline, reviewLoopOrchestrator := e.ReviewLoopInline, e.ReviewLoopOrchestrator
	if reviewLoopInline == reviewLoopOrchestrator {
		reviewLoopInline, reviewLoopOrchestrator = !orchestrator, orchestrator
	}
	g["REVIEW_LOOP_INLINE"] = reviewLoopInline
	g["REVIEW_LOOP_ORCHESTRATOR"] = reviewLoopOrchestrator

	// Nix resolves FILER_ENABLED and WORKER_PROVISIONED at eval time (issue
	// #2533), but SCOUT_PROVISIONED keys off roster membership directly, because
	// opencode provisions scout outside AgentsJSONTemplate.
	g["FILER_ENABLED"] = e.FilerEnabled
	g["WORKER_PROVISIONED"] = e.WorkerProvisioned
	g["SCOUT_PROVISIONED"] = e.ScoutProvisioned

	g["SCOUT_ABSENT"] = !e.ScoutProvisioned

	// Both brief gates exclude research: a research dispatch is scout-less by
	// construction (research-prompt.md never delegates one), so either gate
	// would dangle a "read the brief" instruction on a file research never
	// writes. The fragment registry allows one gate per row, so both
	// conjunctions are computed here rather than nested inside their fragments.
	kind := e.DispatchKind
	if kind == "" {
		kind = defaultDispatchKind
	}
	g["COORDINATOR_SCOUT_BRIEF"] = e.WorkerProvisioned && e.ScoutProvisioned && kind == defaultDispatchKind
	g["WORKER_SCOUT_BRIEF"] = e.ScoutProvisioned && kind == defaultDispatchKind

	for k, v := range trackerGates(e, orchestrator) {
		g[k] = v
	}

	for k, v := range accessForgeGates(e) {
		g[k] = v
	}

	g["AUTO_FORMAT"] = e.AutoFormat
	g["AUTO_LINT"] = e.AutoLint

	// The forwarded value's presence is the gate; there is no separate boolean
	// knob (issue #426).
	g["CI_FAILURE_SUMMARY"] = e.CIFailureSummary != ""

	return g
}
