// Package promptassembly reproduces, in Go, the gate computations box
// (cmd/launcher/box) derives from launcher-forwarded env vars before
// rendering the prompt fragment registry (lib/fragments.nix).
// Gates performs no I/O, so filesystem-derived flags such as the skill-baked
// checks arrive on Env pre-resolved, stat'd at the CLI boundary.
package promptassembly

import "spindrift.dev/launcher/internal/dispatchkind"

// These are the defaults entrypoint.sh's "${VAR:-default}" expansion applies
// when the Env field arrives empty. Issue #2533 moved every other
// gate-family default upstream into nix, carried pre-resolved on Env.
const defaultIssueTracker = "github"

// defaultDispatchKind is a var only because a const can't read
// dispatchkind.Work.Name. Only Env.kind() reads it outside tests.
var defaultDispatchKind = dispatchkind.Work.Name

// Env is the full set of raw inputs box (cmd/launcher/box) reads. Only a
// subset feeds Gates's computed booleans; the rest are passthrough values
// Assemble renders.
type Env struct {
	// Each flag is true only when DRIVER_SKILLS_DIR/<name>/SKILL.md exists.
	// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT
	CavemanSkillBaked                              bool // -f "$DRIVER_SKILLS_DIR/caveman/SKILL.md" (CAVEMAN_BAKED)
	TDDSkillBaked                                  bool // -f "$DRIVER_SKILLS_DIR/tdd/SKILL.md" (TDD_BAKED)
	CommitSkillBaked                               bool // -f "$DRIVER_SKILLS_DIR/commit/SKILL.md" (COMMIT_BAKED)
	CodeReviewSkillBaked                           bool // -f "$DRIVER_SKILLS_DIR/code-review/SKILL.md" (CODE_REVIEW_BAKED)
	AutoFormatSkillBaked                           bool // -f "$DRIVER_SKILLS_DIR/auto-format/SKILL.md" (AUTO_FORMAT_BAKED)
	AutoLintSkillBaked                             bool // -f "$DRIVER_SKILLS_DIR/auto-lint/SKILL.md" (AUTO_LINT_BAKED)
	CheckHygieneSkillBaked                         bool // -f "$DRIVER_SKILLS_DIR/check-hygiene/SKILL.md" (CHECK_HYGIENE_BAKED)
	CodeCommentsSkillBaked                         bool // -f "$DRIVER_SKILLS_DIR/code-comments/SKILL.md" (CODE_COMMENTS_BAKED)
	NixChecksSkillBaked                            bool // -f "$DRIVER_SKILLS_DIR/nix-checks/SKILL.md" (NIX_CHECKS_BAKED)
	PrincipleFixRootCausesSkillBaked               bool // -f "$DRIVER_SKILLS_DIR/principle-fix-root-causes/SKILL.md" (PRINCIPLE_FIX_ROOT_CAUSES_BAKED)
	PrincipleLazinessProtocolSkillBaked            bool // -f "$DRIVER_SKILLS_DIR/principle-laziness-protocol/SKILL.md" (PRINCIPLE_LAZINESS_PROTOCOL_BAKED)
	PrincipleRedesignFromFirstPrinciplesSkillBaked bool // -f "$DRIVER_SKILLS_DIR/principle-redesign-from-first-principles/SKILL.md" (PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED)
	// END GENERATED SKILL-BAKED FIELDS

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

	// HostMediatedRemote and OutboxRelayCapable are per-dispatch presence facts
	// the launcher forwards: the CODE_FORGE has no writable remote at all, and
	// its backend gets outbox-relay treatment under a read-only Box.
	HostMediatedRemote bool // entrypoint.sh: $BOX_HOST_MEDIATED_REMOTE presence
	OutboxRelayCapable bool // entrypoint.sh: $BOX_OUTBOX_RELAY_CAPABLE presence

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
	DispatchKind    string // entrypoint.sh: $DISPATCH_KIND (default "work"; entrypoint.sh never branches on it)
	SelfContained   bool   // entrypoint.sh: $SELF_CONTAINED == "1", read via _is_self_contained
	FixPass         int    // entrypoint.sh: $FIX_PASS (fix-pass number; >0 selects fix-prompt.md)
	ResumeAfterHold bool   // entrypoint.sh: $RESUME_AFTER_HOLD presence

	// DispatchKey, DispatchKeying, DispatchAnnounceVerb are the kind's axes
	// (issue #3996), forwarded as separate facts so the entrypoint and
	// prompts never have to re-derive them from DispatchKind.
	DispatchKey          string // entrypoint.sh: $DISPATCH_KEY (bare issue number, or "butler-<chore>")
	DispatchKeying       string // entrypoint.sh: $DISPATCH_KEYING ("issue" or "chore")
	DispatchAnnounceVerb string // entrypoint.sh: $DISPATCH_ANNOUNCE_VERB (Box start-line verb)

	// SignalCarrier is the BOX_SIGNAL_CARRIER knob (ADR 0052, issue #3725) as the
	// Box sees it: "log" (also what empty means: only an older host launcher
	// forwards nothing, issue #4376) or "socket" (the schema default). It
	// selects which variant of each signal fragment renders (issue #3726).
	SignalCarrier string // entrypoint.sh: $BOX_SIGNAL_CARRIER

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

	// The seven fixed substitution names every prompt render carries alongside
	// the fragment registry's per-row vars. They are not registry-derived, so
	// they live on Env rather than on a FragmentRow.
	IssueNumber     string // entrypoint.sh: $ISSUE_NUMBER
	IssueTitle      string // entrypoint.sh: $ISSUE_TITLE
	Branch          string // entrypoint.sh: $BRANCH
	BaseBranch      string // entrypoint.sh: $BASE_BRANCH
	InProgressLabel string // entrypoint.sh: $IN_PROGRESS_LABEL
	CompleteLabel   string // entrypoint.sh: $COMPLETE_LABEL
	RunNonce        string // entrypoint.sh: $RUN_NONCE

	// RecordID is the host's Record ID for this Dispatch (issue #4786). It keys
	// the Box's prompt_hashes op and is not a prompt var.
	RecordID string // dispatch/box.go: $RECORD_ID

	// IssueText is the subject issue's body plus recent comments
	// (forge.IssueText, issue #3445). Deliberately not one of the seven fixed
	// names above: assemblePromptBodies registers a fenced, sectioned
	// ISSUE_TEXT entry separately, because scalars mirrors entrypoint.sh's
	// fixed-name list byte for byte and substitutes each name's raw value.
	IssueText string // entrypoint.sh: $ISSUE_TEXT

	// ResearchVerdicts is the raw RESEARCH_VERDICTS JSON (empty = the default
	// set). assemblePromptBodies renders the research prompt's status
	// alternation, verdict enum, and verdict bullets from it, so a custom verdict
	// set reaches a prompt-dir override (issues #2630, #4159).
	ResearchVerdicts string // dispatch.go: $RESEARCH_VERDICTS

	// ReviewModelOverride and ReviewEffortOverride carry an operator's explicit
	// dispatch-time REVIEW_MODEL/REVIEW_EFFORT (issue #3171), forwarded only when
	// the operator set them, never a schema default, so empty means no override.
	// When non-empty, Assemble binds them last,
	// over both the AgentsJSONTemplate extraction and the agent-files rewrite.
	ReviewModelOverride  string // dispatch.go: $BOX_REVIEW_MODEL_OVERRIDE
	ReviewEffortOverride string // dispatch.go: $BOX_REVIEW_EFFORT_OVERRIDE

	// ChoreName, ChoreHead, ChoreDiffRange, and ChoreSlice carry the butler's
	// Chore key (ADR 0056, issue #3875): dispatch.go's buildBoxEnv forwards
	// them only for a Factory.NewChore Dispatch (Keying == ByChore), in place
	// of IssueNumber/IssueTitle/IssueText. ChoreName doubles as the
	// prompts/chores/<name>.md lookup key choreSection resolves into
	// CHORE_PROMPT (issue #3875 slice 4).
	ChoreName      string // dispatch.go: $CHORE_NAME
	ChoreHead      string // dispatch.go: $CHORE_HEAD
	ChoreDiffRange string // dispatch.go: $CHORE_DIFF_RANGE
	ChoreSlice     string // dispatch.go: $CHORE_SLICE

	// ChoreClasses is the Chore's promotion-candidate class allow-list
	// (issue #3880), space-joined; empty whenever the host has promotion
	// off (dispatch.Chore.PromotionClasses, set by internal/butler's Runner only
	// while today's promotion room is > 0 -- off, or spent for the day,
	// leaves it empty). It is informational for the Box's prompt only --
	// settle re-checks a finding's class against the host's own
	// allow-list regardless of what this string says.
	ChoreClasses string // dispatch.go: $CHORE_CLASSES

	// ChoreClassList is the Chore's closed finding-class list (issue
	// #4766), space-joined; empty when the Chore declares none. Unlike
	// ChoreClasses it is set whatever the promotion budget
	// (dispatch.Chore.ClassList).
	ChoreClassList string // dispatch.go: $CHORE_CLASS_LIST

	// ChorePatchClasses is the Chore's patch-eligible class allow-list
	// (issue #4072, ADR 0057), the patch-rung sibling of ChoreClasses:
	// dispatch.Chore.PatchClasses space-joined. That field's comment owns
	// when it is empty and why the Box's copy is informational only.
	ChorePatchClasses string // dispatch.go: $CHORE_PATCH_CLASSES

	// ChoreMaxFindings is the run's decimal cap on relayed findings (issue
	// #3994): dispatch.go's buildBoxEnv always sets it, for every Chore
	// Box, to the sweep's own findings room when positive, else the
	// signal socket's fixed default -- so the Box's prompt can name the
	// run's actual cap rather than the socket's fallback alone.
	ChoreMaxFindings string // dispatch.go: $CHORE_MAX_FINDINGS
}

// kind is the DispatchKind fallback every reader of the field must apply
// identically: empty defaults to defaultDispatchKind.
func (e Env) kind() string {
	if e.DispatchKind == "" {
		return defaultDispatchKind
	}
	return e.DispatchKind
}

// descriptor resolves e.kind() against dispatchkind.ByName, so call sites
// read a property off the descriptor instead of comparing kind strings.
// checkCoveredCell already rejects an unknown name before any other reader
// runs, so the Work fallback here only guards a caller that skips that gate.
func (e Env) descriptor() *dispatchkind.Descriptor {
	if d, ok := dispatchkind.ByName(e.kind()); ok {
		return d
	}
	return dispatchkind.Work
}
