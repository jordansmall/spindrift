package connectorruntime_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/retry"
)

const apiPath = "/api/v1"

// sandbox is the in-process engine plus what a runtime test needs of it: the
// Connectors it loaded, and a way to make the next loads fail.
type sandbox struct {
	connectorsandbox.Loader
	mu     sync.Mutex
	loaded []*connectorhost.Connector
	fail   error
}

func (s *sandbox) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	c, err := s.Loader.LoadNetwork(ctx, wasm, n)
	if err == nil {
		s.loaded = append(s.loaded, c)
	}
	return c, err
}

func (s *sandbox) failLoads(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

func (s *sandbox) loads() []*connectorhost.Connector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*connectorhost.Connector(nil), s.loaded...)
}

// killGuest ends the latest loaded Connector's module; the next call on it
// fails and marks it dead, as a trap would.
func (s *sandbox) killGuest() { s.loads()[len(s.loads())-1].Abort() }

type env struct {
	rt   *connectorruntime.Runtime
	sb   *sandbox
	row  backendrows.Row
	spec connectorruntime.TrackerSpec
	now  time.Time
}

// newEnv is a runtime over the fixture tracker Connector served by an
// httptest backend with one issue, 1, titled "first". It runs in a fresh
// working directory, where the audit file and the .log land.
func newEnv(t *testing.T, wasmFile string) *env {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir() // resolves the flake from the working directory, so before the Chdir
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPath+"/repos/"+connectorforgetest.Repo+"/issues/1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"number":1,"title":"first","body":"","state":"open","labels":[]}`))
	}))
	t.Cleanup(srv.Close)
	e := &env{
		sb: &sandbox{Loader: connectorsandboxtest.InProcess()},
		row: backendrows.Row{
			Name:          "fixture",
			Module:        filepath.Join(dir, wasmFile),
			Routes:        []connectorhost.Route{{Method: http.MethodGet, Host: srv.Listener.Addr().String(), Path: apiPath + "/repos/" + connectorforgetest.Repo}},
			CredentialEnv: "FIXTURE_TOKEN",
		},
		now: time.Unix(1000, 0),
	}
	e.spec = connectorruntime.TrackerSpec{
		Target:  connectortracker.Target{API: srv.URL + apiPath, Repo: connectorforgetest.Repo},
		Options: backendrows.Options{Client: srv.Client(), Getenv: func(string) string { return "tok" }},
		Clock:   retry.NoSleepClock(),
	}
	e.rt = connectorruntime.New([]backendrows.Row{e.row}, e.sb)
	e.rt.SetClock(func() time.Time { return e.now })
	t.Cleanup(func() { _ = e.rt.Close() })
	return e
}

func (e *env) tracker(t *testing.T) forge.IssueTracker {
	t.Helper()
	tr, err := e.rt.Tracker(e.row, e.spec)
	if err != nil {
		t.Fatalf("Tracker: %v", err)
	}
	return tr
}

func readFirst(t *testing.T, tr forge.IssueTracker) error {
	t.Helper()
	iss, err := tr.Issue("1")
	if err == nil && iss.Title != "first" {
		t.Errorf("Issue(1).Title = %q, want %q", iss.Title, "first")
	}
	return err
}

// connectorLog is the failed-operation record the runtime left under the
// current working directory.
func connectorLog(t *testing.T) string {
	t.Helper()
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(pwd), connectorrecord.Name("fixture", ".log")))
	if err != nil {
		t.Fatalf("no Connector log: %v", err)
	}
	return string(raw)
}

func TestTrackerPerformsAnOperationThroughTheConnector(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	if err := readFirst(t, e.tracker(t)); err != nil {
		t.Fatal(err)
	}
}

func TestTrackersForOneTargetShareOneLiveConnector(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	for range 3 {
		if err := readFirst(t, e.tracker(t)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.sb.loads()); n != 1 {
		t.Errorf("loads = %d, want the one Connector reused", n)
	}
	other := e.spec
	other.Target.Repo = "o/other"
	if _, err := e.rt.Tracker(e.row, other); err != nil {
		t.Fatal(err)
	}
	if n := len(e.sb.loads()); n != 2 {
		t.Errorf("loads = %d, want a second Connector for another Target", n)
	}
}

func TestDeadConnectorIsReloadedBeforeTheNextCall(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	tr := e.tracker(t)
	if err := readFirst(t, tr); err != nil {
		t.Fatal(err)
	}
	e.sb.killGuest()
	if err := readFirst(t, tr); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("call after the guest died: %v, want a reloaded Connector", err)
	}
	if n := len(e.sb.loads()); n != 2 {
		t.Errorf("loads = %d, want one reload", n)
	}
}

func TestFailedReloadFailsTheCallAndTheNextRetries(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	tr := e.tracker(t)
	e.sb.killGuest()
	_ = readFirst(t, tr)

	boom := errors.New("sandbox gone")
	e.sb.failLoads(boom)
	if err := readFirst(t, tr); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the load error", err)
	}
	e.sb.failLoads(nil)
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("the call after a failed reload: %v, want it to reload and succeed", err)
	}
}

func TestRepeatedReloadFailuresStartACooldown(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	tr := e.tracker(t)
	e.sb.killGuest()
	_ = readFirst(t, tr)
	boom := errors.New("compile module")
	e.sb.failLoads(boom)

	for i := range 3 {
		if err := readFirst(t, tr); !errors.Is(err, boom) {
			t.Fatalf("call %d: err = %v, want the load error", i, err)
		}
	}
	e.sb.failLoads(nil)
	err := readFirst(t, tr)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "3 times in a row") {
		t.Fatalf("in cooldown: err = %v, want the wrapped load error without a load", err)
	}
	if n := len(e.sb.loads()); n != 1 {
		t.Fatalf("loads = %d, want none during the cooldown", n)
	}
	e.now = e.now.Add(time.Minute)
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("after the cooldown: %v, want a reload", err)
	}
}

func TestFirstLoadFailuresCountTowardTheCooldown(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	boom := errors.New("compile module")
	e.sb.failLoads(boom)
	for i := range 3 {
		if _, err := e.rt.Tracker(e.row, e.spec); !errors.Is(err, boom) {
			t.Fatalf("Tracker %d: err = %v, want the load error", i, err)
		}
	}
	e.sb.failLoads(nil)
	_, err := e.rt.Tracker(e.row, e.spec)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "3 times in a row") {
		t.Fatalf("in cooldown: err = %v, want the wrapped load error", err)
	}
	if n := len(e.sb.loads()); n != 0 {
		t.Fatalf("loads = %d, want none during the cooldown", n)
	}
	e.now = e.now.Add(time.Minute)
	if err := readFirst(t, e.tracker(t)); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
}

func TestFailedCallAndFailedReloadAreWrittenToTheFailureNote(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	e.spec.Record = "fixture"
	tr := e.tracker(t)
	e.sb.killGuest()
	if err := readFirst(t, tr); err == nil {
		t.Fatal("want the call on a killed guest to fail")
	}
	e.sb.failLoads(errors.New("sandbox gone"))
	if err := readFirst(t, tr); err == nil {
		t.Fatal("want the failed reload to fail the call")
	}
	log := connectorLog(t)
	if n := strings.Count(log, `"op":"tracker.issue"`); n != 2 {
		t.Errorf("log has %d tracker.issue failures, want the failed call and the failed reload:\n%s", n, log)
	}
	if !strings.Contains(log, "sandbox gone") {
		t.Errorf("log = %q, want the reload error", log)
	}
}

func TestFirstLoadFailureIsWrittenToTheFailureNote(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	e.spec.Record = "fixture"
	boom := errors.New("sandbox gone")
	e.sb.failLoads(boom)
	if _, err := e.rt.Tracker(e.row, e.spec); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the load error as is", err)
	}
	if log := connectorLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "sandbox gone") {
		t.Errorf("log = %q, want the op and the load error", log)
	}
}

func TestUnopenableAuditFileFailsTheLoad(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	if err := os.WriteFile(".spindrift", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rt.Tracker(e.row, e.spec); err == nil || !strings.Contains(err.Error(), "audit file") {
		t.Fatalf("err = %v, want the audit file open failure", err)
	}
	if n := len(e.sb.loads()); n != 0 {
		t.Errorf("loads = %d, want the Connector never to run unrecorded", n)
	}
}

func TestConnectorMissingARequiredCapabilityIsRefusedAtLoad(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	e.spec.Record = "fixture"
	e.spec.Requires = []string{connectortracker.CapTransition, connectortracker.CapEditLabels}
	_, err := e.rt.Tracker(e.row, e.spec)
	if !errors.Is(err, connectorruntime.ErrMissingCapabilities) {
		t.Fatalf("err = %v, want ErrMissingCapabilities", err)
	}
	want := "fixture Connector lacks required capabilities: tracker.transition, tracker.editLabels"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
	loaded := e.sb.loads()
	if len(loaded) != 1 {
		t.Fatalf("loads = %d, want the refused Connector loaded once", len(loaded))
	}
	if err := loaded[0].Call(context.Background(), "tracker.issue", nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("refused Connector's call = %v, want it closed (ErrUnusable)", err)
	}
}

func TestConnectorAdvertisingEveryRequiredCapabilityLoads(t *testing.T) {
	e := newEnv(t, "tracker-writes.wasm")
	e.spec.Requires = []string{connectortracker.CapTransition, connectortracker.CapEditLabels}
	if err := readFirst(t, e.tracker(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCloseClosesLiveConnectors(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	tr := e.tracker(t)
	if err := readFirst(t, tr); err != nil {
		t.Fatal(err)
	}
	if err := e.rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := readFirst(t, tr); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("call after Close = %v, want ErrUnusable", err)
	}
}

func TestConnectorThatIsNotATrackerIsWrittenToTheFailureNote(t *testing.T) {
	e := newEnv(t, "forge.wasm")
	e.spec.Record = "fixture"
	if _, err := e.rt.Tracker(e.row, e.spec); !errors.Is(err, connectortracker.ErrNotTracker) {
		t.Fatalf("err = %v, want ErrNotTracker", err)
	}
	if log := connectorLog(t); !strings.Contains(log, `"op":"load the Connector"`) {
		t.Errorf("log = %q, want the load op", log)
	}
}
