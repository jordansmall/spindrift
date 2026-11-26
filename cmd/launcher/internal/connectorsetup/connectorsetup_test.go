package connectorsetup_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/rowcredential"
	"spindrift.dev/launcher/internal/runner"
)

const (
	rowName = "setuptest"
	credEnv = "CONNECTORSETUP_TEST_TOKEN"
)

// useRow registers a Connector-routed backend and its row, restoring the
// registries and the installed sandbox when the test ends.
func useRow(t *testing.T, totals func()) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	t.Cleanup(connectorsandbox.Use(connectorsandbox.Current()))
	t.Cleanup(backendkit.ReplaceRegistryForTest([]backendkit.Strategy{{
		Descriptor:          backend.Descriptor{Name: rowName},
		ConnectorRouted:     true,
		RecordSandboxTotals: totals,
	}}))
	doc := `[{"name":"` + rowName + `","module":"/nonexistent.wasm","credentialEnv":"` + credEnv + `",
"routes":[{"method":"GET","host":"` + rowName + `.example","path":"/x"}]}]`
	if err := backendrows.Use([]byte(doc), func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func input(m map[string]string) connectorsetup.Input {
	return connectorsetup.Input{Forge: rowName, Setting: func(env string) string { return m[env] }}
}

// recordingLoader is a Connector sandbox that logs its Close into events.
type recordingLoader struct {
	connectorsandbox.Loader
	events *[]string
}

func newRecordingLoader(events *[]string) *recordingLoader {
	return &recordingLoader{Loader: connectorsandboxtest.InProcess(), events: events}
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
			useRow(t, nil)
			rt, err := connectorsetup.New(input(nil), connectorsetup.Options{
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

func TestCommandErrorInstallsNoSandbox(t *testing.T) {
	useRow(t, nil)
	var events []string
	installed := newRecordingLoader(&events)
	connectorsandbox.Use(installed)
	_, err := connectorsetup.New(input(map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox: newRecordingLoader(&events),
		Run:     func(string) (string, error) { return "", errors.New("vault is down") },
	})
	if err == nil || errors.Is(err, rowcredential.ErrUnset) {
		t.Fatalf("err = %v, want the command failure", err)
	}
	if connectorsandbox.Current() != connectorsandbox.Loader(installed) {
		t.Error("a failed setup replaced the installed sandbox")
	}
}

func TestInstallsTheSandboxAndCloseStopsItBeforeRecordingTotals(t *testing.T) {
	var events []string
	useRow(t, func() { events = append(events, "totals") })
	l := newRecordingLoader(&events)
	rt, err := connectorsetup.New(input(map[string]string{credEnv: "tok"}), connectorsetup.Options{Sandbox: l})
	if err != nil {
		t.Fatal(err)
	}
	if connectorsandbox.Current() != connectorsandbox.Loader(l) {
		t.Fatal("the sandbox is not the installed one")
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
	useRow(t, nil)
	var runs atomic.Int32
	start := time.Now()
	rt, err := connectorsetup.New(input(map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
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
	useRow(t, func() { events = append(events, "totals") })
	release, started := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	rt, err := connectorsetup.New(input(map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
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
	useRow(t, nil)
	in := input(map[string]string{credEnv: "tok"})
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
	useRow(t, nil)
	installed := newRecordingLoader(&events)
	connectorsandbox.Use(installed)
	_, err := connectorsetup.New(input(map[string]string{credEnv + "_CMD": "mint"}), connectorsetup.Options{
		Sandbox:      newRecordingLoader(&events),
		RefreshEvery: time.Second,
		Run:          func(string) (string, error) { return "tok", nil },
	})
	if err == nil {
		t.Fatal("a refresh with nowhere to report a failure was accepted")
	}
	if connectorsandbox.Current() != connectorsandbox.Loader(installed) {
		t.Error("a refused setup replaced the installed sandbox")
	}
}

func TestCloseReturnsTheSharedSandboxCloseError(t *testing.T) {
	var totals []string
	useRow(t, func() { totals = append(totals, "totals") })
	boom := errors.New("sandbox close failed")
	rt, err := connectorsetup.New(input(map[string]string{credEnv: "tok"}), connectorsetup.Options{
		Sandbox: failingCloseLoader{connectorsandboxtest.InProcess(), boom},
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
