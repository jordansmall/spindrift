package forge

import "sync"

var _ CIRunReporter = (*RunURLFake)(nil)

// RunURLFake is a Fake that also reports a CI run URL per check poll. It is a
// wrapper rather than part of PRForgeFake so the plain Fake stays a forge with
// no CIRunReporter capability, like forgejo.
type RunURLFake struct {
	*Fake

	mu    sync.Mutex
	urlsQ map[string][]string
}

// NewRunURLFake returns a RunURLFake over an empty Fake.
func NewRunURLFake(labels ...DispatchLabels) *RunURLFake {
	return &RunURLFake{Fake: NewFake(labels...), urlsQ: map[string][]string{}}
}

// SetRunURLs scripts the run URLs successive CheckRun calls return for url,
// one per call alongside the SetCheckStates queue. Once drained, CheckRun
// returns "", the answer for a PR with no run registered yet.
func (f *RunURLFake) SetRunURLs(url string, urls []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urlsQ[url] = append([]string(nil), urls...)
}

// CheckRun is CheckState plus the next scripted run URL.
func (f *RunURLFake) CheckRun(url string) (RollupState, string, error) {
	state, err := f.CheckState(url)
	f.mu.Lock()
	defer f.mu.Unlock()
	var run string
	if q := f.urlsQ[url]; len(q) > 0 {
		run, f.urlsQ[url] = q[0], q[1:]
	}
	return state, run, err
}
