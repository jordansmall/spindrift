package waves

import "sync"

// FakeQueue is an in-memory Queue for unit tests that assert wiring rather than
// exercise a real discovery/claim backend. All methods are safe for concurrent use.
type FakeQueue struct {
	mu sync.Mutex

	DiscoverCalls  int
	DiscoverReturn Batch
	DiscoverErr    error
	// DiscoverFunc scripts per-call results and takes priority over
	// DiscoverReturn/DiscoverErr. callN is 1-indexed, so the first call gets 1.
	DiscoverFunc func(callN int) (Batch, error)

	// ClaimCalls records every issue number Claim was called with, in order.
	// Repeats appear here but collapse into one entry in Claimed.
	ClaimCalls []string
	ClaimErr   error
	// Claimed holds only the issue numbers Claim accepted: a non-nil ClaimErr
	// leaves the number unset here.
	Claimed map[string]bool

	PendingCalls  int
	PendingReturn int
	PendingErr    error
	// PendingFunc computes the count from the caller-supplied claimed set, which
	// is Pending's own parameter and not the Claimed field, and takes priority
	// over PendingReturn/PendingErr.
	PendingFunc func(claimed map[string]bool) (int, error)

	ReportStaleDrainCalls []StaleDrainReport
}

var _ Queue = (*FakeQueue)(nil)

// NewFakeQueue returns an empty FakeQueue.
func NewFakeQueue() *FakeQueue {
	return &FakeQueue{Claimed: make(map[string]bool)}
}

// Discover records the call and returns DiscoverFunc(callN) when set, else
// DiscoverReturn, DiscoverErr. DiscoverFunc runs outside f.mu so it may call
// back into other FakeQueue methods without deadlocking on the non-reentrant
// mutex.
func (f *FakeQueue) Discover() (Batch, error) {
	f.mu.Lock()
	f.DiscoverCalls++
	callN := f.DiscoverCalls
	discoverFunc := f.DiscoverFunc
	discoverReturn, discoverErr := f.DiscoverReturn, f.DiscoverErr
	f.mu.Unlock()

	if discoverFunc != nil {
		return discoverFunc(callN)
	}
	return discoverReturn, discoverErr
}

// Claim records num, marks it Claimed on success, and returns ClaimErr.
// Repeat claims of the same num are idempotent.
func (f *FakeQueue) Claim(num string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ClaimCalls = append(f.ClaimCalls, num)
	if f.ClaimErr == nil {
		if f.Claimed == nil {
			f.Claimed = make(map[string]bool)
		}
		f.Claimed[num] = true
	}
	return f.ClaimErr
}

// Pending records the call and returns PendingFunc(claimed) when set, else
// PendingReturn, PendingErr. claimed is forwarded verbatim, so a test wanting
// PendingFunc to see what Claim recorded must seed it from f.Claimed at the
// call site. PendingFunc runs outside f.mu so it may call back into other
// FakeQueue methods without deadlocking on the non-reentrant mutex.
func (f *FakeQueue) Pending(claimed map[string]bool) (int, error) {
	f.mu.Lock()
	f.PendingCalls++
	pendingFunc := f.PendingFunc
	pendingReturn, pendingErr := f.PendingReturn, f.PendingErr
	f.mu.Unlock()

	if pendingFunc != nil {
		return pendingFunc(claimed)
	}
	return pendingReturn, pendingErr
}

// ReportStaleDrain records report. Unlike headlessQueue, it prints nothing and
// writes no stale-drain.log, so a caller reads ReportStaleDrainCalls directly.
func (f *FakeQueue) ReportStaleDrain(report StaleDrainReport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReportStaleDrainCalls = append(f.ReportStaleDrainCalls, report)
}

// EnsureLogDirExists is a no-op: FakeQueue has no log directory.
func (f *FakeQueue) EnsureLogDirExists() error { return nil }
