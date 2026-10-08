// Package dispatchkind declares each Dispatch kind once (issue #3872):
// every former string-switch on "dispatch"/"research" collapses onto one
// Descriptor field. It imports no other spindrift internal package: forge
// imports outcome, and both forge and outcome import dispatchkind (forge →
// outcome → dispatchkind), so dispatchkind must stay a leaf — importing
// forge back would cycle.
package dispatchkind

// AnnouncePrefix begins the Box start line that stats inference reads back.
const AnnouncePrefix = "==> claude "

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
// The values name tiers, not ranks (PriorityNormal must stay the zero value), so
// never compare them with <; slotOrder in internal/daemon/pool.go is the only
// ordering.
type DaemonPriority int

const (
	PriorityNormal   DaemonPriority = iota // preferred whenever the research floor is met
	PriorityReserved                       // preferred only while fewer than RESEARCH_RESERVATION of them are running, last otherwise
	PriorityIdle                           // tried only after every other kind on every slot (butler, ADR 0056: a slot picks it only when nothing else has work)
	PriorityFirst                          // tried ahead of every other kind on every slot (recover, issue #4656: stranded finished work lands before new work starts, rebasing onto as little new main as possible)
)

// Enablement is when the daemon draws a kind at all.
type Enablement int

const (
	EnabledAlways        Enablement = iota + 1 // drawn whenever selected
	EnabledByChores                            // drawn only while BUTLER_CHORES enables at least one Chore; the daemon resolves that, since this leaf package cannot import chore
	EnabledByOutboxRelay                       // drawn only when CODE_FORGE relays a read-only Box's outbox bundle (OutboxRelayCapable) and BOX_FORGE_AND_ISSUE_ACCESS=read-only; never on local or read-write; the daemon resolves it, since this leaf package cannot import backend
)

// DemandSource is how the daemon learns whether a kind has work waiting.
type DemandSource int

const (
	DemandTrackerProbe  DemandSource = iota + 1 // the host asks the kind's tracker (forge.DemandCounter), when the adapter has one
	DemandChildReported                         // only a child run knows (butler: its work is Ledger Chores, not tracker issues)
	DemandHostOutbox                            // the host counts eligible outbox bundles itself, from the filesystem alone (recover)
)

// preflightWhen is when the daemon's startup preflight passes a kind's doctor flag.
type preflightWhen int

const (
	preflightNone      preflightWhen = iota + 1 // never: doctor always checks this kind's config (work's triage labels)
	preflightWhenDrawn                          // whenever the kind survives the daemon's enablement gate, bare selector included (butler)
	preflightWhenNamed                          // only when the operator's selector names the kind: the bare every-kind default lists it unconditionally, and a repo without its labels must still start (research)
)

// DoctorPreflight is how the daemon's startup preflight treats a kind: when it
// passes the kind's doctor flag, and which flag. It is a struct so a mode and
// its flag cannot drift apart; only the constructors below build one. The zero
// value is unset, and Flag panics on it.
type DoctorPreflight struct {
	when preflightWhen
	flag string
}

// NoPreflight is the preflight of a kind whose config doctor always checks.
func NoPreflight() DoctorPreflight {
	return DoctorPreflight{when: preflightNone}
}

// PreflightWhenDrawn passes flag whenever the daemon draws the kind.
func PreflightWhenDrawn(flag string) DoctorPreflight {
	return newPreflight(preflightWhenDrawn, flag)
}

// PreflightWhenNamed passes flag only when the operator's selector names the kind.
func PreflightWhenNamed(flag string) DoctorPreflight {
	return newPreflight(preflightWhenNamed, flag)
}

func newPreflight(when preflightWhen, flag string) DoctorPreflight {
	if flag == "" {
		panic("dispatchkind: preflight needs a doctor flag")
	}
	return DoctorPreflight{when: when, flag: flag}
}

// Flag returns the doctor flag the preflight adds for this kind, or "" for
// none. explicitSelector is whether the operator named kinds rather than
// taking the bare every-kind default.
func (p DoctorPreflight) Flag(explicitSelector bool) string {
	switch p.when {
	case preflightNone:
		return ""
	case preflightWhenDrawn:
		return p.flag
	case preflightWhenNamed:
		if explicitSelector {
			return p.flag
		}
		return ""
	}
	panic("dispatchkind: doctor preflight unset")
}

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
	DemandSource   DemandSource
	FindingLabel   string          // provenance label settle applies to a filed finding
	PatchLabel     string          // provenance label joining FindingLabel on a finding the host lands as a patch PR (ADR 0057); butler-only
	Contract       PromptContract  // which shared contract block prompt assembly injects
	FilerRelayGate string          // lib/fragments.nix gate selecting the kind's filer-label-relay*.md fragment
	Tracker        Tracker         // which IssueTracker instance this kind's issues live on
	AnnounceVerb   string          // verb of the Box start line (agent/entrypoint.sh); exported to the Box as DISPATCH_ANNOUNCE_VERB (issue #3996)
	Enablement     Enablement      // when the daemon draws this kind; see Enablement
	Preflight      DoctorPreflight // how the daemon's startup preflight passes this kind's doctor flag; see DoctorPreflight
	UnclaimedGate  bool            // merge gate settles a PR on an issue the kind never claimed: no fix passes, and a merge completes it through the configured work Complete label (ADR 0057, issue #4076); butler-only
}

var (
	Work = &Descriptor{
		Name:           "work",
		Verb:           "dispatch",
		Keying:         ByIssue,
		Labels:         LabelsConfigured,
		Settle:         SettleMerge,
		DaemonPriority: PriorityNormal,
		Enablement:     EnabledAlways,
		DemandSource:   DemandTrackerProbe,
		FindingLabel:   "agent-review-finding",
		Contract:       ContractLanding,
		FilerRelayGate: "FILER_FILE_RELAY_WORK",
		Tracker:        TrackerWork,
		AnnounceVerb:   "implementing",
		Preflight:      NoPreflight(),
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
		Enablement:     EnabledAlways,
		DemandSource:   DemandTrackerProbe,
		FindingLabel:   "agent-research-finding",
		Contract:       ContractVerdict,
		FilerRelayGate: "FILER_FILE_RELAY_RESEARCH",
		Tracker:        TrackerResearch,
		AnnounceVerb:   "researching",
		Preflight:      PreflightWhenNamed("--research"),
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
		Enablement:     EnabledByChores,
		DemandSource:   DemandChildReported,
		FindingLabel:   "agent-butler-finding",
		PatchLabel:     "agent-butler-patch",
		Contract:       ContractInline,
		FilerRelayGate: "FILER_FILE_RELAY_BUTLER",
		Tracker:        TrackerWork, // butler files findings onto the work tracker; it has no lifecycle labels of its own
		AnnounceVerb:   "sweeping",
		UnclaimedGate:  true,
		Preflight:      PreflightWhenDrawn("--butler"),
	}
	// Recover is queue-mode `spindrift recover` (issue #4656): it relays a
	// finished work Box's stranded outbox bundle through the work merge gate
	// and runs no Box of its own, so its Box-facing fields mirror work's,
	// spelled as literals because nix/checks/dispatch-labels.nix extracts
	// every FindingLabel literal from this file.
	// PriorityFirst lands stranded work before a new Box starts, so each
	// rebases onto as little new main as possible. Its work is outbox
	// bundles, not tracker issues, so the host counts the eligible ones itself
	// (DemandHostOutbox), leaving out bundles a running daemon child holds;
	// the count is an upper bound, so a child may still exit 2 (nothing
	// eligible).
	Recover = &Descriptor{
		Name:           "recover",
		Verb:           "recover",
		Keying:         ByIssue,
		Labels:         LabelsConfigured,
		Settle:         SettleMerge,
		DaemonPriority: PriorityFirst,
		Enablement:     EnabledByOutboxRelay,
		DemandSource:   DemandHostOutbox,
		FindingLabel:   "agent-review-finding",
		Contract:       ContractLanding,
		FilerRelayGate: "FILER_FILE_RELAY_WORK",
		Tracker:        TrackerWork,
		AnnounceVerb:   "recovering",
		Preflight:      NoPreflight(),
	}
)

// All lists every kind in declaration order; that order is the daemon's
// default pool order.
var All = []*Descriptor{Work, Research, Butler, Recover}

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
