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

// Run records the box and returns the error for this call index. If RunErrs
// is non-nil, RunErrs[i] is used (last element reused when exhausted);
// otherwise RunErr is returned. If WriteToOutput is set and box.Output is
// non-nil, the bytes are written to box.Output before returning.
func (f *Fake) Run(box Box) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.RunCalls)
	f.RunCalls = append(f.RunCalls, box)
	if len(f.WriteToOutput) > 0 && box.Output != nil {
		box.Output.Write(f.WriteToOutput) //nolint:errcheck
	}
	if len(f.RunErrs) > 0 {
		if i < len(f.RunErrs) {
			return f.RunErrs[i]
		}
		return f.RunErrs[len(f.RunErrs)-1]
	}
	return f.RunErr
}

// Reap records the name.
func (f *Fake) Reap(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReapCalls = append(f.ReapCalls, name)
	return nil
}
