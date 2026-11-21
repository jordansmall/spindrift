// Package promptassembly reproduces, in Go, the gate computations box
// (cmd/launcher/box) derives from launcher-forwarded env vars before
// rendering the prompt fragment registry (lib/fragments.nix).
// Gates performs no I/O, so filesystem-derived flags such as the skill-baked
// checks arrive on Env pre-resolved, stat'd at the CLI boundary.
package promptassembly

import "spindrift.dev/launcher/internal/dispatchkind"

// Defaults for an Env field that arrives empty; every other gate-family
// default is resolved in nix and arrives on Env (issue #2533).
const (
	defaultIssueTracker = "github"
)

// defaultDispatchKind is a var only because a const can't read
// dispatchkind.Default.Name. Only Env.kind() reads it outside tests.
var defaultDispatchKind = dispatchkind.Default.Name

// Env is the full set of raw inputs box (cmd/launcher/box) reads. Only a
// subset feeds Gates's computed booleans; the rest are passthrough values
// Assemble renders.
type Env struct {
	// Each flag is true only when DRIVER_SKILLS_DIR/<name>/SKILL.md exists and is not human-only (see SkillBaked).
	// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT
	CavemanSkillBaked                              bool // SkillBaked(DRIVER_SKILLS_DIR, "caveman") (CAVEMAN_BAKED)
	TDDSkillBaked                                  bool // SkillBaked(DRIVER_SKILLS_DIR, "tdd") (TDD_BAKED)
	CommitSkillBaked                               bool // SkillBaked(DRIVER_SKILLS_DIR, "commit") (COMMIT_BAKED)
	CodeReviewSkillBaked                           bool // SkillBaked(DRIVER_SKILLS_DIR, "code-review") (CODE_REVIEW_BAKED)
	AutoFormatSkillBaked                           bool // SkillBaked(DRIVER_SKILLS_DIR, "auto-format") (AUTO_FORMAT_BAKED)
	AutoLintSkillBaked                             bool // SkillBaked(DRIVER_SKILLS_DIR, "auto-lint") (AUTO_LINT_BAKED)
	CheckHygieneSkillBaked                         bool // SkillBaked(DRIVER_SKILLS_DIR, "check-hygiene") (CHECK_HYGIENE_BAKED)
	CodeCommentsSkillBaked                         bool // SkillBaked(DRIVER_SKILLS_DIR, "code-comments") (CODE_COMMENTS_BAKED)
	NixChecksSkillBaked                            bool // SkillBaked(DRIVER_SKILLS_DIR, "nix-checks") (NIX_CHECKS_BAKED)
	PrincipleFixRootCausesSkillBaked               bool // SkillBaked(DRIVER_SKILLS_DIR, "principle-fix-root-causes") (PRINCIPLE_FIX_ROOT_CAUSES_BAKED)
	PrincipleLazinessProtocolSkillBaked            bool // SkillBaked(DRIVER_SKILLS_DIR, "principle-laziness-protocol") (PRINCIPLE_LAZINESS_PROTOCOL_BAKED)
	PrincipleRedesignFromFirstPrinciplesSkillBaked bool // SkillBaked(DRIVER_SKILLS_DIR, "principle-redesign-from-first-principles") (PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED)
	// END GENERATED SKILL-BAKED FIELDS

	// AgentsJSONTemplate is the nix-baked --agents JSON template, empty when no
	// subagent model is configured. The roster presence facts below are
	// resolved in nix, not re-derived from it (issue #2533).
	AgentsJSONTemplate string // $AGENTS_JSON_TEMPLATE

	// FilerEnabled, WorkerProvisioned, and ScoutProvisioned are true when the
	// roster nix bakes carries that entry. nix resolves all three at eval time
	// so no Box reparses the template (issue #2533).
	FilerEnabled      bool
	WorkerProvisioned bool
	ScoutProvisioned  bool

	// IssueTracker names the tracker, defaulting to "github" when empty. The
	// PR-body ticket-reference gates switch on the tracker row's
	// slug-issue-reference fact rather than the name.
	IssueTracker string // $ISSUE_TRACKER

	// IssueRouted is set when the launcher forwards an issue route; it alone
	// gates the cross-repo fragment (ADR 0075). RepoSlug is the Target repo the
	// Box works (REPO_SLUG), only substituted into that fragment;
	// launcherchecks.repoSlugRequired guarantees it is set under a route.
	IssueRouted bool   // $BOX_ISSUE_ROUTED presence
	RepoSlug    string // $REPO_SLUG

	// TrackerAxisRead, TrackerAxisWrite, and TrackerAxisFiler are the tracker's
	// per-axis gate suffixes, resolved in nix (issue #2533). Read is
	// "GITHUB"/"LOCAL"/"FORGEJO"/"JIRA"; Write is "GITHUB"/"FORGEJO", or "" when
	// the tracker has no in-box direct-write path (local always relays);
	// Filer is "GH"/"FORGEJO".
	TrackerAxisRead  string
	TrackerAxisWrite string
	TrackerAxisFiler string

	// HostMediatedRemote and OutboxRelayCapable are per-dispatch presence facts
	// the launcher forwards: the CODE_FORGE has no writable remote at all, and
	// it is a PR-shaped forge (github, forgejo), which the PR gates key on.
	HostMediatedRemote bool // $BOX_HOST_MEDIATED_REMOTE presence
	OutboxRelayCapable bool // $BOX_OUTBOX_RELAY_CAPABLE presence

	// LocalIssueReference is local tracker's PR-body opt-in: the body carries a
	// non-auto-closing `Local-issue: <slug>` breadcrumb instead of no reference.
	LocalIssueReference bool // $LOCAL_ISSUE_REFERENCE presence

	// CodeForge selects the CODE_FORGE-backend gate family. The resolved suffix
	// arrives via ForgeBackend below, but gates_access_forge.go still reads
	// CodeForge directly when ForgeBackend is empty, which happens when an older
	// host launcher never forwarded it (issue #2533).
	CodeForge string // $CODE_FORGE

	// ForgeBackend is nix's pre-resolved CODE_FORGE backend suffix (issue
	// #2533): "GH" or "FORGEJO", with every value other than "forgejo"
	// (github/git/local) riding the shared gh-flavored "GH" arm.
	ForgeBackend string

	// DispatchKind, SelfContained, FixPass, and ResumeAfterHold select which
	// prompt renders (research/fix/issue) and the session-resume mode.
	DispatchKind    string // $DISPATCH_KIND (default "work")
	SelfContained   bool   // $SELF_CONTAINED == "1"
	FixPass         int    // $FIX_PASS (fix-pass number; >0 selects fix-prompt.md)
	ResumeAfterHold bool   // $RESUME_AFTER_HOLD presence

	// DispatchKey, DispatchKeying, DispatchAnnounceVerb are the kind's axes
	// (issue #3996), forwarded as separate facts so box and prompts never have
	// to re-derive them from DispatchKind.
	DispatchKey          string // $DISPATCH_KEY (bare issue number, or "butler-<chore>")
	DispatchKeying       string // $DISPATCH_KEYING ("issue" or "chore")
	DispatchAnnounceVerb string // $DISPATCH_ANNOUNCE_VERB (Box start-line verb)

	// SignalCarrier is the BOX_SIGNAL_CARRIER knob (ADR 0052, issue #3725) as the
	// Box sees it: "log" (also what empty means: only an older host launcher
	// forwards nothing, issue #4376) or "socket" (the schema default). It
	// selects which variant of each signal fragment renders (issue #3726).
	SignalCarrier string // $BOX_SIGNAL_CARRIER

	// SignalIssueRead and SignalIssueSearch are true when the host built an issue
	// reader / searcher for this dispatch, so the Signal socket's `signal issue`
	// and `signal issue-search` verbs answer. Positive-only: absent means the verb
	// is not offered (issue #6924).
	SignalIssueRead   bool // $BOX_SIGNAL_ISSUE_READ
	SignalIssueSearch bool // $BOX_SIGNAL_ISSUE_SEARCH

	// PromptsDir, AgentsPromptFiles, and DriverAgentFilesDir locate the
	// fragment/prompt files and the per-Driver agent-file rewrite target.
	PromptsDir          string // --prompts-dir, from $PROMPTS_DIR (default "/agent/prompts"; SPINDRIFT_PROMPT_DIR override resolved before this phase)
	AgentsPromptFiles   string // --agents-prompt-files, from $AGENTS_PROMPT_FILES (nix-baked agent-name -> promptFile JSON map)
	DriverAgentFilesDir string // --driver-agent-files-dir, from $DRIVER_AGENT_FILES_DIR (opencode-style baked agent files dir; empty for claude)

	// Shared-block contract files injected into the rendered prompt.
	CommsContractFile           string // --comms-contract-file, from $COMMS_CONTRACT_FILE
	CheckContractFile           string // --check-contract-file, from $CHECK_CONTRACT_FILE
	OutcomeContractFile         string // --outcome-contract-file, from $OUTCOME_CONTRACT_FILE
	ResearchOutcomeContractFile string // --research-outcome-contract-file, from $RESEARCH_OUTCOME_CONTRACT_FILE

	// SkillsFound is the comma-separated list of skill directory basenames found
	// under DRIVER_SKILLS_DIR, pre-resolved because Gates does no I/O. It is
	// non-empty exactly when at least one skill was baked, and it is both the
	// SKILLS_FOUND gate's own value and skill-preamble.md's ${SKILLS_FOUND}
	// substitution.
	SkillsFound string // ScanSkillsFound over the skills dir

	// AutoFormat and AutoLint mirror lib/env-schema.nix's Consumer knobs. Each
	// gates on presence (a non-empty env value), not a boolean parse.
	AutoFormat bool // $AUTO_FORMAT knob presence
	AutoLint   bool // $AUTO_LINT knob presence

	// CIFailureSummary is the launcher-forwarded CI failure text, set only on a
	// fix-pass Box when CI failed (issue #426). Its own presence is the gate,
	// and its value is ci-failure.md's ${CI_FAILURE_SUMMARY} substitution.
	CIFailureSummary string // $CI_FAILURE_SUMMARY

	// The seven fixed substitution names every prompt render carries alongside
	// the fragment registry's per-row vars. They are not registry-derived, so
	// they live on Env rather than on a FragmentRow.
	IssueNumber     string // $ISSUE_NUMBER
	IssueTitle      string // $ISSUE_TITLE
	Branch          string // $BRANCH
	BaseBranch      string // $BASE_BRANCH
	InProgressLabel string // $IN_PROGRESS_LABEL
	CompleteLabel   string // $COMPLETE_LABEL
	RunNonce        string // $RUN_NONCE

	// RecordID is the host's Record ID for this Dispatch (issue #4786). It keys
	// the Box's prompt_hashes op and is not a prompt var.
	RecordID string // dispatch/box.go: $RECORD_ID

	// IssueText is the subject issue's body plus recent comments
	// (forge.IssueText, issue #3445). Not one of the fixed names above:
	// ISSUE_TEXT substitutes a fenced section, not this raw value.
	IssueText string // $ISSUE_TEXT

	// ResearchVerdicts is the raw RESEARCH_VERDICTS JSON (empty = the default
	// set). assemblePromptBodies renders the research prompt's status
	// alternation, verdict enum, and verdict bullets from it, so a custom verdict
	// set reaches a prompt-dir override (issues #2630, #4159).
	ResearchVerdicts string // dispatch.go: $RESEARCH_VERDICTS

	// ReviewModelOverride and ReviewEffortOverride carry an operator's explicit
	// dispatch-time REVIEW_MODEL/REVIEW_EFFORT (issue #3171), never a schema
	// default, so empty means no override. Assemble binds them last.
	ReviewModelOverride  string // dispatch.go: $BOX_REVIEW_MODEL_OVERRIDE
	ReviewEffortOverride string // dispatch.go: $BOX_REVIEW_EFFORT_OVERRIDE

	// ChoreName, ChoreHead, ChoreDiffRange, and ChoreSlice carry the butler's
	// Chore key (ADR 0056, issue #3875), forwarded only for a ByChore Dispatch
	// in place of the issue facts. ChoreName is also the prompts/chores/<name>.md
	// key choreSection resolves into CHORE_PROMPT.
	ChoreName      string // dispatch.go: $CHORE_NAME
	ChoreHead      string // dispatch.go: $CHORE_HEAD
	ChoreDiffRange string // dispatch.go: $CHORE_DIFF_RANGE
	ChoreSlice     string // dispatch.go: $CHORE_SLICE

	// ChoreClasses is the Chore's promotion-candidate class allow-list
	// (issue #3880, dispatch.Chore.PromotionClasses), space-joined; empty when
	// promotion is off or spent for the day. Informational only: settle
	// re-checks a finding's class against the host's own allow-list.
	ChoreClasses string // dispatch.go: $CHORE_CLASSES

	// ChoreClassList is the Chore's closed finding-class list (issue
	// #4766), space-joined; empty when the Chore declares none. Unlike
	// ChoreClasses it is set whatever the promotion budget
	// (dispatch.Chore.ClassList).
	ChoreClassList string // dispatch.go: $CHORE_CLASS_LIST

	// ChoreInput is the host-rendered input a Chore reasons from instead of
	// the repo tree (the tuning digest, ADR 0062), unfenced markdown;
	// dispatch.Chore.Input. Empty for a code Chore, whose prompt then renders
	// without the section. choreInputSection renders it fenced.
	ChoreInput string // dispatch.go: $CHORE_INPUT

	// ChorePatchClasses is the Chore's patch-eligible class allow-list
	// (issue #4072, ADR 0057), the patch-rung sibling of ChoreClasses:
	// dispatch.Chore.PatchClasses space-joined. That field's comment owns
	// when it is empty and why the Box's copy is informational only.
	ChorePatchClasses string // dispatch.go: $CHORE_PATCH_CLASSES

	// ChoreMaxFindings is the run's decimal cap on relayed findings (issue
	// #3994), always set for a Chore Box: the sweep's findings room when
	// positive, else the signal socket's fixed default.
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

// descriptor resolves e.kind() so call sites read a property off the
// descriptor instead of comparing kind strings. The Default fallback only
// guards a caller that skips checkCoveredCell.
func (e Env) descriptor() *dispatchkind.Descriptor {
	if d, ok := dispatchkind.ByName(e.kind()); ok {
		return d
	}
	return dispatchkind.Default
}
