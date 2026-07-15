package console

// Kind is the dispatch kind a Pick carries. Only KindWork is exposed by the
// operator-facing commands today; KindResearch exists so the Pick record
// does not need a remodel when research dispatch ships end-to-end (#646).
type Kind string

const (
	KindWork     Kind = "work"
	KindResearch Kind = "research"
)

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
	// PickSettled is a pick whose Dispatch reached settle.
	PickSettled
	// PickDissolved is a pick whose claim failed (raced, closed,
	// relabeled) — Reason names why. A dissolved pick never launches.
	PickDissolved
)

// String renders s as the word View shows on a queue row.
func (s PickState) String() string {
	switch s {
	case PickQueued:
		return "queued"
	case PickClaiming:
		return "claiming"
	case PickRunning:
		return "running"
	case PickSettled:
		return "settled"
	case PickDissolved:
		return "dissolved"
	default:
		return "unknown"
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
	// Heartbeat is the last status line RunningHeartbeat captured for a
	// PickRunning row — "" until a running Box's log carries at least one
	// complete heartbeat line, and left stale (not cleared) once a pick
	// leaves PickRunning, matching every other terminal-state row that keeps
	// its last-known detail rather than blanking it.
	Heartbeat string
}
