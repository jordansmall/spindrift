package console

import (
	"fmt"
	"time"
)

// Kind is the dispatch kind a Pick carries. KindResearch is advise-only: it
// posts one verdict comment instead of opening a branch/PR.
type Kind string

const (
	KindWork     Kind = "work"
	KindResearch Kind = "research"
)

// effectiveKind returns p.Kind, defaulting an unset ("") Kind to KindWork —
// the same empty-defaults-to-work convention dispatch.Config.Kind follows,
// rather than treating a zero-value Kind as a third, undispatchable kind.
func (p Pick) effectiveKind() Kind {
	if p.Kind == "" {
		return KindWork
	}
	return p.Kind
}

// PickState is a queue row's position in its launch lifecycle.
type PickState int

const (
	// Promoted to Dispatchable but not yet claimed: it holds here while the
	// launch slots are occupied, and Unpick can still remove it.
	PickQueued PickState = iota
	// The atomic Dispatchable->InProgress claim is in flight.
	PickClaiming
	// The claim succeeded and the Box is running.
	PickRunning
	// Declared blockers aren't all satisfied. The pick stays Dispatchable on
	// the tracker and re-evaluates on every refill, launching the moment
	// every blocker reaches Complete. BlockedBy names the still-open
	// blockers; Reason carries a blockerFailedPrefix-prefixed note when one
	// landed Failed, but the pick stays held — the Console never auto-unpicks.
	PickHeld
	PickSettled
	// The claim failed (raced, closed, relabeled) and Reason names why. A
	// dissolved pick never launches.
	PickDissolved
	// Ran, and the operator reclaimed it mid-flight (ADR 0024).
	PickTerminated
	// Ran to completion on its own and exited non-zero.
	PickFailed
)

// blockerFailedPrefix opens a held pick's Reason when a declared blocker
// landed Failed (setHeld, queue.go). View's dedup guard (renderQueueColumn,
// view.go) checks the same constant to suppress a Reason that only restates
// BlockedBy — the two must share one source, or a format change in one
// silently breaks the other's match.
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

// Section is a named slice of the session's issues the Console body shows
// one at a time (ADR 0030): Backlog is the pick source; the four work
// Sections slice Picks by PickState via pickSection. Values are contiguous
// from zero so H/L (prev/next) and 1-5 (direct jump) can index straight into
// them without a lookup table.
type Section int

const (
	SectionBacklog Section = iota
	SectionRunning
	SectionHeld
	SectionSettled
	SectionFailed
	// sectionCount is the number of Sections — the modulus H/L wrap by, and
	// the upper bound 1-5 direct-jump validates against.
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

// pickSection maps a PickState onto the work Section that lists it (ADR
// 0030). There are more PickStates than Sections, so states without a
// same-named Section fold into the closest one: PickQueued/PickClaiming are
// still active but not blocked, so they read as SectionRunning; PickDissolved
// and PickTerminated both end a pick without a clean settle, so they join
// PickFailed — SectionSettled is reserved for successful completion.
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
// age column stays a few characters wide at every scale. Anything under a
// minute reads "<1m" rather than "0m", so a pick that just queued doesn't
// look identical to one already stale.
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
	// (native), #43 (body)" — "" for every other state.
	BlockedBy string
	// Heartbeat is the last status line RunningHeartbeat captured for a
	// PickRunning row — "" until a running Box's log carries one complete
	// heartbeat line, and left stale (not cleared) once a pick leaves
	// PickRunning, like every other terminal-state row's last-known detail.
	Heartbeat string
	// QueuedAt is the wall-clock moment Queue.Add landed this pick. Set by
	// the impure Queue, never by Update, so a pick a pure Update-only test
	// constructs carries the zero time.Time rather than a nondeterministic
	// time.Now().
	QueuedAt time.Time
	// Age is QueuedAt's rendered age (e.g. "3m", "1h12m", "2d"), precomputed
	// by refreshPickDecorations on every sync so View stays pure and never
	// calls time.Now() itself. "" until the first sync populates it.
	Age string
}
