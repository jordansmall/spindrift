package runner

import (
	"os"
	"slices"
	"sort"
	"sync"

	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// Fake is an in-memory Runner for unit tests. Every method is safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	// live maps the name of each Box mid-RunFunc to the channel Kill closes to
	// end that Run.
	live map[string]chan struct{}

	EnsureReadyCalls int

	IsReadyCalls int

	RunCalls []Box

	KillCalls []string

	KillErr error

	RemoveHostTreeCalls []string

	EnsureReadyErr error

	IsReadyErr error

	RunErr error

	// RunErrs holds per-call errors: RunErrs[i] is returned for the i-th Run
	// call, and the last element is reused once the sequence is exhausted. A
	// non-nil RunErrs takes precedence over RunErr.
	RunErrs []error

	// WriteToOutput, if non-nil, is written to box.Output before Run returns.
	WriteToOutput []byte

	// RunFunc, if non-nil, replaces the RunErrs/RunErr logic so a test can
	// control completion order and timing without real sleeps. Run calls it
	// with the Fake's lock released, so it may block or start concurrent Run
	// calls without deadlocking.
	RunFunc func(Box) error

	// IsRunningRet, when true, makes IsRunning report true for every name and
	// every Run return ErrAlreadyRunning.
	IsRunningRet bool

	IsRunningCalls []string

	// RunningNames is the orphan-detection seam (issue #651): a test sets it
	// to simulate sandboxes still running from a prior, crashed session.
	RunningNames []string

	ListRunningErr error

	RegistryProxyTransportCalls int

	// RegistryProxyTransportEndpoint defaults to the zero Endpoint (neither
	// IsUnix() nor IsTCP()), so a test opting into transport probing must set
	// it explicitly rather than inherit a default shaped like a real runtime.
	// It is one field, so a test cannot script a unix and a TCP answer at once.
	RegistryProxyTransportEndpoint registrymanifest.Endpoint

	// RegistryProxyTransportAddHost defaults to false: the runtime resolves
	// the TCP host on its own and needs no --add-host mapping.
	RegistryProxyTransportAddHost bool

	RegistryProxyTransportErr error
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

// Run records the box, then returns ErrAlreadyRunning, writing no output, if
// box.Name is running (see IsRunning); else it writes WriteToOutput and returns
// the scripted error for this call index, or calls RunFunc when it is set. A
// Kill of box.Name while RunFunc runs ends the Run with an ExitSIGKILL
// RunError, as a SIGKILLed box does. The refusal is not atomic with the
// RunFunc Run becoming live, so concurrent same-name Runs can both proceed.
func (f *Fake) Run(box Box) error {
	f.mu.Lock()
	i := len(f.RunCalls)
	f.RunCalls = append(f.RunCalls, box)
	if f.runningLocked(box.Name) {
		f.mu.Unlock()
		return ErrAlreadyRunning
	}
	if len(f.WriteToOutput) > 0 && box.Output != nil {
		box.Output.Write(f.WriteToOutput) //nolint:errcheck
	}
	fn := f.RunFunc
	var err error
	switch {
	case fn != nil:
		// RunFunc decides the outcome below, so i is unused here.
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
		return f.runFunc(fn, box)
	}
	return err
}

// runFunc runs fn as the live Box box.Name until it returns or Kill ends it.
// A nameless Box is not tracked, so unnamed concurrent Runs never collide.
func (f *Fake) runFunc(fn func(Box) error, box Box) error {
	if box.Name == "" {
		return fn(box)
	}
	killed := make(chan struct{})
	f.mu.Lock()
	if f.live == nil {
		f.live = map[string]chan struct{}{}
	}
	f.live[box.Name] = killed
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		if f.live[box.Name] == killed {
			delete(f.live, box.Name)
		}
		f.mu.Unlock()
	}()

	// done closes even if fn exits its goroutine (t.FailNow), so Run cannot hang.
	var fnErr error
	done := make(chan struct{})
	panicguard.Go(func() {
		defer close(done)
		fnErr = fn(box)
	})
	select {
	case <-done:
		return fnErr
	case <-killed:
		return &RunError{ExitCode: ExitSIGKILL, Msg: "fake box killed (SIGKILL)"}
	}
}

// Kill records the name and returns KillErr. With KillErr nil it also ends a
// live RunFunc Run of that name; a failed Kill leaves the Box running.
func (f *Fake) Kill(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KillCalls = append(f.KillCalls, name)
	if f.KillErr != nil {
		return f.KillErr
	}
	if ch, ok := f.live[name]; ok {
		close(ch)
		delete(f.live, name)
	}
	return nil
}

// RemoveHostTree records the path and removes it for real, so tests that rely
// on eviction still see the directory gone.
func (f *Fake) RemoveHostTree(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemoveHostTreeCalls = append(f.RemoveHostTreeCalls, path)
	return os.RemoveAll(path)
}

// IsRunning records the name and reports whether it is running: IsRunningRet
// is set, the name is in RunningNames, or a RunFunc Run of it is live.
func (f *Fake) IsRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IsRunningCalls = append(f.IsRunningCalls, name)
	return f.runningLocked(name)
}

func (f *Fake) runningLocked(name string) bool {
	if f.IsRunningRet || slices.Contains(f.RunningNames, name) {
		return true
	}
	_, live := f.live[name]
	return live
}

// ListRunning returns RunningNames plus the names of live RunFunc Runs, or
// ListRunningErr when set.
func (f *Fake) ListRunning() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListRunningErr != nil {
		return nil, f.ListRunningErr
	}
	if len(f.live) == 0 {
		return f.RunningNames, nil
	}
	names := slices.Clone(f.RunningNames)
	var liveNames []string
	for n := range f.live {
		if !slices.Contains(names, n) {
			liveNames = append(liveNames, n)
		}
	}
	sort.Strings(liveNames)
	return append(names, liveNames...), nil
}

// RegistryProxyTransport records the call and returns the scripted endpoint,
// add-host flag and error.
func (f *Fake) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RegistryProxyTransportCalls++
	return f.RegistryProxyTransportEndpoint, f.RegistryProxyTransportAddHost, f.RegistryProxyTransportErr
}
