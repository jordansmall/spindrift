package connectorsetup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/rowcredential"
	"spindrift.dev/launcher/internal/runner"
)

const (
	rowName = "setuptest"
	credEnv = "CONNECTORSETUP_TEST_TOKEN"
)

// useRow registers a Connector-routed backend and returns its decoded row,
// restoring the registry when the test ends.
func useRow(t *testing.T, totals func(*connectorruntime.Runtime, connectorsandbox.Stats)) backendrows.Set {
	t.Helper()
	t.Cleanup(backendkit.ReplaceRegistryForTest([]backendkit.Strategy{{
		Descriptor:          backend.Descriptor{Name: rowName},
		ConnectorRouted:     true,
		RecordSandboxTotals: totals,
	}}))
	doc := `[{"name":"` + rowName + `","module":"/nonexistent.wasm","credentialEnv":"` + credEnv + `",
"routes":[{"method":"GET","host":"` + rowName + `.example","path":"/x"}]}]`
	rows, err := backendrows.Decode([]byte(doc), func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func input(rows backendrows.Set, m map[string]string) connectorsetup.Input {
	return connectorsetup.Input{Rows: rows, Forge: rowName, Setting: func(env string) string { return m[env] }}
}

// recordingLoader is a Connector sandbox that logs its Close into events.
type recordingLoader struct {
	connectorsandbox.Loader
	events *[]string
}

func newRecordingLoader(events *[]string) *recordingLoader {
	return &recordingLoader{Loader: servedLoader{connectorsandboxtest.InProcess()}, events: events}
}

func (r *recordingLoader) Close(context.Context) error {
	*r.events = append(*r.events, "close")
	return nil
}

func TestUnsetCredentialIsOneRuleWithAPerProgramOption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		require bool
	}{{"required", true}, {"tolerated", false}} {
		t.Run(tc.name, func(t *testing.T) {
			rows := useRow(t, nil)
			rt, err := connectorsetup.New(input(rows, nil), connectorsetup.Options{
				RequireCredential: tc.require, Sandbox: connectorsandboxtest.InProcess(),
			})
			if tc.require {
				if !errors.Is(err, rowcredential.ErrUnset) {
					t.Fatalf("err = %v, want ErrUnset", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			row, _ := rt.Row(rowName)
			if rt.CredentialSet(row) {
				t.Error("an unset credential reads as set")
			}
		})
	}
}

func TestCommandErrorReturnsNoRuntime(t *testing.T) {
	rows := useRow(t, nil)
	var events []string
	_, err := connectorsetup.New(input(rows, map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox: newRecordingLoader(&events),
		Run:     func(string) (string, error) { return "", errors.New("vault is down") },
	})
	if err == nil || errors.Is(err, rowcredential.ErrUnset) {
		t.Fatalf("err = %v, want the command failure", err)
	}
}

func TestCloseStopsTheSandboxBeforeRecordingTotals(t *testing.T) {
	var events []string
	rows := useRow(t, func(*connectorruntime.Runtime, connectorsandbox.Stats) { events = append(events, "totals") })
	l := newRecordingLoader(&events)
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv: "tok"}), connectorsetup.Options{Sandbox: l})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Sandbox() != connectorsandbox.Loader(l) {
		t.Fatal("the runtime's sandbox is not the one given")
	}
	row, _ := rt.Row(rowName)
	if !rt.CredentialSet(row) {
		t.Error("the row's credential is unset")
	}
	for range 2 {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"close", "totals"}; !slices.Equal(events, want) {
		t.Errorf("close events = %v, want %v once", events, want)
	}
}

func TestRefreshReRunsCommandSourcesUntilClose(t *testing.T) {
	rows := useRow(t, nil)
	var runs atomic.Int32
	start := time.Now()
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox:        connectorsandboxtest.InProcess(),
		RefreshEvery:   5 * time.Millisecond,
		OnRefreshError: func(string, error) {},
		Run:            func(string) (string, error) { runs.Add(1); return "tok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the command ran %d times, want it re-run on a timer", runs.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	n, ticking := runs.Load(), time.Since(start)
	// Wait out many more ticks than the ones that showed the timer firing, so a
	// loaded runner that stretched those stretches this window too.
	time.Sleep(max(50*time.Millisecond, 5*ticking))
	// One refresh may already be past its stop check when Close runs.
	if got := runs.Load(); got-n > 1 {
		t.Errorf("the command ran %d more times after Close", got-n)
	}
}

// A `_CMD` hung at shutdown (an `op read` awaiting approval) must not hold
// Close, and with it the sandbox close and the totals behind it.
func TestCloseDoesNotWaitForARefreshInFlight(t *testing.T) {
	var events []string
	rows := useRow(t, func(*connectorruntime.Runtime, connectorsandbox.Stats) { events = append(events, "totals") })
	release, started := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox:        newRecordingLoader(&events),
		RefreshEvery:   5 * time.Millisecond,
		OnRefreshError: func(string, error) {},
		Run: func(string) (string, error) {
			if once.Swap(true) {
				close(started)
				<-release
			}
			return "tok", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh ran")
	}
	closed := make(chan struct{})
	go func() { _ = rt.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("Close waited for the hung refresh")
	}
	close(release)
	if want := []string{"close", "totals"}; !slices.Equal(events, want) {
		t.Errorf("close events = %v, want %v", events, want)
	}
}

const countingKind = "counting-setup-test"

var countingStarts atomic.Int32

func init() {
	runner.Register(runner.Strategy{
		Name: countingKind,
		New: func(runner.Config, inputdoc.Artifacts, runner.KnobLookup, string) (runner.Runner, error) {
			countingStarts.Add(1)
			return runner.NewFake(), nil
		},
	})
}

func TestStartsNoSandboxWhenNothingLoads(t *testing.T) {
	rows := useRow(t, nil)
	in := input(rows, map[string]string{credEnv: "tok"})
	in.RunnerKind = countingKind
	rt, err := connectorsetup.New(in, connectorsetup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if got := countingStarts.Load(); got != 0 {
		t.Errorf("runner built %d times for a process that loaded no Connector", got)
	}
}

func TestRefreshWithoutAnErrorHandlerIsRefused(t *testing.T) {
	var events []string
	rows := useRow(t, nil)
	_, err := connectorsetup.New(input(rows, map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox:      newRecordingLoader(&events),
		RefreshEvery: time.Second,
		Run:          func(string) (string, error) { return "tok", nil },
	})
	if err == nil {
		t.Fatal("a refresh with nowhere to report a failure was accepted")
	}
}

func TestCloseReturnsTheSharedSandboxCloseError(t *testing.T) {
	var totals []string
	rows := useRow(t, func(*connectorruntime.Runtime, connectorsandbox.Stats) { totals = append(totals, "totals") })
	boom := errors.New("sandbox close failed")
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv: "tok"}), connectorsetup.Options{
		Sandbox: failingCloseLoader{servedLoader{connectorsandboxtest.InProcess()}, boom},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); !errors.Is(err, boom) {
		t.Errorf("Close = %v, want the sandbox's close error", err)
	}
	if len(totals) != 1 {
		t.Errorf("totals recorded %d times, want once despite the error", len(totals))
	}
}

type failingCloseLoader struct {
	connectorsandbox.Loader
	err error
}

func (f failingCloseLoader) Close(context.Context) error { return f.err }

// servedLoader is a sandbox that reports it served the run.
type servedLoader struct{ connectorsandbox.Loader }

func (servedLoader) Stats() (connectorsandbox.Stats, bool) {
	return connectorsandbox.Stats{Start: time.Millisecond}, true
}

// A sandbox that never served has no cost to record.
func TestCloseRecordsNoTotalsWhenTheSandboxNeverServed(t *testing.T) {
	var totals int
	rows := useRow(t, func(*connectorruntime.Runtime, connectorsandbox.Stats) { totals++ })
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv: "tok"}), connectorsetup.Options{
		Sandbox: unservedLoader{connectorsandboxtest.InProcess()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if totals != 0 {
		t.Errorf("totals recorded %d times, want none", totals)
	}
}

type unservedLoader struct{ connectorsandbox.Loader }

func (unservedLoader) Stats() (connectorsandbox.Stats, bool) { return connectorsandbox.Stats{}, false }

// loadRecorder is a sandbox that notes each wasm it is asked to load and
// refuses it.
type loadRecorder struct {
	connectorsandbox.Loader
	loaded [][]byte
}

func (l *loadRecorder) Load(_ context.Context, wasm []byte) (*connectorhost.Connector, error) {
	l.loaded = append(l.loaded, wasm)
	return nil, errors.New("recorded, not loaded")
}

func TestDriverConnectorLoadsIntoTheRuntimesSandbox(t *testing.T) {
	rows := useRow(t, nil)
	module := filepath.Join(t.TempDir(), "drv.wasm")
	if err := os.WriteFile(module, []byte("wasm-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverRows, err := driverrows.Parse([]byte(`[{"name":"setupdrv","module":`+strconv.Quote(module)+`}]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	l := &loadRecorder{Loader: connectorsandboxtest.InProcess()}
	in := input(rows, map[string]string{credEnv: "tok"})
	in.Drivers = driverRows
	rt, err := connectorsetup.New(in, connectorsetup.Options{Sandbox: l})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Drivers().New("setupdrv"); err == nil || !strings.Contains(err.Error(), "recorded, not loaded") {
		t.Fatalf("New = %v, want the sandbox's refusal", err)
	}
	if len(l.loaded) != 1 || string(l.loaded[0]) != "wasm-bytes" {
		t.Errorf("sandbox loads = %q, want the Driver's module once", l.loaded)
	}
}

func TestRuntimeWithoutDriverRowsOffersNoDrivers(t *testing.T) {
	rows := useRow(t, nil)
	rt, err := connectorsetup.New(input(rows, map[string]string{credEnv: "tok"}), connectorsetup.Options{Sandbox: connectorsandboxtest.InProcess()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Drivers().HasNative("claude") {
		t.Error("a runtime given no rows offers a native Driver")
	}
}
