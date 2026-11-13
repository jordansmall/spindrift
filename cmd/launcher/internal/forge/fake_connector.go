package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// The Fake crosses a real network Connector (ADR 0073, point 7): a read or a
// forge write is a "fetch" through the Connector host, which checks it against
// the fixture Route table, attaches the credential, and sends it to a scripted
// backend that serves the Fake's own state, so a read observes every write.
// Tracker writes stay native (#6753), as does RelayBundle: a bundle relay is git
// in core, not a forge HTTP primitive.

const (
	fakeHost  = "fake.invalid"
	fakeToken = "fake-connector-token"
)

// The read paths, one per Route. Every read names one of these, so a call
// cannot drift from the table below; reads answering from the same state share
// one (pathChecks, pathPRsForBranch, pathListIssues), so its audit entry does
// not tell them apart.
const (
	pathListIssues       = "/tracker/listIssues"
	pathIssue            = "/tracker/issue"
	pathComments         = "/tracker/comments"
	pathNativeDeps       = "/tracker/nativeDeps"
	pathLabels           = "/tracker/labels"
	pathBlocks           = "/tracker/blocks"
	pathAllIssues        = "/tracker/allIssues"
	pathTrackerProbe     = "/tracker/probe"
	pathPriorClaim       = "/tracker/priorClaim"
	pathPRsForBranch     = "/forge/prsForBranch"
	pathPR               = "/forge/pr"
	pathChecks           = "/forge/checks"
	pathBranch           = "/forge/branch"
	pathBranchProtected  = "/forge/branchProtected"
	pathForgeProbe       = "/forge/probe"
	pathLandingRef       = "/forge/landingRef"
	pathIntegrationTip   = "/forge/integrationTip"
	pathLandingContained = "/forge/landingContained"
	pathCommitSubjects   = "/forge/commitSubjects"
	pathMergeable        = "/forge/mergeable"
	pathNeedsUpdate      = "/forge/needsUpdate"
	pathDraft            = "/forge/draft"
	pathFiles            = "/forge/files"
	pathHeadSHA          = "/forge/headSHA"
	pathMergeCommit      = "/forge/mergeCommit"
	pathAutoMerge        = "/forge/autoMerge"
	pathRunURL           = "/forge/runURL"
)

// The write paths, POSTed to; a write has its own Route, so GET and POST on one
// path are distinct.
const (
	pathMerge  = "/forge/merge"
	pathRebase = "/forge/rebase"
)

// fakeRoutes is the fixture Route table: one exact method and path per Route (a
// path is a subtree root, ADR 0047), so a misspelt path is refused.
var fakeRoutes = func() []connectorhost.Route {
	var rs []connectorhost.Route
	for _, p := range []string{
		pathListIssues, pathIssue, pathComments, pathNativeDeps, pathLabels,
		pathBlocks, pathAllIssues, pathTrackerProbe, pathPriorClaim,
		pathPRsForBranch, pathPR, pathChecks, pathBranch, pathBranchProtected,
		pathForgeProbe, pathLandingRef, pathIntegrationTip, pathLandingContained,
		pathCommitSubjects, pathMergeable, pathNeedsUpdate, pathDraft, pathFiles,
		pathHeadSHA, pathMergeCommit, pathAutoMerge, pathRunURL,
	} {
		rs = append(rs, connectorhost.Route{Method: http.MethodGet, Host: fakeHost, Path: p})
	}
	for _, p := range []string{pathMerge, pathRebase} {
		rs = append(rs, connectorhost.Route{Method: http.MethodPost, Host: fakeHost, Path: p})
	}
	return rs
}()

// popErr is the scripted-error rule shared by the Fake's reads: a non-empty
// queue is consumed one entry per call and wins over fallback, and a nil entry
// means "no error this call".
func popErr(q *[]error, fallback error) error {
	if len(*q) == 0 {
		return fallback
	}
	err := (*q)[0]
	*q = (*q)[1:]
	return err
}

// probe is the Probe read shared by Fake, IssueTrackerFake and CodeForgeFake.
func (c *core) probe(path string) (string, error) {
	var repo string
	err := c.read(path, &repo, func() (any, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ProbeErr != nil {
			return nil, c.ProbeErr
		}
		return c.ProbeRepo, nil
	})
	return repo, err
}

// pendingRead is one call in flight: the closure the backend runs against the
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
	val, err := runServe(p.serve)
	var body []byte
	if err == nil {
		body, err = json.Marshal(val)
	}
	if err != nil {
		p.err = err
		http.Error(w, "scripted error", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
})

// runServe turns a panic in serve into the call's error, so a bug in a Fake's
// scripted state fails that read instead of killing the shared Connector.
func runServe(serve func() (any, error)) (val any, err error) {
	defer func() {
		if r := recover(); r != nil {
			val, err = nil, fmt.Errorf("fake backend panic: %v", r)
		}
	}()
	return serve()
}

// inProcess serves the backend without a socket.
type inProcess struct{}

func (inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &bufferedResponse{header: http.Header{}, code: http.StatusOK}
	fakeBackend.ServeHTTP(w, r)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", w.code, http.StatusText(w.code)),
		StatusCode:    w.code,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.header,
		Body:          io.NopCloser(&w.body),
		ContentLength: int64(w.body.Len()),
		Request:       r,
	}, nil
}

// bufferedResponse is the minimal http.ResponseWriter inProcess needs.
type bufferedResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *bufferedResponse) Header() http.Header         { return w.header }
func (w *bufferedResponse) WriteHeader(code int)        { w.code = code }
func (w *bufferedResponse) Write(b []byte) (int, error) { return w.body.Write(b) }

// auditSink attaches each audit line to the pending read its query names.
type auditSink struct{}

func (auditSink) Write(line []byte) (int, error) {
	var e connectorhost.AuditEntry
	if err := json.Unmarshal(bytes.TrimSpace(line), &e); err != nil {
		return 0, err
	}
	q, err := url.ParseQuery(e.Query)
	if err != nil {
		return 0, err
	}
	if id, err := strconv.ParseUint(q.Get("call"), 10, 64); err == nil {
		if v, ok := pending.Load(id); ok {
			p := v.(*pendingRead)
			p.audit, p.audited = e, true
		}
	}
	return len(line), nil
}

var (
	seamMu   sync.Mutex
	seamConn *connectorhost.Connector
)

// seam is the one Connector shared by every Fake in the test binary. It panics
// when the Connector cannot load, because no core test may skip the seam (ADR
// 0067). A failed call (a route refusal, a blown budget) closes a Connector for
// good, so read retires it and the next read loads a fresh one: one bad read
// must not fail every later test in the binary.
func seam() *connectorhost.Connector {
	seamMu.Lock()
	defer seamMu.Unlock()
	if seamConn == nil {
		c, err := loadFakeConnector()
		if err != nil {
			panic(fmt.Sprintf("forge: fake Connector: %v", err))
		}
		seamConn = c
	}
	return seamConn
}

// retireSeam drops c unless another read already replaced it.
func retireSeam(c *connectorhost.Connector) {
	seamMu.Lock()
	defer seamMu.Unlock()
	if seamConn == c {
		seamConn = nil
	}
}

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

// read sends one GET for path through the Connector; see call.
func (c *core) read(path string, out any, serve func() (any, error)) error {
	return c.call(http.MethodGet, path, out, serve)
}

// call sends one method request for path through the Connector, runs serve in
// the backend, and decodes the answer into out (nil for a write with no result).
// serve takes the Fake's lock itself, so the caller must not hold it. The call
// fails unless the host audited it as sent on a declared Route.
func (c *core) call(method, path string, out any, serve func() (any, error)) error {
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
		"method": method,
	}
	conn := seam()
	if err := conn.Call(context.Background(), "fetch", params, &got); err != nil {
		if conn.Err() != nil || errors.Is(err, connectorhost.ErrUnusable) {
			retireSeam(conn)
		}
		return fmt.Errorf("fake connector %s %s: %w", method, path, err)
	}
	if !p.audited || p.audit.Decision != connectorhost.DecisionSent || p.audit.Route == "" {
		return fmt.Errorf("fake connector %s %s: not sent on a declared route: %+v", method, path, p.audit)
	}
	if o, ok := observers.Load(c); ok {
		o.(func(connectorhost.AuditEntry))(p.audit)
	}
	if p.err != nil {
		return p.err
	}
	if got.Status != http.StatusOK {
		return fmt.Errorf("fake connector %s %s: status %d: %s", method, path, got.Status, strings.TrimSpace(got.Body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(got.Body), out)
}
