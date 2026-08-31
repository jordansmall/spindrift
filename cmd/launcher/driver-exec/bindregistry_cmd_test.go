package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
)

// withFakeSocatOnPath prepends a directory holding a fake, no-op executable
// named "socat" onto PATH, so runBindRegistryBindings' own `exec.LookPath`
// check succeeds even in a sandbox with no real socat installed (e.g. the
// Nix go-test sandbox, which doesn't list socat as a build input for this
// package) -- callers here always inject a fake SpawnFunc too, so this stub
// is never actually executed, only found.
func withFakeSocatOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "socat"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake socat: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

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
// (-work-dir/-ecosystem-env-output and -registry-proxy-socket/
// -bindings-env-output) are each either both given or both omitted, and that
// omitting all four is itself an error -- there must be at least one
// complete pair.
func TestRunBindRegistry_MissingFlagsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"all four empty", nil},
		{"work-dir without ecosystem-env-output", []string{"-work-dir", t.TempDir()}},
		{"ecosystem-env-output without work-dir", []string{"-ecosystem-env-output", filepath.Join(t.TempDir(), "nudge.env")}},
		{"registry-proxy-socket without bindings-env-output", []string{"-registry-proxy-socket", filepath.Join(t.TempDir(), "sock")}},
		{"bindings-env-output without registry-proxy-socket", []string{"-bindings-env-output", filepath.Join(t.TempDir(), "bindings.env")}},
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

// TestRunBindRegistryWithDeps_SocketAbsentIsNoOp verifies bindings mode
// silently no-ops (exit 0, no stdout, bindings-env-output left untouched)
// when -registry-proxy-socket doesn't exist -- mirrors the old bash
// `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ] || return 0` short-circuit.
func TestRunBindRegistryWithDeps_SocketAbsentIsNoOp(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "registry-proxy.sock")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { spawnCalled = true; return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when socket is absent")
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when socket is absent")
	}
}

// TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings verifies the
// exec.LookPath("socat") early-return branch (issue #2931's BLOCKING finding
// -- this branch had zero Go coverage; every other test in this file uses
// withFakeSocatOnPath to make the LookPath check succeed instead): given a
// real mounted socket, a probe reporting nothing is listening yet (so
// something would need spawning), and no socat on PATH, bindings mode
// prints the exact warning wording
// tests/entrypoint-registry-proxy-gradle-binding.bats:188 asserts on, exits
// 0, calls probe (issue #2931 finding: the LookPath check only gates the
// spawn path, so it must run after probe, not before) but never spawn (it
// returns before EnsureForwarderReady's own spawn call), and leaves
// bindings-env-output untouched.
func TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings(t *testing.T) {
	// An empty temp dir on PATH guarantees exec.LookPath("socat") fails
	// deterministically, regardless of whether the ambient sandbox happens to
	// have a real socat installed.
	t.Setenv("PATH", t.TempDir())

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

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return false },
		func(string, int) error { spawnCalled = true; return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: " + socketPath + " is mounted but socat is not on PATH — cargo, npm, pnpm, and yarn will fall back to the public registry\n"
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
// double-spawn-prevention path at the CLI-integration level: given a real
// unix-socket file and a fake probe that reports "already listening"
// immediately, spawn is never called, and the ready path writes the
// bindings-env-output file (Go + npm-family exports plus FORWARDER_READY)
// and the cargo config.toml under a fake $CARGO_HOME.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings(t *testing.T) {
	withFakeSocatOnPath(t)
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

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { spawnCalled = true; return nil },
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
		`FORWARDER_READY="1"`,
		`export GOPROXY="http://127.0.0.1:27182"`,
		`export npm_config_registry="http://127.0.0.1:27182/"`,
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("bindings env output = %q, want it to contain %q", gotStr, want)
		}
	}

	cargoConfig, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := bindregistry.CargoConfigTOML(27182); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines verifies
// the happy path restores the two success log lines
// agent/entrypoint.sh's deleted phase_go_binding/phase_registry_proxy_forwarder
// printed (issue #2931's BLOCKING finding) -- the Go port had silently
// dropped both, leaving the success path printing nothing to stdout.
func TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines(t *testing.T) {
	withFakeSocatOnPath(t)
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

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
	t.Setenv("GOTOOLCHAIN", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	wantGoLine := "==> go bound to it via GOPROXY=http://127.0.0.1:27182"
	if !strings.Contains(stdout.String(), wantGoLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantGoLine)
	}
	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:27182 — cargo bound to it via " + cargoHome + "/config.toml, npm bound to it via npm_config_registry, pnpm bound to it via pnpm_config_registry, and yarn berry bound to it via YARN_NPM_REGISTRY_SERVER"
	if !strings.Contains(stdout.String(), wantForwarderLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantForwarderLine)
	}
}

// TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud verifies that
// when both $CARGO_HOME and $HOME are unset, bindings mode fails loud
// (non-zero exit, error on stdout) rather than silently resolving cargo's
// config.toml to a relative ".cargo" path under the process's cwd --
// matching the bash this replaced, which ran under `set -u` and would have
// died on the unset $HOME expansion instead of silently going relative.
func TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud(t *testing.T) {
	withFakeSocatOnPath(t)
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
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
// timeout warning wording entrypoint.sh:546 used, and leaves
// bindings-env-output unwritten. Passes a small timeout/pollInterval (rather
// than the real registryProxyForwarderTimeout/PollInterval constants,
// production callers still use those unchanged) so this test's own
// wall-clock cost stays well under a second instead of eating the real 5s.
func TestRunBindRegistryWithDeps_TimeoutWarnsAndSkipsBindings(t *testing.T) {
	withFakeSocatOnPath(t)
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

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	port := 27183
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", strconv.Itoa(port),
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) error { spawnCalled = true; return nil },
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if !spawnCalled {
		t.Error("spawn was never called, want it called once probe reports not-yet-ready")
	}
	wantWarning := "registry proxy Forwarder did not start listening on 127.0.0.1:" + strconv.Itoa(port)
	if !strings.Contains(stdout.String(), wantWarning) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched on timeout")
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
	withFakeSocatOnPath(t)
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
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
	withFakeSocatOnPath(t)
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
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
// exec.LookPath("socat") PATH check only gates the *spawn* path (issue
// #2931 finding: the LookPath check used to run unconditionally before the
// readiness probe, so an already-ready Forwarder -- nothing left to spawn --
// still failed bindings mode entirely, warning "socat is not on PATH" and
// emitting zero bindings, if socat had since been removed from PATH). With
// probe reporting "already listening" immediately and no socat anywhere on
// PATH, bindings mode must still compute and write bindings, never emit the
// socat-missing warning, and never call spawn.
func TestRunBindRegistryWithDeps_AlreadyListeningSkipsSocatCheck(t *testing.T) {
	// An empty temp dir on PATH guarantees exec.LookPath("socat") would fail
	// if it ran at all -- this test asserts it must not run when probe
	// already reports ready.
	t.Setenv("PATH", t.TempDir())

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

	cargoHome := t.TempDir()
	t.Setenv("CARGO_HOME", cargoHome)
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
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return true },
		func(string, int) error { spawnCalled = true; return nil },
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
