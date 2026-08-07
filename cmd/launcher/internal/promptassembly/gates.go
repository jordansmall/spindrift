package promptassembly

import "encoding/json"

// Gates reproduces, as a pure function with no I/O, every computed gate
// agent/entrypoint.sh's phase_prompt_assembly derives from e before
// rendering the prompt fragment registry (lib/fragments.nix). The returned
// map is keyed by the exact bash local-variable name each gate corresponds
// to (e.g. "CAVEMAN_BAKED", "ORCHESTRATOR") — the same name the fragment
// registry's gate column names and the bash loop reads via "${!_fgate}" —
// so the mapping back to entrypoint.sh stays traceable one key at a time.
func Gates(e Env) map[string]bool {
	g := map[string]bool{}

	// Skill-baking gates (entrypoint.sh: 736-747): each fires exactly when
	// its corresponding skill was baked at DRIVER_SKILLS_DIR/<name>/SKILL.md.
	g["CAVEMAN_BAKED"] = e.CavemanSkillBaked
	g["TDD_BAKED"] = e.TDDSkillBaked
	g["COMMIT_BAKED"] = e.CommitSkillBaked
	g["CODE_REVIEW_BAKED"] = e.CodeReviewSkillBaked

	// ORCHESTRATOR (entrypoint.sh: 761-762): the single canonical
	// master-switch gate every orchestrator-conditioned fork reads.
	orchestrator := e.OrchestratorEnabled
	g["ORCHESTRATOR"] = orchestrator

	// REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR (entrypoint.sh: 771-779):
	// exactly one is ever on, picked by $ORCHESTRATOR alone.
	g["REVIEW_LOOP_INLINE"] = !orchestrator
	g["REVIEW_LOOP_ORCHESTRATOR"] = orchestrator

	// FILER_ENABLED/WORKER_PROVISIONED (entrypoint.sh: 781-799): each fires
	// when AgentsJSONTemplate carries the corresponding top-level key. bash
	// resolves this with `jq -e 'has("filer"|"worker")'`; unmarshalling into
	// a map and checking key presence is the pure Go equivalent. An empty
	// or malformed template behaves as "no such key" (jq -e likewise fails,
	// leaving the gate off), never a panic.
	agentsKeys := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(e.AgentsJSONTemplate), &agentsKeys)
	_, hasFiler := agentsKeys["filer"]
	_, hasWorker := agentsKeys["worker"]
	g["FILER_ENABLED"] = hasFiler
	g["WORKER_PROVISIONED"] = hasWorker

	// Issue-Tracker gate family (entrypoint.sh: 801-814, 816-860, 862-938):
	// the tracker read/write/filer descriptor gates and the PR-body
	// ticket-reference gates, computed in gates_tracker.go and merged in.
	for k, v := range trackerGates(e, hasFiler, orchestrator) {
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
