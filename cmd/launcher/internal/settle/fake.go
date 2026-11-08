package settle

import (
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/fakehook"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
)

// SettleCall records one Settle invocation.
type SettleCall struct {
	Num    string
	Gen    uint64
	Result dispatch.Result
}

// SettlePatchCall records one SettlePatch invocation.
type SettlePatchCall struct {
	Owner      dispatchkey.Key
	Num, PRURL string
}

// FailCall records one Fail invocation.
type FailCall struct {
	Num    string
	Gen    uint64
	Result dispatch.Result
}

// RecoverCall records one Recover invocation that passed its pre-Begin
// declines and whose Begin succeeded.
type RecoverCall struct {
	Num, OpenPR string
	Strict      bool
	Resolved    outcome.Resolved
	Passes      []passmanifest.Entry
}

// Fake is an in-memory Settler for tests that assert call wiring rather than
// merge-gate behavior. All methods are safe for concurrent use.
type Fake struct {
	// Hooks lets a test run a callback inside Recover, before the call is
	// recorded; see fakehook.
	fakehook.Hooks

	mu sync.Mutex

	SettleCalls      []SettleCall
	SettlePatchCalls []SettlePatchCall
	FailCalls        []FailCall
	// Settlement is what Settle, SettlePatch and Recover return.
	Settlement   Settlement
	RecoverCalls []RecoverCall
	// RecoverDecline, when non-zero, is returned by Recover before Begin, as
	// the real Recover's unreviewed decline is.
	RecoverDecline Settlement
	// RecoverNothing makes Recover return ErrNothingToRecover before Begin.
	RecoverNothing bool
	// RecoverErr is returned by Recover after a successful Begin.
	RecoverErr error
}

var _ Settler = (*Fake)(nil)
var _ WorkSettler = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{}
}

// Settle records the call.
func (f *Fake) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) Settlement {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettleCalls = append(f.SettleCalls, SettleCall{Num: num, Gen: gen, Result: result})
	return f.Settlement
}

// SettlePatch records the call.
func (f *Fake) SettlePatch(owner dispatchkey.Key, num, prURL string) Settlement {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SettlePatchCalls = append(f.SettlePatchCalls, SettlePatchCall{Owner: owner, Num: num, PRURL: prURL})
	return f.Settlement
}

// Fail records the call.
func (f *Fake) Fail(num string, gen uint64, result dispatch.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FailCalls = append(f.FailCalls, FailCall{Num: num, Gen: gen, Result: result})
}

// Recover returns RecoverDecline or ErrNothingToRecover without calling Begin
// when set. Otherwise it calls Begin and returns its error unchanged, so a
// declined Begin records nothing and RecoverCalls proves a landing was tried;
// then it returns Settlement and RecoverErr.
func (f *Fake) Recover(in RecoverInput) (Settlement, error) {
	if f.RecoverDecline != (Settlement{}) {
		return f.RecoverDecline, nil
	}
	if f.RecoverNothing {
		return Settlement{}, ErrNothingToRecover
	}
	if _, err := in.Begin(); err != nil {
		return Settlement{}, err
	}
	f.Fire("Recover")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RecoverCalls = append(f.RecoverCalls, RecoverCall{Num: in.Num, OpenPR: in.OpenPR, Strict: in.Strict, Resolved: in.Resolved, Passes: in.Passes})
	return f.Settlement, f.RecoverErr
}
