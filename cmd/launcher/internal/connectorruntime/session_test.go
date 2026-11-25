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
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
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
	rt     *connectorruntime.Runtime
	sb     *sandbox
	row    backendrows.Row
	spec   connectorruntime.TrackerSpec
	now    time.Time
	err503 *atomic.Int32 // backend requests still to answer 503
	// auth is the Authorization header of the backend's latest request.
	auth atomic.Value
}

// newEnv is a runtime over a fixture Connector served by an httptest backend
// with one issue, 1, titled "first", and one branch, main. It runs in a fresh
// working directory, where the audit file and the .log land.
func newEnv(t *testing.T, wasmFile string) *env {
	t.Helper()
	return newEnvWith(t, wasmFile, map[string]credsource.Row{"fixture": {Secret: credsource.Static("tok")}})
}

// newEnvWith is newEnv over the given credential sources.
func newEnvWith(t *testing.T, wasmFile string, creds map[string]credsource.Row) *env {
	t.Helper()
	dir, err := fixturebuild.ConnectorDir() // resolves the flake from the working directory, so before the Chdir
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	err503 := new(atomic.Int32)
	e := &env{err503: err503}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.auth.Store(r.Header.Get("Authorization"))
		if err503.Add(-1) >= 0 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case apiPath + "/repos/" + connectorforgetest.Repo + "/issues/1":
			_, _ = w.Write([]byte(`{"number":1,"title":"first","body":"","state":"open","labels":[]}`))
		case apiPath + "/repos/" + connectorforgetest.Repo + "/branches/main":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	e.sb = &sandbox{Loader: connectorsandboxtest.InProcess()}
	e.row = backendrows.Row{
		Name:          "fixture",
		Module:        filepath.Join(dir, wasmFile),
		Routes:        []connectorhost.Route{{Method: http.MethodGet, Host: srv.Listener.Addr().String(), Path: apiPath + "/repos/" + connectorforgetest.Repo}},
		CredentialEnv: "FIXTURE_TOKEN",
	}
	e.now = time.Unix(1000, 0)
	e.spec = connectorruntime.TrackerSpec{
		SessionSpec: connectorruntime.SessionSpec{
			Target:  connectortracker.Target{API: srv.URL + apiPath, Repo: connectorforgetest.Repo},
			Options: backendrows.Options{Client: srv.Client()},
		},
		Clock: retry.NoSleepClock(),
	}
	e.rt = connectorruntime.New([]backendrows.Row{e.row}, creds, e.sb)
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

// forgeSpec is the Code Forge's spec over the same session as e.spec.
func (e *env) forgeSpec() connectorruntime.ForgeSpec {
	return connectorruntime.ForgeSpec{
		SessionSpec: e.spec.SessionSpec,
		Plumbing:    func(string, gitexec.Credential) forge.CodeForge { return nil },
		WebBase:     "https://forge.example",
		Clock:       retry.NoSleepClock(),
	}
}

func readFirst(t *testing.T, tr forge.IssueTracker) error {
	t.Helper()
	iss, err := tr.Issue("1")
	if err == nil && iss.Title != "first" {
		t.Errorf("Issue(1).Title = %q, want %q", iss.Title, "first")
	}
	return err
}

func readMain(t *testing.T, cf forge.CodeForge) error {
	t.Helper()
	ok, err := cf.BranchExists("main")
	if err == nil && !ok {
		t.Error("BranchExists(main) = false, want true")
	}
	return err
}

// axis is one way into a session: the fixture serving it, the read that
// exercises it, and the handle the runtime hands back. The lifecycle suite runs
// once per axis.
type axis struct {
	name string
	wasm string
	// op is the failure-note op of read.
	op string
	// requires are capabilities wasm lacks; okWasm advertises okRequires.
	requires   []string
	okWasm     string
	okRequires []string
	// otherWasm serves only the other axis, which open refuses with errNot and
	// notes under otherOp.
	otherWasm string
	errNot    error
	otherOp   string
	// dropsOther is whether open closes the Connector it refused: a tracker's
	// build can fail on a guest call, a Code Forge's compose never does.
	dropsOther bool
	// open is the runtime's handle for e.spec's session; read performs one
	// operation through it.
	open func(t *testing.T, e *env) (read func() error, err error)
}

var axes = []axis{
	{
		name:       "tracker",
		wasm:       "tracker.wasm",
		op:         "tracker.issue",
		requires:   []string{connectortracker.CapTransition, connectortracker.CapEditLabels},
		okWasm:     "tracker-writes.wasm",
		okRequires: []string{connectortracker.CapTransition, connectortracker.CapEditLabels},
		otherWasm:  "forge.wasm",
		errNot:     connectortracker.ErrNotTracker,
		otherOp:    "compose the tracker",
		dropsOther: true,
		open: func(t *testing.T, e *env) (func() error, error) {
			tr, err := e.rt.Tracker(e.row, e.spec)
			if err != nil {
				return nil, err
			}
			return func() error { return readFirst(t, tr) }, nil
		},
	},
	{
		name:       "code forge",
		wasm:       "forge.wasm",
		op:         "forge.branchExists",
		requires:   []string{connectorforge.CapPRs, "forge.draftPRs"},
		okWasm:     "forge.wasm",
		okRequires: []string{connectorforge.CapPushOnly, connectorforge.CapBranches},
		otherWasm:  "tracker.wasm",
		errNot:     connectorforge.ErrNotForge,
		otherOp:    "compose the Code Forge",
		open: func(t *testing.T, e *env) (func() error, error) {
			cf, err := e.rt.CodeForge(e.row, e.forgeSpec())
			if err != nil {
				return nil, err
			}
			return func() error { return readMain(t, cf) }, nil
		},
	},
}

func eachAxis(t *testing.T, f func(t *testing.T, a axis)) {
	t.Helper()
	for _, a := range axes {
		t.Run(a.name, func(t *testing.T) { f(t, a) })
	}
}

func (a axis) mustOpen(t *testing.T, e *env) func() error {
	t.Helper()
	read, err := a.open(t, e)
	if err != nil {
		t.Fatalf("open %s: %v", a.name, err)
	}
	return read
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

func TestHandlePerformsAnOperationThroughTheConnector(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		if err := a.mustOpen(t, e)(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHandlesForOneTargetShareOneLiveConnector(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		for range 3 {
			if err := a.mustOpen(t, e)(); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(e.sb.loads()); n != 1 {
			t.Errorf("loads = %d, want the one Connector reused", n)
		}
		e.spec.Target.Repo = "o/other"
		a.mustOpen(t, e)
		if n := len(e.sb.loads()); n != 2 {
			t.Errorf("loads = %d, want a second Connector for another Target", n)
		}
	})
}

// One row serving both axes keeps one Connector, however each axis opens.
func TestTrackerAndCodeForgeOfOneRowShareOneConnector(t *testing.T) {
	e := newEnv(t, "forge-tracker.wasm")
	tr := e.tracker(t)
	cf, err := e.rt.CodeForge(e.row, e.forgeSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := readFirst(t, tr); err != nil {
		t.Fatal(err)
	}
	if err := readMain(t, cf); err != nil {
		t.Fatal(err)
	}
	if n := len(e.sb.loads()); n != 1 {
		t.Fatalf("loads = %d, want one Connector for both axes", n)
	}

	e.sb.killGuest()
	if err := readMain(t, cf); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("tracker after the Code Forge's call saw the guest die: %v, want the reloaded Connector", err)
	}
	if n := len(e.sb.loads()); n != 2 {
		t.Errorf("loads = %d, want one reload for both axes", n)
	}
}

func TestDeadConnectorIsReloadedBeforeTheNextCall(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		read := a.mustOpen(t, e)
		if err := read(); err != nil {
			t.Fatal(err)
		}
		e.sb.killGuest()
		if err := read(); err == nil {
			t.Fatal("the call on a killed guest succeeded, want it to fail")
		}
		if err := read(); err != nil {
			t.Fatalf("call after the guest died: %v, want a reloaded Connector", err)
		}
		if n := len(e.sb.loads()); n != 2 {
			t.Errorf("loads = %d, want one reload", n)
		}
	})
}

func TestFailedReloadFailsTheCallAndTheNextRetries(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		_ = read()

		boom := errors.New("sandbox gone")
		e.sb.failLoads(boom)
		if err := read(); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the load error", err)
		}
		e.sb.failLoads(nil)
		if err := read(); err != nil {
			t.Fatalf("the call after a failed reload: %v, want it to reload and succeed", err)
		}
	})
}

func TestRepeatedReloadFailuresStartACooldown(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		_ = read()
		boom := errors.New("compile module")
		e.sb.failLoads(boom)

		for i := range 3 {
			if err := read(); !errors.Is(err, boom) {
				t.Fatalf("call %d: err = %v, want the load error", i, err)
			}
		}
		e.sb.failLoads(nil)
		err := read()
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "3 times in a row") {
			t.Fatalf("in cooldown: err = %v, want the wrapped load error without a load", err)
		}
		if n := len(e.sb.loads()); n != 1 {
			t.Fatalf("loads = %d, want none during the cooldown", n)
		}
		e.now = e.now.Add(time.Minute)
		if err := read(); err != nil {
			t.Fatalf("after the cooldown: %v, want a reload", err)
		}
	})
}

func TestFirstLoadFailuresCountTowardTheCooldown(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		boom := errors.New("compile module")
		e.sb.failLoads(boom)
		for i := range 3 {
			if _, err := a.open(t, e); !errors.Is(err, boom) {
				t.Fatalf("open %d: err = %v, want the load error", i, err)
			}
		}
		e.sb.failLoads(nil)
		_, err := a.open(t, e)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "3 times in a row") {
			t.Fatalf("in cooldown: err = %v, want the wrapped load error", err)
		}
		if n := len(e.sb.loads()); n != 0 {
			t.Fatalf("loads = %d, want none during the cooldown", n)
		}
		e.now = e.now.Add(time.Minute)
		if err := a.mustOpen(t, e)(); err != nil {
			t.Fatalf("after the cooldown: %v", err)
		}
	})
}

func TestFailedCallAndFailedReloadAreWrittenToTheFailureNote(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		e.spec.Record = "fixture"
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		if err := read(); err == nil {
			t.Fatal("want the call on a killed guest to fail")
		}
		e.sb.failLoads(errors.New("sandbox gone"))
		if err := read(); err == nil {
			t.Fatal("want the failed reload to fail the call")
		}
		log := connectorLog(t)
		if n := strings.Count(log, `"op":"`+a.op+`"`); n != 2 {
			t.Errorf("log has %d %s failures, want the failed call and the failed reload:\n%s", n, a.op, log)
		}
		if !strings.Contains(log, "sandbox gone") {
			t.Errorf("log = %q, want the reload error", log)
		}
	})
}

func TestFirstLoadFailureIsWrittenToTheFailureNote(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		e.spec.Record = "fixture"
		boom := errors.New("sandbox gone")
		e.sb.failLoads(boom)
		if _, err := a.open(t, e); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the load error as is", err)
		}
		if log := connectorLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "sandbox gone") {
			t.Errorf("log = %q, want the op and the load error", log)
		}
	})
}

func TestUnopenableAuditFileFailsTheLoad(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		if err := os.WriteFile(".spindrift", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := a.open(t, e); err == nil || !strings.Contains(err.Error(), "audit file") {
			t.Fatalf("err = %v, want the audit file open failure", err)
		}
		if n := len(e.sb.loads()); n != 0 {
			t.Errorf("loads = %d, want the Connector never to run unrecorded", n)
		}
	})
}

func TestConnectorMissingARequiredCapabilityIsRefusedAtLoad(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		e.spec.Record = "fixture"
		e.spec.Requires = a.requires
		_, err := a.open(t, e)
		if !errors.Is(err, connectorruntime.ErrMissingCapabilities) {
			t.Fatalf("err = %v, want ErrMissingCapabilities", err)
		}
		want := "fixture Connector lacks required capabilities: " + strings.Join(a.requires, ", ")
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
		loaded := e.sb.loads()
		if len(loaded) != 1 {
			t.Fatalf("loads = %d, want the refused Connector loaded once", len(loaded))
		}
		if err := loaded[0].Call(context.Background(), a.op, nil, nil); !errors.Is(err, connectorhost.ErrUnusable) {
			t.Errorf("refused Connector's call = %v, want it closed (ErrUnusable)", err)
		}
	})
}

func TestConnectorAdvertisingEveryRequiredCapabilityLoads(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.okWasm)
		e.spec.Requires = a.okRequires
		if err := a.mustOpen(t, e)(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCloseClosesLiveConnectors(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		read := a.mustOpen(t, e)
		if err := read(); err != nil {
			t.Fatal(err)
		}
		if err := e.rt.Close(); err != nil {
			t.Fatal(err)
		}
		if err := read(); !errors.Is(err, connectorhost.ErrUnusable) {
			t.Errorf("call after Close = %v, want ErrUnusable", err)
		}
	})
}

func TestConnectorServingOnlyTheOtherAxisIsWrittenToTheFailureNote(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.otherWasm)
		e.spec.Record = "fixture"
		if _, err := a.open(t, e); !errors.Is(err, a.errNot) {
			t.Fatalf("err = %v, want %v", err, a.errNot)
		}
		if log := connectorLog(t); !strings.Contains(log, `"op":"`+a.otherOp+`"`) {
			t.Errorf("log = %q, want the load op", log)
		}
		loaded := e.sb.loads()
		if len(loaded) != 1 {
			t.Fatalf("loads = %d, want one", len(loaded))
		}
		err := loaded[0].Call(context.Background(), a.op, nil, nil)
		if dropped := errors.Is(err, connectorhost.ErrUnusable); dropped != a.dropsOther {
			t.Errorf("refused Connector's call = %v, want dropped = %v", err, a.dropsOther)
		}
	})
}

func TestFailedRetryRestartsTheCooldownAndASuccessResetsTheCount(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		_ = read()
		boom := errors.New("compile module")
		e.sb.failLoads(boom)
		for range 3 {
			_ = read()
		}

		e.now = e.now.Add(time.Minute)
		e.sb.failLoads(boom)
		if err := read(); !errors.Is(err, boom) {
			t.Fatalf("after the cooldown: err = %v, want the load error from one more attempt", err)
		}
		e.sb.failLoads(nil)
		err := read()
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "4 times in a row") {
			t.Fatalf("after a failed retry: err = %v, want the cooldown restarted", err)
		}
		if n := len(e.sb.loads()); n != 1 {
			t.Fatalf("loads = %d, want none during the restarted cooldown", n)
		}

		e.now = e.now.Add(time.Minute)
		if err := read(); err != nil {
			t.Fatalf("recovery: %v", err)
		}
		e.sb.killGuest()
		_ = read()
		e.sb.failLoads(boom)
		for i := range 3 {
			if err := read(); !errors.Is(err, boom) || strings.Contains(err.Error(), "times in a row") {
				t.Fatalf("call %d after recovery: err = %v, want a load attempt, not the cooldown", i, err)
			}
		}
	})
}

// The retry wraps outside the session, so the failed first attempt reaches the
// failure note even though a later attempt recovers.
func TestRetriedReadNotesTheFailedAttempt(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	e.spec.Record = "fixture"
	tr := e.tracker(t)
	e.err503.Store(1)
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("read over one 503: %v, want the retry to succeed", err)
	}
	if log := connectorLog(t); strings.Count(log, `"op":"tracker.issue"`) != 1 {
		t.Errorf("log = %q, want the failed first attempt noted once", log)
	}
}

func TestOpenAfterCloseFailsAndLoadsNothing(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		if err := e.rt.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.open(t, e); !errors.Is(err, connectorruntime.ErrClosed) {
			t.Fatalf("open after Close = %v, want ErrClosed", err)
		}
		if n := len(e.sb.loads()); n != 0 {
			t.Errorf("loads = %d, want none after Close", n)
		}
		if err := e.rt.Close(); err != nil {
			t.Errorf("second Close = %v, want nil", err)
		}
	})
}

// A Code Forge refused for the host's own configuration leaves the Connector
// the tracker of the same row is using alone.
func TestCodeForgeComposeFailureLeavesTheTrackersConnector(t *testing.T) {
	e := newEnv(t, "forge-tracker.wasm")
	tr := e.tracker(t)
	spec := e.forgeSpec()
	spec.Plumbing = nil
	if _, err := e.rt.CodeForge(e.row, spec); err == nil {
		t.Fatal("CodeForge without plumbing succeeded, want a compose failure")
	}
	if err := readFirst(t, tr); err != nil {
		t.Fatalf("tracker after the Code Forge's compose failure: %v", err)
	}
	if n := len(e.sb.loads()); n != 1 {
		t.Errorf("loads = %d, want the one Connector kept", n)
	}
}

// The read-only forge view shares the session with a direct Connector request,
// and a dead guest is reloaded behind it.
func TestForgeReaderSharesTheSessionAndSurvivesAReload(t *testing.T) {
	e := newEnv(t, connectorforgetest.PRWasm)
	rd, err := e.rt.ForgeReader(e.row, e.spec.SessionSpec, "forge.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := readMain(t, rd); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rt.Connector(e.row, e.spec.SessionSpec); err != nil {
		t.Fatal(err)
	}
	if n := len(e.sb.loads()); n != 1 {
		t.Fatalf("loads = %d, want one Connector for reader and Connector()", n)
	}

	e.sb.killGuest()
	if err := readMain(t, rd); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}
	if err := readMain(t, rd); err != nil {
		t.Fatalf("read after the guest died: %v, want a reloaded Connector", err)
	}
	if n := len(e.sb.loads()); n != 2 {
		t.Errorf("loads = %d, want one reload", n)
	}
}

func TestForgeReaderOnAConnectorWithoutAForgeIsRefused(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	if _, err := e.rt.ForgeReader(e.row, e.spec.SessionSpec, "forge.example"); !errors.Is(err, connectorforge.ErrNotForge) {
		t.Fatalf("err = %v, want ErrNotForge", err)
	}
}

func TestConnectorIsTheSessionsLiveOneAndReloadsWhenDead(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	first, err := e.rt.Connector(e.row, e.spec.SessionSpec)
	if err != nil {
		t.Fatal(err)
	}
	if first.Err() != nil || len(first.Capabilities) == 0 {
		t.Fatalf("Connector err = %v, capabilities = %v, want a live handshake", first.Err(), first.Capabilities)
	}
	if again, err := e.rt.Connector(e.row, e.spec.SessionSpec); err != nil || again != first {
		t.Fatalf("second Connector = %p, %v, want the same live one %p", again, err, first)
	}
	e.sb.killGuest()
	// The host learns the guest died from a failed call, not from the abort.
	if err := first.Call(context.Background(), "tracker.issue", nil, nil); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}
	next, err := e.rt.Connector(e.row, e.spec.SessionSpec)
	if err != nil {
		t.Fatal(err)
	}
	if next == first || next.Err() != nil {
		t.Errorf("after the guest died Connector = %p (err %v), want a fresh live one, not %p", next, next.Err(), first)
	}
	if n := len(e.sb.loads()); n != 2 {
		t.Errorf("loads = %d, want one reload", n)
	}
}

func TestUnopenableAuditFileFailsConnector(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	if err := os.WriteFile(".spindrift", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rt.Connector(e.row, e.spec.SessionSpec); err == nil || !strings.Contains(err.Error(), "audit file") {
		t.Fatalf("err = %v, want the audit file open failure", err)
	}
	if n := len(e.sb.loads()); n != 0 {
		t.Errorf("loads = %d, want the Connector never to run unrecorded", n)
	}
}

func TestReplacingADeadConnectorNotesTheReloadOnce(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		e.spec.Record = "fixture"
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		if err := read(); err == nil {
			t.Fatal("want the call on a killed guest to fail")
		}
		e.sb.failLoads(errors.New("sandbox gone"))
		for range 2 {
			if err := read(); err == nil {
				t.Fatal("want the failed reload to fail the call")
			}
		}
		if n := strings.Count(connectorLog(t), `"op":"reload the Connector"`); n != 1 {
			t.Errorf("log has %d reload notes, want one for the one dead Connector", n)
		}
	})
}

func TestBackendThatNotesItsOwnOperationsKeepsOnlyTheReloadNote(t *testing.T) {
	eachAxis(t, func(t *testing.T, a axis) {
		e := newEnv(t, a.wasm)
		e.spec.Record = "fixture"
		e.spec.NotesOperations = true
		read := a.mustOpen(t, e)
		e.sb.killGuest()
		if err := read(); err == nil {
			t.Fatal("want the call on a killed guest to fail")
		}
		e.sb.failLoads(errors.New("sandbox gone"))
		if err := read(); err == nil {
			t.Fatal("want the failed reload to fail the call")
		}
		log := connectorLog(t)
		if !strings.Contains(log, `"op":"reload the Connector"`) {
			t.Errorf("log = %q, want the reload noted", log)
		}
		if n := strings.Count(log, `"op"`); n != 1 {
			t.Errorf("log has %d notes, want only the reload:\n%s", n, log)
		}
	})
}
