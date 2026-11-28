package forge

import "sync"

var _ CIRerunner = (*RerunFake)(nil)

// RerunFake is a Fake that also re-runs failed CI. It is a wrapper rather than
// part of PRForgeFake so the plain Fake stays a forge with no CIRerunner
// capability, like forgejo. A re-run is recorded in the wrapper, not on the
// Fake's Connector.
type RerunFake struct {
	*Fake

	mu    sync.Mutex
	err   error
	calls []string
}

// NewRerunFake returns a RerunFake over an empty Fake.
func NewRerunFake(labels ...DispatchLabels) *RerunFake {
	return &RerunFake{Fake: NewFake(labels...)}
}

// SetRerunErr scripts the error every RerunFailedCI call returns.
func (f *RerunFake) SetRerunErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Reruns returns the PR URL of each RerunFailedCI call, in order.
func (f *RerunFake) Reruns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// RerunFailedCI records the call and returns the scripted error.
func (f *RerunFake) RerunFailedCI(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, url)
	return f.err
}
