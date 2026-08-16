// Package promptassembly reproduces, in Go, the pure gate computations
// agent/entrypoint.sh's phase_prompt_assembly derives from launcher-forwarded
// env vars before rendering the prompt fragment registry (lib/fragments.nix).
// Env carries one typed field per raw input phase_prompt_assembly reads;
// Gates (gates.go) turns an Env into the same named booleans the bash
// function computes as locals (CAVEMAN_BAKED, ORCHESTRATOR, ISSUE_TRACKER_
// GITHUB, and so on) — the gate variable names the fragment registry itself
// reads via "${!_fgate}".
//
// A bash presence-check gate ("[ -n "$VAR" ]") converts to a plain Go bool
// field here; a filesystem-derived presence flag (the skill-baked checks,
// which bash resolves with `[ -f "$DRIVER_SKILLS_DIR/<name>/SKILL.md" ]`)
// likewise arrives pre-resolved as a bool, since Gates itself performs no
// I/O — the CLI boundary (a later slice) is what actually stats the skills
// directory and AGENTS_JSON_TEMPLATE-adjacent files before constructing Env.
package promptassembly

// Default values entrypoint.sh's "${VAR:-default}" bash parameter expansion
// applies when the corresponding Env field arrives empty. Named once here so
// checkCoveredCell (assemble.go, DispatchKind only as of issue #2540),
// Gates, and issueTrackerAxis (gates_tracker.go) resolve the same default
// rather than each restating its own "github"/"work" literal.
const (
	defaultIssueTracker = "github"
	defaultCodeForge    = "github"
	defaultDispatchKind = "work"
)

// Env is the full set of raw inputs agent/entrypoint.sh's
// phase_prompt_assembly reads. Only a subset feeds Gates's computed
// booleans today; the rest (contract file paths, prompt dirs, and similar
// passthrough surface) round out the type for later slices (Assemble, the
// CLI verb) that render the rest of the prompt, not just its gates.
type Env struct {
	// Skill-baking presence flags (entrypoint.sh: 733-739). Each is true
	// only when DRIVER_SKILLS_DIR/<name>/SKILL.md exists — bash resolves
	// the stat itself; Env only ever sees the already-computed flag.
	// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT
	CavemanSkillBaked    bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" (CAVEMAN_BAKED)
	TDDSkillBaked        bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" (TDD_BAKED)
	CommitSkillBaked     bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" (COMMIT_BAKED)
	CodeReviewSkillBaked bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" (CODE_REVIEW_BAKED)
	AutoFormatSkillBaked bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" (AUTO_FORMAT_BAKED)
	AutoLintSkillBaked   bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" (AUTO_LINT_BAKED)
	// END GENERATED SKILL-BAKED FIELDS

	// OrchestratorEnabled is the launcher-delivered master switch every
	// orchestrator-conditioned fork reads (entrypoint.sh: 761-762).
	OrchestratorEnabled bool // entrypoint.sh: $ORCHESTRATOR_ENABLED presence

	// AgentsJSONTemplate is the nix-baked --agents JSON template (empty
	// when no subagent model is configured). FILER_ENABLED and
	// WORKER_PROVISIONED (entrypoint.sh: 781-799) are derived from whether
	// it carries a "filer"/"worker" key, respectively — a pure parse, not
	// an I/O read, so Gates performs it itself rather than requiring the
	// CLI boundary to pre-resolve two more presence flags.
	AgentsJSONTemplate string // entrypoint.sh: $AGENTS_JSON_TEMPLATE

	// IssueTracker selects the per-axis issue-tracker gate family
	// (entrypoint.sh: 810-814, 899-938). Defaults to "github" when empty,
	// matching entrypoint.sh's "${ISSUE_TRACKER:-github}".
	IssueTracker string // entrypoint.sh: $ISSUE_TRACKER

	// BoxWriteEnabled is the single explicit write-enable signal the
	// launcher resolves host-side from BOX_FORGE_AND_ISSUE_ACCESS and
	// forwards only when writes are permitted (entrypoint.sh: 917-923,
	// 949-957).
	BoxWriteEnabled bool // entrypoint.sh: $BOX_WRITE_ENABLED presence

	// LocalIssueReference is local tracker's PR-body opt-in: when set, the
	// PR body carries a non-auto-closing `Local-issue: <slug>` breadcrumb
	// instead of no reference at all (entrypoint.sh: 862-889).
	LocalIssueReference bool // entrypoint.sh: $LOCAL_ISSUE_REFERENCE presence

	// CodeForge selects the CODE_FORGE-backend gate family
	// (entrypoint.sh: 959-989): OPEN_PR_CREATE_RW_*/FIX_CI_READ_*.
	CodeForge string // entrypoint.sh: $CODE_FORGE

	// DispatchKind, SelfContained, FixPass, and ResumeAfterHold drive which
	// prompt phase_prompt_assembly selects (research/fix/issue) and its
	// session-resume mode (entrypoint.sh: 1013-1063) — Assemble's concern,
	// not Gates's, but still raw inputs the function reads.
	DispatchKind    string // entrypoint.sh: $DISPATCH_KIND (default "work"), read via _is_research_kind
	SelfContained   bool   // entrypoint.sh: $SELF_CONTAINED == "1", read via _is_self_contained
	FixPass         int    // entrypoint.sh: $FIX_PASS (fix-pass number; >0 selects fix-prompt.md)
	ResumeAfterHold bool   // entrypoint.sh: $RESUME_AFTER_HOLD presence

	// PromptsDir, AgentsPromptFiles, and DriverAgentFilesDir locate the
	// fragment/prompt files and per-Driver agent-file rewrite target
	// (entrypoint.sh: 1076-1187) — Assemble's concern, not Gates's.
	PromptsDir          string // entrypoint.sh: $PROMPTS_DIR (default "/agent/prompts"; SPINDRIFT_PROMPT_DIR override resolved before this phase)
	AgentsPromptFiles   string // entrypoint.sh: $AGENTS_PROMPT_FILES (nix-baked agent-name -> promptFile JSON map)
	DriverAgentFilesDir string // entrypoint.sh: $DRIVER_AGENT_FILES_DIR (opencode-style baked agent files dir; empty for claude)

	// Shared-block contract files injected into the rendered prompt
	// (entrypoint.sh: 1064-1074) — Assemble's concern, not Gates's.
	CommsContractFile           string // entrypoint.sh: $COMMS_CONTRACT_FILE
	CheckContractFile           string // entrypoint.sh: $CHECK_CONTRACT_FILE
	OutcomeContractFile         string // entrypoint.sh: $OUTCOME_CONTRACT_FILE
	ResearchOutcomeContractFile string // entrypoint.sh: $RESEARCH_OUTCOME_CONTRACT_FILE

	// SkillsFound is the pre-resolved SKILLS_FOUND local (entrypoint.sh:
	// 716-724): a comma-separated list of skill directory basenames found
	// under DRIVER_SKILLS_DIR, built by a filesystem scan — I/O Gates itself
	// never performs (see the package doc above), so, like the per-skill
	// *SkillBaked flags, it arrives here pre-resolved as a plain string.
	// Non-empty exactly when at least one skill was baked; also doubles as
	// the SKILLS_FOUND fragment-registry gate's own value and the
	// skill-preamble.md fragment's own ${SKILLS_FOUND} extraSubstVars
	// substitution.
	SkillsFound string // entrypoint.sh: local SKILLS_FOUND

	// AutoFormat and AutoLint mirror lib/env-schema.nix's AUTO_FORMAT/
	// AUTO_LINT Consumer-facing knobs (env-schema.nix:670,681), forwarded
	// into the Box unchanged. entrypoint.sh's fragment loop reads each as a
	// plain passthrough-env-var presence gate ("[ -n "${AUTO_FORMAT:-}" ]"
	// / "[ -n "${AUTO_LINT:-}" ]", not a strict boolean parse -- see git
	// history b84c05bc/54b22cf3), so a bool field here (true only when the
	// knob was set) reproduces that presence semantics exactly.
	AutoFormat bool // entrypoint.sh: $AUTO_FORMAT knob presence
	AutoLint   bool // entrypoint.sh: $AUTO_LINT knob presence

	// CIFailureSummary is the launcher-forwarded CI failure text set only on
	// a fix-pass Box when CI failed (issue #426). It doubles as both the
	// CI_FAILURE_SUMMARY gate's own presence check (entrypoint.sh: old
	// "[ -n "${CI_FAILURE_SUMMARY:-}" ]") and the ci-failure.md fragment's
	// own ${CI_FAILURE_SUMMARY} extraSubstVars substitution value -- unlike
	// AutoFormat/AutoLint above, there is no separate raw value to carry:
	// the string's own presence is the gate.
	CIFailureSummary string // entrypoint.sh: $CI_FAILURE_SUMMARY

	// The seven fixed _subst allowlist names every _subst call carries
	// alongside the fragment registry's per-row vars (entrypoint.sh:
	// 405-420) — not fragment-registry-derived, so they live on Env rather
	// than a FragmentRow, but still part of Assemble's substitution
	// allowlist for every rendered fragment and base template.
	IssueNumber     string // entrypoint.sh: $ISSUE_NUMBER
	IssueTitle      string // entrypoint.sh: $ISSUE_TITLE
	Branch          string // entrypoint.sh: $BRANCH
	BaseBranch      string // entrypoint.sh: $BASE_BRANCH
	InProgressLabel string // entrypoint.sh: $IN_PROGRESS_LABEL
	CompleteLabel   string // entrypoint.sh: $COMPLETE_LABEL
	RunNonce        string // entrypoint.sh: $RUN_NONCE

	// ResearchStatusEnum is the regen-generated research-kind verdict
	// enumeration (lib/prompt-contract.nix's outcomeStatusesFor "research",
	// entrypoint.sh's generated RESEARCH_STATUS_ENUM span, issue #2504) --
	// an eighth fixed _subst allowlist name, added alongside the other seven
	// so research-prompt.md's and research-self-contained-prompt.md's OUTCOME
	// grammar line renders the registry's status set instead of a hand-typed
	// literal.
	ResearchStatusEnum string // entrypoint.sh: $RESEARCH_STATUS_ENUM
}
