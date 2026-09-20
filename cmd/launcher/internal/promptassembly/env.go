// Package promptassembly reproduces, in Go, the gate computations
// agent/entrypoint.sh's phase_prompt_assembly derives from launcher-forwarded
// env vars before rendering the prompt fragment registry (lib/fragments.nix).
// Gates performs no I/O, so filesystem-derived flags such as the skill-baked
// checks arrive on Env pre-resolved, stat'd at the CLI boundary.
package promptassembly

// These are the defaults entrypoint.sh's "${VAR:-default}" expansion applies
// when the Env field arrives empty, named once so checkCoveredCell
// (assemble.go) and Gates resolve the same literal. Issue #2533 moved every
// other gate-family default upstream into nix, carried pre-resolved on Env.
const (
	defaultIssueTracker = "github"
	defaultDispatchKind = "work"
)

// Env is the full set of raw inputs agent/entrypoint.sh's
// phase_prompt_assembly reads. Only a subset feeds Gates's computed booleans;
// the rest are passthrough values Assemble renders.
type Env struct {
	// Each flag is true only when DRIVER_SKILLS_DIR/<name>/SKILL.md exists.
	// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT
	CavemanSkillBaked                              bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" (CAVEMAN_BAKED)
	TDDSkillBaked                                  bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" (TDD_BAKED)
	CommitSkillBaked                               bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" (COMMIT_BAKED)
	CodeReviewSkillBaked                           bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" (CODE_REVIEW_BAKED)
	AutoFormatSkillBaked                           bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" (AUTO_FORMAT_BAKED)
	AutoLintSkillBaked                             bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" (AUTO_LINT_BAKED)
	CheckHygieneSkillBaked                         bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/check-hygiene/SKILL.md" (CHECK_HYGIENE_BAKED)
	CodeCommentsSkillBaked                         bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/code-comments/SKILL.md" (CODE_COMMENTS_BAKED)
	NixChecksSkillBaked                            bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/nix-checks/SKILL.md" (NIX_CHECKS_BAKED)
	PrincipleFixRootCausesSkillBaked               bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/principle-fix-root-causes/SKILL.md" (PRINCIPLE_FIX_ROOT_CAUSES_BAKED)
	PrincipleLazinessProtocolSkillBaked            bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/principle-laziness-protocol/SKILL.md" (PRINCIPLE_LAZINESS_PROTOCOL_BAKED)
	PrincipleRedesignFromFirstPrinciplesSkillBaked bool // entrypoint.sh: -f "$DRIVER_SKILLS_DIR/principle-redesign-from-first-principles/SKILL.md" (PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED)
	// END GENERATED SKILL-BAKED FIELDS

	// OrchestratorEnabled is the master switch every orchestrator-conditioned
	// fork reads.
	OrchestratorEnabled bool // entrypoint.sh: $ORCHESTRATOR_ENABLED presence

	// AgentsJSONTemplate is the nix-baked --agents JSON template, empty when no
	// subagent model is configured. Assemble parses its JSON content, but the
	// filer/worker presence facts are no longer re-derived from it: they arrive
	// pre-resolved below (issue #2533).
	AgentsJSONTemplate string // entrypoint.sh: $AGENTS_JSON_TEMPLATE

	// FilerEnabled, WorkerProvisioned, and ScoutProvisioned are true when the
	// roster nix bakes into AgentsJSONTemplate carries that entry. nix resolves
	// all three at eval time so no Box reparses the template (issue #2533).
	FilerEnabled      bool
	WorkerProvisioned bool
	ScoutProvisioned  bool

	// ReviewLoopInline is !OrchestratorEnabled and ReviewLoopOrchestrator is
	// OrchestratorEnabled, both computed in nix (lib/mkHarness.nix) instead of
	// in-box (issue #2533). The two cross the process boundary independently, so
	// Gates repairs the self-contradicting cases, both true or both false, from
	// the live ORCHESTRATOR gate.
	ReviewLoopInline       bool
	ReviewLoopOrchestrator bool

	// IssueTracker selects the per-axis issue-tracker gate family, defaulting to
	// "github" when empty. The PR-body ticket-reference gates switch on the raw
	// "local" comparison rather than an axis; the per-axis suffixes arrive
	// pre-resolved below (issue #2533).
	IssueTracker string // entrypoint.sh: $ISSUE_TRACKER

	// TrackerAxisRead, TrackerAxisWrite, and TrackerAxisFiler are nix's
	// pre-resolved form of entrypoint.sh's "${ISSUE_TRACKER:-github}" case
	// statement (issue #2533). Read is "GITHUB"/"LOCAL"/"FORGEJO"; Write is
	// "GITHUB"/"FORGEJO", or "" when the tracker has no in-box direct-write path
	// (local always relays instead); Filer is "GH"/"FORGEJO".
	TrackerAxisRead  string
	TrackerAxisWrite string
	TrackerAxisFiler string

	// BoxWriteEnabled is the launcher's host-side resolution of
	// BOX_FORGE_AND_ISSUE_ACCESS, forwarded only when writes are permitted.
	BoxWriteEnabled bool // entrypoint.sh: $BOX_WRITE_ENABLED presence

	// LocalIssueReference is local tracker's PR-body opt-in: the body carries a
	// non-auto-closing `Local-issue: <slug>` breadcrumb instead of no reference.
	LocalIssueReference bool // entrypoint.sh: $LOCAL_ISSUE_REFERENCE presence

	// CodeForge selects the CODE_FORGE-backend gate family. The resolved suffix
	// arrives via ForgeBackend below, but gates_access_forge.go still reads
	// CodeForge directly when ForgeBackend is empty, which happens when an older
	// host launcher never forwarded it (issue #2533).
	CodeForge string // entrypoint.sh: $CODE_FORGE

	// ForgeBackend is nix's pre-resolved CODE_FORGE backend suffix (issue
	// #2533): "GH" or "FORGEJO", with every value other than "forgejo"
	// (github/git/local) riding the shared gh-flavored "GH" arm.
	ForgeBackend string

	// DispatchKind, SelfContained, FixPass, and ResumeAfterHold select which
	// prompt renders (research/fix/issue) and the session-resume mode.
	DispatchKind    string // entrypoint.sh: $DISPATCH_KIND (default "work"), read via _is_research_kind
	SelfContained   bool   // entrypoint.sh: $SELF_CONTAINED == "1", read via _is_self_contained
	FixPass         int    // entrypoint.sh: $FIX_PASS (fix-pass number; >0 selects fix-prompt.md)
	ResumeAfterHold bool   // entrypoint.sh: $RESUME_AFTER_HOLD presence

	// PromptsDir, AgentsPromptFiles, and DriverAgentFilesDir locate the
	// fragment/prompt files and the per-Driver agent-file rewrite target.
	PromptsDir          string // entrypoint.sh: $PROMPTS_DIR (default "/agent/prompts"; SPINDRIFT_PROMPT_DIR override resolved before this phase)
	AgentsPromptFiles   string // entrypoint.sh: $AGENTS_PROMPT_FILES (nix-baked agent-name -> promptFile JSON map)
	DriverAgentFilesDir string // entrypoint.sh: $DRIVER_AGENT_FILES_DIR (opencode-style baked agent files dir; empty for claude)

	// Shared-block contract files injected into the rendered prompt.
	CommsContractFile           string // entrypoint.sh: $COMMS_CONTRACT_FILE
	CheckContractFile           string // entrypoint.sh: $CHECK_CONTRACT_FILE
	OutcomeContractFile         string // entrypoint.sh: $OUTCOME_CONTRACT_FILE
	ResearchOutcomeContractFile string // entrypoint.sh: $RESEARCH_OUTCOME_CONTRACT_FILE

	// SkillsFound is the comma-separated list of skill directory basenames found
	// under DRIVER_SKILLS_DIR, pre-resolved because Gates does no I/O. It is
	// non-empty exactly when at least one skill was baked, and it is both the
	// SKILLS_FOUND gate's own value and skill-preamble.md's ${SKILLS_FOUND}
	// substitution.
	SkillsFound string // entrypoint.sh: local SKILLS_FOUND

	// AutoFormat and AutoLint mirror lib/env-schema.nix's Consumer knobs.
	// entrypoint.sh gates each on presence ("[ -n "${AUTO_FORMAT:-}" ]"), not a
	// boolean parse, so a bool set only when the knob was set reproduces it.
	AutoFormat bool // entrypoint.sh: $AUTO_FORMAT knob presence
	AutoLint   bool // entrypoint.sh: $AUTO_LINT knob presence

	// CIFailureSummary is the launcher-forwarded CI failure text, set only on a
	// fix-pass Box when CI failed (issue #426). Its own presence is the gate,
	// and its value is ci-failure.md's ${CI_FAILURE_SUMMARY} substitution.
	CIFailureSummary string // entrypoint.sh: $CI_FAILURE_SUMMARY

	// The seven fixed _subst allowlist names every _subst call carries alongside
	// the fragment registry's per-row vars. They are not registry-derived, so
	// they live on Env rather than on a FragmentRow.
	IssueNumber     string // entrypoint.sh: $ISSUE_NUMBER
	IssueTitle      string // entrypoint.sh: $ISSUE_TITLE
	Branch          string // entrypoint.sh: $BRANCH
	BaseBranch      string // entrypoint.sh: $BASE_BRANCH
	InProgressLabel string // entrypoint.sh: $IN_PROGRESS_LABEL
	CompleteLabel   string // entrypoint.sh: $COMPLETE_LABEL
	RunNonce        string // entrypoint.sh: $RUN_NONCE

	// IssueText is the subject issue's body plus recent comments
	// (forge.IssueText, issue #3445). Deliberately not one of the seven fixed
	// _subst names above: assemblePromptBodies registers a fenced, sectioned
	// ISSUE_TEXT entry separately, because scalars mirrors entrypoint.sh's
	// fixed-name list byte for byte and substitutes each name's raw value.
	IssueText string // entrypoint.sh: $ISSUE_TEXT

	// ResearchStatusEnum is the regen-generated research-kind verdict enumeration
	// (lib/prompt-contract.nix's outcomeStatusesFor "research", issue #2504), an
	// eighth fixed _subst name so the research prompts' OUTCOME grammar line
	// renders the registry's status set instead of a hand-typed literal.
	ResearchStatusEnum string // entrypoint.sh: $RESEARCH_STATUS_ENUM

	// ReviewModelOverride and ReviewEffortOverride carry an operator's explicit
	// dispatch-time REVIEW_MODEL/REVIEW_EFFORT (issue #3171), forwarded only when
	// the operator set them, never a schema default, so empty means no override.
	// When non-empty and the ORCHESTRATOR gate is on, Assemble binds them last,
	// over both the AgentsJSONTemplate extraction and the agent-files rewrite.
	ReviewModelOverride  string // dispatch.go: $BOX_REVIEW_MODEL_OVERRIDE
	ReviewEffortOverride string // dispatch.go: $BOX_REVIEW_EFFORT_OVERRIDE
}

// kind is the DispatchKind fallback every reader of the field must apply
// identically: empty defaults to defaultDispatchKind.
func (e Env) kind() string {
	if e.DispatchKind == "" {
		return defaultDispatchKind
	}
	return e.DispatchKind
}
