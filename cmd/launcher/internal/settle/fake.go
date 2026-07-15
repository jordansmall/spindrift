package settle

import (
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
)

// SettleCall records one Settle invocation.
type SettleCall struct {
	Num    string
	Result dispatch.Result
}

// SettleAdoptedCall records one SettleAdopted invocation.
type SettleAdoptedCall struct {
	Num, PRURL string
}

// FailCall records one Fail invocation.
type FailCall struct {
	Num    string
	Result dispatch.Result
}

// Fake is an in-memory Settler for unit tests that only need to assert
// wiring (that Settle/SettleAdopted was called with the expected arguments)
// rather than exercise the real merge-gate behavior. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// SettleCalls records all Settle invocations in order.
	SettleCalls []SettleCall
	// SettleAdoptedCalls records all SettleAdopted invocations in order.
	SettleAdoptedCalls []SettleAdoptedCall
	// FailCalls records all Fail invocations in order.
	FailCalls []FailCall
}

var _ Settler = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{}
}

// Settle records the call.
func (f *Fake) Settle(d dispatch.Dispatcher, num string, result dispatch.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleCalls = append(f.SettleCalls, SettleCall{Num: num, Result: result})
}

// SettleAdopted records the call.
func (f *Fake) SettleAdopted(d dispatch.Dispatcher, num, prURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleAdoptedCalls = append(f.SettleAdoptedCalls, SettleAdoptedCall{Num: num, PRURL: prURL})
}

// Fail records the call.
func (f *Fake) Fail(num string, result dispatch.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FailCalls = append(f.FailCalls, FailCall{Num: num, Result: result})
}
