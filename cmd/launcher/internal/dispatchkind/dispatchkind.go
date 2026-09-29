// Package dispatchkind declares each Dispatch kind once (issue #3872):
// every former string-switch on "dispatch"/"research" collapses onto one
// Descriptor field. It imports no other spindrift internal package: forge
// imports outcome, and both forge and outcome import dispatchkind (forge →
// outcome → dispatchkind), so dispatchkind must stay a leaf — importing
// forge back would cycle.
package dispatchkind

// Keying is how a Dispatch of this kind is identified against the tracker.
type Keying int

const (
	ByIssue Keying = iota // a Dispatch carries one tracker issue
	ByChore               // the butler (ADR 0056, #3870) carries one Ledger Chore
)

// String is the DISPATCH_KEYING wire value (issue #3996).
func (k Keying) String() string {
	if k == ByChore {
		return "chore"
	}
	return "issue"
}

// LabelFamily is the set of triage labels a kind's lifecycle moves through.
// It starts at 1 so a Descriptor that omits Labels hits forge.FamilyLabels'
// unknown-family panic rather than silently taking work's family.
type LabelFamily int

const (
	LabelsConfigured LabelFamily = iota + 1 // operator-configured LABEL/IN_PROGRESS_LABEL/... (work)
	LabelsResearch                          // fixed agent-research family, forge.ResearchDispatchLabels (ADR 0022)
	LabelsNone                              // no lifecycle labels: the butler's claims live in the Ledger, not the tracker (ADR 0056)
)

// Settle is how a finished Box's outcome gets settled against the tracker.
type Settle int

const (
	SettleMerge   Settle = iota // work's full merge gate, settle.New
	SettleVerdict               // research's one-shot verdict comment, settle.NewResearchSettle*
	SettleLedger                // butler's one-shot run: files findings, writes the Ledger's done commit (ADR 0056)
)

// PromptContract is which shared contract block prompt assembly injects.
// It starts at 1 so a Descriptor that omits Contract is detectably unset,
// same idiom as LabelFamily.
type PromptContract int

const (
	ContractLanding PromptContract = iota + 1 // work: shared comms, check, and outcome contract blocks
	ContractVerdict                           // research: only the research-verdict contract
	ContractInline                            // butler: nothing; butler-prompt.md carries its own OUTCOME section (ADR 0056)
)

// Tracker is which IssueTracker instance a kind's issues live on.
type Tracker int

const (
	TrackerWork     Tracker = iota + 1 // the work IssueTracker instance
	TrackerResearch                    // the separate tracker instance carrying the research label family (issue #1708)
)

// DaemonPriority is a kind's standing in the daemon's slotOrder preference.
type DaemonPriority int

const (
	PriorityNormal   DaemonPriority = iota // preferred on every unreserved slot
	PriorityReserved                       // preferred only on the RESEARCH_RESERVATION slots, last elsewhere
	PriorityIdle                           // tried only after every other kind on every slot (butler, ADR 0056: a slot picks it only when nothing else has work)
)

// Prompts names the kind's prompt templates. A zero value means "defer to
// work's selection" for Base, and "no self-contained sub-mode" for
// SelfContainedBase.
type Prompts struct {
	Base              string // "" = work's pass-dependent issue-prompt.md / fix-prompt.md selection
	SelfContainedBase string // "" = kind has no --self-contained sub-mode
	// Reviewer is "" for the roster's own reviewer prompt (review-prompt.md),
	// dropped when the orchestrator's review pass replaces it; else the
	// kind's own in-Box reviewer prompt, kept under the orchestrator.
	Reviewer string
}

// Descriptor is everything that used to be a separate switch on a kind
// string, gathered into one value per kind.
type Descriptor struct {
	Name           string // DISPATCH_KIND value, Box env (display-only there — the Box never branches on it), prompt assembly, outcome: "work" / "research"
	Verb           string // CLI subcommand and daemon kind selector: "dispatch" / "research"
	Keying         Keying
	Labels         LabelFamily
	Prompts        Prompts
	Settle         Settle
	AdviseOnly     bool // read-only posture: never lands code (no branch/PR/merge); ignores blockers
	ReadOnlyBox    bool // Box always runs read-only (guards, outbox relay), regardless of BOX_FORGE_AND_ISSUE_ACCESS
	DaemonPriority DaemonPriority
	FindingLabel   string         // provenance label settle applies to a filed finding
	Contract       PromptContract // which shared contract block prompt assembly injects
	FilerRelayGate string         // lib/fragments.nix gate selecting the kind's filer-label-relay*.md fragment
	Tracker        Tracker        // which IssueTracker instance this kind's issues live on
	AnnounceVerb   string         // verb of the Box start line (agent/entrypoint.sh); exported to the Box as DISPATCH_ANNOUNCE_VERB (issue #3996)
}

var (
	Work = &Descriptor{
		Name:           "work",
		Verb:           "dispatch",
		Keying:         ByIssue,
		Labels:         LabelsConfigured,
		Settle:         SettleMerge,
		DaemonPriority: PriorityNormal,
		FindingLabel:   "agent-review-finding",
		Contract:       ContractLanding,
		FilerRelayGate: "FILER_FILE_RELAY_WORK",
		Tracker:        TrackerWork,
		AnnounceVerb:   "implementing",
	}
	Research = &Descriptor{
		Name:   "research",
		Verb:   "research",
		Keying: ByIssue,
		Labels: LabelsResearch,
		Prompts: Prompts{
			Base:              "research-prompt.md",
			SelfContainedBase: "research-self-contained-prompt.md",
		},
		Settle:         SettleVerdict,
		AdviseOnly:     true,
		DaemonPriority: PriorityReserved,
		FindingLabel:   "agent-research-finding",
		Contract:       ContractVerdict,
		FilerRelayGate: "FILER_FILE_RELAY_RESEARCH",
		Tracker:        TrackerResearch,
		AnnounceVerb:   "researching",
	}
	// Butler is the one-shot butler run (ADR 0056, #3870): it carries one
	// Ledger Chore (ByChore), never a tracker issue, and files findings the
	// same advise-only way research does, so it never lands code either. It
	// has no lifecycle labels of its own (LabelsNone) — its only
	// doctor-visible label is the agent-butler-finding provenance one. Its
	// PriorityIdle means a slot picks it only once dispatch and research have
	// both reported no work (the awake-window rule: gate starting, never
	// stopping — a running butler child is never preempted once picked).
	Butler = &Descriptor{
		Name:   "butler",
		Verb:   "butler",
		Keying: ByChore,
		Labels: LabelsNone,
		Prompts: Prompts{
			Base:     "butler-prompt.md",
			Reviewer: "butler-review-prompt.md",
		},
		Settle:         SettleLedger,
		AdviseOnly:     true,
		ReadOnlyBox:    true, // issue #3906
		DaemonPriority: PriorityIdle,
		FindingLabel:   "agent-butler-finding",
		Contract:       ContractInline,
		FilerRelayGate: "FILER_FILE_RELAY_BUTLER",
		Tracker:        TrackerWork, // butler files findings onto the work tracker; it has no lifecycle labels of its own
		AnnounceVerb:   "sweeping",
	}
)

// All lists every kind in declaration order; that order is the daemon's
// default pool order.
var All = []*Descriptor{Work, Research, Butler}

// ByName looks up a kind by its Name. "" is not special here — callers that
// default "" to work do so explicitly via Work.Name.
func ByName(name string) (*Descriptor, bool) {
	for _, d := range All {
		if d.Name == name {
			return d, true
		}
	}
	return nil, false
}

// ByVerb looks up a kind by its CLI/daemon Verb.
func ByVerb(verb string) (*Descriptor, bool) {
	for _, d := range All {
		if d.Verb == verb {
			return d, true
		}
	}
	return nil, false
}
