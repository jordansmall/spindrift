package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// emitErrW is where Emit reports a failed Encode. A package-level var rather
// than an Emitter field so NewEmitter keeps its two-argument shape and its
// call sites stay put; tests swap it to assert on the diagnostic. Carrying
// mainRun's own stderr here instead — so this diagnostic lands where every
// other one does — needs the field and the signature change, and is left to
// a follow-up.
var emitErrW io.Writer = os.Stderr

// Event is one JSON-lines record in the daemon's event stream: the durable,
// machine-readable record of every child started and finished, and every
// halt with its reason.
type Event struct {
	Time  string `json:"time"`
	Event string `json:"event"`
	Kind  Kind   `json:"kind,omitempty"`
	// Kinds is tip_moved's own field, naming the kinds whose backoff the
	// reset actually ended (see pool.go's pollSlices) — plural because a
	// moved tip is evidence for every jammed kind at once, not just
	// whichever kind Kind would have named.
	Kinds    []Kind `json:"kinds,omitempty"`
	Issue    string `json:"issue,omitempty"`
	Revision string `json:"revision,omitempty"`
	Slot     *int   `json:"slot,omitempty"`
	Exit     *int   `json:"exit,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Wait     string `json:"wait,omitempty"`
	// Failures is the breaker's failure count, stamped on breaker_trip so
	// the event names the transition without cross-referencing an earlier
	// backoff event.
	Failures *int `json:"failures,omitempty"`
}

// ShutdownDrain is the reason on the shutdown event the daemon emits for the
// first stop signal: docs/reference.md documents this exact string as
// operator-facing grammar, so changing it is a documented-behaviour change,
// not a rename (see HaltSelfBuildPrefix in outcome.go for the same
// convention).
const ShutdownDrain = "signalled stop: forwarding a drain request to every running child"

// ShutdownEscalate is the reason on the shutdown event the daemon emits for
// the second stop signal — the escalation forwarded to every running child
// so each reaps and releases (issue #3521 handles the escalation itself;
// the daemon only forwards it). Same documented-string convention as
// ShutdownDrain.
const ShutdownEscalate = "second signal: forwarding the escalation so every child reaps and releases"

// Emitter writes Events as JSON-lines to an injected io.Writer.
type Emitter struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewEmitter builds an Emitter writing to w, stamping each Event with now().
// now is injected so tests get a deterministic timestamp and a later slice
// can hand the emitter a real clock.
func NewEmitter(w io.Writer, now func() time.Time) *Emitter {
	return &Emitter{w: w, now: now}
}

// Emit writes ev as one JSON line. It is safe to call concurrently — a later
// slice tees child output from another goroutine while the loop goroutine
// emits its own events.
func (e *Emitter) Emit(ev Event) {
	ev.Time = e.now().UTC().Format(time.RFC3339)
	e.mu.Lock()
	defer e.mu.Unlock()
	enc := json.NewEncoder(e.w)
	// Encode appends the trailing newline each JSON-lines record needs. A
	// marshal failure is unreachable (no Event field is unmarshalable), but
	// Encode also does the write, and a closed or full e.w makes that fail —
	// silently dropping the record would make the durable stream lie about
	// what happened, so report it instead of swallowing it.
	if err := enc.Encode(ev); err != nil {
		fmt.Fprintf(emitErrW, "daemon: event stream write failed: %v\n", err)
	}
}
