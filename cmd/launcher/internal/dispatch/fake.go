package dispatch

import "sync"

// FixCall records one Fix invocation.
type FixCall struct {
	Pass             int
	CIFailureSummary string
}

// Fake is an in-memory Dispatcher for unit tests. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// RunCalls counts how many times Run was called.
	RunCalls int

	// RunResult is returned by every Run call (when RunResults is nil).
	RunResult Result

	// RunResults, if non-nil, provides per-call results: RunResults[i] is
	// returned for the i-th Run call. The last element is reused when the
	// sequence is exhausted. Takes precedence over RunResult.
	RunResults []Result

	// FixCalls records all Fix invocations in order.
	FixCalls []FixCall

	// FixResult is returned by every Fix call (when FixResults is nil).
	FixResult Result

	// FixResults, if non-nil, provides per-call results, indexed like
	// RunResults.
	FixResults []Result

	// ResolveConflictCalls records the pr argument of every ResolveConflict
	// call in order.
	ResolveConflictCalls []string

	// ResolveConflictErr, if non-nil, is returned by every ResolveConflict
	// call.
	ResolveConflictErr error

	// UsageReportBody is returned by UsageReport.
	UsageReportBody string

	// CloseCalls counts how many times Close was called.
	CloseCalls int
}

var _ Dispatcher = (*Fake)(nil)

// NewFake returns a Fake that reports success on Run and Fix by default.
func NewFake() *Fake {
	return &Fake{
		RunResult: Result{Success: true},
		FixResult: Result{Success: true},
	}
}

// Run records the call and returns the result for this call index.
func (f *Fake) Run() Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.RunCalls
	f.RunCalls++
	if len(f.RunResults) > 0 {
		if i < len(f.RunResults) {
			return f.RunResults[i]
		}
		return f.RunResults[len(f.RunResults)-1]
	}
	return f.RunResult
}

// Fix records the call and returns the result for this call index.
func (f *Fake) Fix(pass int, ciFailureSummary string) Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.FixCalls)
	f.FixCalls = append(f.FixCalls, FixCall{Pass: pass, CIFailureSummary: ciFailureSummary})
	if len(f.FixResults) > 0 {
		if i < len(f.FixResults) {
			return f.FixResults[i]
		}
		return f.FixResults[len(f.FixResults)-1]
	}
	return f.FixResult
}

// ResolveConflict records pr and returns ResolveConflictErr.
func (f *Fake) ResolveConflict(pr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ResolveConflictCalls = append(f.ResolveConflictCalls, pr)
	return f.ResolveConflictErr
}

// UsageReport returns UsageReportBody.
func (f *Fake) UsageReport() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.UsageReportBody
}

// Close records the call.
func (f *Fake) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseCalls++
}
