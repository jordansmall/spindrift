package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

// eventVersion is every event line's "v". Bump it only on a breaking change
// — a rename, a removal, or a changed meaning; adding a field never bumps it
// (issue #4713).
const eventVersion = 1

// Event is one JSON-lines record in the daemon's event stream: the durable,
// machine-readable record of every child started and finished, and every
// halt with its reason.
type Event struct {
	V     int    `json:"v"`
	Time  string `json:"time"`
	Event string `json:"event"`
	Kind  Kind   `json:"kind,omitempty"`
	// Kinds is tip_moved's own field, naming the kinds whose backoff the
	// reset actually ended (see pool.go's noteTipMoved) — plural because a
	// moved tip is evidence for every jammed kind at once, not just
	// whichever kind Kind would have named.
	Kinds []Kind `json:"kinds,omitempty"`
	// Key is a box/settled/child_finish event's Dispatch key — a tracker
	// issue for an ordinary Dispatch, a butler Chore for a butler run (ADR
	// 0056, issue #3878) — and zero for every other event. It marshals onto
	// the wire's unchanged "issue"/"chore" fields (see MarshalJSON/
	// UnmarshalJSON, issue #3988), never as "key" itself.
	Key dispatchkey.Key `json:"-"`
	// Phase is the box event's own field — "initial", "fix-pass-N",
	// "conflict-resolve" or "recover" (report.PhaseRecover, queue-mode
	// recover's claim) — carried straight from the child's report.Record
	// (issue #3627); no other event sets it.
	Phase    string `json:"phase,omitempty"`
	Revision string `json:"revision,omitempty"`
	Slot     *int   `json:"slot,omitempty"`
	Exit     *int   `json:"exit,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Wait     string `json:"wait,omitempty"`
	// State and Note are the settled event's own fields, carried straight
	// from the child's report.Record — issue's terminal outcome
	// (complete/failed/recoverable/ambiguous) and any free-text detail.
	State string `json:"state,omitempty"`
	Note  string `json:"note,omitempty"`
	// Failures is the breaker's failure count, stamped on breaker_trip so
	// the event names the transition without cross-referencing an earlier
	// backoff event.
	Failures *int `json:"failures,omitempty"`
	// Ready is demand_appeared/demand_drained/demand_rose's count of startable
	// items the probe found. A pointer so a drained event's 0 survives omitempty.
	Ready *int `json:"ready,omitempty"`
	// Tracker and Until are probe_rate_limited's (Tracker also probe_resumed's):
	// the tracker the paused kinds share and the RFC3339 UTC instant the pause
	// ends.
	Tracker string `json:"tracker,omitempty"`
	Until   string `json:"until,omitempty"`
}

// eventWire is Event's JSON shape: eventFields sheds Event's methods so
// marshalling it doesn't recurse, and Key splits back into the wire's
// "issue"/"chore" pair, which sits at the end of the object.
type eventWire struct {
	eventFields
	Issue string `json:"issue,omitempty"`
	Chore string `json:"chore,omitempty"`
}

type eventFields Event

func (ev Event) MarshalJSON() ([]byte, error) {
	issue, chore := ev.Key.Fields()
	return json.Marshal(eventWire{eventFields: eventFields(ev), Issue: issue, Chore: chore})
}

// UnmarshalJSON leaves Key zero, without error, when neither issue nor chore
// is set: most events carry no key at all. Both set is always an error —
// ParseRecord already enforces exactly one on the way in, so a decoded
// event with both is a genuine wire corruption, not a case to tolerate.
func (ev *Event) UnmarshalJSON(data []byte) error {
	var w eventWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	var key dispatchkey.Key
	if w.Issue != "" || w.Chore != "" {
		var err error
		if key, err = dispatchkey.Parse(w.Issue, w.Chore); err != nil {
			return err
		}
	}
	*ev = Event(w.eventFields)
	ev.Key = key
	return nil
}

// ShutdownDrain is the reason on the shutdown event the daemon emits for the
// first stop signal: docs/reference.md documents this exact string as
// operator-facing grammar, so changing it is a documented-behaviour change,
// not a rename (see Halt.String() in halt.go for the same convention).
const ShutdownDrain = "signalled stop: forwarding a drain request to every running child"

// ShutdownEscalate is the reason on the shutdown event the daemon emits for
// the second stop signal — the escalation forwarded to every running child
// so each reaps and releases (issue #3521 handles the escalation itself;
// the daemon only forwards it). Same documented-string convention as
// ShutdownDrain.
const ShutdownEscalate = "second signal: forwarding the escalation so every child reaps and releases"

// Emitter writes Events as JSON-lines to an injected io.Writer and reports
// advisory write failures to a second, injected error writer.
type Emitter struct {
	mu   sync.Mutex
	w    io.Writer
	errW io.Writer
	now  func() time.Time
	// file, when set, receives a copy of every line written to w.
	file *EventsFile
}

// NewEmitter builds an Emitter writing to w, stamping each Event with now().
// now is injected so tests get a deterministic timestamp and a later slice
// can hand the emitter a real clock. errW is where advisory write failures —
// a failed event or status-file write — are reported; nil means io.Discard.
func NewEmitter(w, errW io.Writer, now func() time.Time) *Emitter {
	if errW == nil {
		errW = io.Discard
	}
	return &Emitter{w: w, errW: errW, now: now}
}

// warnf reports an advisory failure to errW under mu, so a diagnostic cannot
// interleave with an event write when w and errW share a buffer. Emit holds
// mu already and writes to errW itself rather than calling this.
func (e *Emitter) warnf(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fmt.Fprintf(e.errW, format, args...)
}

// Emit writes ev as one JSON line. It is safe to call concurrently — a later
// slice tees child output from another goroutine while the loop goroutine
// emits its own events.
func (e *Emitter) Emit(ev Event) {
	ev.V = eventVersion
	ev.Time = e.now().UTC().Format(time.RFC3339)
	e.mu.Lock()
	defer e.mu.Unlock()
	var line bytes.Buffer
	// Encode appends the trailing newline each JSON-lines record needs. A
	// marshal failure is unreachable (no Event field is unmarshalable), but
	// report it rather than silently drop a record from the durable stream.
	if err := json.NewEncoder(&line).Encode(ev); err != nil {
		fmt.Fprintf(e.errW, "daemon: event encode failed: %v\n", err)
		return
	}
	// A closed or full e.w makes this fail — silently dropping the record
	// would make the durable stream lie about what happened, so report it.
	if _, err := e.w.Write(line.Bytes()); err != nil {
		fmt.Fprintf(e.errW, "daemon: event stream write failed: %v\n", err)
	}
	// Independent of the stdout write above: neither failure may cost the
	// other copy.
	if e.file != nil {
		if err := e.file.append(line.Bytes()); err != nil {
			fmt.Fprintf(e.errW, "daemon: events file write failed: %v\n", err)
		}
	}
}

// TeeEvents makes every later Emit also append its line to f. A second call
// replaces f.
func (e *Emitter) TeeEvents(f *EventsFile) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.file = f
}
