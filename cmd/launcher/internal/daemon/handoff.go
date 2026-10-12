package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
)

// HandoffVersion is the version the writer stamps. A reader also accepts
// handoffMinVersion, so a build still resumes from an older predecessor's
// document, and refuses every other version: a misread child pid or
// descriptor would signal or read the wrong process.
const HandoffVersion = 2

// HandoffRollbackVersion is the first version whose readers know the rollback
// fields. A successor rolls back only onto a predecessor that wrote at least
// this, since an older reader refuses the document and orphans the children.
const HandoffRollbackVersion = 2

// handoffMinVersion is the oldest document version DecodeHandoff still reads.
const handoffMinVersion = 1

// Handoff is the state a daemon passes to its successor so the successor can
// adopt the children still running instead of waiting them out (ADR 0063).
type Handoff struct {
	V int `json:"v"`
	// LockFD is the inherited descriptor holding the instance lock.
	LockFD int `json:"lock_fd"`
	// FromPath, ToPath and Revision name the restart for the successor to
	// record: the predecessor's own program, the prebuilt program it exec'd and
	// that program's revision. The predecessor does not emit the handoff event
	// itself, since an exec that then fails would leave one behind.
	FromPath string `json:"from_path,omitempty"`
	ToPath   string `json:"to_path,omitempty"`
	Revision string `json:"revision,omitempty"`
	// RolledBack marks a handoff in the reverse direction: the successor
	// refused to start after re-attaching and exec'd back to the build it came
	// from. FromPath is then the refusing build, ToPath the build resumed,
	// Revision the revision whose build was refused, and Reason the refusal.
	RolledBack bool   `json:"rolled_back,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Stop and Abort carry the stop latch's state.
	Stop  bool `json:"stop,omitempty"`
	Abort bool `json:"abort,omitempty"`
	// SIGHUPIgnored: the predecessor's SIGHUP was ignored before the exec, so
	// the successor keeps it ignored rather than restoring the default.
	SIGHUPIgnored bool          `json:"sighup_ignored,omitempty"`
	Slots         []HandoffSlot `json:"slots,omitempty"`
	Kinds         []HandoffKind `json:"kinds,omitempty"`
	// Breaker is the failure times inside the breaker's window.
	Breaker []time.Time `json:"breaker,omitempty"`
}

// HandoffSlot is one busy slot: what the pool tracked about its child plus
// the runner-level state of the inherited child itself.
type HandoffSlot struct {
	Slot     int    `json:"slot"`
	Kind     Kind   `json:"kind"`
	Revision string `json:"revision,omitempty"`
	// ChildStart and ChildStartN are the slot's childStartPin.
	ChildStart  string `json:"child_start,omitempty"`
	ChildStartN int    `json:"child_start_n,omitempty"`
	ChildLog    string `json:"child_log,omitempty"`
	// GateGen is the kind's gate generation the child started under, so its
	// no-work exit is judged against the gate it actually saw (#4618), not the
	// successor's.
	GateGen int `json:"gate_gen,omitempty"`
	// Key is the flight key. On the wire it is the "issue"/"chore" pair
	// Event uses, never a "key" object.
	Key       dispatchkey.Key `json:"-"`
	Issues    []string        `json:"issues,omitempty"`
	Pass      string          `json:"pass,omitempty"`
	Model     string          `json:"model,omitempty"`
	ModelRole string          `json:"model_role,omitempty"`
	Child     ChildState      `json:"child"`
}

// ChildState is the runner-level state of one inherited child.
type ChildState struct {
	PID      int `json:"pid"`
	ReportFD int `json:"report_fd"`
	// Partial is the report line read so far, without its newline. Raw bytes,
	// not a string: a line cut mid-rune must survive the round trip, and a
	// JSON string would rewrite the invalid UTF-8.
	Partial []byte `json:"partial,omitempty"`
	// Discarding marks a child mid-way through dropping an over-long line.
	Discarding bool `json:"discarding,omitempty"`
}

// HandoffKind is the schedule state of one kind worth keeping across the
// handoff: its no-work gate, its reported next-due, and its rate-limit pause.
// The backoff's floor and cap come from the successor's own config, so a
// changed knob takes effect on restart instead of being pinned by the
// document.
type HandoffKind struct {
	Kind         Kind          `json:"kind"`
	BackoffCur   time.Duration `json:"backoff_cur,omitempty"`
	Until        time.Time     `json:"until,omitzero"`
	Jammed       bool          `json:"jammed,omitempty"`
	Gen          int           `json:"gen,omitempty"`
	DueAt        time.Time     `json:"due_at,omitzero"`
	DueOnTipMove bool          `json:"due_on_tip_move,omitempty"`
	LimitedUntil time.Time     `json:"limited_until,omitzero"`
}

// handoffSlotWire is HandoffSlot's JSON shape: handoffSlotFields sheds
// HandoffSlot's methods so marshalling it doesn't recurse, and Key splits into
// the "issue"/"chore" pair, as eventWire does.
type handoffSlotWire struct {
	handoffSlotFields
	Issue string `json:"issue,omitempty"`
	Chore string `json:"chore,omitempty"`
}

// handoffSlotFields is HandoffSlot without its methods, so embedding it in
// handoffSlotWire doesn't re-enter MarshalJSON/UnmarshalJSON.
type handoffSlotFields HandoffSlot

// MarshalJSON writes Key as the "issue"/"chore" pair.
func (s HandoffSlot) MarshalJSON() ([]byte, error) {
	issue, chore := s.Key.Fields()
	return json.Marshal(handoffSlotWire{handoffSlotFields: handoffSlotFields(s), Issue: issue, Chore: chore})
}

// UnmarshalJSON refuses a slot naming both an issue and a Chore. Naming
// neither is fine: a child has no key until its first box record.
func (s *HandoffSlot) UnmarshalJSON(data []byte) error {
	var w handoffSlotWire
	if err := decodeStrict(data, &w); err != nil {
		return err
	}
	*s = HandoffSlot(w.handoffSlotFields)
	if w.Issue == "" && w.Chore == "" {
		return nil
	}
	key, err := dispatchkey.Parse(w.Issue, w.Chore)
	if err != nil {
		return fmt.Errorf("handoff slot %d: %w", w.Slot, err)
	}
	s.Key = key
	return nil
}

// EncodeHandoff writes h to w as one JSON document, stamped with
// HandoffVersion whatever h.V says.
func EncodeHandoff(w io.Writer, h Handoff) error {
	h.V = HandoffVersion
	return json.NewEncoder(w).Encode(h)
}

// DecodeHandoff reads one document from r. It refuses any version outside
// handoffMinVersion..HandoffVersion, a missing one included, and any field or
// trailing data the version does not define. The version is checked before the
// shape, so a document from another build is reported as such rather than as
// whichever of its fields this build trips over first.
func DecodeHandoff(r io.Reader) (Handoff, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Handoff{}, fmt.Errorf("handoff: read: %w", err)
	}
	var head struct {
		V int `json:"v"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&head); err != nil {
		return Handoff{}, fmt.Errorf("handoff: decode: %w", err)
	}
	if head.V < handoffMinVersion || head.V > HandoffVersion {
		return Handoff{}, fmt.Errorf("handoff: document is version %d, this build supports %d to %d", head.V, handoffMinVersion, HandoffVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var h Handoff
	if err := dec.Decode(&h); err != nil {
		return Handoff{}, fmt.Errorf("handoff: decode: %w", err)
	}
	switch _, err := dec.Token(); {
	case err == nil:
		return Handoff{}, errors.New("handoff: trailing data after the document")
	case err != io.EOF:
		return Handoff{}, fmt.Errorf("handoff: after the document: %w", err)
	}
	if err := h.validate(); err != nil {
		return Handoff{}, err
	}
	return h, nil
}

// validate refuses a pid or descriptor that cannot name a real child: pid 0
// or a negative one would make a signal reach a whole process group, a
// negative descriptor is no descriptor at all, and one pid on two slots would
// let both supervise, signal and report the same child. A report descriptor
// shared with another slot or the lock would put two handles on one descriptor.
func (h Handoff) validate() error {
	if h.LockFD < 0 {
		return fmt.Errorf("handoff: lock_fd %d is not a descriptor", h.LockFD)
	}
	pids := make(map[int]int, len(h.Slots))
	fds := make(map[int]int, len(h.Slots))
	for _, s := range h.Slots {
		if s.Child.PID <= 0 {
			return fmt.Errorf("handoff slot %d: pid %d is not a process", s.Slot, s.Child.PID)
		}
		if first, dup := pids[s.Child.PID]; dup {
			return fmt.Errorf("handoff slot %d: pid %d is already slot %d's child", s.Slot, s.Child.PID, first)
		}
		pids[s.Child.PID] = s.Slot
		if s.Child.ReportFD < 0 {
			return fmt.Errorf("handoff slot %d: report_fd %d is not a descriptor", s.Slot, s.Child.ReportFD)
		}
		if s.Child.ReportFD == h.LockFD {
			return fmt.Errorf("handoff slot %d: report_fd %d is the lock_fd", s.Slot, s.Child.ReportFD)
		}
		if first, dup := fds[s.Child.ReportFD]; dup {
			return fmt.Errorf("handoff slot %d: report_fd %d is already slot %d's", s.Slot, s.Child.ReportFD, first)
		}
		fds[s.Child.ReportFD] = s.Slot
	}
	return nil
}

// decodeStrict is json.Unmarshal that refuses unknown fields, which a custom
// UnmarshalJSON would otherwise let through the outer decoder's setting.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
