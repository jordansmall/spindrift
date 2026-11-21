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
// included. One shared number, so writer and reader cannot drift: a reader
// limit declared separately once silently discarded a long-Note record
// (issue #3627).
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
	// PassLog is a box record's Pass log: the file that pass writes, relative
	// to the checkout. Empty on every other event, and on a recover box, which
	// runs no Box.
	PassLog string
	// RecordID is a box or settled record's Dispatch Record ID, minted once at claim and
	// shared by every Pass log of the Dispatch. Empty on every other event, and
	// on a recover box, which runs no Box.
	RecordID string
	// PRURL is a settled record's PR: the one the Dispatch opened or adopted,
	// empty when none; a ci_wait record's PR, the one the host now waits on.
	// Empty on every other event.
	PRURL string
	// RunURL is a ci_wait record's CI run for the PR's head commit, empty until
	// the forge registers one or when it cannot report one. Empty on every
	// other event.
	RunURL string
	// NextDue is a not_due record's answer, parsed off the wire by
	// UnmarshalJSON; zero on every other event.
	NextDue NextDue
	// Model and ModelRole are a model record's fields: the exact model id now
	// behind the Box's messages and the role speaking (empty when the driver
	// cannot name one). Empty on every other event.
	Model     string
	ModelRole string
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

	PassLog  string `json:"pass_log,omitempty"`
	RecordID string `json:"record_id,omitempty"`
	PRURL    string `json:"pr_url,omitempty"`
	RunURL   string `json:"run_url,omitempty"`
	NextDue  string `json:"next_due,omitempty"`

	Model     string `json:"model,omitempty"`
	ModelRole string `json:"model_role,omitempty"`
}

func (r Record) MarshalJSON() ([]byte, error) {
	issue, chore := r.Key.Fields()
	return json.Marshal(recordWire{Event: r.Event, Issue: issue, Chore: chore, Phase: r.Phase, State: r.State, Note: r.Note, PassLog: r.PassLog, RecordID: r.RecordID, PRURL: r.PRURL, RunURL: r.RunURL, NextDue: r.NextDue.wire(), Model: r.Model, ModelRole: r.ModelRole})
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
	*r = Record{Event: w.Event, Key: key, Phase: w.Phase, State: w.State, Note: w.Note, PassLog: w.PassLog, RecordID: w.RecordID, PRURL: w.PRURL, RunURL: w.RunURL, NextDue: nd, Model: w.Model, ModelRole: w.ModelRole}
	return nil
}

// EventBox, EventSettled, EventNotDue, EventModel, EventCIWait and
// EventImageBuild are the Record.Event values this package ever writes.
// Every reader uses these names, so a typo is a compile error rather than a
// silent parse miss (issue #3627).
const (
	EventBox     = "box"
	EventSettled = "settled"
	EventNotDue  = "not_due"
	EventModel   = "model"
	EventCIWait  = "ci_wait"

	// EventImageBuild is keyless: the Box image builds in bootstrap, before any
	// dispatch key exists, so the record carries only a State.
	EventImageBuild = "image_build"
)

// The names below are the events the Daemon itself writes to the Events file,
// as opposed to the Record.Event values above, which a child writes and the
// Daemon relays.
const (
	EventAwakeClose          = "awake_close"
	EventAwakeOpen           = "awake_open"
	EventBackoff             = "backoff"
	EventBatonHold           = "baton_hold"
	EventBatonPass           = "baton_pass"
	EventBreakerTrip         = "breaker_trip"
	EventChildFinish         = "child_finish"
	EventChildInherited      = "child_inherited"
	EventChildStart          = "child_start"
	EventDemandAppeared      = "demand_appeared"
	EventDemandDrained       = "demand_drained"
	EventDemandRose          = "demand_rose"
	EventDemandSourceMissing = "demand_source_missing"
	EventHalt                = "halt"
	EventIdle                = "idle"
	EventJam                 = "jam"
	EventPreflight           = "preflight"
	EventProbeFailed         = "probe_failed"
	EventProbeRateLimited    = "probe_rate_limited"
	EventProbeResumed        = "probe_resumed"
	EventSelfUpdate          = "self_update"
	EventShutdown            = "shutdown"
	EventTipMoved            = "tip_moved"
)

// Events is every event name the Daemon writes to the Events file: the relayed
// child Record events that reach it (box, settled, model, image_build) plus the
// Daemon's own. not_due and ci_wait are Record-only: they update a flight and
// never reach the file. nix/checks/event-stream-parity.nix holds this equal to
// the emit sites. A fresh slice each call, so no caller can mutate shared
// state.
func Events() []string {
	return []string{
		EventBox, EventSettled, EventModel, EventImageBuild,
		EventAwakeClose, EventAwakeOpen, EventBackoff, EventBatonHold, EventBatonPass,
		EventBreakerTrip, EventChildFinish, EventChildInherited, EventChildStart,
		EventDemandAppeared, EventDemandDrained, EventDemandRose, EventDemandSourceMissing,
		EventHalt, EventIdle, EventJam, EventPreflight, EventProbeFailed,
		EventProbeRateLimited, EventProbeResumed, EventSelfUpdate, EventShutdown,
		EventTipMoved,
	}
}

// ImageBuildState is an image_build record's State. The type keeps the state
// vocabulary apart from other strings, so a plain string variable does not
// compile where a state is expected; a misspelled literal still compiles, and
// only daemon.ParseRecord's runtime check rejects it. Record.State stays a
// plain string because ci_wait shares it.
type ImageBuildState string

const (
	ImageBuildStarted  ImageBuildState = "started"
	ImageBuildFinished ImageBuildState = "finished"
)

// NextDueOnTipMove is the next_due wire value for a Chore that only a
// branch-head move lifts, so no instant names when it becomes due. The
// Dashboard mirrors it; the contract fixture
// (dashboard/testdata/contract) pins it.
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

// PhaseInitial, PhaseConflictResolve and PhaseRecover are the fixed Box.phase
// values; a fix pass carries a number, so build it with PhaseFixPass.
// PhaseRecover is queue-mode recover's claim: it runs no Box, so it announces
// itself with this phase to give the daemon's flight its key.
const (
	PhaseInitial         = "initial"
	PhaseConflictResolve = "conflict-resolve"
	PhaseRecover         = "recover"
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
	// fd is the raw, unowned report-pipe descriptor the parent passed down.
	// Never hand it to os.NewFile, exec.Cmd.ExtraFiles or anything else that
	// claims ownership: a finalizer would close a descriptor someone else
	// holds and let its number be recycled. Only syscall touches it.
	fd int
}

// Box records that a Box started for key (issue or Chore, ADR 0056) at phase.
// passLog is the Pass log the phase writes, relative to the checkout; "" when
// the phase runs no Box (PhaseRecover). recordID is the Dispatch Record the
// phase belongs to, "" when none was minted.
func (r *Reporter) Box(key dispatchkey.Key, phase, passLog, recordID string) {
	r.emit(Record{Event: EventBox, Key: key, Phase: phase, PassLog: passLog, RecordID: recordID})
}

// Settled records key's terminal state, once, using the host-decided
// vocabulary from the existing outcome/settle machinery. prURL is the PR the
// Dispatch opened or adopted, "" when none; recordID is the Dispatch Record the
// state settles, "" when none was minted.
func (r *Reporter) Settled(key dispatchkey.Key, state, note, prURL, recordID string) {
	r.emit(Record{Event: EventSettled, Key: key, State: state, Note: note, PRURL: prURL, RecordID: recordID})
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

// Model records that the Box's messages now come from the exact model id
// model, speaking as role ("" when the driver cannot name one). The caller
// debounces: one record per change of the (role, model) pair, not per message.
func (r *Reporter) Model(key dispatchkey.Key, model, role string) {
	r.emit(Record{Event: EventModel, Key: key, Model: model, ModelRole: role})
}

// CIWait records that the host has entered a CI wait on key's PR prURL, so the
// parent can show the Dispatch as waiting on CI. One record per wait entered,
// plus one each time runURL first appears or changes mid-wait; never per poll.
func (r *Reporter) CIWait(key dispatchkey.Key, prURL, runURL string) {
	r.emit(Record{Event: EventCIWait, Key: key, PRURL: prURL, RunURL: runURL})
}

// ImageBuild records that the Box image build entered state (ImageBuildStarted
// or ImageBuildFinished). Keyless: see EventImageBuild.
func (r *Reporter) ImageBuild(state ImageBuildState) {
	r.emit(Record{Event: EventImageBuild, State: string(state)})
}

// emit swallows write failures: a broken report pipe must never fail a
// dispatch, since the reporter is only a side channel. The swallow can cost
// the next record too (a partial write leaves a newline-less fragment the
// reader glues onto it), which beats blocking or failing the child.
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
	// MaxLine can exceed PIPE_BUF (512 on darwin), so one write(2) is not
	// portably atomic; r.mu plus FromEnv's close-on-exec (this process is the
	// pipe's only writer) keep records from interleaving.
	for len(line) > 0 {
		n, err := syscall.Write(r.fd, line)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return
		}
		// A zero-byte write with no error is undocumented but not ruled out;
		// treat it as a failure rather than spin forever holding r.mu.
		if n == 0 {
			return
		}
		line = line[n:]
	}
}

// clippedLine marshals rec to its wire line (JSON + "\n"), shortening Note,
// the one unbounded field, so the result never exceeds MaxLine. A shortened
// Note always keeps clipMark, so an absent note never means a dropped reason.
// ok=false only when not even clipMark alone fits.
func clippedLine(rec Record) (line []byte, ok bool) {
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, false
	}
	line = append(line, '\n')
	if len(line) <= MaxLine {
		return line, true
	}
	if rec.Note == "" {
		return nil, false
	}
	// JSON escaping inflates a byte up to sixfold, so the encoded overshoot is
	// not a rune count to drop: subtracting it emptied the note (issue #3627).
	// Encoded length is non-decreasing in kept runes, so binary-search the
	// longest prefix whose re-encoded line fits.
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
// wraps the named descriptor. It returns nil when the variable is unset, and
// nil after one stderr line when the value is not an open pipe descriptor,
// rather than write into the wrong file. On success the fd is marked
// close-on-exec, so no later spawn can hold the write end open.
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
