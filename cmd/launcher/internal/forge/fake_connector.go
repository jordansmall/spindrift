package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// The Fake's read path crosses a real network Connector (ADR 0073, point 7): a
// read is a "fetch" through the Connector host, which checks it against the
// fixture Route table, attaches the credential, and sends it to a scripted
// backend that serves the Fake's own state. Writes stay native and mutate that
// same state, so a read observes every write.

const (
	fakeHost  = "fake.invalid"
	fakeToken = "fake-connector-token"
)

// fakeRoutes is the fixture Route table. Paths are subtree roots (ADR 0047).
var fakeRoutes = []connectorhost.Route{
	{Method: http.MethodGet, Host: fakeHost, Path: "/tracker"},
	{Method: http.MethodGet, Host: fakeHost, Path: "/forge"},
}

// pendingRead is one read in flight: the closure the backend runs against the
// Fake's state, and what came back through the Connector.
type pendingRead struct {
	serve func() (any, error)
	// err is the serve error, kept here because a Go error loses its identity
	// crossing the wasm boundary (a Connector failure wraps ErrCall).
	err   error
	audit connectorhost.AuditEntry
	// audited is set when the host audited the call.
	audited bool
}

var (
	pending    sync.Map // uint64 call id -> *pendingRead
	nextCallID atomic.Uint64
)

// observers are the test hooks watching a Fake's reads, keyed by its core. They
// live outside core because TestCoreFieldShape pins core's fields.
var observers sync.Map // *core -> func(connectorhost.AuditEntry)

// fakeBackend is the scripted backend. It answers 401 without the host-attached
// credential, so a read that skipped Authorize cannot succeed.
var fakeBackend = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseUint(r.URL.Query().Get("call"), 10, 64)
	v, ok := pending.Load(id)
	if err != nil || !ok {
		http.Error(w, "unknown call", http.StatusNotFound)
		return
	}
	p := v.(*pendingRead)
	val, err := p.serve()
	if err != nil {
		p.err = err
		http.Error(w, "scripted error", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(val)
})

// inProcess serves the backend without a socket.
type inProcess struct{}

func (inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	fakeBackend.ServeHTTP(rec, r)
	return rec.Result(), nil
}

// auditSink attaches each audit line to the pending read its query names.
type auditSink struct{}

func (auditSink) Write(line []byte) (int, error) {
	var e connectorhost.AuditEntry
	if err := json.Unmarshal(bytes.TrimSpace(line), &e); err != nil {
		return 0, err
	}
	q, err := url.ParseQuery(e.Query)
	if err != nil {
		return len(line), nil
	}
	if id, err := strconv.ParseUint(q.Get("call"), 10, 64); err == nil {
		if v, ok := pending.Load(id); ok {
			p := v.(*pendingRead)
			p.audit, p.audited = e, true
		}
	}
	return len(line), nil
}

// seam is the one Connector shared by every Fake in the test binary. It panics
// when the Connector cannot load, because no core test may skip the seam (ADR
// 0067). A route refusal closes a Connector for good, so nothing hostile may be
// sent through it.
var seam = sync.OnceValue(func() *connectorhost.Connector {
	c, err := loadFakeConnector()
	if err != nil {
		panic(fmt.Sprintf("forge: fake Connector: %v", err))
	}
	return c
})

func loadFakeConnector() (*connectorhost.Connector, error) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		return nil, err
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "network.wasm"))
	if err != nil {
		return nil, err
	}
	return connectorhost.LoadNetworkCompiled(context.Background(), wasm, connectorhost.Network{
		Name:   "fake",
		Routes: fakeRoutes,
		Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+fakeToken)
			return nil
		},
		Client: &http.Client{Transport: inProcess{}},
		Audit:  connectorhost.NewAuditLog(auditSink{}),
	}, fixturebuild.WazeroCacheDir())
}

// read sends one GET for path through the Connector, runs serve in the
// backend, and decodes the answer into out. serve takes the Fake's lock itself,
// so the caller must not hold it. The read fails unless the host audited it as
// sent on a declared Route.
func (c *core) read(path string, out any, serve func() (any, error)) error {
	id := nextCallID.Add(1)
	p := &pendingRead{serve: serve}
	pending.Store(id, p)
	defer pending.Delete(id)

	var got struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	params := map[string]string{
		"url":    "https://" + fakeHost + path + "?call=" + strconv.FormatUint(id, 10),
		"method": http.MethodGet,
	}
	if err := seam().Call(context.Background(), "fetch", params, &got); err != nil {
		return fmt.Errorf("fake connector read %s: %w", path, err)
	}
	if !p.audited || p.audit.Decision != connectorhost.DecisionSent || p.audit.Route == "" {
		return fmt.Errorf("fake connector read %s: not sent on a declared route: %+v", path, p.audit)
	}
	if o, ok := observers.Load(c); ok {
		o.(func(connectorhost.AuditEntry))(p.audit)
	}
	if p.err != nil {
		return p.err
	}
	if got.Status != http.StatusOK {
		return fmt.Errorf("fake connector read %s: status %d: %s", path, got.Status, strings.TrimSpace(got.Body))
	}
	return json.Unmarshal([]byte(got.Body), out)
}
