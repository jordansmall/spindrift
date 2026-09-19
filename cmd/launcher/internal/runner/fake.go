package runner

import (
	"sync"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// Fake is an in-memory Runner for unit tests. Every method is safe for
// concurrent use.
type Fake struct {
	mu sync.Mutex

	EnsureReadyCalls int

	IsReadyCalls int

	RunCalls []Box

	ReapCalls []string

	KillCalls []string
	KillErr   error

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

// Run records the box and returns the scripted error for this call index, or
// calls RunFunc when it is set.
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

// IsRunning records the name and returns IsRunningRet.
func (f *Fake) IsRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.IsRunningCalls = append(f.IsRunningCalls, name)
	return f.IsRunningRet
}

// ListRunning returns RunningNames, or ListRunningErr when set.
func (f *Fake) ListRunning() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListRunningErr != nil {
		return nil, f.ListRunningErr
	}
	return f.RunningNames, nil
}

// RegistryProxyTransport records the call and returns the scripted endpoint,
// add-host flag and error.
func (f *Fake) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RegistryProxyTransportCalls++
	return f.RegistryProxyTransportEndpoint, f.RegistryProxyTransportAddHost, f.RegistryProxyTransportErr
}
