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

	// ClaimCalls records every issue number Claim was called with, in order.
	ClaimCalls []string
	// ClaimErr is the error Claim returns.
	ClaimErr error

	// PendingCalls counts Pending invocations.
	PendingCalls int
	// PendingReturn is the value Pending returns.
	PendingReturn int

	// ReportStaleDrainCalls records every report ReportStaleDrain was
	// called with, in order.
	ReportStaleDrainCalls []StaleDrainReport
}

var _ Queue = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{}
}

// Discover records the call and returns DiscoverReturn, DiscoverErr.
func (f *Fake) Discover() (Batch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DiscoverCalls++
	return f.DiscoverReturn, f.DiscoverErr
}

// Claim records num and returns ClaimErr.
func (f *Fake) Claim(num string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ClaimCalls = append(f.ClaimCalls, num)
	return f.ClaimErr
}

// Pending records the call and returns PendingReturn.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PendingCalls++
	return f.PendingReturn
}

// ReportStaleDrain records report.
func (f *Fake) ReportStaleDrain(report StaleDrainReport) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReportStaleDrainCalls = append(f.ReportStaleDrainCalls, report)
}
