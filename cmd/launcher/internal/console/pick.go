package console

import (
	"fmt"
	"time"
)

// Kind is the dispatch kind a Pick carries. KindResearch (issue #1839) is
// advise-only: it posts one verdict comment instead of opening a branch or PR.
type Kind string

const (
	KindWork     Kind = "work"
	KindResearch Kind = "research"
)

// effectiveKind defaults an unset Kind to KindWork: every Pick literal built
// before #1708 (test fixtures included) leaves Kind empty, so a zero value
// must dispatch as work rather than as an undispatchable third kind.
func (p Pick) effectiveKind() Kind {
	if p.Kind == "" {
		return KindWork
	}
	return p.Kind
}

// PickState is a queue row's position in its launch lifecycle.
type PickState int

const (
	// PickQueued is promoted to Dispatchable but not yet claimed. It holds
	// here while the single launch slot is occupied, and Unpick can still
	// remove it.
	PickQueued PickState = iota
	// PickClaiming has an atomic Dispatchable to InProgress claim in flight.
	PickClaiming
	// PickRunning is a pick whose claim succeeded and whose Box is running.
	PickRunning
	// PickHeld has unsatisfied blockers. It stays Dispatchable on the tracker
	// and re-evaluates on every refill, launching once every blocker reaches
	// Complete. A blocker that landed Failed only sets Reason: the pick stays
	// held, because the Console never auto-unpicks (#650).
	PickHeld
	// PickSettled is a pick whose Dispatch reached settle.
	PickSettled
	// PickDissolved is a pick whose claim failed (raced, closed, relabeled)
	// and so never launched. Reason names why.
	PickDissolved
	// PickTerminated ran and the operator reclaimed it mid-flight (ADR 0024,
	// issue #649).
	PickTerminated
	// PickFailed ran to completion on its own and exited non-zero (issue
	// #705).
	PickFailed
)

// blockerFailedPrefix opens a held pick's Reason when a blocker landed Failed
// (setHeld, queue.go). View's dedup guard (renderQueueColumn, view.go) matches
// the same constant to suppress a Reason that only restates BlockedBy, so a
// format change on one side silently breaks the other (issue #1111).
const blockerFailedPrefix = "blocker "

// String renders s as the word View shows on a queue row.
func (s PickState) String() string {
	switch s {
	case PickQueued:
		return "queued"
	case PickClaiming:
		return "claiming"
	case PickRunning:
		return "running"
	case PickHeld:
		return "held"
	case PickSettled:
		return "settled"
	case PickDissolved:
		return "dissolved"
	case PickTerminated:
		return "terminated"
	case PickFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Section is a named slice of the session's issues the Console body shows one
// at a time (ADR 0030). Backlog is the pick source; the four work Sections
// slice Picks by PickState via pickSection. Values stay contiguous from zero
// so H/L and the 1-5 direct jump index into them without a lookup table.
type Section int

const (
	SectionBacklog Section = iota
	SectionRunning
	SectionHeld
	SectionSettled
	SectionFailed
	// sectionCount is the modulus H/L wrap by and the upper bound the 1-5
	// direct jump validates against.
	sectionCount
)

// String renders s as the word the section tabs show.
func (s Section) String() string {
	switch s {
	case SectionBacklog:
		return "Backlog"
	case SectionRunning:
		return "Running"
	case SectionHeld:
		return "Held"
	case SectionSettled:
		return "Settled"
	case SectionFailed:
		return "Failed"
	default:
		return "unknown"
	}
}

// pickSection maps a PickState onto the work Section that lists it (ADR 0030).
// There are more states than work Sections, so the extras fold in: PickQueued
// and PickClaiming are in flight and not blocked, so they read as
// SectionRunning; PickDissolved and PickTerminated end a pick without a clean
// settle, so they join SectionFailed. SectionSettled means success only.
func pickSection(state PickState) Section {
	switch state {
	case PickHeld:
		return SectionHeld
	case PickSettled:
		return SectionSettled
	case PickDissolved, PickTerminated, PickFailed:
		return SectionFailed
	default: // PickQueued, PickClaiming, PickRunning
		return SectionRunning
	}
}

// formatAge renders d at the coarsest unit that still reads precisely, so the
// age column stays a few characters wide at any scale. Under a minute reads
// "<1m" rather than "0m", so a just-queued pick does not look like a stale one.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// Pick is one row of the session's operator queue: an issue the operator
// has picked, its Dispatch kind, and its current lifecycle state.
type Pick struct {
	Number string
	Title  string
	Kind   Kind
	State  PickState
	Reason string
	// BlockedBy names a PickHeld row's still-open blockers, e.g. "#41
	// (native), #43 (body)", and is "" for every other state.
	BlockedBy string
	// Heartbeat is the last status line RunningHeartbeat captured for a
	// PickRunning row. It is left stale rather than cleared once a pick leaves
	// PickRunning, so a terminal row keeps its last-known detail.
	Heartbeat string
	// PassState is the last-parsed pass-manifest summary for a PickRunning row
	// (issue #2983), left stale the way Heartbeat is. Advisory only: it is a
	// display annotation, never consulted by a settle or dispatch decision.
	PassState string
	// QueuedAt is the wall-clock moment Queue.Add landed this pick. Only the
	// impure Queue sets it, never Update, so a pick a pure Update-only test
	// constructs carries the zero time.Time instead of a nondeterministic
	// time.Now() (issue #1500).
	QueuedAt time.Time
	// Age is QueuedAt's rendered age, precomputed by refreshPickDecorations on
	// every sync so View stays pure and never calls time.Now() itself.
	Age string
}
