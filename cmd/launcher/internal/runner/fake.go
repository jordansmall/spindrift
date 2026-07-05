package runner

import "sync"

// Fake is an in-memory Runner for unit tests. All methods are safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// EnsureReadyCalls counts how many times EnsureReady was called.
	EnsureReadyCalls int

	// RunCalls records all Run invocations in order.
	RunCalls []Box

	// ReapCalls records names passed to Reap in order.
	ReapCalls []string

	// EnsureReadyErr, if non-nil, is returned by EnsureReady.
	EnsureReadyErr error

	// RunErr, if non-nil, is returned by every Run call.
	RunErr error
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

// Run records the box and returns RunErr.
func (f *Fake) Run(box Box) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RunCalls = append(f.RunCalls, box)
	return f.RunErr
}

// Reap records the name.
func (f *Fake) Reap(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReapCalls = append(f.ReapCalls, name)
	return nil
}
