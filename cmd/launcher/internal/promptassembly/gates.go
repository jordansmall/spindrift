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
	g["PRINCIPLE_FIX_ROOT_CAUSES_BAKED"] = e.PrincipleFixRootCausesSkillBaked
	g["PRINCIPLE_LAZINESS_PROTOCOL_BAKED"] = e.PrincipleLazinessProtocolSkillBaked
	g["PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED"] = e.PrincipleRedesignFromFirstPrinciplesSkillBaked
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

	// FILER_ENABLED and WORKER_PROVISIONED come from agentsJsonTemplate's
	// rendered output (issue #2533); SCOUT_PROVISIONED keys off roster
	// membership instead — see lib/mkHarness.nix's scoutProvisioned comment.
	g["FILER_ENABLED"] = e.FilerEnabled
	g["WORKER_PROVISIONED"] = e.WorkerProvisioned
	g["SCOUT_PROVISIONED"] = e.ScoutProvisioned

	g["SCOUT_ABSENT"] = !e.ScoutProvisioned

	// Both brief gates exclude every advise-only kind (research today): an
	// advise-only dispatch is scout-less by construction (research-prompt.md
	// never delegates one), so either gate would dangle a "read the brief"
	// instruction on a file that kind never writes. The fragment registry
	// allows one gate per row, so both conjunctions are computed here rather
	// than nested inside their fragments.
	landsCode := !e.descriptor().AdviseOnly
	g["COORDINATOR_SCOUT_BRIEF"] = e.WorkerProvisioned && e.ScoutProvisioned && landsCode
	g["WORKER_SCOUT_BRIEF"] = e.ScoutProvisioned && landsCode

	for k, v := range trackerGates(e) {
		g[k] = v
	}

	for k, v := range accessForgeGates(e) {
		g[k] = v
	}

	// Empty or unknown means log although the schema default is "socket"
	// (issue #4376): an older host launcher forwards nothing and still speaks
	// the log carrier.
	socket := e.SignalCarrier == "socket"
	g["SIGNAL_CARRIER_SOCKET"] = socket

	// These gates carry a signal fragment (comment/PR-intent/issue-intent),
	// each split into a log/socket pair so the fragment registry's one row
	// per rendered fragment can pick the carrier variant. Both members are
	// false whenever the base gate is off, so this is a plain conjunction
	// rather than the exactly-one-on inverseOf mechanic (lib/fragment-pairs.nix).
	for _, base := range []string{
		"ISSUE_TRACKER_GITHUB_READONLY",
		"ISSUE_TRACKER_LOCAL",
		"ISSUE_TRACKER_FORGEJO_READONLY",
		"BOX_ACCESS_READ_ONLY",
		"FILER_FILE_RELAY",
	} {
		g[base+"_LOG"] = g[base] && !socket
		g[base+"_SOCKET"] = g[base] && socket
	}

	g["AUTO_FORMAT"] = e.AutoFormat
	g["AUTO_LINT"] = e.AutoLint

	// The forwarded value's presence is the gate; there is no separate boolean
	// knob (issue #426).
	g["CI_FAILURE_SUMMARY"] = e.CIFailureSummary != ""

	// Same presence-is-the-gate shape (ADR 0057, issue #4073).
	g["CHORE_PATCH_CLASSES"] = e.ChorePatchClasses != ""

	return g
}
