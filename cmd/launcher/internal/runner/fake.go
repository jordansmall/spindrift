package runner

import "sync"

// Fake is an in-memory Runner for unit tests. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// EnsureReadyCalls counts how many times EnsureReady was called.
	EnsureReadyCalls int

	// IsReadyCalls counts how many times IsReady was called.
	IsReadyCalls int

	// RunCalls records all Run invocations in order.
	RunCalls []Box

	// ReapCalls records names passed to Reap in order.
	ReapCalls []string

	// KillCalls records names passed to Kill in order.
	KillCalls []string
	// KillErr, if non-nil, is returned by every Kill call.
	KillErr error

	// EnsureReadyErr, if non-nil, is returned by EnsureReady.
	EnsureReadyErr error

	// IsReadyErr, if non-nil, is returned by IsReady.
	IsReadyErr error

	// RunErr, if non-nil, is returned by every Run call (when RunErrs is nil).
	RunErr error

	// RunErrs, if non-nil, provides per-call errors: RunErrs[i] is returned for
	// the i-th Run call. The last element is reused when the sequence is
	// exhausted. Takes precedence over RunErr.
	RunErrs []error

	// WriteToOutput, if non-nil, is written to box.Output before Run returns.
	WriteToOutput []byte

	// RunFunc, if non-nil, is called instead of the RunErrs/RunErr logic —
	// tests use it to control completion order and timing (e.g. staggered
	// finishes for continuous-dispatch tests) without real sleeps. Called
	// with the Fake's lock released, so it may block or trigger concurrent
	// Run calls without deadlocking.
	RunFunc func(Box) error

	// IsRunningRet is returned by every IsRunning call.
	IsRunningRet bool

	// IsRunningCalls records the names passed to IsRunning, in order.
	IsRunningCalls []string
}

// NewFake returns an empty Fake runner.
func NewFake() *Fake { return &Fake{} }

// EnsureReady records the call and returns EnsureReadyErr.
func (f *Fake) EnsureReady() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnsureReadyCalls++
	return f.EnsureReadyErr
}

// IsReady records the call and returns IsReadyErr.
func (f *Fake) IsReady() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IsReadyCalls++
	return f.IsReadyErr
}

// Run records the box and returns the error for this call index. When
// RunFunc is set, it is called instead (lock released first, so it may
// block or trigger concurrent Run calls). Otherwise, if RunErrs is non-nil,
// RunErrs[i] is used (last element reused when exhausted); otherwise RunErr
// is returned. If WriteToOutput is set and box.Output is non-nil, the bytes
// are written to box.Output before returning.
func (f *Fake) Run(box Box) error {
	f.mu.Lock()
	i := len(f.RunCalls)
	f.RunCalls = append(f.RunCalls, box)
	if len(f.WriteToOutput) > 0 && box.Output != nil {
		box.Output.Write(f.WriteToOutput) //nolint:errcheck
	}
	fn := f.RunFunc
	var err error
	switch {
	case fn != nil:
		// i unused; RunFunc decides the outcome below.
	case len(f.RunErrs) > 0:
		if i < len(f.RunErrs) {
			err = f.RunErrs[i]
		} else {
			err = f.RunErrs[len(f.RunErrs)-1]
		}
	default:
		err = f.RunErr
	}
	f.mu.Unlock()

	if fn != nil {
		return fn(box)
	}
	return err
}

// Reap records the name.
func (f *Fake) Reap(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReapCalls = append(f.ReapCalls, name)
	return nil
}

// Kill records the name and returns KillErr.
func (f *Fake) Kill(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KillCalls = append(f.KillCalls, name)
	return f.KillErr
}

// IsRunning records name and returns IsRunningRet.
func (f *Fake) IsRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IsRunningCalls = append(f.IsRunningCalls, name)
	return f.IsRunningRet
}
