package dispatch

import (
	"sync"

	"spindrift.dev/launcher/internal/usage"
)

// FixCall records one Fix invocation.
type FixCall struct {
	Pass             int
	CIFailureSummary string
}

// Fake is an in-memory Dispatcher for unit tests. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	RunCalls int

	RunResult Result

	// RunResults[i] answers the i-th Run call, reusing the last element once
	// the sequence is exhausted. A non-nil RunResults overrides RunResult.
	RunResults []Result

	FixCalls []FixCall

	FixResult Result

	// FixResults is indexed like RunResults.
	FixResults []Result

	ResolveConflictCalls []string

	ResolveConflictErr error

	UsageReportBody string

	CumulativeUsageResult usage.Usage

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

// CumulativeUsage returns CumulativeUsageResult.
func (f *Fake) CumulativeUsage() usage.Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.CumulativeUsageResult
}

// Close records the call.
func (f *Fake) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseCalls++
}
