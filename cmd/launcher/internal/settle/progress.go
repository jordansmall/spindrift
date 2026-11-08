package settle

import (
	"os"
	"sync"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

// Status is the machine-readable token after "status=" in a settle progress
// line. The Box's own outcome status tokens pass through as Status(o.Status)
// (gate.go, research.go) and so are not enumerated here.
type Status string

// StatusMergeByHand is the merge-by-hand token, shared with the settled
// record's reason.
const StatusMergeByHand = Status(dispatchrecord.ReasonMergeByHand)

const (
	StatusPostForcePushWait Status = "post-force-push-wait"
	StatusPostForcePushRed  Status = "post-force-push-red"
	StatusMarkReadyFailed   Status = "mark-ready-failed"
)

// Step is one settle progress event. Key is the issue key without its "#".
// Detail carries the verbatim tail of the line (e.g. "!! <err>", "note=<x>").
type Step struct {
	Key     string
	Landing string
	Status  Status
	Detail  string
}

// Line renders the step as the operator-facing progress line, omitting each
// empty field. An empty Landing drops the "landing=" segment: every producer
// passes a non-empty landing (outcome.Parse rejects an empty one; AgentBranch
// and PR URLs are never empty), and steps that never had a landing (fix-pass,
// filed=, dedup lines) rely on the omission.
func (st Step) Line() string {
	line := "    #" + st.Key
	if st.Landing != "" {
		line += "  landing=" + st.Landing
	}
	if st.Status != "" {
		line += "  status=" + string(st.Status)
	}
	if st.Detail != "" {
		line += "  " + st.Detail
	}
	return line + "\n"
}

// Progress receives settle's progress steps.
type Progress interface {
	Step(Step)
}

// StdoutProgress prints each step's line to stdout.
type StdoutProgress struct{}

// Step resolves os.Stdout per write, never at construction: tests swap
// os.Stdout after the Settle is built.
func (StdoutProgress) Step(st Step) {
	os.Stdout.WriteString(st.Line())
}

// ProgressRecorder collects steps for tests. Settle runs concurrently, so it
// carries its own mutex.
type ProgressRecorder struct {
	mu    sync.Mutex
	steps []Step
}

// Step records st.
func (r *ProgressRecorder) Step(st Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, st)
}

// Steps returns a copy of the steps recorded so far.
func (r *ProgressRecorder) Steps() []Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.steps...)
}
