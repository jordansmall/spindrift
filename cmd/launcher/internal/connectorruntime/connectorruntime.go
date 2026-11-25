// Package connectorruntime owns what a Launcher process needs to host
// Connectors: the backend rows, the sandbox it loads them into, and the live
// sessions, closed together.
//
// A session is the one Connector a (row, Target) pair keeps alive, whichever
// axis (tracker or Code Forge) asks for it. It owns, for every backend, the
// load, the reload of a Connector that died, the cooldown after repeated load
// failures, the failure note, and the one-time check of the capabilities the
// backend requires.
package connectorruntime

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
	"spindrift.dev/launcher/internal/retry"
)

// ErrMissingCapabilities is a Connector whose handshake omits a capability its
// backend requires.
var ErrMissingCapabilities = errors.New("Connector lacks required capabilities")

// ErrClosed is a Tracker or Code Forge request on a runtime already closed.
var ErrClosed = errors.New("Connector runtime closed")

// ErrNoRuntime is a tracker or Code Forge built without a Connector runtime.
var ErrNoRuntime = errors.New("tracker or Code Forge built without a Connector runtime")

const (
	maxLoadFailures = 3
	loadRetryAfter  = time.Minute
)

// Runtime is the rows, the sandbox and the live sessions, with one Close.
type Runtime struct {
	lookup  func(string) (backendrows.Row, bool)
	sandbox connectorsandbox.Loader
	// now is the cooldown's clock, a test seam.
	now func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[sessionKey]*session
}

type sessionKey struct {
	row    string
	target connectortracker.Target
}

// New is a runtime over rows that loads Connectors into sandbox; a nil sandbox
// is the installed one.
func New(rows []backendrows.Row, sandbox connectorsandbox.Loader) *Runtime {
	byName := make(map[string]backendrows.Row, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	return newRuntime(func(name string) (backendrows.Row, bool) { row, ok := byName[name]; return row, ok }, sandbox)
}

// FromRegistries is a runtime over the applied backend rows and the installed
// sandbox.
func FromRegistries() *Runtime { return newRuntime(backendrows.Lookup, nil) }

func newRuntime(lookup func(string) (backendrows.Row, bool), sandbox connectorsandbox.Loader) *Runtime {
	return &Runtime{lookup: lookup, sandbox: sandbox, now: time.Now, sessions: map[sessionKey]*session{}}
}

// Row is the named backend row.
func (r *Runtime) Row(name string) (backendrows.Row, bool) { return r.lookup(name) }

// Close closes every session's live Connector and refuses further Trackers and
// Code Forges; a second Close does nothing.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sessions := r.sessions
	r.sessions = nil
	r.mu.Unlock()
	var errs []error
	for _, s := range sessions {
		errs = append(errs, s.close())
	}
	return errors.Join(errs...)
}

// SessionSpec names a (row, Target) session and how it loads. Its loading
// fields (Requires, Record, Options) are those of the call that first opened
// the session, whichever axis made it.
type SessionSpec struct {
	Target connectortracker.Target
	// Requires are the capabilities the backend requires; a Connector missing
	// one is refused at load.
	Requires []string
	// Record names the connectorrecord files the failure note goes to; empty
	// means the row's name.
	Record string
	// Options are the host's Client and Getenv for the load; the runtime
	// supplies the sandbox.
	Options backendrows.Options
}

// TrackerSpec is what a tracker needs of its backend beyond the row.
type TrackerSpec struct {
	SessionSpec
	Lifecycle connectortracker.Lifecycle
	// Clock paces the retry backoff; the zero value sleeps for real.
	Clock retry.Clock
}

// Tracker is a live tracker for row and spec.Target. Trackers for one
// (row, Target) share its session, so the Connector loads once and a dead one
// reloads before the next call, whichever tracker makes it.
func (r *Runtime) Tracker(row backendrows.Row, spec TrackerSpec) (forge.IssueTracker, error) {
	s, err := r.session(row, spec.SessionSpec)
	if err != nil {
		return nil, err
	}
	c, err := s.live()
	if err != nil {
		_ = connectorrecord.Note(s.record, "load the Connector", err)
		return nil, err
	}
	it, err := connectortracker.NewCalling(c, s.call, spec.Lifecycle, spec.Target, spec.Clock)
	if err != nil {
		// Unlike the Code Forge's compose, building the tracker can call the
		// guest (the count interval), so a failure may mean a dead Connector.
		s.drop(c)
		_ = connectorrecord.Note(s.record, "compose the tracker", err)
		return nil, err
	}
	return it, nil
}

// ForgeSpec is what a Code Forge needs of its backend beyond the row. Its
// fields are those of connectorforge.Git but Note, which the session supplies:
// it already notes every failed guest call by method.
type ForgeSpec struct {
	SessionSpec
	Credential gitexec.Credential
	Plumbing   func(cloneURL string, cred gitexec.Credential) forge.CodeForge
	Clock      retry.Clock
	WebBase    string
}

// CodeForge is a live Code Forge for row and spec.Target, sharing the
// (row, Target) session with Trackers: one row serving both axes loads one
// Connector, and a dead one reloads before the next call, whichever axis makes
// it.
func (r *Runtime) CodeForge(row backendrows.Row, spec ForgeSpec) (forge.CodeForge, error) {
	s, err := r.session(row, spec.SessionSpec)
	if err != nil {
		return nil, err
	}
	c, err := s.live()
	if err != nil {
		_ = connectorrecord.Note(s.record, "load the Connector", err)
		return nil, err
	}
	// Dialect is tracker-only: the forge axis keys on API and Repo.
	target := connectorforge.Target{API: spec.Target.API, Repo: spec.Target.Repo}
	git := connectorforge.Git{Credential: spec.Credential, Plumbing: spec.Plumbing, Clock: spec.Clock, WebBase: spec.WebBase}
	cf, _, err := connectorforge.ComposeCalling(c, s.call, s, target, git)
	if err != nil {
		// Compose reads the loaded manifest and host configuration and makes no
		// guest call, so a failure never means a dead Connector, and this one
		// may be serving the tracker too: do not drop it.
		_ = connectorrecord.Note(s.record, "compose the Code Forge", err)
		return nil, err
	}
	return cf, nil
}

func (r *Runtime) session(row backendrows.Row, spec SessionSpec) (*session, error) {
	key := sessionKey{row.Name, spec.Target}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	s, ok := r.sessions[key]
	if !ok {
		s = &session{rt: r, row: row, requires: spec.Requires, record: cmp.Or(spec.Record, row.Name), opts: spec.Options}
		s.opts.Sandbox = r.sandbox
		r.sessions[key] = s
	}
	return s, nil
}

// session keeps one Connector alive. After maxLoadFailures failed loads in a
// row a Connector that cannot load is retried at most once per loadRetryAfter,
// so a broken module is not recompiled on every call.
type session struct {
	rt       *Runtime
	row      backendrows.Row
	requires []string
	record   string
	opts     backendrows.Options

	mu  sync.Mutex
	cur *connectorhost.Connector
	// failures counts consecutive failed loads; lastErr and lastFail describe
	// the latest.
	failures int
	lastErr  error
	lastFail time.Time
}

// live is the session's usable Connector, loaded or reloaded as needed.
func (s *session) live() (*connectorhost.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && s.cur.Err() == nil {
		return s.cur, nil
	}
	if s.failures >= maxLoadFailures && s.rt.now().Sub(s.lastFail) < loadRetryAfter {
		return nil, fmt.Errorf("connector load failed %d times in a row; next retry allowed %s after the last failure: %w", s.failures, loadRetryAfter, s.lastErr)
	}
	c, err := s.load()
	if err != nil {
		s.failures, s.lastErr, s.lastFail = s.failures+1, err, s.rt.now()
		return nil, err
	}
	s.failures, s.lastErr = 0, nil
	s.cur = c
	return c, nil
}

func (s *session) load() (*connectorhost.Connector, error) {
	c, err := backendrows.Load(context.Background(), s.row, s.opts)
	if err != nil {
		return nil, err
	}
	if absent := missing(s.requires, c.Capabilities); len(absent) > 0 {
		_ = c.Close(context.Background())
		return nil, fmt.Errorf("%s %w: %s", s.record, ErrMissingCapabilities, strings.Join(absent, ", "))
	}
	return c, nil
}

func missing(required, have []string) []string {
	var out []string
	for _, c := range required {
		if !slices.Contains(have, c) {
			out = append(out, c)
		}
	}
	return out
}

// call passes every failure the Connector causes to the failure note, by
// method: a failed reload, a refused retry, or a failed call. Errors core
// raises itself, such as a refused lifecycle transition, never reach it.
func (s *session) call(ctx context.Context, method string, params, result any) error {
	err := s.callOnce(ctx, method, params, result)
	if err != nil {
		_ = connectorrecord.Note(s.record, method, err)
	}
	return err
}

func (s *session) callOnce(ctx context.Context, method string, params, result any) error {
	c, err := s.live()
	if err != nil {
		return err
	}
	return c.Call(ctx, method, params, result)
}

// AuditRefusal records a refusal core made itself in the live Connector's
// audit log, so after a reload it lands in the guest now serving. With no live
// Connector the refusal stays in the returned chain.
func (s *session) AuditRefusal(method string, refusal error) error {
	c, err := s.live()
	if err != nil {
		return errors.Join(refusal, err)
	}
	return c.AuditRefusal(method, refusal)
}

// drop closes c, a Connector the caller cannot use, unless the session has
// already moved on to another.
func (s *session) drop(c *connectorhost.Connector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == c {
		s.cur = nil
		_ = c.Close(context.Background())
	}
}

func (s *session) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return nil
	}
	return s.cur.Close(context.Background())
}
