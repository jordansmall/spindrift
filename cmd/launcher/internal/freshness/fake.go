package freshness

import "sync"

// FakeCall records one (pwd, rev, attr) call to Fake.Eval or RealizerFake.Start.
type FakeCall struct {
	Pwd, Rev, Attr string
}

// Fake is an in-memory Evaluator for unit tests, with no nix round-trip.
type Fake struct {
	// OutPath is returned by Eval when Err is nil.
	OutPath string
	// Err, if non-nil, is returned by Eval instead of OutPath.
	Err error
	// OutPathForAttr overrides OutPath for the attrs it names, so one Probe
	// call can return different outpaths for an image attr and a launcher
	// attr. An attr with no entry falls back to OutPath/Err.
	OutPathForAttr map[string]string
	// ErrForAttr overrides Err for the attrs it names, mirroring OutPathForAttr.
	ErrForAttr map[string]error
	// Calls records the (pwd, rev, attr) tuples passed to Eval, in order.
	Calls []FakeCall
}

// Eval records the call and returns the per-attr override if attr has one,
// else OutPath/Err.
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

// RealizerFake is an in-memory Realizer for unit tests, with no nix round-trip.
// Start records the call before returning, matching the real Realizer, so a
// test can read CallsCopy right after RealizeTip returns without waiting.
// Only Calls is mutex-guarded; set every other field before calling Start and
// do not mutate it afterward, since a prior wait closure may still be running.
type RealizerFake struct {
	mu sync.Mutex

	// Err, if non-nil, is returned by the wait function from every Start call.
	// It simulates `nix build` running and then failing, unlike StartErr.
	Err error

	// StartErr, if non-nil, simulates Start failing to fork at all (e.g. the
	// `nix` binary is missing): Start returns (nil, StartErr) and appends no
	// call, matching the real Realizer, which records a call only once forked.
	StartErr error

	// Calls records the (pwd, rev, attr) tuples passed to Start, in order.
	// Read it via CallsCopy, since Start may run concurrently with a read.
	Calls []FakeCall

	// Block, if non-nil, is read from inside the wait function before it
	// returns, so a test can prove a caller doesn't wait for the realize.
	Block chan struct{}

	// Done receives a value after every completed wait call, once Block has
	// been read. The capacity of 8 covers a test with several RealizeTip
	// calls; past 8 undrained completions the sending goroutine blocks and
	// leaks.
	Done chan struct{}
}

// NewRealizerFake returns an empty RealizerFake with Done ready to receive.
func NewRealizerFake() *RealizerFake {
	return &RealizerFake{Done: make(chan struct{}, 8)}
}

// Start records the call and returns a wait function that blocks on Block when
// set, signals Done, and returns Err.
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

// CallsCopy returns a copy of the recorded calls, safe to read while a wait
// function is still running in a background goroutine.
func (f *RealizerFake) CallsCopy() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.Calls))
	copy(out, f.Calls)
	return out
}
