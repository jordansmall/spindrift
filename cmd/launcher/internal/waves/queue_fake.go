package waves

import "sync"

// Fake is an in-memory Queue for unit tests that only need to assert wiring
// -- what Discover/Claim/Pending/ReportStaleDrain were called with, and
// what they returned -- rather than exercise a real discovery/claim
// backend. All methods are safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	// DiscoverCalls counts Discover invocations.
	DiscoverCalls int
	// DiscoverReturn is the Batch Discover returns.
	DiscoverReturn Batch
	// DiscoverErr is the error Discover returns.
	DiscoverErr error
	// DiscoverFunc, if set, scripts per-call Discover results -- e.g. a
	// rate-limited error on the first call and success thereafter -- and
	// takes priority over DiscoverReturn/DiscoverErr. callN is 1-indexed:
	// it equals DiscoverCalls's value after this call's own increment (so
	// the first call gets callN=1).
	DiscoverFunc func(callN int) (Batch, error)

	// ClaimCalls records every issue number Claim was called with, in
	// order -- including repeats, unlike Claimed.
	ClaimCalls []string
	// ClaimErr is the error Claim returns.
	ClaimErr error
	// Claimed tracks which issue numbers Claim has successfully claimed,
	// mirroring headlessQueue.claimed in queue.go. Claim only records num
	// here when ClaimErr is nil, so a scripted ClaimErr leaves Claimed
	// unset for that call -- claiming the same num twice is idempotent:
	// both calls append to ClaimCalls and return the same ClaimErr.
	Claimed map[string]bool

	// PendingCalls counts Pending invocations.
	PendingCalls int
	// PendingReturn is the value Pending returns.
	PendingReturn int
	// PendingErr is the error Pending returns.
	PendingErr error
	// PendingFunc, if set, scripts Pending results as a function of the
	// Fake's current Claimed set -- e.g. wiring in a real exclusion-computing
	// closure (this package's own fakePending test helper) so the returned
	// count is recomputed from what's actually been claimed, rather than a
	// hardcoded constant -- and takes priority over PendingReturn/PendingErr.
	PendingFunc func(claimed map[string]bool) (int, error)

	// ReportStaleDrainCalls records every report ReportStaleDrain was
	// called with, in order.
	ReportStaleDrainCalls []StaleDrainReport
}

var _ Queue = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{Claimed: make(map[string]bool)}
}

// Discover records the call and returns DiscoverFunc(callN) if DiscoverFunc
// is set, else DiscoverReturn, DiscoverErr. DiscoverFunc is invoked outside
// f.mu so it may call back into other Fake methods (e.g. Pending) on the
// same Fake without self-deadlocking on Go's non-reentrant sync.Mutex.
func (f *Fake) Discover() (Batch, error) {
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
// Claiming the same num more than once is idempotent: every call appends
// to ClaimCalls and returns ClaimErr, but Claimed only ever needs setting
// once.
func (f *Fake) Claim(num string) error {
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

// Pending records the call and returns PendingFunc(Claimed) if PendingFunc
// is set, else PendingReturn, PendingErr. PendingFunc is invoked outside
// f.mu, on a snapshot copy of Claimed rather than the live map, so it may
// call back into other Fake methods on the same Fake -- including a
// concurrent Claim mutating Claimed -- without self-deadlocking on Go's
// non-reentrant sync.Mutex or racing on the map itself.
func (f *Fake) Pending() (int, error) {
	f.mu.Lock()
	f.PendingCalls++
	pendingFunc := f.PendingFunc
	claimed := make(map[string]bool, len(f.Claimed))
	for num, ok := range f.Claimed {
		claimed[num] = ok
	}
	pendingReturn, pendingErr := f.PendingReturn, f.PendingErr
	f.mu.Unlock()

	if pendingFunc != nil {
		return pendingFunc(claimed)
	}
	return pendingReturn, pendingErr
}

// ReportStaleDrain records report. Unlike headlessQueue.ReportStaleDrain
// (queue.go), it never prints to stdout or appends to a stale-drain.log
// file -- a caller reads the report directly off ReportStaleDrainCalls
// instead of parsing it back out of that missing text.
func (f *Fake) ReportStaleDrain(report StaleDrainReport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReportStaleDrainCalls = append(f.ReportStaleDrainCalls, report)
}
