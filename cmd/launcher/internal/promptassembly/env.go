// Package promptassembly computes, in Go, the gates that select which prompt
// fragments (lib/fragments.nix) render. Env carries one typed field per raw
// launcher-forwarded input; Gates (gates.go) turns an Env into the named
// booleans the fragment registry's gate column reads.
//
// Gates performs no I/O: every filesystem-derived presence flag (the
// skill-baked checks, SkillsFound) arrives on Env already resolved by the CLI
// boundary that constructs it.
package promptassembly

// Defaults applied when the corresponding Env field arrives empty. Named once
// here so checkCoveredCell (assemble.go) and Gates resolve the same value.
const (
	defaultIssueTracker = "github"
	defaultDispatchKind = "work"
)

// Env is the full set of raw inputs prompt assembly reads. Only a subset
// feeds Gates's computed booleans; the rest (contract file paths, prompt
// dirs, substitution values) is Assemble's concern.
type Env struct {
	// Each is true only when DRIVER_SKILLS_DIR/<name>/SKILL.md exists.
	// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT
	CavemanSkillBaked    bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" (CAVEMAN_BAKED)
	TDDSkillBaked        bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" (TDD_BAKED)
	CommitSkillBaked     bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" (COMMIT_BAKED)
	CodeReviewSkillBaked bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" (CODE_REVIEW_BAKED)
	AutoFormatSkillBaked bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" (AUTO_FORMAT_BAKED)
	AutoLintSkillBaked   bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" (AUTO_LINT_BAKED)
	// END GENERATED SKILL-BAKED FIELDS

	// OrchestratorEnabled is the master switch every orchestrator-conditioned
	// fork reads.
	OrchestratorEnabled bool // entrypoint.sh: $ORCHESTRATOR_ENABLED presence

	// AgentsJSONTemplate is the nix-baked --agents JSON template, empty when
	// no subagent model is configured. Assemble reads its JSON content for
	// the reviewer-drop/model-extraction and per-agent injection loop; the
	// roster presence facts it implies arrive separately, pre-resolved, on
	// FilerEnabled/WorkerProvisioned.
	AgentsJSONTemplate string // entrypoint.sh: $AGENTS_JSON_TEMPLATE

	// True iff the roster nix bakes into AgentsJSONTemplate carries a
	// "filer"/"worker" entry — resolved at eval time, not reparsed per Box.
	FilerEnabled      bool // nix-resolved roster fact: roster carries a "filer" entry
	WorkerProvisioned bool // nix-resolved roster fact: roster carries a "worker" entry

	// nix guarantees exactly one of these is true, but the two fields cross
	// the process boundary independently, so Gates repairs both agreeing
	// cases from the live ORCHESTRATOR gate rather than trusting an Env value
	// that disagrees with itself.
	ReviewLoopInline       bool // nix-resolved: !OrchestratorEnabled
	ReviewLoopOrchestrator bool // nix-resolved: OrchestratorEnabled

	// IssueTracker defaults to "github" when empty. Read directly by the
	// PR-body ticket-reference gates (which compare against raw "local", not
	// an axis) and by Assemble/Validate; the per-axis suffixes arrive
	// pre-resolved below.
	IssueTracker string // entrypoint.sh: $ISSUE_TRACKER

	// nix-resolved gate-family suffixes for the ISSUE_TRACKER axis.
	// TrackerAxisRead is one of "GITHUB"/"LOCAL"/"FORGEJO";
	// TrackerAxisWrite is "GITHUB"/"FORGEJO", or "" when the tracker has no
	// in-box direct-write path (local always relays instead);
	// TrackerAxisFiler is "GH"/"FORGEJO".
	TrackerAxisRead  string // nix-resolved ISSUE_TRACKER read-step suffix
	TrackerAxisWrite string // nix-resolved ISSUE_TRACKER write-step suffix
	TrackerAxisFiler string // nix-resolved ISSUE_TRACKER filer suffix

	// BoxWriteEnabled is the single explicit write-enable signal the launcher
	// resolves host-side from BOX_FORGE_AND_ISSUE_ACCESS and forwards only
	// when writes are permitted.
	BoxWriteEnabled bool // entrypoint.sh: $BOX_WRITE_ENABLED presence

	// LocalIssueReference is local tracker's PR-body opt-in: when set, the PR
	// body carries a non-auto-closing `Local-issue: <slug>` breadcrumb
	// instead of no reference at all.
	LocalIssueReference bool // entrypoint.sh: $LOCAL_ISSUE_REFERENCE presence

	// CodeForge is read only by gates_access_forge.go's version-skew
	// fallback, when ForgeBackend arrives empty from an older host launcher
	// that never forwarded it.
	CodeForge string // entrypoint.sh: $CODE_FORGE

	// ForgeBackend is the nix-resolved backend suffix: "GH" or "FORGEJO",
	// with every non-forgejo value (github/git/local) riding the shared
	// gh-flavored "GH" arm.
	ForgeBackend string // nix-resolved CODE_FORGE backend suffix

	// Which prompt gets selected (research/fix/issue) and its session-resume
	// mode — Assemble's concern, not Gates's.
	DispatchKind    string // entrypoint.sh: $DISPATCH_KIND (default "work"), read via _is_research_kind
	SelfContained   bool   // entrypoint.sh: $SELF_CONTAINED == "1", read via _is_self_contained
	FixPass         int    // entrypoint.sh: $FIX_PASS (fix-pass number; >0 selects fix-prompt.md)
	ResumeAfterHold bool   // entrypoint.sh: $RESUME_AFTER_HOLD presence

	// Where the fragment/prompt files and per-Driver agent-file rewrite
	// target live — Assemble's concern, not Gates's.
	PromptsDir          string // entrypoint.sh: $PROMPTS_DIR (default "/agent/prompts"; SPINDRIFT_PROMPT_DIR override resolved before this phase)
	AgentsPromptFiles   string // entrypoint.sh: $AGENTS_PROMPT_FILES (nix-baked agent-name -> promptFile JSON map)
	DriverAgentFilesDir string // entrypoint.sh: $DRIVER_AGENT_FILES_DIR (opencode-style baked agent files dir; empty for claude)

	// Shared-block contract files injected into the rendered prompt.
	CommsContractFile           string // entrypoint.sh: $COMMS_CONTRACT_FILE
	CheckContractFile           string // entrypoint.sh: $CHECK_CONTRACT_FILE
	CodeCommentsContractFile    string // entrypoint.sh: $CODE_COMMENTS_CONTRACT_FILE
	OutcomeContractFile         string // entrypoint.sh: $OUTCOME_CONTRACT_FILE
	ResearchOutcomeContractFile string // entrypoint.sh: $RESEARCH_OUTCOME_CONTRACT_FILE

	// SkillsFound is a comma-separated list of skill directory basenames
	// found under DRIVER_SKILLS_DIR, resolved before Env is constructed. It
	// doubles as the SKILLS_FOUND gate's own value (non-empty iff at least
	// one skill was baked) and as skill-preamble.md's ${SKILLS_FOUND}
	// substitution.
	SkillsFound string // entrypoint.sh: local SKILLS_FOUND

	// Presence gates, not strict boolean parses: true only when the
	// Consumer-facing knob (lib/env-schema.nix) was set at all.
	AutoFormat bool // entrypoint.sh: $AUTO_FORMAT knob presence
	AutoLint   bool // entrypoint.sh: $AUTO_LINT knob presence

	// CIFailureSummary is the CI failure text set only on a fix-pass Box when
	// CI failed. Its own presence is the gate; it is also ci-failure.md's
	// ${CI_FAILURE_SUMMARY} substitution value.
	CIFailureSummary string // entrypoint.sh: $CI_FAILURE_SUMMARY

	// The fixed substitution allowlist every fragment and base template
	// carries alongside the registry's per-row vars — not
	// fragment-registry-derived, so they live on Env rather than a
	// FragmentRow.
	IssueNumber     string // entrypoint.sh: $ISSUE_NUMBER
	IssueTitle      string // entrypoint.sh: $ISSUE_TITLE
	Branch          string // entrypoint.sh: $BRANCH
	BaseBranch      string // entrypoint.sh: $BASE_BRANCH
	InProgressLabel string // entrypoint.sh: $IN_PROGRESS_LABEL
	CompleteLabel   string // entrypoint.sh: $COMPLETE_LABEL
	RunNonce        string // entrypoint.sh: $RUN_NONCE

	// ResearchStatusEnum is the generated research-kind verdict enumeration
	// (lib/prompt-contract.nix's outcomeStatusesFor "research"), so the
	// research prompts' OUTCOME grammar line renders the registry's status
	// set instead of a hand-typed literal.
	ResearchStatusEnum string // entrypoint.sh: $RESEARCH_STATUS_ENUM
}
