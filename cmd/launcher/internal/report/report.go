// Package report gives the child (Launcher) process a way to tell the parent
// (daemon) what it is doing without polluting stdout, which the Box and its
// subprocesses already own. The parent hands down a pipe write-end fd via
// SPINDRIFT_REPORT_FD; this package writes one JSON line per event to it.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

// MaxLine is the largest line this package writes and the most the daemon's
// reader (cmd/launcher/daemon/runner.go's readReports) accepts, newline
// included. It is the single shared number for both sides: the writer
// (emit, below) clips Note to keep every line within it, and the reader
// sizes its buffer to it, so the two can never drift apart (issue #3627's
// review finding — before this, a Box's long free-text note made the
// encoded line exceed the reader's separately-declared 4096 and the whole
// record was silently discarded).
const MaxLine = 4096

// clipMark is appended to a shortened Note so a reader can tell the note
// was clipped rather than merely short.
const clipMark = "…"

// Record is one line of the report protocol, always terminated by "\n" and
// containing exactly one JSON object. Key is an issue for an ordinary
// Dispatch and a Chore for a butler run (ADR 0056); daemon.ParseRecord
// enforces on the reading side that a known event carries one.
type Record struct {
	Event string
	Key   dispatchkey.Key
	Phase string
	State string
	Note  string
	// NextDue is a not_due record's answer, parsed off the wire by
	// UnmarshalJSON; zero on every other event.
	NextDue NextDue
}

// NextDue is a not_due record's answer: the earliest instant a Chore lifts
// (zero for none) and whether some Chore waits on a branch-head move instead.
// The zero value is no answer, which is never written or accepted.
type NextDue struct {
	At        time.Time
	OnTipMove bool
}

// IsZero reports that n carries no answer.
func (n NextDue) IsZero() bool { return n.At.IsZero() && !n.OnTipMove }

// Merge folds another record into n: the earliest non-zero instant, and
// OnTipMove if either has it.
func (n NextDue) Merge(o NextDue) NextDue {
	if !o.At.IsZero() && (n.At.IsZero() || o.At.Before(n.At)) {
		n.At = o.At
	}
	n.OnTipMove = n.OnTipMove || o.OnTipMove
	return n
}

// wire is n's next_due string: UTC at full nanosecond precision, since a live
// claim lifts one nanosecond past its timeout. "" for the zero value.
func (n NextDue) wire() string {
	switch {
	case n.OnTipMove:
		return NextDueOnTipMove
	case n.At.IsZero():
		return ""
	}
	return n.At.UTC().Format(time.RFC3339Nano)
}

// recordWire is Record's JSON shape. The Key splits back into the
// "issue"/"chore" pair the wire has always carried, so records stay
// byte-identical across the move to dispatchkey.Key (issue #3988).
type recordWire struct {
	Event string `json:"event"`
	Issue string `json:"issue,omitempty"`
	Chore string `json:"chore,omitempty"`
	Phase string `json:"phase,omitempty"`
	State string `json:"state,omitempty"`
	Note  string `json:"note,omitempty"`

	NextDue string `json:"next_due,omitempty"`
}

func (r Record) MarshalJSON() ([]byte, error) {
	issue, chore := r.Key.Fields()
	return json.Marshal(recordWire{Event: r.Event, Issue: issue, Chore: chore, Phase: r.Phase, State: r.State, Note: r.Note, NextDue: r.NextDue.wire()})
}

// UnmarshalJSON leaves Key zero, without error, when neither issue nor chore
// is set: an event this reader doesn't know yet may carry no key, and
// ParseRecord decides whether a known one may. Both set is always an error.
func (r *Record) UnmarshalJSON(data []byte) error {
	var w recordWire
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
	var nd NextDue
	// Parsed only for a not_due: an event this reader doesn't know yet may
	// carry a next_due of its own shape.
	if w.Event == EventNotDue && w.NextDue != "" {
		var err error
		if nd, err = ParseNextDue(w.NextDue); err != nil {
			return err
		}
	}
	*r = Record{Event: w.Event, Key: key, Phase: w.Phase, State: w.State, Note: w.Note, NextDue: nd}
	return nil
}

// EventBox, EventSettled and EventNotDue are the Record.Event values this
// package ever writes. Naming them once here and using the name everywhere else
// (internal/daemon's parser, dispatch loop, and pool event stream) means a
// typo in the wire value is a compile error, not a silent parse miss on the
// reading side — issue #3627's review finding. The JSON on the wire is
// unchanged: these are still the bare strings "box"/"settled"/"not_due".
const (
	EventBox     = "box"
	EventSettled = "settled"
	EventNotDue  = "not_due"
)

// NextDueOnTipMove is the next_due wire value for a Chore that only a
// branch-head move lifts, so no instant names when it becomes due.
const NextDueOnTipMove = "on_tip_move"

// ParseNextDue is the one decoder for a not_due record's NextDue: the
// NextDueOnTipMove sentinel, or an RFC3339Nano instant. Anything else,
// including the empty string and the zero instant (which the writer's own
// zero value would produce), is an error.
func ParseNextDue(s string) (NextDue, error) {
	if s == NextDueOnTipMove {
		return NextDue{OnTipMove: true}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return NextDue{}, fmt.Errorf("report: invalid next_due %q: %w", s, err)
	}
	if at.IsZero() {
		return NextDue{}, fmt.Errorf("report: invalid next_due %q: zero instant", s)
	}
	return NextDue{At: at}, nil
}

// PhaseInitial and PhaseConflictResolve are two of the three Box.phase
// values this package's callers ever pass; the third, a fix pass, has no
// fixed spelling since it carries a pass number — build it with
// PhaseFixPass instead. Naming the fixed two here, rather than leaving them
// as bare literals at each dispatch call site, keeps the vocabulary this
// doc comment above and dispatch's announce map between spelled once.
const (
	PhaseInitial         = "initial"
	PhaseConflictResolve = "conflict-resolve"
)

// PhaseFixPass builds the phase value for a fix pass ("fix-pass-N"), the one
// Box.phase value that isn't a fixed constant.
func PhaseFixPass(pass int) string {
	return fmt.Sprintf("fix-pass-%d", pass)
}

// Reporter writes Records to a single fd. All methods are nil-receiver safe
// so call sites never need to branch on "is reporting on" — a nil *Reporter
// (the FromEnv result when SPINDRIFT_REPORT_FD is unset or refused) makes
// every call a no-op.
type Reporter struct {
	mu sync.Mutex
	// fd is the raw, unowned report-pipe descriptor the parent daemon opened
	// and passed down. Wrapping it in an *os.File (os.NewFile) would make Go
	// treat it as owned and attach a finalizer that closes it when the
	// Reporter becomes unreachable — closing a descriptor someone else still
	// holds, and later recycling its number onto an unrelated open. So this
	// package only ever reads/writes fd via syscall, never hands it to
	// anything (os.NewFile, exec.Cmd.ExtraFiles, ...) that would claim
	// ownership, and never closes it itself.
	fd int
}

// Box records that a Box started for key at phase (e.g. "initial",
// "fix-pass-N", "conflict-resolve"). key is issue-keyed for an ordinary
// dispatch or Chore-keyed for a butler run (ADR 0056) — same method, either
// key shape.
func (r *Reporter) Box(key dispatchkey.Key, phase string) {
	r.emit(Record{Event: EventBox, Key: key, Phase: phase})
}

// Settled records key's terminal state, once, using the host-decided
// vocabulary from the existing outcome/settle machinery.
func (r *Reporter) Settled(key dispatchkey.Key, state, note string) {
	r.emit(Record{Event: EventSettled, Key: key, State: state, Note: note})
}

// NotDue records that key's Chore is not due, and when it will be. A zero
// next is dropped: the reader rejects a not_due that names neither an instant
// nor a tip move.
func (r *Reporter) NotDue(key dispatchkey.Key, next NextDue) {
	if next.IsZero() {
		return
	}
	r.emit(Record{Event: EventNotDue, Key: key, NextDue: next})
}

// emit swallows write failures: a broken report pipe (parent gone, pipe
// full and non-blocking, whatever) must never fail a dispatch. The reporter
// is a side channel for the parent's convenience, not part of the contract
// the child's own success/failure depends on.
//
// The swallow can cost the next record too: a failure after a partial write
// leaves a newline-less fragment the reader glues onto whatever emit writes
// next. Bytes already on the pipe cannot be un-written, and blocking or
// failing the child over a side channel is the worse trade.
func (r *Reporter) emit(rec Record) {
	if r == nil {
		return
	}
	line, ok := clippedLine(rec)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Records are well under PIPE_BUF, so a single successful write is
	// atomic against a sibling writer on the same pipe; the loop below is
	// only for the short-write/EINTR cases syscall.Write can still return.
	for len(line) > 0 {
		n, err := syscall.Write(r.fd, line)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return
		}
		// A zero-byte write with no error is not a documented syscall.Write
		// outcome, but nothing rules it out on every platform; treat it as a
		// failed write rather than loop forever holding r.mu.
		if n == 0 {
			return
		}
		line = line[n:]
	}
}

// clippedLine marshals rec to its wire line (JSON + "\n"), shortening Note
// as needed so the result never exceeds MaxLine — Note is free text (e.g. a
// Box's blocked-status reason), the one field a caller can hand this
// package of unbounded length. A shortened Note always keeps clipMark, so an
// absent note field on the wire never means "there was a reason and we
// dropped it". It reports ok=false only when not even clipMark alone fits;
// the caller writes nothing rather than a record the reader would just
// discard anyway.
func clippedLine(rec Record) (line []byte, ok bool) {
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, false
	}
	line = append(line, '\n')
	if len(line) <= MaxLine {
		// Already fits: return byte-identical to the pre-clipping encoding,
		// no mark added.
		return line, true
	}
	if rec.Note == "" {
		return nil, false
	}
	// JSON escaping inflates one raw byte into up to six encoded ones (quotes,
	// control chars, <, >, &), so the line's overshoot in encoded bytes is not
	// a count of runes to drop — subtracting one from the other over-clips by
	// up to 6x, and clamping that against the rune count empties the note
	// outright (issue #3627's review finding). Encoded length is instead
	// non-decreasing in the kept rune count, so binary-search for the longest
	// prefix whose re-encoded line fits and measure every candidate encoded.
	note := []rune(rec.Note)
	encode := func(keep int) ([]byte, bool) {
		candidate := rec
		candidate.Note = string(note[:keep]) + clipMark
		cl, err := json.Marshal(candidate)
		if err != nil {
			return nil, false
		}
		return append(cl, '\n'), true
	}
	best, encOK := encode(0)
	if !encOK || len(best) > MaxLine {
		return nil, false
	}
	// The full note already overflowed without the mark, so keeping all of it
	// plus the mark cannot fit: the search's upper bound is one rune short.
	lo, hi := 1, len(note)-1
	for lo <= hi {
		mid := lo + (hi-lo)/2
		cl, encOK := encode(mid)
		if !encOK {
			return nil, false
		}
		if len(cl) <= MaxLine {
			best = cl
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, true
}

// FromEnv reads SPINDRIFT_REPORT_FD via getenv and, if set, validates and
// wraps the named descriptor. It returns nil (writing nothing) when the
// variable is unset or empty, and also returns nil — after printing one
// line to stderr — when the value doesn't name an open pipe descriptor: an
// unparseable number, a closed fd, or an fd pointing at anything but a
// named pipe are all refused rather than risking a crash or writing into
// the wrong file.
//
// On success the descriptor is marked close-on-exec before FromEnv returns,
// so every later spawn (Box, runtime, any subprocess) inherits nothing and
// can never hold the write end open.
func FromEnv(getenv func(string) string, stderr io.Writer) *Reporter {
	v := getenv("SPINDRIFT_REPORT_FD")
	if v == "" {
		return nil
	}
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 0 {
		fmt.Fprintf(stderr, "report: SPINDRIFT_REPORT_FD=%q is not a valid descriptor number, not reporting\n", v)
		return nil
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		fmt.Fprintf(stderr, "report: SPINDRIFT_REPORT_FD=%d: %v, not reporting\n", fd, err)
		return nil
	}
	// Stat_t.Mode is uint32 on linux, uint16 on darwin; widen before masking
	// so the same comparison works on both.
	if uint32(st.Mode)&syscall.S_IFMT != syscall.S_IFIFO {
		fmt.Fprintf(stderr, "report: SPINDRIFT_REPORT_FD=%d is not a pipe, not reporting\n", fd)
		return nil
	}
	syscall.CloseOnExec(fd)
	return &Reporter{fd: fd}
}
