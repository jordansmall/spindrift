package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// TestMain gives this whole package a hermetic $HOME before any test runs,
// and leaves $CARGO_HOME unset (issue #3201): once cargo binds via
// RepoAwareHomeConfig, any -intree-action=apply invocation with a ready gate
// writes $CARGO_HOME/config.toml (or $HOME/.cargo/config.toml) unconditionally
// -- exactly as bindings mode already did -- so an intree-apply test that
// never cared about cargo before would otherwise silently write into the
// real ambient $HOME/.cargo/config.toml running these tests. A handful of
// tests still t.Setenv their own HOME/CARGO_HOME (to point cargo's config at
// an assertable temp path, or to exercise the "both unset" failure path);
// t.Setenv scopes back to this default at that test's teardown.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "bindregistry-cmd-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("CARGO_HOME")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// lookPathFound is the hermetic stub for resolveRegistryProxyGate's injected
// lookPathFunc dep (issue #3141's CI fix): it reports socat found without
// ever touching the real PATH, so a test's outcome no longer depends on
// whether the ambient sandbox happens to have socat installed (e.g. the Nix
// go-test sandbox, which doesn't list socat as a build input for this
// package -- the exact gap that let this shared-gate test pass locally but
// fail in-box). Callers here always inject a fake SpawnFunc too, so this
// stub's returned path is never actually executed, only "found". Nearly
// every unix-endpoint test in this file injects this one; only the handful
// asserting the socat-missing warning itself inject lookPathMissing below.
func lookPathFound(string) (string, error) { return "/fake/bin/socat", nil }

// lookPathMissing is lookPathFound's counterpart: it deterministically
// reports socat absent, replacing the old trick of emptying $PATH (which
// depended on no other test having already widened PATH via t.Setenv in a
// way that outlived its own subtest, and depended on exec.LookPath being
// the thing actually consulted rather than a stub).
func lookPathMissing(string) (string, error) { return "", exec.ErrNotFound }

// shortUnixSocketPath returns a socket path inside a fresh directory under
// os.TempDir() directly (not t.TempDir(), whose path embeds the full,
// often-long subtest name) -- AF_UNIX's sun_path is capped at 108 bytes on
// Linux, and t.TempDir()'s own path length here regularly blows past that,
// failing net.Listen("unix", ...) with EINVAL.
func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "brsock")
	if err != nil {
		t.Fatalf("os.MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "registry-proxy.sock")
}

// setUnixManifestEnv sets REGISTRY_PROXY_MANIFEST (ADR 0045) to a manifest
// naming a unix endpoint at socketPath, restored automatically at the end of
// the (sub)test by t.Setenv -- the replacement for the deleted
// -registry-proxy-socket/-forwarder-port flags (issue #3141): bindings mode
// and intree-apply mode now both self-serve the transport from this one env
// var instead of taking it as argv.
func setUnixManifestEnv(t *testing.T, socketPath string, routes ...registrymanifest.Route) {
	t.Helper()
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewUnixEndpoint(socketPath), Routes: routes}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)
}

// setTCPManifestEnv is setUnixManifestEnv's TCP-endpoint counterpart (issue
// #3111's TCP-fallback transport), replacing the deleted
// -registry-proxy-tcp-host/-registry-proxy-tcp-port flags.
func setTCPManifestEnv(t *testing.T, host, port string, routes ...registrymanifest.Route) {
	t.Helper()
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewTCPEndpoint(host, port), Routes: routes}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)
}

// clearManifestEnv sets REGISTRY_PROXY_MANIFEST explicitly empty --
// registrymanifest.Parse's ErrAbsent shape ("feature off") -- explicit
// rather than relying on the ambient test environment happening to already
// lack the var, so a silent-absent test's intent reads directly off the
// call site.
func clearManifestEnv(t *testing.T) {
	t.Helper()
	t.Setenv(registrymanifest.EnvVar, "")
}

// forwarderPortStr is bindregistry.ForwarderPort (issue #3141's single port
// declaration) rendered once for the exact-wording assertions below.
var forwarderPortStr = strconv.Itoa(bindregistry.ForwarderPort)

// TestIsBindRegistryInvocation verifies the bind-registry subcommand's
// dispatch guard: a bare "bind-registry" first arg selects it, while every
// other invocation shape falls through to a different path, mirroring
// TestIsReadonlyGuardsInvocation/TestIsMarkerGateInvocation.
func TestIsBindRegistryInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"bind-registry first arg", []string{"bind-registry"}, true},
		{"bind-registry with flags", []string{"bind-registry", "--work-dir", "x"}, true},
		{"no args", nil, false},
		{"other", []string{"other"}, false},
		{"flag names bind-registry as a value, not args[0]", []string{"--work-dir", "bind-registry"}, false},
	}
	for _, c := range cases {
		if got := isBindRegistryInvocation(c.args); got != c.want {
			t.Errorf("%s: isBindRegistryInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// TestRunBindRegistry_WritesClassification verifies runBindRegistry calls
// bindregistry.Classify against -work-dir and writes the classification into
// -ecosystem-env-output as a sourceable NUDGE_ECOSYSTEM assignment.
func TestRunBindRegistry_WritesClassification(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Cargo.lock"), []byte(""), 0o644); err != nil {
		t.Fatalf("write Cargo.lock: %v", err)
	}
	envOut := filepath.Join(t.TempDir(), "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBindRegistry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read ecosystem env output: %v", err)
	}
	want := "NUDGE_ECOSYSTEM=\"cargo\"\n"
	if string(got) != want {
		t.Errorf("ecosystem env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistry_NoLockfileWritesEmptyClassification verifies a
// work-dir with no recognized lockfile writes an empty NUDGE_ECOSYSTEM
// assignment rather than erroring.
func TestRunBindRegistry_NoLockfileWritesEmptyClassification(t *testing.T) {
	workDir := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBindRegistry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read ecosystem env output: %v", err)
	}
	want := "NUDGE_ECOSYSTEM=\"\"\n"
	if string(got) != want {
		t.Errorf("ecosystem env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistry_MissingFlagsErrors verifies the two flag pairs
// (-work-dir/-ecosystem-env-output and -intree-work-dir/-intree-action) are
// each either both given or both omitted, and that omitting every mode's
// flag(s) is itself an error -- there must be at least one complete mode
// requested (-bindings-env-output alone is now sufficient on its own, issue
// #3141, so it no longer appears among the error cases here).
func TestRunBindRegistry_MissingFlagsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no flags at all", nil},
		{"work-dir without ecosystem-env-output", []string{"-work-dir", t.TempDir()}},
		{"ecosystem-env-output without work-dir", []string{"-ecosystem-env-output", filepath.Join(t.TempDir(), "nudge.env")}},
		{"intree-work-dir without intree-action", []string{"-intree-work-dir", t.TempDir()}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			rc := runBindRegistry(c.args, &stdout)
			if rc == 0 {
				t.Fatalf("runBindRegistry exit = 0, want non-zero for %v", c.args)
			}
		})
	}
}

// TestRunBindRegistryWithDeps_ManifestAbsentIsNoOp verifies bindings mode
// silently no-ops (exit 0, no stdout, bindings-env-output left untouched,
// probe/spawn never called) when REGISTRY_PROXY_MANIFEST is unset/empty
// (registrymanifest.ErrAbsent) -- the manifest-driven replacement (issue
// #3141) for the old `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ] || return 0`
// short-circuit: the launcher only ever sets the manifest when the registry
// proxy is genuinely enabled, so its absence is now the "feature off" signal.
func TestRunBindRegistryWithDeps_ManifestAbsentIsNoOp(t *testing.T) {
	clearManifestEnv(t)
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	probeCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if probeCalled {
		t.Error("probe was called, want it never called when REGISTRY_PROXY_MANIFEST is absent")
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when REGISTRY_PROXY_MANIFEST is absent")
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_MANIFEST is absent")
	}
}

// TestRunBindRegistryWithDeps_ManifestMalformedJSONWarnsAndSkipsBindings
// verifies a REGISTRY_PROXY_MANIFEST value that isn't valid JSON at all
// (distinct from ErrAbsent's empty-string case) warns rather than silently
// no-ops -- the manifest is present, just broken, so issue #3141's "manifest
// present but unusable" branch fires, never probe/spawn.
func TestRunBindRegistryWithDeps_ManifestMalformedJSONWarnsAndSkipsBindings(t *testing.T) {
	t.Setenv(registrymanifest.EnvVar, "{not valid json")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	// The parse error text is reproduced here rather than hardcoded, exactly
	// as sibling tests interpolate their own runtime values (e.g. socketPath)
	// into wantWarning, since err.Error() is registrymanifest's own to define.
	_, parseErr := registrymanifest.Parse("{not valid json")
	wantWarning := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed: " + parseErr.Error() + " — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_MANIFEST is malformed")
	}
}

// TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings verifies the
// injected lookPath's early-return branch (issue #2931's BLOCKING finding --
// this branch had zero Go coverage; every other test in this file injects
// lookPathFound to make the check succeed instead): given a manifest naming
// a real mounted socket, a probe reporting nothing is listening yet (so
// something would need spawning), and lookPathMissing injected, bindings
// mode prints the exact warning wording below (naming the manifest
// endpoint, issue #3141), exits 0, calls probe (issue #2931 finding: the
// lookPath check only gates the spawn path, so it must run after probe, not
// before) but never spawn, and leaves bindings-env-output untouched.
func TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathMissing,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if !probeCalled {
		t.Error("probe was never called, want it called to check whether a Forwarder is already listening before gating on socat")
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when socat is missing from PATH")
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when socat is missing from PATH")
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings verifies the
// double-spawn-prevention path at the CLI-integration level: given a
// manifest naming a real unix-socket file and a fake probe that reports
// "already listening" immediately, spawn is never called, and the ready path
// writes the bindings-env-output file (Go + npm-family exports) and the
// cargo config.toml under a fake $CARGO_HOME.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when probe already reports ready")
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotStr := string(got)
	for _, want := range []string{
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/r0'`,
		`export npm_config_registry='http://127.0.0.1:` + forwarderPortStr + `/r0/'`,
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("bindings env output = %q, want it to contain %q", gotStr, want)
		}
	}
	if strings.Contains(gotStr, "FORWARDER_READY") {
		t.Errorf("bindings env output = %q, want it to not contain FORWARDER_READY (dead sentinel, nothing reads it)", gotStr)
	}

	cargoConfig, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := ecosystem.CargoConfigTOML(bindregistry.ForwarderPort, "r0", nil); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
	}
}

// swapTable appends stub rows to ecosystem.Table for the duration of one
// test, preserving the load-bearing order of the real rows. Because the
// swap is package-level, no test in this file may call t.Parallel -- a
// parallel neighbour would observe the stub rows.
func swapTable(t *testing.T, extra ...ecosystem.Row) {
	t.Helper()
	original := ecosystem.Table
	ecosystem.Table = append(append([]ecosystem.Row{}, original...), extra...)
	t.Cleanup(func() { ecosystem.Table = original })
}

// TestRunBindRegistryWithDeps_ExportsComeFromEcosystemTableWalk pins the
// row-generic contract (issue #3181): bindings mode must collect exports by
// walking ecosystem.Table, not by naming individual ecosystems' renderers.
// It proves this by appending a stub row with its own EnvExports renderer to
// the table and asserting the stub's export reaches the written bindings
// file -- a call site that still named npm/go directly would never see it.
func TestRunBindRegistryWithDeps_ExportsComeFromEcosystemTableWalk(t *testing.T) {
	swapTable(t, ecosystem.Row{
		Name: "stub-ecosystem",
		EnvExports: func(port int, prefix string, _ func(string) string, _ []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
			return []ecosystem.EnvExport{{Name: "STUB_ECOSYSTEM_URL", Value: "http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix}}, nil
		},
	})

	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	want := `export STUB_ECOSYSTEM_URL='http://127.0.0.1:` + forwarderPortStr + `/r0'`
	if !strings.Contains(string(got), want) {
		t.Errorf("bindings env output = %q, want it to contain %q (a stub row's export reaching the file proves a table walk, not by-name calls)", got, want)
	}
}

// TestRunBindRegistryWithDeps_HomeConfigsComeFromEcosystemTableWalk pins the
// row-generic contract for home-level config writes (issue #3182): bindings
// mode must write them by walking ecosystem.Table, not by hand-copying a
// block per ecosystem. It proves this by appending a stub row with its own
// HomeConfig to the table and asserting the stub's rendered file reaches
// disk -- a call site that still named cargo/gradle directly would never
// write it.
func TestRunBindRegistryWithDeps_HomeConfigsComeFromEcosystemTableWalk(t *testing.T) {
	swapTable(t, ecosystem.Row{
		Name: "stub-ecosystem",
		HomeConfig: &ecosystem.HomeConfig{
			HomeEnvVar:          "STUB_ECOSYSTEM_HOME",
			HomeRelativeDefault: ".stub-ecosystem",
			ConfigPath:          "stub.conf",
			Render: func(port int, prefix string, _ []registrymanifest.Route) string {
				return "stub=" + strconv.Itoa(port) + "/" + prefix
			},
		},
	})

	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	stubHome := t.TempDir()
	t.Setenv("STUB_ECOSYSTEM_HOME", stubHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(stubHome, "stub.conf"))
	if err != nil {
		t.Fatalf("read stub home config: %v", err)
	}
	want := "stub=" + forwarderPortStr + "/r0"
	if string(got) != want {
		t.Errorf("stub home config = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_SuccessSummaryFragmentsComeFromEcosystemTableWalk
// pins the row-generic contract (issue #3185) for the success summary's
// per-ecosystem "bound to it via ..." fragments: they must come from walking
// ecosystem.Table, not a hand-listed line. It appends two stub rows -- one
// with a BindingEnvVar, one with only a HomeConfig -- and asserts both
// fragments reach the summary, proving the precedence a row-generic walk
// applies (BindingEnvVar before HomeConfig path).
func TestRunBindRegistryWithDeps_SuccessSummaryFragmentsComeFromEcosystemTableWalk(t *testing.T) {
	stubHome := t.TempDir()
	swapTable(t,
		ecosystem.Row{
			Name: "stub-env-ecosystem",
			// The renderer exists so the row's var is actually
			// among the run's rendered exports: the summary skips
			// a BindingEnvVar row whose var went unexported, so a
			// stub declaring the var alone would prove nothing
			// about the walk.
			EnvExports: func(port int, prefix string, _ func(string) string, _ []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
				return []ecosystem.EnvExport{{Name: "STUB_ENV_REGISTRY", Value: "http://127.0.0.1:" + strconv.Itoa(port) + "/" + prefix}}, nil
			},
			BindingEnvVar: "STUB_ENV_REGISTRY",
		},
		ecosystem.Row{
			Name: "stub-home-ecosystem",
			HomeConfig: &ecosystem.HomeConfig{
				HomeEnvVar:          "STUB_HOME_ECOSYSTEM_HOME",
				HomeRelativeDefault: ".stub-home-ecosystem",
				ConfigPath:          "stub.conf",
				Render: func(port int, prefix string, _ []registrymanifest.Route) string {
					return "stub=" + strconv.Itoa(port) + "/" + prefix
				},
			},
		},
	)

	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("STUB_HOME_ECOSYSTEM_HOME", stubHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	wantEnvFragment := "stub-env-ecosystem bound to it via STUB_ENV_REGISTRY"
	if !strings.Contains(stdout.String(), wantEnvFragment) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's BindingEnvVar reaching the summary proves a table walk)", stdout.String(), wantEnvFragment)
	}
	wantHomeFragment := "stub-home-ecosystem bound to it via " + filepath.Join(stubHome, "stub.conf")
	if !strings.Contains(stdout.String(), wantHomeFragment) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's resolved HomeConfig path reaching the summary proves the fallback branch of the walk)", stdout.String(), wantHomeFragment)
	}
}

// TestRunBindRegistryWithDeps_UnusableGateFallbackNamesComeFromEcosystemTableWalk
// pins the row-generic contract (issue #3185) for the gate-unusable fallback
// warning: the ecosystem list it prints must come from walking
// ecosystem.Table, not a hand-maintained literal a new row could silently be
// left out of. It proves this by appending a stub row carrying nothing but a
// Name to the table and asserting that name reaches the warning.
func TestRunBindRegistryWithDeps_UnusableGateFallbackNamesComeFromEcosystemTableWalk(t *testing.T) {
	swapTable(t, ecosystem.Row{Name: "stub-ecosystem"})

	t.Setenv(registrymanifest.EnvVar, "{not valid json")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if want := "stub-ecosystem will fall back to the public registry"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's name reaching the fallback warning proves a table walk)", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_NoRoutePrefixFallbackNamesComeFromEcosystemTableWalk
// mirrors the above for the sibling no-route-prefix fallback warning.
func TestRunBindRegistryWithDeps_NoRoutePrefixFallbackNamesComeFromEcosystemTableWalk(t *testing.T) {
	swapTable(t, ecosystem.Row{Name: "stub-ecosystem"})

	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath) // no routes at all

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if want := "stub-ecosystem will fall back to the public registry"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's name reaching the fallback warning proves a table walk)", stdout.String(), want)
	}
}

// exportNamesInFileOrder parses a rendered bindings env file into its
// export names, in the order the lines appear -- the property
// TestRunBindRegistryWithDeps_ExportOrderIsGoThenNpmFamily pins, which a
// strings.Contains assertion cannot see.
func exportNamesInFileOrder(t *testing.T, rendered string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n") {
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			t.Fatalf("bindings env line %q is not an `export NAME=VALUE` line", line)
		}
		names = append(names, name)
	}
	return names
}

// TestRunBindRegistryWithDeps_ExportOrderIsGoThenNpmFamily pins the rendered
// file's line order: go's exports first, then the npm family's. That order
// predates the ecosystem table and is independent of the table's own
// classification-precedence order (npm precedes go there), so a walk over
// Table itself would silently reverse it -- issue #3181's acceptance
// criterion is a byte-identical file, same names, same values, same order.
func TestRunBindRegistryWithDeps_ExportOrderIsGoThenNpmFamily(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	// An empty GO* snapshot is the no-exemption case, so GOSUMDB=off is
	// exported too -- the widest go export set, and the one the pre-table
	// call site rendered first.
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotNames := exportNamesInFileOrder(t, string(got))
	wantNames := []string{
		"GOPROXY", "GOTOOLCHAIN", "GONOPROXY", "GOSUMDB",
		"npm_config_registry", "pnpm_config_registry", "YARN_NPM_REGISTRY_SERVER",
	}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("bindings env exports = %v, want %v", gotNames, wantNames)
	}
	for i, want := range wantNames {
		if gotNames[i] != want {
			t.Errorf("export %d = %q, want %q (full order got %v, want %v)", i, gotNames[i], want, gotNames, wantNames)
		}
	}
}

// TestRunBindRegistryWithDeps_BindsToFirstRoutePrefixNotSecond verifies
// bindings mode derives its one prefix from gate.manifest.Routes[0]
// specifically, not any other route in a multi-route manifest (issue #3142:
// bindings mode has no per-ecosystem route mapping, so it binds everything
// to the first manifest route, preserving the pre-prefix routes[0] fallback
// semantics). Uses a distinctive, non-"r0" prefix so this proves real
// propagation from the manifest rather than coincidentally matching a
// hardcoded default.
func TestRunBindRegistryWithDeps_BindsToFirstRoutePrefixNotSecond(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath,
		registrymanifest.Route{Prefix: "artifactory-go"},
		registrymanifest.Route{Prefix: "artifactory-npm"},
	)

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotStr := string(got)
	for _, want := range []string{
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/artifactory-go'`,
		`export npm_config_registry='http://127.0.0.1:` + forwarderPortStr + `/artifactory-go/'`,
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("bindings env output = %q, want it to contain %q", gotStr, want)
		}
	}
	if strings.Contains(gotStr, "artifactory-npm") {
		t.Errorf("bindings env output = %q, want it to never mention the second route's prefix", gotStr)
	}

	cargoConfig, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := ecosystem.CargoConfigTOML(bindregistry.ForwarderPort, "artifactory-go", nil); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
	}

	gradleScript, err := os.ReadFile(filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := ecosystem.GradleInitScript(bindregistry.ForwarderPort, "artifactory-go", nil); string(gradleScript) != want {
		t.Errorf("gradle init script = %q, want %q", gradleScript, want)
	}
}

// TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings verifies that a
// manifest with a live, ready Forwarder but zero routes -- the manifest
// carries no prefix bindings mode could bind to -- warns in the established
// register (naming the consequence for every ecosystem, matching
// registryProxyUnusable's own wording) and skips rather than binding every
// ecosystem to the bare, now-404ing "http://127.0.0.1:<port>/" URL (issue
// #3142). This is a defensive path: the launcher always mints at least one
// route whenever it sets REGISTRY_PROXY_MANIFEST at all.
func TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath) // no routes at all

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when the manifest carries no route prefix")
	}
}

// TestRunBindRegistryWithDeps_EmptyRoutePrefixWarnsAndSkipsBindings mirrors
// TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings for the other
// defensive shape named in the same guard: a route present but its own
// Prefix field left empty.
func TestRunBindRegistryWithDeps_EmptyRoutePrefixWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{UpstreamHost: "upstream.example"})

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when routes[0].Prefix is empty")
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines verifies
// the happy path restores the two success log lines
// agent/entrypoint.sh's deleted phase_go_binding/phase_registry_proxy_forwarder
// printed (issue #2931's BLOCKING finding) -- the Go port had silently
// dropped both, leaving the success path printing nothing to stdout.
func TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	wantGoLine := "==> go bound to it via GOPROXY=http://127.0.0.1:" + forwarderPortStr
	if !strings.Contains(stdout.String(), wantGoLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantGoLine)
	}
	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + forwarderPortStr + " — cargo bound to it via " + cargoHome + "/config.toml, npm bound to it via npm_config_registry, yarn bound to it via YARN_NPM_REGISTRY_SERVER, pnpm bound to it via pnpm_config_registry, go bound to it via GOPROXY, and gradle bound to it via " + gradleUserHome + "/init.d/spindrift-registry-proxy.init.gradle"
	found := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == wantForwarderLine {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("stdout = %q, want a line equal to %q", stdout.String(), wantForwarderLine)
	}
}

// TestRunBindRegistryWithDeps_HostRootedGoTaggedPathPrintsFullPathGoLine
// pins the route-aware GOPROXY line (issue #3260): under a host-rooted
// route with a "go"-tagged EnforcedPath, the "==> go bound to it via
// GOPROXY=<url>" line must carry the full-path URL, not the bare-prefix
// guess the line used to hardcode.
func TestRunBindRegistryWithDeps_HostRootedGoTaggedPathPrintsFullPathGoLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{
		Prefix:     "r0",
		HostRooted: true,
		EnforcedPaths: []registrymanifest.EcosystemPath{
			{Ecosystem: "go", Path: "/artifactory/api/go/go-local"},
		},
	})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	wantGoLine := "==> go bound to it via GOPROXY=http://127.0.0.1:" + forwarderPortStr + "/r0/artifactory/api/go/go-local"
	if !strings.Contains(stdout.String(), wantGoLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantGoLine)
	}
}

// TestRunBindRegistryWithDeps_HostRootedNoGoTaggedPathOmitsGoLine pins the
// other half of issue #3260: under a host-rooted route with no "go"-tagged
// EnforcedPath, GOPROXY renders no export at all, so the "==> go bound to
// it via GOPROXY=<url>" line must not print either -- no binding, no line
// claiming one.
func TestRunBindRegistryWithDeps_HostRootedNoGoTaggedPathOmitsGoLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", HostRooted: true})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if strings.Contains(stdout.String(), "go bound to it via GOPROXY=") {
		t.Errorf("stdout = %q, want it NOT to contain a go-bound-via-GOPROXY line (no go-tagged path declared)", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_HostRootedSummaryOmitsUnexportedBindingVars
// pins the summary half of the same rule: a row whose BindingEnvVar was
// never rendered must not be named in the "Forwarder up" summary either.
// Under a host-rooted route with no ecosystem-tagged paths go's GOPROXY
// (issue #3260) and the npm family's three vars (issue #3259) all go
// unexported, leaving only the two file-bound rows -- a summary still
// naming the env-var rows would claim four bindings that do not exist.
func TestRunBindRegistryWithDeps_HostRootedSummaryOmitsUnexportedBindingVars(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", HostRooted: true})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + forwarderPortStr + " — cargo bound to it via " + cargoHome + "/config.toml and gradle bound to it via " + gradleUserHome + "/init.d/spindrift-registry-proxy.init.gradle"
	found := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == wantForwarderLine {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("stdout = %q, want a line equal to %q", stdout.String(), wantForwarderLine)
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningWritesGradleInitScript verifies
// bindings mode also writes the Gradle init script under
// $GRADLE_USER_HOME/init.d/, mirroring the deleted entrypoint.sh
// phase_gradle_binding (see git history) -- gated the same all-or-nothing
// way the cargo config.toml write above already is.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesGradleInitScript(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := ecosystem.GradleInitScript(bindregistry.ForwarderPort, "r0", nil); string(got) != want {
		t.Errorf("gradle init script = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_EmptyGradleUserHomeFallsBackToHomeGradle
// verifies the $HOME/.gradle fallback branch itself, not just the
// both-unset guard around it: every other gradle test in this file either
// pins $GRADLE_USER_HOME directly or empties both it and $HOME, so none of
// them ever runs the `gradleUserHome = filepath.Join(home, ".gradle")` line
// -- the one branch every real deployment takes, since $GRADLE_USER_HOME is
// never set outside this file (see nix/ and agent/, which never reference
// it).
func TestRunBindRegistryWithDeps_EmptyGradleUserHomeFallsBackToHomeGradle(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GRADLE_USER_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(home, ".gradle", "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := ecosystem.GradleInitScript(bindregistry.ForwarderPort, "r0", nil); string(got) != want {
		t.Errorf("gradle init script = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_TimeoutSkipsGradleInitScript verifies the
// gradle init script write shares the cargo config.toml write's
// all-or-nothing readiness gate: a Forwarder that never becomes ready must
// leave $GRADLE_USER_HOME/init.d/ completely untouched, not just
// bindings-env-output.
func TestRunBindRegistryWithDeps_TimeoutSkipsGradleInitScript(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath)

	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	// Pinned so that if the readiness gate this test exercises ever
	// regressed and fell through to the cargo home resolve, it would write
	// into an isolated temp dir rather than the real developer's
	// $HOME/.cargo/config.toml.
	t.Setenv("CARGO_HOME", t.TempDir())

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	gotPath := filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle")
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("os.Stat(gradle init script) err = %v, want a not-exist error when the Forwarder never becomes ready", err)
	}
}

// TestRunBindRegistryWithDeps_EmptyGradleUserHomeAndHomeFailsLoud verifies
// that when $GRADLE_USER_HOME and $HOME are both unset (with $CARGO_HOME
// set, so the cargo branch above is skipped and doesn't mask this), bindings
// mode fails loud (non-zero exit, warning on stdout) rather than silently
// resolving gradleUserHome to the literal "/.gradle" -- an absolute
// root-level path that MkdirAll/WriteFile happily create when running as
// root, claiming success while binding nothing anyone will ever read from.
// Mirrors cargo's own both-unset guard immediately above in
// runBindRegistryBindings (see
// TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud below): the
// bash this replaced ran under `set -u` and would have died on the unset
// $HOME expansion too, not permissively concatenated it.
func TestRunBindRegistryWithDeps_EmptyGradleUserHomeAndHomeFailsLoud(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GRADLE_USER_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero when GRADLE_USER_HOME and HOME are both empty (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stdout.String(), "GRADLE_USER_HOME") {
		t.Errorf("stdout = %q, want it to mention GRADLE_USER_HOME", stdout.String())
	}
	if strings.Contains(stdout.String(), "go bound to it via GOPROXY=") {
		t.Errorf("stdout = %q, want it NOT to claim go was bound to the registry proxy when a later write (gradle home resolve) failed", stdout.String())
	}
	if strings.Contains(stdout.String(), "registry proxy Forwarder up on") {
		t.Errorf("stdout = %q, want it NOT to claim the registry proxy Forwarder is up when a later write (gradle home resolve) failed", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud verifies that
// when both $CARGO_HOME and $HOME are unset, bindings mode fails loud
// (non-zero exit, error on stdout) rather than silently resolving cargo's
// config.toml to a relative ".cargo" path under the process's cwd --
// matching the bash this replaced, which ran under `set -u` and would have
// died on the unset $HOME expansion instead of silently going relative.
func TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero when CARGO_HOME and HOME are both empty (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stdout.String(), "CARGO_HOME") {
		t.Errorf("stdout = %q, want it to mention CARGO_HOME", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_TimeoutWarnsAndSkipsBindings verifies that
// when probe never reports ready, bindings mode exits 0, prints the exact
// timeout warning wording naming the manifest's endpoint (issue #3141), and
// leaves bindings-env-output unwritten. Passes a small timeout/pollInterval
// (rather than the real registryProxyForwarderTimeout/PollInterval constants
// -- production callers still use those unchanged) so this test's own
// wall-clock cost stays well under a second instead of eating the real 5s.
func TestRunBindRegistryWithDeps_TimeoutWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if !spawnCalled {
		t.Error("spawn was never called, want it called once probe reports not-yet-ready")
	}
	wantWarning := "registry proxy Forwarder for endpoint unix://" + socketPath + " did not start listening on 127.0.0.1:" + forwarderPortStr
	if !strings.Contains(stdout.String(), wantWarning) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched on timeout")
	}
}

// TestRunBindRegistryWithDeps_ForwarderSpawnErrorWarnsNamingEndpoint verifies
// EnsureForwarderReady's other failure mode -- spawn itself returning an
// error, rather than a readiness timeout -- also warns, naming both the
// manifest endpoint and the spawn error (issue #3141's "manifest present but
// unusable" branch covers this alongside the timeout and socat-missing
// cases already exercised above).
func TestRunBindRegistryWithDeps_ForwarderSpawnErrorWarnsNamingEndpoint(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, errors.New("boom") },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy Forwarder for endpoint unix://" + socketPath + " failed to start: boom — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when spawn fails")
	}
}

// TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoBoundLine verifies the
// "==> go bound to it via GOPROXY=" success line is only printed once every
// fallible write in bindings mode has succeeded (issue #2931 finding: the
// line used to print before the bindings-env-output write and the cargo
// home resolve/mkdir/config.toml write, so a failure in any of those left
// stdout falsely claiming Go was bound even though the caller
// (agent/entrypoint.sh's phase_registry_proxy_bindings) sees the nonzero
// exit, warns, and skips sourcing entirely -- so no binding is ever actually
// applied).
func TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoBoundLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero when CARGO_HOME and HOME are both empty (stdout=%q)", stdout.String())
	}
	if strings.Contains(stdout.String(), "go bound to it via GOPROXY=") {
		t.Errorf("stdout = %q, want it NOT to claim go was bound to the registry proxy when a later write (cargo home resolve) failed", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoWarningLine verifies
// ComputeGoBindings' warning lines are only printed once every fallible
// write in bindings mode has succeeded (issue #2931 finding: the warning
// loop used to run right after ComputeGoBindings, before the
// bindings-env-output write and the cargo home resolve/mkdir/config.toml
// write, so a failure in any of those left stdout claiming an override --
// e.g. "overriding explicit GOSUMDB=... with GOSUMDB=off" -- that was never
// actually applied, since the caller (agent/entrypoint.sh's
// phase_registry_proxy_bindings) sees the nonzero exit and skips sourcing
// entirely). Unlike
// TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoBoundLine, this test
// sets GOSUMDB to a value ComputeGoBindings actually warns about, since
// blanking all five Go env vars (as that test does) never produces a
// warning to begin with.
func TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoWarningLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "sum.golang.org")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero when CARGO_HOME and HOME are both empty (stdout=%q)", stdout.String())
	}
	if strings.Contains(stdout.String(), "WARNING: overriding") {
		t.Errorf("stdout = %q, want it NOT to claim a Go env var was overridden when a later write (cargo home resolve) failed", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningSkipsSocatCheck verifies the
// injected lookPath's PATH check only gates the *spawn* path (issue #2931
// finding: the LookPath check used to run unconditionally before the
// readiness probe, so an already-ready Forwarder -- nothing left to spawn --
// still failed bindings mode entirely, warning "socat is not on PATH" and
// emitting zero bindings, if socat had since been removed from PATH). With
// probe reporting "already listening" immediately, and lookPath itself
// wired to t.Fatal if ever called, bindings mode must still compute and
// write bindings, never emit the socat-missing warning, and never call
// lookPath or spawn.
func TestRunBindRegistryWithDeps_AlreadyListeningSkipsSocatCheck(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// net.UnixListener.Close unlinks its socket file by default, which
	// would delete the very ModeSocket fixture this test needs to still
	// exist on disk after Close -- keep the file around.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		func(string) (string, error) {
			t.Fatal("lookPath should not be called when probe already reports ready")
			return "", nil
		},
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if !probeCalled {
		t.Error("probe was never called")
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when probe already reports ready")
	}
	if strings.Contains(stdout.String(), "socat is not on PATH") {
		t.Errorf("stdout = %q, want no socat-missing warning when the Forwarder is already ready", stdout.String())
	}
	if _, err := os.Stat(bindingsOut); err != nil {
		t.Errorf("bindings-env-output not written: %v", err)
	}
}

// TestRunBindRegistryWithDeps_TCPTransportWritesBindings verifies bindings
// mode's TCP-fallback transport (issue #3111 slice 8, manifest-driven since
// #3141): given a manifest naming a TCP endpoint and REGISTRY_PROXY_TCP_SECRET
// set, it must build a spawn closure over spawnHTTPForwarder (the
// package-level indirection over bindregistry.SpawnHTTPForwarder -- see that
// var's own doc for why a test must never invoke the real function
// directly), call it with the upstream host/port/secret/listen port, never
// call the injected socket-shaped spawn at all, and write the exact same
// bindings/cargo config the socket-mode success test
// (TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings) asserts --
// proving the downstream computation really is transport-blind.
func TestRunBindRegistryWithDeps_TCPTransportWritesBindings(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", registrymanifest.Route{Prefix: "r0"})

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	origSpawnHTTPForwarder := spawnHTTPForwarder
	var gotHost, gotSecret string
	var gotUpstreamPort, gotListenPort int
	spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, listenPort int) (int, error) {
		gotHost, gotUpstreamPort, gotSecret, gotListenPort = upstreamHost, upstreamPort, secret, listenPort
		return 4242, nil
	}
	t.Cleanup(func() { spawnHTTPForwarder = origSpawnHTTPForwarder })

	// probe reports "not yet listening" once (forcing EnsureForwarderReady to
	// call spawn), then "ready" on every subsequent call (simulating the
	// Forwarder having just come up), exercising both the spawn-invocation
	// assertion and the successful-bindings-write path in one test.
	probeCalls := 0
	probe := func(int) bool {
		probeCalls++
		return probeCalls > 1
	}

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		probe,
		func(string, int) (int, error) {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if gotHost != "registry.example" || gotUpstreamPort != 9443 || gotSecret != "s3cr3t" || gotListenPort != bindregistry.ForwarderPort {
		t.Errorf("spawnHTTPForwarder called with (%q, %d, %q, %d), want (%q, %d, %q, %d)",
			gotHost, gotUpstreamPort, gotSecret, gotListenPort, "registry.example", 9443, "s3cr3t", bindregistry.ForwarderPort)
	}

	// issue #3044: a spawned Forwarder's PID (4242 above) must reach stdout
	// so a bats teardown can kill the Setsid-detached child directly.
	if want := "==> registry proxy Forwarder pid 4242"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotStr := string(got)
	for _, want := range []string{
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/r0'`,
		`export npm_config_registry='http://127.0.0.1:` + forwarderPortStr + `/r0/'`,
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("bindings env output = %q, want it to contain %q", gotStr, want)
		}
	}

	cargoConfig, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := ecosystem.CargoConfigTOML(bindregistry.ForwarderPort, "r0", nil); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
	}
}

// TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings
// verifies that when the manifest names a TCP endpoint but
// REGISTRY_PROXY_TCP_SECRET is unset, bindings mode warns (naming the
// endpoint) and no-ops (exit 0) rather than hard-failing the whole verb --
// the launcher always mints the secret together with the TCP endpoint, so a
// missing secret here is a genuine misconfiguration, not an expected shape.
func TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")
	setTCPManifestEnv(t, "registry.example", "9443")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_TCP_SECRET is unset")
	}
}

// TestRunBindRegistryWithDeps_TCPTransportNeverChecksSocat verifies the TCP
// branch never reaches the injected lookPath at all (unlike the unix
// branch, which gates it on the spawn path): lookPath is wired to t.Fatal if
// ever called, so the TCP transport must still spawn (via
// spawnHTTPForwarder) and write bindings without ever invoking it or
// warning about socat.
func TestRunBindRegistryWithDeps_TCPTransportNeverChecksSocat(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", registrymanifest.Route{Prefix: "r0"})

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	origSpawnHTTPForwarder := spawnHTTPForwarder
	spawnHTTPForwarder = func(string, int, string, int) (int, error) { return 0, nil }
	t.Cleanup(func() { spawnHTTPForwarder = origSpawnHTTPForwarder })

	probeCalls := 0
	probe := func(int) bool {
		probeCalls++
		return probeCalls > 1
	}

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		probe,
		func(string, int) (int, error) {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return 0, nil
		},
		func(string) (string, error) {
			t.Fatal("lookPath should not be called on the TCP transport branch")
			return "", nil
		},
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if strings.Contains(stdout.String(), "socat") {
		t.Errorf("stdout = %q, want no socat-related warning on the TCP transport branch", stdout.String())
	}
	if _, err := os.Stat(bindingsOut); err != nil {
		t.Errorf("bindings-env-output not written: %v", err)
	}
}

// TestRunBindRegistryWithDeps_TCPManifestNonNumericPortWarns verifies a
// manifest whose TCP endpoint carries a non-numeric port (ParseEndpoint only
// checks host/port are both non-empty, not that port parses as a number)
// warns naming the endpoint rather than panicking on the strconv.Atoi this
// internal-consistency guard exists for.
func TestRunBindRegistryWithDeps_TCPManifestNonNumericPortWarns(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "https")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called with a non-numeric manifest port"); return false },
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called with a non-numeric manifest port")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:https has a non-numeric port — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
}

// TestRunBindRegistryWithDeps_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes
// is the direct AC1 test (issue #3141): a single -intree-action=apply
// invocation runs BOTH the tracked-file in-tree rewrite and the repo-aware
// home config render (runBindRegistryIntree, then
// runBindRegistryRepoAwareHomeConfigs -- see runBindRegistryWithDeps'
// dispatch) and must resolve REGISTRY_PROXY_MANIFEST and probe/spawn the
// Forwarder exactly once, sharing that one result across both -- not once
// per mode. probe always reports "not ready" so EnsureForwarderReady would
// call spawn on every independent attempt; if the gate were resolved twice
// (once per mode, the pre-#3141 shape), spawn would be called twice.
//
// This no longer also folds in bindings mode: issue #3201 made
// -intree-action=apply and -bindings-env-output mutually exclusive in one
// invocation (see TestRunBindRegistryWithDeps_IntreeApplyWithBindingsEnvOutputRejected),
// since bindings mode would re-render the repo-aware rows' home configs from
// the base template and clobber the apply pass. The two in-apply modes
// above still share the gate on their own, so this test's AC1 coverage
// survives that split.
func TestRunBindRegistryWithDeps_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"})

	spawnCalls := 0
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false }, // never ready
		func(string, int) (int, error) { spawnCalls++; return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times across one invocation running both in-tree apply modes, want exactly 1 (issue #3141's shared gate)", spawnCalls)
	}
	if got := strings.Count(stdout.String(), "did not start listening"); got != 2 {
		t.Errorf("stdout = %q, want the timeout warning exactly twice (once per mode -- in-tree rewrite, repo-aware home config -- from the one shared gate result), got %d", stdout.String(), got)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want byte-for-byte unchanged when the Forwarder never becomes ready")
	}
}

// newIntreeTestRepo returns a fresh, empty git repo -- a single local repo
// dir, no bare/clone/push needed since skip-worktree/checkout are purely
// local operations. Reuses the package's own shared runGitCmd helper
// (bundleout_cmd_test.go), not a duplicate.
func newIntreeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	return dir
}

// intreeCargoConfigContent is a cargo config.toml referencing
// upstream.example via the sparse+https scheme. The two-scheme
// (sparse+https and plain http) ReplaceAll proof lives in
// intreebinding_test.go's TestApplyInTreeBindingRewritesTrackedFileBothSchemes,
// not here.
const intreeCargoConfigContent = "[source.crates-io]\nreplace-with = \"proxy\"\n\n[source.proxy]\nregistry = \"sparse+https://upstream.example/index/\"\n"

// intreeNpmStyleConfigContent is a generic tracked-file fixture for the
// non-cargo in-tree ecosystems (npm/yarn/pnpm) -- ApplyInTreeBinding only
// string-replaces the upstream host, never parses the file's real syntax, so
// one line referencing upstream.example over https suffices for all three.
const intreeNpmStyleConfigContent = "registry=https://upstream.example/\n"

// writeTrackedIntreeFile writes and commits relPath under dir with content,
// so ApplyInTreeBinding/RevertInTreeBinding see a git-tracked file to
// operate on, for any of the four in-tree config paths (.cargo/config.toml,
// .npmrc, .yarnrc.yml, pnpm-workspace.yaml).
func writeTrackedIntreeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "add", relPath)
	runGitCmd(t, dir, "commit", "-m", "add "+relPath)
}

// intreeSkipWorktreeSet reports whether relPath's skip-worktree bit is set,
// via the same "S "-prefix `git ls-files -v` convention
// bindregistry.skipWorktreeBitSet uses internally.
func intreeSkipWorktreeSet(t *testing.T, dir, relPath string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-files", "-v", "--", relPath).CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-files -v: %v: %s", err, out)
	}
	return strings.HasPrefix(string(out), "S ")
}

// listenOnFakeSocket opens (and immediately closes, without unlinking) a
// real unix-socket file at socketPath -- the same ModeSocket fixture every
// bindings-mode test in this file already relies on for isMountedSocket's
// check, reused here for intree-apply's identical check.
func listenOnFakeSocket(t *testing.T, socketPath string) {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
}

// intreeUpstreamRoute is the route both intree-apply tests and
// setUnixManifestEnv/setTCPManifestEnv share below: a single route naming
// upstream.example as its UpstreamHost and "r0" as its Prefix -- a rewritten
// URL therefore carries a "/r0" path segment (buildIntreeHostRewrites, issue
// #3142), which the exact-match content assertions below account for.
var intreeUpstreamRoute = registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"}

// TestRunBindRegistryWithDeps_IntreeApplyDeadForwarderLeavesFileUntouched is
// the AC5 all-or-nothing-gate test (issue #2932 brief §3): given a tracked
// cargo config referencing the upstream host and a fake probe that never
// reports ready (Forwarder dead), apply must leave the file byte-for-byte
// unchanged and the skip-worktree bit unset -- no partial rewrite.
func TestRunBindRegistryWithDeps_IntreeApplyDeadForwarderLeavesFileUntouched(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want byte-for-byte unchanged:\ngot:  %q\nwant: %q", got, intreeCargoConfigContent)
	}
	if intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
		t.Error("skip-worktree bit set, want it unset when the Forwarder never became ready")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyReadyRewritesAndHidesFromGit
// verifies the happy path: a probe reporting the Forwarder already
// listening rewrites the tracked cargo config to the local Forwarder URL and
// sets its skip-worktree bit.
func TestRunBindRegistryWithDeps_IntreeApplyReadyRewritesAndHidesFromGit(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when probe already reports ready")
	}

	// issue #3044: the already-ready short-circuit never spawns, so there is
	// no PID to report -- the stdout line must stay silent on this path.
	if strings.Contains(stdout.String(), "registry proxy Forwarder pid") {
		t.Errorf("stdout = %q, want no Forwarder pid line when probe already reports ready (nothing was spawned)", stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "127.0.0.1:"+forwarderPortStr) {
		t.Errorf("rewritten content missing expected rewrite: %s", got)
	}
	if !intreeSkipWorktreeSet(t, dir, ".npmrc") {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplySkipsRouteWithEmptyUpstreamHost
// covers issue #3142's buildIntreeHostRewrites filter: a manifest carrying
// one route with a real upstream host alongside a second route whose
// UpstreamHost is empty must still apply the first route's rewrite
// successfully -- the empty-host route is silently skipped when building
// rewrites, never reaching ApplyInTreeBinding's own internal-consistency
// error for an empty UpstreamHost entry.
func TestRunBindRegistryWithDeps_IntreeApplySkipsRouteWithEmptyUpstreamHost(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)

	validRoute := registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"}
	emptyHostRoute := registrymanifest.Route{Prefix: "r1", UpstreamHost: ""}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, validRoute, emptyHostRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "127.0.0.1:"+forwarderPortStr) {
		t.Errorf("rewritten content missing expected rewrite from the valid route: %s", got)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyAllRoutesEmptyUpstreamHostWarns
// covers the other half of buildIntreeHostRewrites' filter from the caller
// side: a manifest carrying a route (not zero routes, unlike
// TestRunBindRegistryWithDeps_IntreeApplyEmptyUpstreamHostWarns above) whose
// UpstreamHost is empty must still be treated as "no route upstream host at
// all" and produce the same warning, since every rewrite candidate was
// filtered out.
func TestRunBindRegistryWithDeps_IntreeApplyAllRoutesEmptyUpstreamHostWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", UpstreamHost: ""})

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want untouched when every route's upstream host is empty")
	}
	if want := "registry proxy manifest carries no route upstream host"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

// TestBuildIntreeHostRewrites_DuplicateUpstreamHostDropsBothAndReportsCollision
// covers the reviewer's blocking finding on issue #3142: two manifest routes
// naming the same UpstreamHost (legal -- e.g. one Artifactory host fronting
// separate npm and cargo path prefixes) can't be told apart by
// ApplyInTreeBinding's host-only content match, so buildIntreeHostRewrites
// must drop every rewrite for the shared host -- not keep the first -- and
// report the collision, while a third route on a distinct host still
// survives untouched.
func TestBuildIntreeHostRewrites_DuplicateUpstreamHostDropsBothAndReportsCollision(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example"},
		{Prefix: "r1", UpstreamHost: "shared.example"},
		{Prefix: "r2", UpstreamHost: "distinct.example"},
	}

	rewrites, collisions := buildIntreeHostRewrites(routes, 9999)

	if len(rewrites) != 1 || rewrites[0].UpstreamHost != "distinct.example" {
		t.Errorf("rewrites = %+v, want exactly the distinct.example route surviving", rewrites)
	}
	if len(collisions) != 1 {
		t.Fatalf("collisions = %+v, want exactly one collision entry", collisions)
	}
	if collisions[0].Host != "shared.example" {
		t.Errorf("collisions[0].Host = %q, want %q", collisions[0].Host, "shared.example")
	}
	if got := strings.Join(collisions[0].Prefixes, ","); got != "r0,r1" {
		t.Errorf("collisions[0].Prefixes = %v, want [r0 r1] in table order", collisions[0].Prefixes)
	}
}

// TestRewriteHostNames_DedupesPreservingFirstOccurrenceOrder covers the
// reviewer's non-blocking finding on issue #3142: rewriteHostNames must not
// repeat a host that appears in more than one rewrite (e.g.
// "host.example, host.example"), and must preserve first-occurrence order
// rather than sorting.
func TestRewriteHostNames_DedupesPreservingFirstOccurrenceOrder(t *testing.T) {
	rewrites := []bindregistry.HostRewrite{
		{UpstreamHost: "a.example", LocalURL: "http://127.0.0.1:1/a"},
		{UpstreamHost: "b.example", LocalURL: "http://127.0.0.1:1/b"},
		{UpstreamHost: "a.example", LocalURL: "http://127.0.0.1:1/a2"},
	}

	if got, want := rewriteHostNames(rewrites), "a.example, b.example"; got != want {
		t.Errorf("rewriteHostNames = %q, want %q", got, want)
	}
}

// TestBindingSummaryProse_SkipsUnrenderedBindingEnvVars covers both halves
// of the summary's skip rule directly: a BindingEnvVar row is named only
// when the rendered exports carry its var, while a HomeConfig row is named
// unconditionally because its file is always written.
func TestBindingSummaryProse_SkipsUnrenderedBindingEnvVars(t *testing.T) {
	original := ecosystem.Table
	ecosystem.Table = []ecosystem.Row{
		{Name: "stub-bound", BindingEnvVar: "STUB_BOUND_REGISTRY"},
		{Name: "stub-unbound", BindingEnvVar: "STUB_UNBOUND_REGISTRY"},
		{Name: "stub-file", HomeConfig: &ecosystem.HomeConfig{}},
	}
	t.Cleanup(func() { ecosystem.Table = original })

	got := bindingSummaryProse(
		[]ecosystem.EnvExport{{Name: "STUB_BOUND_REGISTRY", Value: "http://127.0.0.1:1/r0"}},
		map[string]string{"stub-file": "/tmp/stub.conf"},
	)
	want := "stub-bound bound to it via STUB_BOUND_REGISTRY and stub-file bound to it via /tmp/stub.conf"
	if got != want {
		t.Errorf("bindingSummaryProse = %q, want %q", got, want)
	}
}

// TestBindingSummaryProse_EmptyWhenNothingBound pins the degenerate case the
// skip rule newly makes reachable: with every binding row unrendered the
// prose is empty, which is what lets its caller drop the "— " separator
// rather than print a dangling one.
func TestBindingSummaryProse_EmptyWhenNothingBound(t *testing.T) {
	original := ecosystem.Table
	ecosystem.Table = []ecosystem.Row{{Name: "stub-unbound", BindingEnvVar: "STUB_UNBOUND_REGISTRY"}}
	t.Cleanup(func() { ecosystem.Table = original })

	if got := bindingSummaryProse(nil, nil); got != "" {
		t.Errorf("bindingSummaryProse(nothing bound) = %q, want %q", got, "")
	}
}

// TestJoinProse covers joinProse's three cardinalities: one item names it
// bare, two items join on a bare "and" (no comma), and three or more take an
// Oxford comma before the final "and" -- the shape both bindings-mode
// fallback warnings and the repo-aware no-route warning rely on to read as
// operator prose rather than a raw comma-joined dump.
func TestJoinProse(t *testing.T) {
	if got, want := joinProse([]string{"cargo"}), "cargo"; got != want {
		t.Errorf("joinProse(1 item) = %q, want %q", got, want)
	}
	if got, want := joinProse([]string{"cargo", "npm"}), "cargo and npm"; got != want {
		t.Errorf("joinProse(2 items) = %q, want %q", got, want)
	}
	if got, want := joinProse([]string{"cargo", "npm", "yarn"}), "cargo, npm, and yarn"; got != want {
		t.Errorf("joinProse(3 items) = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyDuplicateUpstreamHostWarnsAndSuppressesGenericWarning
// covers the caller side of the same reviewer finding: a manifest whose only
// two routes share an upstream host must print the per-collision warning
// naming both prefixes and the host, leave the tracked file untouched, and
// must NOT also print the generic "carries no route upstream host" warning
// -- that warning would mislead, since the manifest does carry an upstream
// host, it's just unusable for host-based rewriting.
func TestRunBindRegistryWithDeps_IntreeApplyDuplicateUpstreamHostWarnsAndSuppressesGenericWarning(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	routeA := registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"}
	routeB := registrymanifest.Route{Prefix: "r1", UpstreamHost: "upstream.example"}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, routeA, routeB)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want untouched when every route's upstream host collides")
	}

	want := "==> WARNING: registry proxy manifest routes r0, r1 share upstream host upstream.example — host-based in-tree rewriting cannot tell them apart, their in-tree registry rewrite is skipped, those ecosystems fall back to the public registry\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want exactly %q (generic no-upstream-host warning must be suppressed)", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyManifestAbsentIsNoOp verifies an
// unset/empty REGISTRY_PROXY_MANIFEST (entrypoint.sh's intree_binding_apply
// call site no longer passes any transport flag at all, issue #3141)
// silently no-ops apply mode rather than erroring or touching the tracked
// file -- the manifest-driven replacement for the old empty
// -registry-proxy-socket no-op.
func TestRunBindRegistryWithDeps_IntreeApplyManifestAbsentIsNoOp(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	clearManifestEnv(t)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want untouched when REGISTRY_PROXY_MANIFEST is absent")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty when REGISTRY_PROXY_MANIFEST is absent (the one deliberate silence, per issue #3082)", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyEmptyUpstreamHostWarns verifies a
// manifest present (mounted socket, Forwarder ready) but carrying no route
// upstream host warns (rather than silently no-ops) apply mode, since a
// present manifest means the registry proxy is genuinely configured.
func TestRunBindRegistryWithDeps_IntreeApplyEmptyUpstreamHostWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath) // no routes at all

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want untouched when the manifest carries no route upstream host")
	}
	if want := "registry proxy manifest carries no route upstream host"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (a present manifest means the proxy is genuinely configured, so a missing upstream host must warn)", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplySocatMissingWarnsAndSkipsRewrite
// mirrors TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings for
// the intree-apply path (reviewer finding on issue #2932): given a manifest
// naming a real mounted socket, a probe reporting nothing listening yet, and
// lookPathMissing injected, apply must print a socat-specific warning naming
// the endpoint, exit 0, and leave the tracked file byte-for-byte untouched
// -- rather than falling through to EnsureForwarderReady's generic "failed
// to start" warning.
func TestRunBindRegistryWithDeps_IntreeApplySocatMissingWarnsAndSkipsRewrite(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { probeCalled = true; return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathMissing,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n" +
		"==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — the repo-aware registry binding is skipped, ecosystems fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if !probeCalled {
		t.Error("probe was never called, want it called to check whether a Forwarder is already listening before gating on socat")
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when socat is missing from PATH")
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want byte-for-byte unchanged when socat is missing from PATH:\ngot:  %q\nwant: %q", got, intreeCargoConfigContent)
	}
	if intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
		t.Error("skip-worktree bit set, want it unset when socat is missing from PATH")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyTCPTransportRewritesFile mirrors
// TestRunBindRegistryWithDeps_TCPTransportWritesBindings for intree-apply
// mode (issue #3111 slice 8, manifest-driven since #3141): given a manifest
// naming a TCP endpoint and REGISTRY_PROXY_TCP_SECRET set, apply must spawn
// via spawnHTTPForwarder (never the injected socket-shaped spawn), and still
// rewrite the tracked cargo config to the local Forwarder URL and set its
// skip-worktree bit exactly as the socket-mode happy path does.
func TestRunBindRegistryWithDeps_IntreeApplyTCPTransportRewritesFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", intreeUpstreamRoute)

	origSpawnHTTPForwarder := spawnHTTPForwarder
	var gotHost, gotSecret string
	var gotUpstreamPort, gotListenPort int
	spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, listenPort int) (int, error) {
		gotHost, gotUpstreamPort, gotSecret, gotListenPort = upstreamHost, upstreamPort, secret, listenPort
		return 0, nil
	}
	t.Cleanup(func() { spawnHTTPForwarder = origSpawnHTTPForwarder })

	// Same "not-ready-once-then-ready" probe shape as the bindings-mode TCP
	// test: forces EnsureForwarderReady to call spawn once, then reports
	// ready so the apply proceeds.
	probeCalls := 0
	probe := func(int) bool {
		probeCalls++
		return probeCalls > 1
	}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		probe,
		func(string, int) (int, error) {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if gotHost != "registry.example" || gotUpstreamPort != 9443 || gotSecret != "s3cr3t" || gotListenPort != bindregistry.ForwarderPort {
		t.Errorf("spawnHTTPForwarder called with (%q, %d, %q, %d), want (%q, %d, %q, %d)",
			gotHost, gotUpstreamPort, gotSecret, gotListenPort, "registry.example", 9443, "s3cr3t", bindregistry.ForwarderPort)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "127.0.0.1:"+forwarderPortStr) {
		t.Errorf("rewritten content missing expected rewrite: %s", got)
	}
	if !intreeSkipWorktreeSet(t, dir, ".npmrc") {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyTCPTransportMissingSecretWarns
// mirrors TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings
// for intree-apply mode: the manifest names a TCP endpoint but
// REGISTRY_PROXY_TCP_SECRET is unset, so apply must warn (naming the
// endpoint) and no-op (exit 0), leaving the tracked file byte-for-byte
// untouched.
func TestRunBindRegistryWithDeps_IntreeApplyTCPTransportMissingSecretWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")
	setTCPManifestEnv(t, "registry.example", "9443", intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n" +
		"==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — the repo-aware registry binding is skipped, ecosystems fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want untouched when REGISTRY_PROXY_TCP_SECRET is unset")
	}
}

// TestRunBindRegistryWithDeps_IntreeRevertRestoresAppliedFile verifies
// revert mode, given a previously-applied file (rewritten content,
// skip-worktree bit set), restores the original tracked content and clears
// the skip-worktree bit -- with no manifest set at all, since revert is a
// pure git operation that never consults REGISTRY_PROXY_MANIFEST or the
// probe/spawn deps.
func TestRunBindRegistryWithDeps_IntreeRevertRestoresAppliedFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	clearManifestEnv(t)

	npmBinding := bindregistry.InTreeBindings()[0]
	outcome, err := bindregistry.ApplyInTreeBinding(dir, npmBinding, []bindregistry.HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:" + forwarderPortStr}})
	if err != nil || outcome != bindregistry.ApplyApplied {
		t.Fatalf("ApplyInTreeBinding (setup) = (%v, %v), want (%v, nil)", outcome, err, bindregistry.ApplyApplied)
	}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "revert",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on revert"); return false },
		func(string, int) (int, error) { t.Fatal("spawn should not be called on revert"); return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeNpmStyleConfigContent {
		t.Errorf(".npmrc = %q, want restored to %q", got, intreeNpmStyleConfigContent)
	}
	if intreeSkipWorktreeSet(t, dir, ".npmrc") {
		t.Error("skip-worktree bit still set, want it cleared after revert")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllThreeRows is the
// multi-row happy-path test a review finding called out as missing: with all
// three in-tree-bound config files (npm, yarn, pnpm) tracked and present,
// apply must rewrite and skip-worktree-tag every one of them, and a
// following revert must restore every one of them -- not just the first
// row, the only shape every existing intree test here exercised. A tracked
// cargo config sits alongside them the whole time and must never be touched
// by any of the three passes below (issue #3201: cargo no longer
// participates in InTreeBindings at all), proving the two mechanisms' own
// non-composing exclusion holds under the same multi-row choreography.
func TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllThreeRows(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, ".yarnrc.yml", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, "pnpm-workspace.yaml", intreeNpmStyleConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	relPaths := []string{".npmrc", ".yarnrc.yml", "pnpm-workspace.yaml"}

	assertCargoUntouched := func() {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != intreeCargoConfigContent {
			t.Errorf(".cargo/config.toml = %q, want byte-for-byte unchanged (cargo binds via RepoAwareHomeConfig, not the in-tree rewrite)", got)
		}
		if intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
			t.Error(".cargo/config.toml skip-worktree bit set, want it never tagged")
		}
	}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps apply exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	for _, rel := range relPaths {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(got), "upstream.example") {
			t.Errorf("%s still mentions upstream.example after apply: %s", rel, got)
		}
		if !strings.Contains(string(got), "127.0.0.1:"+forwarderPortStr) {
			t.Errorf("%s missing rewritten local Forwarder URL after apply: %s", rel, got)
		}
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set after apply", rel)
		}
	}
	assertCargoUntouched()

	stdout.Reset()
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", "revert",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps revert exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	for _, rel := range relPaths {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != intreeNpmStyleConfigContent {
			t.Errorf("%s = %q after revert, want restored to %q", rel, got, intreeNpmStyleConfigContent)
		}
		if intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit still set after revert", rel)
		}
	}
	assertCargoUntouched()

	// Re-apply pass (AC4): the revert/re-apply choreography around branch
	// recovery must cover all three rows, not just the apply/revert pair
	// tested above -- a re-apply after revert must rewrite and re-tag every
	// row again, exactly as the first apply did.
	stdout.Reset()
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps re-apply exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	for _, rel := range relPaths {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(got), "upstream.example") {
			t.Errorf("%s still mentions upstream.example after re-apply: %s", rel, got)
		}
		if !strings.Contains(string(got), "127.0.0.1:"+forwarderPortStr) {
			t.Errorf("%s missing rewritten local Forwarder URL after re-apply: %s", rel, got)
		}
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set after re-apply", rel)
		}
	}
	assertCargoUntouched()
}

// newIntreeUnmergedNpmTestRepo builds a repo with plain tracked yarn and
// pnpm config files, but an .npmrc left genuinely unmerged (UU) -- the
// same fixture shape as bindregistry's own unexported newUnmergedTestRepo
// (intreebinding_test.go), replicated here since that helper is unexported
// in a different package. `git update-index --skip-worktree`
// fails with exit 128 on the unmerged .npmrc, giving ApplyInTreeBinding a
// genuine per-row failure to prove the sibling rows aren't blocked by it.
func newIntreeUnmergedNpmTestRepo(t *testing.T) string {
	t.Helper()
	dir := newIntreeTestRepo(t)

	writeTrackedIntreeFile(t, dir, ".yarnrc.yml", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, "pnpm-workspace.yaml", intreeNpmStyleConfigContent)

	npmRel := ".npmrc"
	full := filepath.Join(dir, npmRel)
	write := func(content string) {
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(intreeNpmStyleConfigContent)
	runGitCmd(t, dir, "add", npmRel)
	runGitCmd(t, dir, "commit", "-m", "add .npmrc")
	base := strings.TrimSpace(runGitCmd(t, dir, "symbolic-ref", "--short", "HEAD"))

	runGitCmd(t, dir, "checkout", "-b", "feature")
	write("registry=https://upstream.example/other/\n")
	runGitCmd(t, dir, "add", npmRel)
	runGitCmd(t, dir, "commit", "-m", "feature")

	runGitCmd(t, dir, "checkout", base)
	write("registry=https://upstream.example/index2/\n")
	runGitCmd(t, dir, "add", npmRel)
	runGitCmd(t, dir, "commit", "-m", "base2")

	// A conflicting merge is the point of this fixture -- unlike runGitCmd's
	// other calls, a nonzero exit here is the expected/desired outcome, not a
	// setup failure.
	if err := exec.Command("git", "-C", dir, "merge", "feature").Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", npmRel).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", npmRel, status, err)
	}

	return dir
}

// TestRunBindRegistryWithDeps_IntreeApplyPartialFailureDoesNotBlockSiblingRows
// is the regression test for a review finding: the apply loop used to return
// as soon as one row errored, aborting the rest. A row that genuinely fails
// (unmerged .npmrc) must not stop the loop from attempting its siblings, and
// the overall apply must still report failure.
func TestRunBindRegistryWithDeps_IntreeApplyPartialFailureDoesNotBlockSiblingRows(t *testing.T) {
	dir := newIntreeUnmergedNpmTestRepo(t)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 1 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 1 when one row genuinely fails (stdout=%q)", rc, stdout.String())
	}

	npmGot, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(npmGot), "upstream.example") {
		t.Errorf(".npmrc rewritten despite the failing skip-worktree call: %s", npmGot)
	}
	if intreeSkipWorktreeSet(t, dir, ".npmrc") {
		t.Error(".npmrc skip-worktree bit set, want it unset since the row genuinely failed")
	}

	for _, rel := range []string{".yarnrc.yml", "pnpm-workspace.yaml"} {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(got), "upstream.example") {
			t.Errorf("%s not rewritten, want npm's failure not to block its siblings: %s", rel, got)
		}
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set, want npm's failure not to block its siblings", rel)
		}
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyMissingConfigWarns verifies apply
// mode prints a distinct, ecosystem/path-naming warning when an in-tree
// config file simply doesn't exist (ApplyMissing) -- issue #3082's AC that
// this must read differently from ApplyNoopContent's "content no longer
// mentions the upstream host" case, since the two point an operator at
// different fixes (registry pinned outside the repo vs. wrong upstream
// host).
func TestRunBindRegistryWithDeps_IntreeApplyMissingConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> npm config .npmrc not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyNotRegularConfigWarns verifies apply
// mode prints a distinct warning when an in-tree config path exists but
// isn't a plain regular file (ApplyNotRegular) -- issue #2933's `[ -f ]`
// parity guard had no operator-facing message at all before #3082.
func TestRunBindRegistryWithDeps_IntreeApplyNotRegularConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".npmrc"), 0o755); err != nil {
		t.Fatalf("mkdir .npmrc as a directory: %v", err)
	}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: npm config .npmrc exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyUntrackedConfigWarns pins the
// existing ApplyUntracked warning survives the bool-to-ApplyOutcome
// signature change unchanged (#3082 slice 2 only added the four other
// messages -- this one already existed).
func TestRunBindRegistryWithDeps_IntreeApplyUntrackedConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte(intreeNpmStyleConfigContent), 0o644); err != nil {
		t.Fatal(err)
	}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: npm config .npmrc exists but is not tracked by git — skipping the in-tree registry rewrite for it"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplySkipWorktreeAlreadySetWarns verifies
// apply mode prints a warning distinct from ApplyNoopContent's "nothing to
// do" when the skip-worktree bit is already set (ApplySkipWorktreeSet) --
// issue #2932's crash window, where the bit can be tagged before content is
// rewritten, so "bit set" alone never proves the content converged.
func TestRunBindRegistryWithDeps_IntreeApplySkipWorktreeAlreadySetWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	runGitCmd(t, dir, "update-index", "--skip-worktree", "--", ".npmrc")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: npm config .npmrc already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyNoopContentWarns verifies apply mode
// prints a warning naming the configured upstream host when a tracked,
// skip-worktree-clear config file simply no longer mentions it
// (ApplyNoopContent) -- distinct from ApplyMissing's "file not found",
// pointing an operator at a different fix (the manifest's route upstream
// host is wrong, not that the registry pin lives outside this file).
func TestRunBindRegistryWithDeps_IntreeApplyNoopContentWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", "registry=https://other.example/\n")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: npm config .npmrc no longer references upstream host upstream.example — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
	}
}

// intreeCargoNamedRegistryRepoConfig is a repo's own tracked
// .cargo/config.toml declaring one named registry, "private", whose index
// host matches intreeUpstreamRoute's UpstreamHost -- the fixture every
// runBindRegistryRepoAwareHomeConfigs test below that needs a real repo
// registry declaration shares.
const intreeCargoNamedRegistryRepoConfig = "[registries.private]\n" +
	"index = \"sparse+https://upstream.example/private/index/\"\n"

// TestRunBindRegistryWithDeps_IntreeApplyWritesCargoSourceReplacementConfig
// covers issue #3201's replacement of the in-tree cargo rewrite entirely:
// given a repo's own un-rewritten .cargo/config.toml naming "private" (whose
// index host matches the one manifest route's UpstreamHost), apply must
// render $CARGO_HOME/config.toml with CargoConfigTOML's base crates-io
// replacement plus one [source.spindrift-upstream-private] stanza replaced
// with the reused spindrift-registry-proxy source, exactly matching
// ecosystem.CargoRepoAwareConfig's own output for the same inputs -- this is
// the end-to-end proof that the verb wiring reaches that renderer at all.
func TestRunBindRegistryWithDeps_IntreeApplyWritesCargoSourceReplacementConfig(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoNamedRegistryRepoConfig)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	want, _, _ := ecosystem.CargoRepoAwareConfig(bindregistry.ForwarderPort, "r0", []registrymanifest.Route{intreeUpstreamRoute}, intreeCargoNamedRegistryRepoConfig)
	if string(got) != want {
		t.Errorf("cargo config.toml = %q, want %q", got, want)
	}

	// The tracked repo file itself is untouched -- cargo no longer
	// participates in the in-tree rewrite at all (issue #3201).
	repoConfig, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(repoConfig) != intreeCargoNamedRegistryRepoConfig {
		t.Errorf(".cargo/config.toml = %q, want byte-for-byte unchanged", repoConfig)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyWritesCargoSourceReplacementEnvOutput
// covers the other half of the same apply: the placeholder token export the
// rewritten config.toml's [registries.spindrift-registry-proxy] needs bound,
// keyed to the reused proxy source name since route "r0" coincides with the
// manifest's own routes[0].Prefix.
func TestRunBindRegistryWithDeps_IntreeApplyWritesCargoSourceReplacementEnvOutput(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoNamedRegistryRepoConfig)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	t.Setenv("CARGO_HOME", t.TempDir())
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	want := `export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_TOKEN='` + ecosystem.CargoPlaceholderToken + "'\n"
	if string(got) != want {
		t.Errorf("intree bindings env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyNoRegistriesTableWritesEmptyEnvOutputAndBaseConfig
// covers the common case (issue #3201): a repo with no .cargo/config.toml at
// all declares no named registry, so the read-error-is-not-an-error path
// treats it as empty content, the rendered $CARGO_HOME/config.toml is
// CargoConfigTOML's own crates-io-only base render byte-for-byte, and the
// env output carries no exports at all.
func TestRunBindRegistryWithDeps_IntreeApplyNoRegistriesTableWritesEmptyEnvOutputAndBaseConfig(t *testing.T) {
	dir := newIntreeTestRepo(t)
	// No .cargo/config.toml written at all -- npm's is enough to give the
	// repo a tracked in-tree file and prove the run otherwise succeeds.
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := ecosystem.CargoConfigTOML(bindregistry.ForwarderPort, "r0", nil); string(got) != want {
		t.Errorf("cargo config.toml = %q, want the crates-io-only base render %q", got, want)
	}

	envGot, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	if string(envGot) != "" {
		t.Errorf("intree bindings env output = %q, want empty (no repo-declared registries)", envGot)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyTwoRouteManifestDedupesReusedProxySource
// covers issue #3201's URL->source-name 1:1 constraint end-to-end: a
// manifest with two routes, each naming its own registry, must not double up
// the [source.spindrift-registry-proxy] stanza the "r0" route's own
// registry shares with the base crates-io replacement -- CargoConfigTOML
// already wrote it -- while the "r1" route's registry still gets its own
// distinct proxy source and its own distinct placeholder export.
func TestRunBindRegistryWithDeps_IntreeApplyTwoRouteManifestDedupesReusedProxySource(t *testing.T) {
	dir := newIntreeTestRepo(t)
	repoConfig := intreeCargoNamedRegistryRepoConfig +
		"\n[registries.other-private]\n" +
		"index = \"sparse+https://other.example/idx/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", repoConfig)

	route0 := intreeUpstreamRoute
	route1 := registrymanifest.Route{Prefix: "r1", UpstreamHost: "other.example", CargoRegistries: []string{"other-private"}}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, route0, route1)

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	want, _, _ := ecosystem.CargoRepoAwareConfig(bindregistry.ForwarderPort, "r0", []registrymanifest.Route{route0, route1}, repoConfig)
	if string(got) != want {
		t.Errorf("cargo config.toml = %q, want %q", got, want)
	}
	if n := strings.Count(string(got), "[source.spindrift-registry-proxy]"); n != 1 {
		t.Errorf("cargo config.toml has %d [source.spindrift-registry-proxy] stanzas, want exactly 1 (the reused base one, not re-emitted for route r0's own registry)", n)
	}

	envGot, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	for _, wantExport := range []string{
		`export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_TOKEN='` + ecosystem.CargoPlaceholderToken + `'`,
		`export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_TOKEN='` + ecosystem.CargoPlaceholderToken + `'`,
	} {
		if !strings.Contains(string(envGot), wantExport) {
			t.Errorf("intree bindings env output = %q, want it to contain %q", envGot, wantExport)
		}
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyPrintsUndeclaredRegistryWarningToStdout
// pins issue #3201's warning parity at the verb boundary: the row value only
// returns warning strings, so without an end-to-end assertion the print loop
// in runBindRegistryRepoAwareHomeConfigs could be deleted and nothing would
// fail. The route declares only "declared" while the repo's own
// .cargo/config.toml names an "undeclared" registry whose index host still
// matches that route's upstream host -- the one case that warns.
func TestRunBindRegistryWithDeps_IntreeApplyPrintsUndeclaredRegistryWarningToStdout(t *testing.T) {
	dir := newIntreeTestRepo(t)
	repoConfig := "[registries.undeclared]\n" +
		"index = \"sparse+https://upstream.example/undeclared/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", repoConfig)

	route := registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example", CargoRegistries: []string{"declared"}}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, route)

	t.Setenv("CARGO_HOME", t.TempDir())

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	want := `==> WARNING: cargo registry "undeclared" matches route prefix "r0"'s upstream host but is not declared in that route's cargo-registries`
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyUnreadableRepoConfigFails covers the
// repo-aware phase's non-ENOENT read-error branch (issue #3201), the one the
// missing-file case must not be confused with: a missing config means "this
// repo declares no named registry", while an unreadable one means the plan
// was derived from nothing and the run must fail rather than write a config
// that silently binds less than the repo needs. The unreadable file is a
// directory, since os.ReadFile then returns EISDIR without depending on the
// test running as a non-root user -- a Box does not guarantee that.
func TestRunBindRegistryWithDeps_IntreeApplyUnreadableRepoConfigFails(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	if err := os.MkdirAll(filepath.Join(dir, ".cargo", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	t.Setenv("CARGO_HOME", t.TempDir())
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want non-zero when a repo config is unreadable (stdout=%q)", rc, stdout.String())
	}

	if want := "driver-exec bind-registry: read cargo repo config:"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}

	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%s) err = %v, want the env output never written on a failed run", envOut, err)
	}
}

// TestRunBindRegistryWithDeps_IntreeBindingsEnvOutputRequiresApply verifies
// -intree-bindings-env-output prints the exact validation message and exits
// non-zero unless paired with -intree-action=apply -- mirrors the other
// flag-pair validation errors in runBindRegistryWithDeps.
func TestRunBindRegistryWithDeps_IntreeBindingsEnvOutputRequiresApply(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on a validation error"); return false },
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called on a validation error")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	want := "driver-exec bind-registry: -intree-bindings-env-output requires -intree-action=apply\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyWithBindingsEnvOutputRejected
// covers a review finding on issue #3201: -intree-action=apply re-renders
// the repo-aware home configs (e.g. cargo's $CARGO_HOME/config.toml) from
// the repo's own tracked config, but bindings mode (-bindings-env-output)
// would then re-render the same HomeConfig rows from the base template in
// the same invocation, silently clobbering the replacement stanzas apply
// just wrote. No caller combines the two flags today; this guards against
// one starting to.
func TestRunBindRegistryWithDeps_IntreeApplyWithBindingsEnvOutputRejected(t *testing.T) {
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-work-dir", t.TempDir(),
		"-intree-action", "apply",
		"-bindings-env-output", filepath.Join(t.TempDir(), "bindings.env"),
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on a validation error"); return false },
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called on a validation error")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	want := "driver-exec bind-registry: -intree-action=apply and -bindings-env-output cannot be combined in one invocation — bindings mode would re-render the repo-aware rows' home configs from the base template and undo the apply\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_IntreeFlagValidation verifies the
// -intree-work-dir/-intree-action pairing and the -intree-action value
// check, mirroring TestRunBindRegistry_MissingFlagsErrors' style for the
// other pre-existing flag pair.
func TestRunBindRegistryWithDeps_IntreeFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"intree-work-dir without intree-action", []string{"-intree-work-dir", t.TempDir()}},
		{"intree-action without intree-work-dir", []string{"-intree-action", "apply"}},
		{"bogus intree-action", []string{"-intree-work-dir", t.TempDir(), "-intree-action", "bogus"}},
		{"intree-bindings-env-output without intree-action=apply", []string{"-intree-bindings-env-output", filepath.Join(t.TempDir(), "intree-bindings.env")}},
		{"intree-bindings-env-output with intree-action=revert", []string{"-intree-work-dir", t.TempDir(), "-intree-action", "revert", "-intree-bindings-env-output", filepath.Join(t.TempDir(), "intree-bindings.env")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			rc := runBindRegistryWithDeps(c.args, &stdout,
				func(int) bool { return true },
				func(string, int) (int, error) { return 0, nil },
				lookPathFound,
				registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
			)
			if rc == 0 {
				t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero for %v (stdout=%q)", c.args, stdout.String())
			}
		})
	}
}

// TestRunBindRegistry_WriteFailureReturnsNonZero verifies runBindRegistry
// surfaces an os.WriteFile failure on -ecosystem-env-output (rather than a
// panic or a silent success): pointing the output at a path whose parent
// directory doesn't exist forces WriteFile to fail past the Classify call,
// which can no longer itself return an error.
func TestRunBindRegistry_WriteFailureReturnsNonZero(t *testing.T) {
	workDir := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "nonexistent-subdir", "nudge.env")

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-work-dir", workDir,
		"-ecosystem-env-output", envOut,
	}, &stdout)
	if rc == 0 {
		t.Fatalf("runBindRegistry exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stdout.String(), "write ecosystem env output") {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), "write ecosystem env output")
	}
}

// TestDropCollidedRoutes_SkipsRouteWithCollidedUpstreamHost covers a
// reviewer finding on issue #3142: a route whose UpstreamHost
// buildIntreeHostRewrites already dropped as a collision must not survive
// dropCollidedRoutes, since nothing on disk was ever rewritten to that
// route's LocalURL -- the collision means ApplyInTreeBinding skipped the
// rewrite for it entirely. (cargo's own exports-side analogue --
// CargoSourceReplacements deriving nothing for a route its repo config
// doesn't declare -- is covered by that function's own package tests in
// ecosystem, issue #3201.)
func TestDropCollidedRoutes_SkipsRouteWithCollidedUpstreamHost(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example", CargoRegistries: []string{"collided-one"}},
		{Prefix: "r1", UpstreamHost: "shared.example", CargoRegistries: []string{"collided-two"}},
		{Prefix: "r2", UpstreamHost: "distinct.example", CargoRegistries: []string{"valid-registry"}},
	}
	_, collisions := buildIntreeHostRewrites(routes, 9999)

	filtered := dropCollidedRoutes(routes, collisions)

	if len(filtered) != 1 || filtered[0].Prefix != "r2" {
		t.Errorf("filtered = %+v, want exactly one surviving route (r2/distinct.example)", filtered)
	}
}

// TestRunBindRegistryWithDeps_LockfileScanWarnsOnHit verifies
// -lockfile-scan-work-dir on its own (issue #3199): given a manifest present
// (so the registry proxy is on for this dispatch) and a tracked Cargo.lock
// naming the Forwarder URL, it prints one ==> WARNING line naming the
// lockfile path and the matched URL, and exits 0. probe/spawn must never be
// called: this scan reads the manifest only to decide on/off, it never
// resolves the shared Forwarder-readiness gate (design constraint, issue
// #3199) -- unlike bindings/intree-apply mode, it must never itself spawn a
// Forwarder.
func TestRunBindRegistryWithDeps_LockfileScanWarnsOnHit(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, "Cargo.lock", "source = \"registry+http://127.0.0.1:"+forwarderPortStr+"/\"\n")
	setUnixManifestEnv(t, shortUnixSocketPath(t))

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called by lockfile-scan mode")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called by lockfile-scan mode")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:" + forwarderPortStr + " — this will ship in the PR (issue #3199)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_LockfileScanManifestAbsentIsSilent verifies a
// dispatch with the registry proxy off (REGISTRY_PROXY_MANIFEST
// unset/empty, registrymanifest.ErrAbsent) produces no scan and no output at
// all -- a clean run never gets "scanned N lockfiles" chatter, and an
// off-dispatch never gets scanned regardless of what any tracked lockfile
// happens to contain.
func TestRunBindRegistryWithDeps_LockfileScanManifestAbsentIsSilent(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, "Cargo.lock", "source = \"registry+http://127.0.0.1:"+forwarderPortStr+"/\"\n")
	clearManifestEnv(t)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty output when the registry proxy is off", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_LockfileScanMalformedManifestWarnsAndSucceeds
// covers a malformed REGISTRY_PROXY_MANIFEST (registrymanifest.Parse returns
// a non-ErrAbsent error): lockfile-scan mode must warn once and still exit
// 0, never fail the run over a malformed manifest it isn't even trying to
// connect to.
func TestRunBindRegistryWithDeps_LockfileScanMalformedManifestWarnsAndSucceeds(t *testing.T) {
	dir := newIntreeTestRepo(t)
	t.Setenv(registrymanifest.EnvVar, "{not valid json")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called by lockfile-scan mode")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called by lockfile-scan mode")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed, skipping the lockfile Forwarder-URL scan:"
	if !strings.HasPrefix(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_LockfileScanErrorWarnsAndSucceeds covers a
// scan error (bindregistry.ScanLockfilesForForwarder's own git ls-files
// failing because -lockfile-scan-work-dir names a non-repo directory):
// lockfile-scan mode must warn and still exit 0.
func TestRunBindRegistryWithDeps_LockfileScanErrorWarnsAndSucceeds(t *testing.T) {
	dir := t.TempDir() // not a git repo
	setUnixManifestEnv(t, shortUnixSocketPath(t))

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called by lockfile-scan mode")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called by lockfile-scan mode")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: lockfile Forwarder-URL scan failed, skipping:"
	if !strings.HasPrefix(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout.String(), want)
	}
}

// TestRunBindRegistryWithDeps_LockfileScanCleanRepoIsSilent verifies a
// tracked repo with lockfiles that don't name the Forwarder URL produces no
// output at all -- no "scanned N lockfiles, all clean" chatter (issue
// #3199's clean-run invariant).
func TestRunBindRegistryWithDeps_LockfileScanCleanRepoIsSilent(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, "Cargo.lock", "source = \"registry+https://index.crates.io/\"\n")
	setUnixManifestEnv(t, shortUnixSocketPath(t))

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called by lockfile-scan mode")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called by lockfile-scan mode")
			return 0, nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty output for a clean repo", stdout.String())
	}
}

// TestRunBindRegistryWithDeps_LockfileScanFlagAloneSatisfiesModeGuard
// verifies -lockfile-scan-work-dir alone satisfies the "at least one mode"
// guard (bindregistry_cmd.go's own comment names the exact line) without
// requiring any other flag.
func TestRunBindRegistryWithDeps_LockfileScanFlagAloneSatisfiesModeGuard(t *testing.T) {
	dir := newIntreeTestRepo(t)
	clearManifestEnv(t)

	var stdout bytes.Buffer
	rc := runBindRegistry([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout)
	if rc != 0 {
		t.Fatalf("runBindRegistry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
}

// TestRenderEnvExports_ShellMetacharactersDoNotExecute pins the fix for the
// command-injection finding on renderEnvExports: a value carrying shell
// metacharacters (`$(...)`, backtick, embedded single quote) must round-trip
// through a real `source` unexpanded, byte-for-byte, rather than being
// interpreted as command substitution. This is the injection vector flagged
// against issue #3259 (a host-rooted route's derived path, sourced from a
// repo's own committed, therefore untrusted, .npmrc).
func TestRenderEnvExports_ShellMetacharactersDoNotExecute(t *testing.T) {
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	cases := []struct {
		name  string
		value string
	}{
		{"dollar-paren-command-substitution", "http://127.0.0.1:1234/r0/$(id)/"},
		{"backtick-command-substitution", "http://127.0.0.1:1234/r0/`id`/"},
		{"embedded-single-quote", "http://127.0.0.1:1234/r0/it's/here/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := renderEnvExports([]ecosystem.EnvExport{
				{Name: "npm_config_registry", Value: tc.value},
			})

			dir := t.TempDir()
			envFile := filepath.Join(dir, "env.sh")
			if err := os.WriteFile(envFile, []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(path, "-c", `source "$1"; printf '%s' "$npm_config_registry"`, "--", envFile)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sourcing rendered exports failed: %v (output=%q)", err, out)
			}
			if got := string(out); got != tc.value {
				t.Errorf("sourced npm_config_registry = %q, want unexpanded %q (rendered=%q)", got, tc.value, rendered)
			}
		})
	}
}
