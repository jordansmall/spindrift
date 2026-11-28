// Package sandbox is the Connector sandboxes a run starts through its runner.
// A Sandbox starts on its first load. A Pool holds the one sandbox every Driver
// Connector shares and hands each credential-holding Connector a sandbox of its
// own, so no Connector's credential shares a pipe or an address space with
// another's.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/runner"
)

var errClosed = errors.New("the Connector sandbox is closed")

// Starter is the part of a runner.Runner a Sandbox needs.
type Starter interface {
	StartConnectorSandbox(name string) (runner.ConnectorSandbox, error)
	Kill(name string) error
}

// logOut is where kill reports; a var so a test can read the lines.
var logOut io.Writer = os.Stderr

// helloTimeout bounds the wait for a started sandbox's connector host to speak.
var helloTimeout = 30 * time.Second

// stderrTail bounds the sandbox's stderr quoted in a start error.
const stderrTail = 2048

// state is where a Sandbox is in its life. Every move goes through transition.
type state string

const (
	// idle means no load has asked for a sandbox yet.
	idle state = "idle"
	// running means the current generation started, and its host served when
	// ensure last checked; a host that dies stays running until ensure sees it.
	running state = "running"
	// dead means ensure saw the current generation's host die, or a start
	// failed; the next use starts a new generation.
	dead state = "dead"
	// closed is final: a closed Sandbox starts nothing.
	closed state = "closed"
)

// legalTransitions lists, per state, the states transition may move it to.
var legalTransitions = map[state][]state{
	idle:    {running, dead, closed},
	running: {dead, closed},
	dead:    {running, dead, closed},
	closed:  {closed},
}

// generation is one started sandbox. A Sandbox may outlive several, and each
// one's killer is bound to its own, so a killer that fires late stops the
// sandbox it was made for and never its successor.
type generation struct {
	r        Starter
	name     string
	sessions *connectorhost.Sessions // nil if the hello failed
	start    time.Duration

	// stopMu guards stopped. It is not Sandbox.mu: Sessions' killer fires kill
	// on its demux goroutine while Close or retire holds mu and waits for that
	// goroutine in Sessions.Close.
	stopMu  sync.Mutex
	stopped bool
}

// Sandbox is one run's Connector sandbox, started by its first load and
// restarted by the first load after its host died. Sessions open on a dead
// generation fail with it; they are not moved to the next.
type Sandbox struct {
	r     Starter
	label string

	// closeMu serializes Close, which holds it across the stops it waits on and
	// so cannot be mu: Err, Stats and loads must not wait behind a hung host.
	closeMu sync.Mutex

	mu    sync.Mutex
	state state
	// cur is the latest generation, running or dead; nil while idle, after a
	// start whose runner call failed, and between a retirement and the next
	// start. A daemon's Sandbox lives as long as its
	// process, so earlier generations are retired, not kept.
	cur *generation
	// past totals the retired generations' stats; served is whether any
	// generation's hello succeeded.
	past   connectorsandbox.Stats
	served bool
	err    error
	// retiring is the generations whose stop is still running off mu, each with
	// a channel closed once that stop returns. A stop can hang on a host the
	// runner failed to kill, so nothing holding mu may wait on one.
	retiring map[*generation]chan struct{}
}

var _ connectorsandbox.Loader = (*Sandbox)(nil)

// New returns a Sandbox that starts through r. label names r in a start error.
func New(r Starter, label string) *Sandbox {
	return &Sandbox{r: r, label: label, state: idle, retiring: map[*generation]chan struct{}{}}
}

// transition moves s to state to. An illegal move is a bug in this file, so it
// panics rather than returning an error that would mask the caller's own. s.mu
// is held.
func (s *Sandbox) transition(to state) {
	if !slices.Contains(legalTransitions[s.state], to) {
		panic(fmt.Sprintf("Connector sandbox: illegal transition %s -> %s", s.state, to))
	}
	s.state = to
}

// ensure returns the sessions of the running generation, starting one first if
// none runs or its host has died. A failed start is that call's error and
// leaves the sandbox dead, so the next call tries again.
func (s *Sandbox) ensure() (*connectorhost.Sessions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case closed:
		return nil, errClosed
	case running:
		if s.cur.sessions.Dead() == nil {
			return s.cur.sessions, nil
		}
		s.transition(dead)
	}
	if s.cur != nil {
		s.retire(s.cur)
		s.cur = nil
	}
	began := time.Now()
	g := &generation{r: s.r, name: Name(os.Getpid(), began)}
	sb, err := s.r.StartConnectorSandbox(g.name)
	if err != nil {
		return nil, s.startFailed(s.startError(err, ""))
	}
	s.cur = g
	// Not derived from a loader's ctx: every later load shares this generation
	// and its failure sticks in Err, so one caller's short or cancelled ctx must
	// not decide it.
	hctx, cancel := context.WithTimeout(context.Background(), helloTimeout)
	defer cancel()
	g.sessions, err = connectorhost.NewSessions(hctx, sb.Stdout, sb.Stdin, func() { _ = g.kill() }, sb.Wait)
	if err != nil {
		_ = sb.Stdin.Close()
		_ = g.kill()
		return nil, s.startFailed(s.startError(err, sb.Stderr()))
	}
	g.start = time.Since(began)
	s.served = true
	s.transition(running)
	return g.sessions, nil
}

// retire folds a generation that is being replaced into past and stops it off
// s.mu: the stop can hang on a host that will not die. The stats fold now
// because a dead host's meter no longer grows. Close retries the kill only while
// the stop is still running; a Kill that failed after it returned is left to the
// Orphans sweep, and a stop that never returns keeps its goroutine for the life
// of the process. s.mu is held.
func (s *Sandbox) retire(g *generation) {
	s.past = g.addStats(s.past)
	done := make(chan struct{})
	s.retiring[g] = done
	panicguard.Go(func() {
		_ = g.stop(context.Background())
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.retiring, g)
		close(done)
	})
}

// addStats folds g's start time and exchanges into st; a generation whose hello
// failed served nothing and adds nothing.
func (g *generation) addStats(st connectorsandbox.Stats) connectorsandbox.Stats {
	if g.sessions != nil {
		st.Start += g.start
		st.HopStats = st.HopStats.Add(g.sessions.Stats())
	}
	return st
}

// stop closes g's sessions, which reaps its host, then kills it.
func (g *generation) stop(ctx context.Context) error {
	var errs []error
	if g.sessions != nil {
		errs = append(errs, g.sessions.Close(ctx))
	}
	return errors.Join(append(errs, g.kill())...)
}

// kill is the stop hook of a started generation. Closing its stdin is the first
// signal and this the backstop, so it runs until it succeeds once however many
// paths reach it. A failed stop is reported and left unmarked so a later call on
// g, such as Close's, retries and returns the real error.
func (g *generation) kill() error {
	g.stopMu.Lock()
	defer g.stopMu.Unlock()
	if g.stopped {
		return nil
	}
	if err := g.r.Kill(g.name); err != nil {
		fmt.Fprintf(logOut, "warning: stopping Connector sandbox %s: %v\n", g.name, err)
		return err
	}
	g.stopped = true
	// bwrap's kill is an in-process signal that leaves no CLI call to
	// observe, so this line is the one trace of the stop on both families.
	fmt.Fprintf(logOut, "==> stopped Connector sandbox: %s\n", g.name)
	return nil
}

// startFailed records err as the latest start failure and leaves the sandbox
// dead, to be started again by the next use. s.mu is held.
func (s *Sandbox) startFailed(err error) error {
	s.err = err
	s.transition(dead)
	return err
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

// Err is the latest start failure, or nil if none occurred. It is kept after a
// later start succeeds, so the run's verdict still sees a failure a load site
// swallowed.
func (s *Sandbox) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Stats is the start time and the exchanges of every sandbox that served, also
// after Close; false when none served.
func (s *Sandbox) Stats() (connectorsandbox.Stats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.past
	if s.cur != nil {
		st = s.cur.addStats(st)
	}
	return st, s.served
}

// Close closes the Sandbox, so no load starts a sandbox after it, and stops the
// current sandbox, if one started. It also retries the Kill of every
// generation whose retirement is still running and waits for those stops to
// finish, until ctx ends: a stuck retirement is then reported by name. ctx does
// not bound the current sandbox's stop, which waits on its host without limit.
// Concurrent Closes serialize on closeMu, so none returns before the host is
// reaped; s.mu is taken only to snapshot the generations.
func (s *Sandbox) Close(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	s.transition(closed)
	cur := s.cur
	pending := maps.Clone(s.retiring)
	s.mu.Unlock()

	// The retirements are killed first: cur's stop may itself never return.
	var errs []error
	for g := range pending {
		errs = append(errs, g.kill())
	}
	if cur != nil {
		errs = append(errs, cur.stop(ctx))
	}
	for g, done := range pending {
		// Checked first: select picks at random when both are ready, and a
		// finished retirement must not be reported as still stopping.
		select {
		case <-done:
			continue
		default:
		}
		select {
		case <-done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("Connector sandbox %s: still stopping: %w", g.name, ctx.Err()))
		}
	}
	return errors.Join(errs...)
}
