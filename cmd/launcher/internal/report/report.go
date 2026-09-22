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
// containing exactly one JSON object.
type Record struct {
	Event string `json:"event"`
	Issue string `json:"issue"`
	Phase string `json:"phase,omitempty"`
	State string `json:"state,omitempty"`
	Note  string `json:"note,omitempty"`
}

// EventBox and EventSettled are the two Record.Event values this package
// ever writes. Naming them once here and using the name everywhere else
// (internal/daemon's parser, dispatch loop, and pool event stream) means a
// typo in the wire value is a compile error, not a silent parse miss on the
// reading side — issue #3627's review finding. The JSON on the wire is
// unchanged: these are still the bare strings "box"/"settled".
const (
	EventBox     = "box"
	EventSettled = "settled"
)

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

// Box records that a Box started for issue at phase (e.g. "initial",
// "fix-pass-N", "conflict-resolve").
func (r *Reporter) Box(issue, phase string) {
	r.emit(Record{Event: EventBox, Issue: issue, Phase: phase})
}

// Settled records issue's terminal state, once, using the host-decided
// vocabulary from the existing outcome/settle machinery.
func (r *Reporter) Settled(issue, state, note string) {
	r.emit(Record{Event: EventSettled, Issue: issue, State: state, Note: note})
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
