package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/readshadow"
)

const (
	// shadowCloseBudget is what the end of a run waits for queued shadow calls.
	shadowCloseBudget = 30 * time.Second
)

// readShadows owns a dispatch run's read shadows, one per backend name, so a
// backend serving both the tracker and the code forge shares one. A backend
// row without Shadow, or no row, is left unwrapped.
type readShadows struct {
	pwd    string
	warn   io.Writer
	getenv func(string) string
	now    func() time.Time
	load   func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error)

	mu      sync.Mutex
	byName  map[string]*shadowEntry
	filesMu sync.Mutex
	closers []io.Closer
}

// shadowEntry is one backend's shadow plus the native sides its Connector's
// reads shadow; Open reads them when the first sampled call arrives.
type shadowEntry struct {
	shadow  *readshadow.Shadow
	tracker forge.IssueTracker
	forge   forge.CodeForge
}

func newReadShadows(pwd string, warn io.Writer) *readShadows {
	return &readShadows{pwd: pwd, warn: warn, getenv: os.Getenv, now: time.Now, load: backendrows.Load, byName: map[string]*shadowEntry{}}
}

func (r *readShadows) wrapTracker(c config, native forge.IssueTracker) forge.IssueTracker {
	if r == nil {
		return native
	}
	e := r.entry(c, c.IssueTracker)
	if e == nil {
		return native
	}
	r.mu.Lock()
	e.tracker = native
	r.mu.Unlock()
	return e.shadow.Tracker(native)
}

func (r *readShadows) wrapCodeForge(c config, native forge.CodeForge) forge.CodeForge {
	if r == nil {
		return native
	}
	e := r.entry(c, c.CodeForge)
	if e == nil {
		return native
	}
	r.mu.Lock()
	e.forge = native
	r.mu.Unlock()
	return e.shadow.CodeForge(native)
}

// entry returns name's shadow, creating it on first use; nil when the backend
// has no row asking for one.
func (r *readShadows) entry(c config, name string) *shadowEntry {
	row, ok := backendrows.Lookup(name)
	if !ok || !row.Shadow {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byName[name]; ok {
		return e
	}
	stem := fmt.Sprintf("shadow-%s-%s-%d", name, r.now().UTC().Format("20060102T150405Z"), os.Getpid())
	ops := &warnOnceWriter{w: r.createLog(stem + ".log"), name: stem + ".log", warn: r.warn}
	var opsMu sync.Mutex
	e := &shadowEntry{}
	e.shadow = readshadow.New(readshadow.Config{
		Backend: name,
		Every:   row.ShadowEvery,
		Emit: func(op hostop.SpindriftOp) {
			opsMu.Lock()
			defer opsMu.Unlock()
			_, _ = io.WriteString(ops, hostop.EncodeSpindriftOp(op))
		},
		Warn: r.warn,
		Open: func(ctx context.Context, observe func(connectorhost.AuditEntry)) (readshadow.Reads, error) {
			r.mu.Lock()
			it, cf := e.tracker, e.forge
			r.mu.Unlock()
			return r.open(ctx, row, c, it, cf, observe, stem+".audit.jsonl")
		},
	})
	r.byName[name] = e
	return e
}

// warnOnceWriter forwards to w and, on its first write error, warns once: a
// record that failed to land must not leave the run looking clean. The caller
// serialises writes.
type warnOnceWriter struct {
	w      io.Writer
	name   string
	warn   io.Writer
	warned bool
}

func (w *warnOnceWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil && !w.warned {
		w.warned = true
		fmt.Fprintf(w.warn, "==> shadow: cannot write %s (%v); later records may be missing\n", w.name, err)
	}
	return n, err
}

// createLog opens name under the Pass-log directory. A failure must not fail
// the run, so it warns and discards.
func (r *readShadows) createLog(name string) io.Writer {
	dir := hostpaths.LogDir(r.pwd)
	err := os.MkdirAll(dir, 0o755)
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	}
	if err != nil {
		fmt.Fprintf(r.warn, "==> shadow: cannot write %s (%v); its records are discarded\n", name, err)
		return io.Discard
	}
	r.filesMu.Lock()
	r.closers = append(r.closers, f)
	r.filesMu.Unlock()
	return f
}

// open loads row's Connector and builds its read sides over the native
// tracker and code forge. A side whose native is absent stays nil
// (unshadowed). A side that cannot be built fails the whole open, which dirties
// the summary: a run that never compared a read path must not count as clean.
func (r *readShadows) open(ctx context.Context, row backendrows.Row, c config, it forge.IssueTracker, cf forge.CodeForge, observe func(connectorhost.AuditEntry), auditName string) (readshadow.Reads, error) {
	if r.getenv(row.CredentialEnv) == "" {
		return readshadow.Reads{}, fmt.Errorf("%s is not set, so the %s shadow sends no requests", row.CredentialEnv, row.Name)
	}
	row.Routes = connectortracker.Routes(row.Routes, c.RepoSlug)
	conn, err := r.load(ctx, row, backendrows.Options{
		Getenv:  r.getenv,
		Audit:   r.createLog(auditName),
		Shadow:  true,
		Observe: observe,
	})
	if err != nil {
		return readshadow.Reads{}, err
	}
	reads := readshadow.Reads{
		Failed: conn.Err,
		Abort:  conn.Abort,
		Close: func() {
			if err := conn.Close(context.Background()); err != nil {
				fmt.Fprintf(r.warn, "==> shadow: cannot close the %s connector (%v)\n", row.Name, err)
			}
		},
	}
	reads.Tracker, reads.PR, err = shadowSides(conn, row, c, it, cf)
	if err != nil {
		reads.Close()
		return readshadow.Reads{}, err
	}
	return reads, nil
}

// shadowTargets derives the API and web hosts from row, whose Routes are
// already substituted. The Route table names the API host; PR URLs must carry
// the web host the native backend itself links to.
// An empty web host is not an error here: connectorforge.New refuses it with
// ErrNoWebHost.
func shadowTargets(row backendrows.Row, c config) (api, webHost string, err error) {
	if len(row.Routes) == 0 {
		return "", "", fmt.Errorf("the %s backend row has no Route, so its API host is unknown", row.Name)
	}
	api = "https://" + row.Routes[0].Host
	if s, ok := backendkit.ByName(row.Name); ok && s.WebURL != nil {
		u, err := url.Parse(s.WebURL(backendShared(c), c.knobLookup))
		if err != nil {
			return "", "", fmt.Errorf("the %s web URL does not parse: %w", row.Name, err)
		}
		webHost = u.Host
	}
	return api, webHost, nil
}

// shadowSides builds the tracker and PR reads over conn. An absent native
// yields a nil side without error; any construction error is returned.
func shadowSides(conn *connectorhost.Connector, row backendrows.Row, c config, it forge.IssueTracker, cf forge.CodeForge) (forge.IssueTracker, forge.PRForge, error) {
	api, webHost, err := shadowTargets(row, c)
	if err != nil {
		return nil, nil, err
	}
	var (
		tracker forge.IssueTracker
		prReads forge.PRForge
		errs    []error
	)
	if it != nil {
		t, err := connectortracker.New(conn, it, dispatchLabels(c), connectortracker.Target{API: api, Repo: c.RepoSlug})
		tracker = t
		errs = append(errs, err)
	}
	if pr, ok := forge.AsCodeForge[forge.PRForge](cf); ok {
		p, err := connectorforge.New(conn, pr, connectorforge.Target{API: api, Repo: c.RepoSlug}, webHost)
		prReads = p
		errs = append(errs, err)
	}
	return tracker, prReads, errors.Join(errs...)
}

// close ends every shadow within shadowCloseBudget, which writes each one's
// summary, then closes the record files.
func (r *readShadows) close() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shadowCloseBudget)
	defer cancel()
	r.mu.Lock()
	entries := make([]*shadowEntry, 0, len(r.byName))
	for _, e := range r.byName {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	for _, e := range entries {
		e.shadow.Close(ctx)
	}
	r.filesMu.Lock()
	defer r.filesMu.Unlock()
	for _, f := range r.closers {
		_ = f.Close()
	}
	r.closers = nil
}
