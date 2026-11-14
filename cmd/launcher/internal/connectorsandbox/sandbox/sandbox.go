// Package sandbox is the Connector sandbox a run starts through its runner, on
// the first Connector load, and shares among every later one.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/runner"
)

var errClosed = errors.New("the Connector sandbox is closed")

// Starter is the part of a runner.Runner a Sandbox needs.
type Starter interface {
	StartConnectorSandbox(name string) (runner.ConnectorSandbox, error)
	Kill(name string) error
}

// helloTimeout bounds the wait for a started sandbox's connector host to speak.
var helloTimeout = 30 * time.Second

// stderrTail bounds the sandbox's stderr quoted in a start error.
const stderrTail = 2048

// Sandbox is one run's Connector sandbox, started by its first load.
type Sandbox struct {
	r     Starter
	label string

	mu        sync.Mutex
	started   bool
	closed    bool
	name      string
	startedOK bool
	sessions  *connectorhost.Sessions
	start     time.Duration
	err       error

	// stopMu guards stopped. It is not mu: Sessions' killer can fire kill
	// while Close holds mu.
	stopMu  sync.Mutex
	stopped bool
}

var _ connectorsandbox.Loader = (*Sandbox)(nil)

// New returns a Sandbox that starts through r. label names r in a start error.
func New(r Starter, label string) *Sandbox { return &Sandbox{r: r, label: label} }

// ensure starts the sandbox on its first call; the outcome, success or
// failure, is every later call's.
func (s *Sandbox) ensure() (*connectorhost.Sessions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if s.started {
		return s.sessions, s.err
	}
	s.started = true
	s.name = fmt.Sprintf("%s%d-%d", runner.ConnectorSandboxPrefix, os.Getpid(), time.Now().UnixNano())
	began := time.Now()
	sb, err := s.r.StartConnectorSandbox(s.name)
	if err != nil {
		s.err = s.startError(err, "")
		return nil, s.err
	}
	s.startedOK = true
	// Not derived from a loader's ctx: the outcome is the whole run's, so one
	// short or cancelled ctx must not decide it.
	hctx, cancel := context.WithTimeout(context.Background(), helloTimeout)
	defer cancel()
	s.sessions, err = connectorhost.NewSessions(hctx, sb.Stdout, sb.Stdin, func() { _ = s.kill() }, sb.Wait)
	if err != nil {
		_ = sb.Stdin.Close()
		_ = s.kill()
		s.err = s.startError(err, sb.Stderr())
	} else {
		s.start = time.Since(began)
	}
	return s.sessions, s.err
}

// kill is the stop hook of a started sandbox. Closing its stdin is the first
// signal and this the backstop, so it runs until it succeeds once however many
// paths reach it. A failed stop is left unmarked so a later call, such as
// Close's, retries and returns the real error.
func (s *Sandbox) kill() error {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopped {
		return nil
	}
	if err := s.r.Kill(s.name); err != nil {
		return err
	}
	s.stopped = true
	return nil
}

func (s *Sandbox) startError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > stderrTail {
		stderr = "..." + stderr[len(stderr)-stderrTail:]
	}
	if stderr == "" {
		stderr = "(none)"
	}
	return fmt.Errorf("%w: runner %s: %v; stderr: %s", connectorsandbox.ErrConnectorSandboxStart, s.label, err, stderr)
}

// Load is connectorhost.Load into the sandbox.
func (s *Sandbox) Load(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
	ss, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return ss.Load(ctx, wasm)
}

// LoadNetwork is connectorhost.LoadNetwork into the sandbox.
func (s *Sandbox) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	ss, err := s.ensure()
	if err != nil {
		return nil, err
	}
	return ss.LoadNetwork(ctx, wasm, n)
}

// Err is the start failure, or nil if the sandbox started or no load has asked
// for it.
func (s *Sandbox) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Stats is the sandbox's start time and the exchanges it has carried, also after
// Close; false when no load started it or its start failed.
func (s *Sandbox) Stats() (connectorsandbox.Stats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		return connectorsandbox.Stats{}, false
	}
	return connectorsandbox.Stats{Start: s.start, HopStats: s.sessions.Stats()}, true
}

// Close stops the sandbox if a load started one, and is a no-op otherwise. No
// load starts one after it.
func (s *Sandbox) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if !s.startedOK {
		return nil
	}
	if s.sessions == nil {
		return s.kill()
	}
	return errors.Join(s.sessions.Close(ctx), s.kill())
}
