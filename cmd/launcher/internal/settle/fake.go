package settle

import (
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
)

// SettleCall records one Settle invocation.
type SettleCall struct {
	Num    string
	Gen    uint64
	Result dispatch.Result
}

// SettleAdoptedCall records one SettleAdopted invocation.
type SettleAdoptedCall struct {
	Num, PRURL string
	Gen        uint64
}

// FailCall records one Fail invocation.
type FailCall struct {
	Num    string
	Gen    uint64
	Result dispatch.Result
}

// SettleRelayedBranchCall records one SettleRelayedBranch invocation.
type SettleRelayedBranchCall struct {
	Num    string
	Gen    uint64
	Sit    Situation
	Result dispatch.Result
}

// SituationForCall records one SituationFor invocation.
type SituationForCall struct {
	Num         string
	OpenPRFound bool
	Result      dispatch.Result
}

// Fake is an in-memory Settler for tests that assert call wiring rather than
// merge-gate behavior. All methods are safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	SettleCalls               []SettleCall
	SettleAdoptedCalls        []SettleAdoptedCall
	FailCalls                 []FailCall
	SettleRelayedBranchCalls  []SettleRelayedBranchCall
	SettleRelayedBranchReturn bool
	SituationForCalls         []SituationForCall
	SituationForReturn        Situation
}

var _ Settler = (*Fake)(nil)
var _ WorkSettler = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{}
}

// Settle records the call.
func (f *Fake) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleCalls = append(f.SettleCalls, SettleCall{Num: num, Gen: gen, Result: result})
}

// SettleAdopted records the call.
func (f *Fake) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleAdoptedCalls = append(f.SettleAdoptedCalls, SettleAdoptedCall{Num: num, PRURL: prURL, Gen: gen})
}

// Fail records the call.
func (f *Fake) Fail(num string, gen uint64, result dispatch.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FailCalls = append(f.FailCalls, FailCall{Num: num, Gen: gen, Result: result})
}

// SettleRelayedBranch records the call and returns SettleRelayedBranchReturn.
func (f *Fake) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleRelayedBranchCalls = append(f.SettleRelayedBranchCalls, SettleRelayedBranchCall{Num: num, Gen: gen, Sit: sit, Result: result})
	return f.SettleRelayedBranchReturn
}

// SituationFor records the call and returns SituationForReturn.
func (f *Fake) SituationFor(num string, openPRFound bool, result dispatch.Result) Situation {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SituationForCalls = append(f.SituationForCalls, SituationForCall{Num: num, OpenPRFound: openPRFound, Result: result})
	return f.SituationForReturn
}
