package freshness

import "sync"

// FakeCall records one (pwd, rev, attr) call — used by both Fake.Eval and
// RealizerFake.Start.
type FakeCall struct {
	Pwd, Rev, Attr string
}

// Fake is an in-memory Evaluator for unit tests — no nix round-trip.
type Fake struct {
	// OutPath is returned by Eval when Err is nil.
	OutPath string
	// Err, if non-nil, is returned by Eval instead of OutPath.
	Err error
	// OutPathForAttr, if it has an entry for the requested attr, overrides
	// OutPath for that attr — used by a test that needs Eval to return
	// different outpaths for different attrs in the same call (e.g. an image
	// attr and a launcher attr probed within one Probe call). An attr not
	// present here falls back to the plain OutPath/Err fields, so every
	// existing test that only sets OutPath/Err keeps working unchanged.
	OutPathForAttr map[string]string
	// ErrForAttr, if it has an entry for the requested attr, overrides Err
	// for that attr, mirroring OutPathForAttr.
	ErrForAttr map[string]error
	// Calls records the (pwd, rev, attr) tuples passed to Eval, in order.
	Calls []FakeCall
}

// Eval records the call and returns the per-attr override from
// OutPathForAttr/ErrForAttr if attr has one, else falls back to the plain
// OutPath/Err fields.
func (f *Fake) Eval(pwd, rev, attr string) (string, error) {
	f.Calls = append(f.Calls, FakeCall{pwd, rev, attr})
	if err, ok := f.ErrForAttr[attr]; ok {
		return "", err
	}
	if outPath, ok := f.OutPathForAttr[attr]; ok {
		return outPath, nil
	}
	if f.Err != nil {
		return "", f.Err
	}
	return f.OutPath, nil
}

// RealizerFake is an in-memory Realizer for unit tests — no nix round-trip.
// Start records the call synchronously, before returning, matching the real
// Realizer's contract that the call is durably recorded by the time Start
// returns (see the Realizer doc comment) — so a test can read CallsCopy
// right after RealizeTip returns with no wait needed to prove the call
// happened. Only Calls/CallsCopy are mutex-guarded, since Start may be
// called concurrently with a test reading CallsCopy while a prior wait()
// closure is still running in the background. Block, Done, and Err describe
// the async completion phase (the closure Start returns) rather than Start
// itself: set them before calling Start (directly, or via RealizeTip) and
// do not mutate them concurrently afterward — they are plain fields, not
// mutex-guarded. StartErr is a third, distinct failure mode: it simulates
// Start itself failing to fork (e.g. the `nix` binary is missing), a
// different error from Err (which simulates `nix build` running and then
// failing, surfaced via the wait closure instead) — set it before calling
// Start to make Start return (nil, StartErr) without recording a call.
type RealizerFake struct {
	mu sync.Mutex

	// Err, if non-nil, is returned by the wait function returned from every
	// Start call.
	Err error

	// StartErr, if non-nil, is returned by Start itself instead of forking —
	// simulating Start failing to fork the underlying process (e.g. the
	// `nix` binary is missing). When set, Start returns (nil, StartErr) and
	// does not append to Calls, mirroring the real Realizer's contract that
	// a call is durably recorded only once it is actually forked.
	StartErr error

	// Calls records the (pwd, rev, attr) tuples passed to Start, in order.
	// Read it via CallsCopy, not directly, since Start may still be running
	// concurrently with a read.
	Calls []FakeCall

	// Block, if non-nil, is read from inside the wait function returned
	// from Start, before that function returns — a test uses it to prove a
	// caller doesn't wait for the realize to complete.
	Block chan struct{}

	// Done receives a value after every completed wait call (after Block,
	// if set, is read from). Buffered generously (capacity 8) so a fake
	// used across multiple RealizeTip calls in one test doesn't leak a
	// blocked goroutine: with more than 8 undrained wait() completions in
	// one test, the buffer fills and the wait-calling goroutine blocks
	// (leaks) on the send rather than panicking or dropping the value.
	Done chan struct{}
}

// NewRealizerFake returns an empty RealizerFake with Done ready to receive.
func NewRealizerFake() *RealizerFake {
	return &RealizerFake{Done: make(chan struct{}, 8)}
}

// Start records the call synchronously and returns a wait function that
// blocks on Block (if set), signals Done, and returns Err. If StartErr is
// set, Start returns (nil, StartErr) instead, without recording the call.
func (f *RealizerFake) Start(pwd, rev, attr string) (func() error, error) {
	if f.StartErr != nil {
		return nil, f.StartErr
	}

	f.mu.Lock()
	f.Calls = append(f.Calls, FakeCall{pwd, rev, attr})
	f.mu.Unlock()

	wait := func() error {
		if f.Block != nil {
			<-f.Block
		}
		if f.Done != nil {
			f.Done <- struct{}{}
		}
		return f.Err
	}
	return wait, nil
}

// CallsCopy returns a copy of the recorded calls, safe to read while a
// previously-returned wait function may still be running in a background
// goroutine.
func (f *RealizerFake) CallsCopy() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.Calls))
	copy(out, f.Calls)
	return out
}
