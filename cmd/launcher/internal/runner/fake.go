package runner

import "sync"

// Fake is an in-memory Runner for unit tests. All methods are safe for
// concurrent use. It records calls so tests can assert on what crossed the seam.
type Fake struct {
	mu sync.Mutex

	ensureErr    error
	runErr       error
	imagePresent bool

	// EnsureCalls counts how many times EnsureReady was called.
	EnsureCalls int
	// RunCalls records every Box passed to Run, in order.
	RunCalls []Box
	// ReapCalls records every name passed to Reap, in order.
	ReapCalls []string
}

// NewFake returns an empty Fake runner.
func NewFake() *Fake {
	return &Fake{}
}

// SetEnsureError scripts the error returned by EnsureReady.
func (f *Fake) SetEnsureError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureErr = err
}

// SetRunError scripts the error returned by Run.
func (f *Fake) SetRunError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runErr = err
}

// SetImagePresent controls the simulated image-presence state. When true,
// EnsureReady models the "image already present" path (still succeeds).
// The fake does not change its return value based on this flag — it exists so
// orchestration tests can reason about which ensure path was "taken".
func (f *Fake) SetImagePresent(present bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imagePresent = present
}

func (f *Fake) EnsureReady() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.EnsureCalls++
	return f.ensureErr
}

func (f *Fake) Run(box Box) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy env so mutations after Run don't affect the recorded call.
	env := make(map[string]string, len(box.Env))
	for k, v := range box.Env {
		env[k] = v
	}
	f.RunCalls = append(f.RunCalls, Box{
		Issue: box.Issue,
		Name:  box.Name,
		Env:   env,
	})
	return f.runErr
}

func (f *Fake) Reap(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReapCalls = append(f.ReapCalls, name)
	return nil
}
