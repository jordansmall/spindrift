package console

import (
	"fmt"
	"time"
)

// Kind is the dispatch kind a Pick carries: KindWork for "p"/"P" and the
// detail modal's own "p", KindResearch for "r" (issue #1839) — an
// advise-only pick that posts one verdict comment instead of opening a
// branch/PR.
type Kind string

const (
	KindWork     Kind = "work"
	KindResearch Kind = "research"
)

// effectiveKind returns p.Kind, defaulting an unset ("") Kind to KindWork —
// every Pick literal built before #1708 (test fixtures included) never sets
// Kind, and dispatch.Config.Kind's own established empty-defaults-to-work
// convention (buildBoxEnv) gives precedent for the same fallback here rather
// than treating a zero-value Kind as a third, undispatchable kind.
func (p Pick) effectiveKind() Kind {
	if p.Kind == "" {
		return KindWork
	}
	return p.Kind
}

// PickState is a queue row's position in its launch lifecycle.
type PickState int

const (
	// PickQueued is a pick that has been promoted to Dispatchable but not
	// yet claimed — it holds here for as long as the single launch slot is
	// occupied, and Unpick can still remove it.
	PickQueued PickState = iota
	// PickClaiming is a pick whose atomic Dispatchable->InProgress claim is
	// in flight.
	PickClaiming
	// PickRunning is a pick whose claim succeeded and whose Box is running.
	PickRunning
	// PickHeld is a pick whose declared blockers are not all satisfied yet —
	// it stays Dispatchable on the tracker and re-evaluates on every refill,
	// launching the moment every blocker reaches Complete. BlockedBy names
	// the still-open blockers; Reason carries a blockerFailedPrefix-prefixed
	// note when one of them landed Failed, but the pick stays held — the
	// Console never auto-unpicks (#650).
	PickHeld
	// PickSettled is a pick whose Dispatch reached settle.
	PickSettled
	// PickDissolved is a pick whose claim failed (raced, closed,
	// relabeled) — Reason names why. A dissolved pick never launches.
	PickDissolved
	// PickTerminated is a pick the operator ended by hand (ADR 0024, issue
	// #649) — distinct from PickDissolved (a claim that never launched):
	// this pick ran, and the operator reclaimed it mid-flight.
	PickTerminated
	// PickFailed is a pick whose Box ran and exited non-zero (issue #705) —
	// distinct from PickDissolved (a claim that never launched, see
	// PickDissolvedMsg in msg.go) and PickTerminated (the operator ended a
	// still-running pick by hand): this pick ran to completion on its own
	// and failed.
	PickFailed
)

// blockerFailedPrefix opens a held pick's Reason when a declared blocker
// landed Failed (setHeld, queue.go). View's dedup guard (renderQueueColumn,
// view.go) checks the same constant to recognize and suppress a Reason that
// only restates BlockedBy — the two must share one source, or a format
// change in one silently breaks the other's match (issue #1111).
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
// 0030's "Running / Held / Settled / Failed slice the work queue by
// PickState"). There are more PickStates than work Sections, so states
// without a same-named Section fold into the closest one: PickQueued and
// PickClaiming are still active in the pipeline, not yet running but not
// blocked either, so they read as SectionRunning alongside PickRunning
// itself. PickDissolved (a claim that never launched) and PickTerminated
// (the operator ended it, ADR 0024) both end a pick without a clean settle,
// so they join PickFailed in SectionFailed — SectionSettled is reserved for
// an actual successful completion.
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

// formatAge renders d at the coarsest unit that still reads precisely: whole
// minutes under an hour, hours+minutes under a day, whole days beyond that —
// so the work Sections' age column stays a handful of characters wide
// however long a pick has been queued, rather than growing to hh:mm:ss at
// every scale. Anything under a minute reads "<1m" rather than "0m", so a
// pick that just queued doesn't look identical to one already stale.
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
	// PickRunning row — "" until a running Box's log carries at least one
	// complete heartbeat line, and left stale (not cleared) once a pick
	// leaves PickRunning, matching every other terminal-state row that keeps
	// its last-known detail rather than blanking it.
	Heartbeat string
	// QueuedAt is the wall-clock moment Queue.Add landed this pick — the
	// source Age formats from. Set by the impure Queue, never by Update, so
	// a pick a pure Update-only test constructs (no Launcher) carries the
	// zero time.Time rather than a nondeterministic time.Now() (issue
	// #1500).
	QueuedAt time.Time
	// Age is QueuedAt's rendered age (e.g. "3m", "1h12m", "2d"), precomputed
	// by refreshPickDecorations on every sync the same way Heartbeat is — View stays pure
	// and never calls time.Now() itself. "" until the first sync populates
	// it.
	Age string
}
