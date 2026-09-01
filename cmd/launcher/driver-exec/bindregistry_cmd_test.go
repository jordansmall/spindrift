package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
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

// TestIsBindRegistryInvocation mirrors
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
// prints the exact warning wording below, exits 0, calls probe (issue
// #2931 finding: the LookPath check only gates the spawn path, so it must
// run after probe, not before) but never spawn (it returns before
// EnsureForwarderReady's own spawn call), and leaves bindings-env-output
// untouched.
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
	wantWarning := "==> WARNING: " + socketPath + " is mounted but socat is not on PATH — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
// bindings-env-output file (Go + npm-family exports) and the cargo
// config.toml under a fake $CARGO_HOME.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings(t *testing.T) {
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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
		`export GOPROXY="http://127.0.0.1:27182"`,
		`export npm_config_registry="http://127.0.0.1:27182/"`,
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
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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
	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:27182 — cargo bound to it via " + cargoHome + "/config.toml, npm bound to it via npm_config_registry, pnpm bound to it via pnpm_config_registry, yarn berry bound to it via YARN_NPM_REGISTRY_SERVER, and gradle bound to it via " + gradleUserHome + "/init.d/spindrift-registry-proxy.init.gradle"
	if !strings.Contains(stdout.String(), wantForwarderLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantForwarderLine)
	}
}

// TestRunBindRegistryWithDeps_AlreadyListeningWritesGradleInitScript verifies
// bindings mode also writes the Gradle init script under
// $GRADLE_USER_HOME/init.d/, gated the same all-or-nothing way the cargo
// config.toml write above already is.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesGradleInitScript(t *testing.T) {
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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

	got, err := os.ReadFile(filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := bindregistry.GradleInitScript(27182); string(got) != want {
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
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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

	port := 27183
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", strconv.Itoa(port),
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(filepath.Join(home, ".gradle", "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := bindregistry.GradleInitScript(port); string(got) != want {
		t.Errorf("gradle init script = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_TimeoutSkipsGradleInitScript verifies the
// gradle init script write shares the cargo config.toml write's
// all-or-nothing readiness gate: a Forwarder that never becomes ready must
// leave $GRADLE_USER_HOME/init.d/ completely untouched, not just
// bindings-env-output.
func TestRunBindRegistryWithDeps_TimeoutSkipsGradleInitScript(t *testing.T) {
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	// Pinned so that if the readiness gate this test exercises ever
	// regressed and fell through to the cargo home resolve, it would write
	// into an isolated temp dir rather than the real developer's
	// $HOME/.cargo/config.toml.
	t.Setenv("CARGO_HOME", t.TempDir())

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	port := 27184
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", strconv.Itoa(port),
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) error { return nil },
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
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
	withFakeSocatOnPath(t)
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
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
// timeout warning wording entrypoint.sh used, and leaves
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
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
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
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
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
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
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
	// Keep the socket file on disk -- UnixListener.Close unlinks it (see above).
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

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

// withGitOnlyOnPath replaces PATH with a fresh directory holding only a
// symlink to the real git binary -- unlike withFakeSocatOnPath's PATH
// prepend, this guarantees exec.LookPath("socat") fails deterministically
// (no real socat anywhere on PATH, regardless of the ambient sandbox) while
// still letting the intree helpers below (newIntreeTestRepo,
// writeTrackedIntreeFile, intreeSkipWorktreeSet) shell out to git.
func withGitOnlyOnPath(t *testing.T) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("exec.LookPath(git): %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("symlink git into fake PATH dir: %v", err)
	}
	t.Setenv("PATH", dir)
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

// TestRunBindRegistryWithDeps_IntreeApplyDeadForwarderLeavesFileUntouched is
// the AC5 all-or-nothing-gate test (issue #2932 brief §3): given a tracked
// cargo config referencing the upstream host and a fake probe that never
// reports ready (Forwarder dead), apply must leave the file byte-for-byte
// unchanged and the skip-worktree bit unset -- no partial rewrite.
func TestRunBindRegistryWithDeps_IntreeApplyDeadForwarderLeavesFileUntouched(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool { return false },
		func(string, int) error { return nil },
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

func TestRunBindRegistryWithDeps_IntreeApplyReadyRewritesAndHidesFromGit(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
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

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:27182/index/") {
		t.Errorf("rewritten content missing expected rewrite: %s", got)
	}
	if !intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// intreeCargoRegistryConfigContent is a cargo config.toml carrying a single
// [registries.NAME] table whose index references upstream.example -- the
// table shape ParseCargoRegistryNames looks for, distinct from
// intreeCargoConfigContent's [source.*] tables above, which
// ParseCargoRegistryNames deliberately ignores.
const intreeCargoRegistryConfigContent = "[registries.my-registry]\nindex = \"sparse+https://upstream.example/my-registry/index/\"\n"

// TestRunBindRegistryWithDeps_IntreeApplyWritesCargoRegistryPlaceholderEnvOutput
// verifies apply mode, given -intree-bindings-env-output and a cargo config
// with one rewritten [registries.NAME] table, writes a sourceable env file
// exporting the fixed cargo placeholder token under that registry's
// CARGO_REGISTRIES_<NAME>_TOKEN var name (issue #3053 slice 2).
func TestRunBindRegistryWithDeps_IntreeApplyWritesCargoRegistryPlaceholderEnvOutput(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoRegistryConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(intreeBindingsOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	want := "export CARGO_REGISTRIES_MY_REGISTRY_TOKEN=\"" + bindregistry.CargoPlaceholderToken + "\"\n"
	if string(got) != want {
		t.Errorf("intree bindings env output = %q, want %q", got, want)
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyNoRegistriesTableWritesEmptyEnvOutput
// verifies apply mode with -intree-bindings-env-output still writes the file
// -- empty, since intreeCargoConfigContent's [source.*] tables carry no
// [registries.*] table for ParseCargoRegistryNames to find -- rather than
// leaving it unwritten, mirroring how -bindings-env-output is always written
// in bindings mode regardless of whether any exports exist.
func TestRunBindRegistryWithDeps_IntreeApplyNoRegistriesTableWritesEmptyEnvOutput(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(intreeBindingsOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("intree bindings env output = %q, want empty (no [registries.*] table rewritten)", got)
	}
}

func TestRunBindRegistryWithDeps_IntreeApplyMultipleRegistriesWritesBothPlaceholders(t *testing.T) {
	dir := newIntreeTestRepo(t)
	content := "[registries.first-one]\n" +
		"index = \"sparse+https://upstream.example/first/index/\"\n\n" +
		"[registries.second-one]\n" +
		"index = \"sparse+https://upstream.example/second/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", content)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(intreeBindingsOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	for _, want := range []string{
		"export CARGO_REGISTRIES_FIRST_ONE_TOKEN=\"" + bindregistry.CargoPlaceholderToken + "\"\n",
		"export CARGO_REGISTRIES_SECOND_ONE_TOKEN=\"" + bindregistry.CargoPlaceholderToken + "\"\n",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("intree bindings env output = %q, want it to contain %q", got, want)
		}
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyEmptySocketIsNoOp verifies an empty
// -registry-proxy-socket value (entrypoint.sh always passes the flag, even
// when the registry proxy is disabled) silently no-ops apply mode rather
// than erroring or touching the tracked file.
func TestRunBindRegistryWithDeps_IntreeApplyEmptySocketIsNoOp(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", "",
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called when the socket path is empty"); return false },
		func(string, int) error {
			t.Fatal("spawn should not be called when the socket path is empty")
			return nil
		},
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
		t.Errorf("cargo config.toml changed, want untouched when the socket path is empty")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyEmptyUpstreamHostIsNoOp verifies an
// unset/empty REGISTRY_PROXY_UPSTREAM_HOST silently no-ops apply mode before
// ever consulting the Forwarder.
func TestRunBindRegistryWithDeps_IntreeApplyEmptyUpstreamHostIsNoOp(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_UPSTREAM_HOST is empty")
			return false
		},
		func(string, int) error {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_UPSTREAM_HOST is empty")
			return nil
		},
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
		t.Errorf("cargo config.toml changed, want untouched when REGISTRY_PROXY_UPSTREAM_HOST is empty")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplySocatMissingWarnsAndSkipsRewrite
// mirrors TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings for
// the intree-apply path (reviewer finding on issue #2932): given a real
// mounted socket, a probe reporting nothing listening yet, and no socat on
// PATH, apply must print a socat-specific warning, exit 0, and leave the
// tracked file byte-for-byte untouched -- rather than falling through to
// EnsureForwarderReady's generic "failed to start" warning.
func TestRunBindRegistryWithDeps_IntreeApplySocatMissingWarnsAndSkipsRewrite(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	// Swap PATH down to git-only right before the call under test: the
	// setup above (repo init, tracked commit) and the intreeSkipWorktreeSet
	// check below both still need git, only runBindRegistryWithDeps' own
	// exec.LookPath("socat") must fail.
	withGitOnlyOnPath(t)

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool { probeCalled = true; return false },
		func(string, int) error { spawnCalled = true; return nil },
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: " + socketPath + " is mounted but socat is not on PATH — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n"
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

// TestRunBindRegistryWithDeps_IntreeRevertRestoresAppliedFile verifies
// revert mode, given a previously-applied file (rewritten content,
// skip-worktree bit set), restores the original tracked content and clears
// the skip-worktree bit -- with no socket/port/host flags at all, since
// revert is a pure git operation.
func TestRunBindRegistryWithDeps_IntreeRevertRestoresAppliedFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	cargoBinding := bindregistry.InTreeBindings()[0]
	applied, _, err := bindregistry.ApplyInTreeBinding(dir, cargoBinding, "upstream.example", "http://127.0.0.1:27182")
	if err != nil || !applied {
		t.Fatalf("ApplyInTreeBinding (setup) = (%v, %v), want (true, nil)", applied, err)
	}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "revert",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) error { return nil },
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
		t.Errorf("cargo config.toml = %q, want restored to %q", got, intreeCargoConfigContent)
	}
	if intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
		t.Error("skip-worktree bit still set, want it cleared after revert")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllFourRows covers the
// multi-row happy path: with all four in-tree config files (cargo, npm, yarn,
// pnpm) tracked and present, apply must rewrite and skip-worktree-tag every
// one of them, and a following revert must restore every one of them -- not
// just the first row, the only shape every other intree test here exercises.
func TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllFourRows(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, ".yarnrc.yml", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, "pnpm-workspace.yaml", intreeNpmStyleConfigContent)
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	relPaths := []string{".cargo/config.toml", ".npmrc", ".yarnrc.yml", "pnpm-workspace.yaml"}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
		if !strings.Contains(string(got), "127.0.0.1:27182") {
			t.Errorf("%s missing rewritten local Forwarder URL after apply: %s", rel, got)
		}
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set after apply", rel)
		}
	}

	wantAfterRevert := map[string]string{
		".cargo/config.toml":  intreeCargoConfigContent,
		".npmrc":              intreeNpmStyleConfigContent,
		".yarnrc.yml":         intreeNpmStyleConfigContent,
		"pnpm-workspace.yaml": intreeNpmStyleConfigContent,
	}

	stdout.Reset()
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", "revert",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) error { return nil },
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
		if string(got) != wantAfterRevert[rel] {
			t.Errorf("%s = %q after revert, want restored to %q", rel, got, wantAfterRevert[rel])
		}
		if intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit still set after revert", rel)
		}
	}

	// Re-apply pass (AC4): the revert/re-apply choreography around branch
	// recovery must cover all four rows, not just the apply/revert pair
	// tested above -- a re-apply after revert must rewrite and re-tag every
	// row again, exactly as the first apply did.
	stdout.Reset()
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
		if !strings.Contains(string(got), "127.0.0.1:27182") {
			t.Errorf("%s missing rewritten local Forwarder URL after re-apply: %s", rel, got)
		}
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set after re-apply", rel)
		}
	}
}

// newIntreeUnmergedNpmTestRepo builds a repo with plain tracked cargo, yarn,
// and pnpm config files, but an .npmrc left genuinely unmerged (UU) -- the
// same fixture shape as bindregistry's own unexported newUnmergedTestRepo
// (intreebinding_test.go), replicated here since that helper is unexported
// in a different package. `git update-index --skip-worktree`
// fails with exit 128 on the unmerged .npmrc, giving ApplyInTreeBinding a
// genuine per-row failure to prove the sibling rows aren't blocked by it.
func newIntreeUnmergedNpmTestRepo(t *testing.T) string {
	t.Helper()
	dir := newIntreeTestRepo(t)

	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
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
	t.Setenv("REGISTRY_PROXY_UPSTREAM_HOST", "upstream.example")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-registry-proxy-socket", socketPath,
		"-forwarder-port", "27182",
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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

	for _, rel := range []string{".cargo/config.toml", ".yarnrc.yml", "pnpm-workspace.yaml"} {
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

// TestRunBindRegistryWithDeps_IntreeBindingsEnvOutputRequiresApply mirrors the
// other flag-pair validation errors in runBindRegistryWithDeps.
func TestRunBindRegistryWithDeps_IntreeBindingsEnvOutputRequiresApply(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on a validation error"); return false },
		func(string, int) error { t.Fatal("spawn should not be called on a validation error"); return nil },
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

// TestRunBindRegistryWithDeps_IntreeFlagValidation mirrors
// TestRunBindRegistry_MissingFlagsErrors' style for the intree flag pair.
func TestRunBindRegistryWithDeps_IntreeFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"intree-work-dir without intree-action", []string{"-intree-work-dir", t.TempDir()}},
		{"intree-action without intree-work-dir", []string{"-intree-action", "apply"}},
		{"bogus intree-action", []string{"-intree-work-dir", t.TempDir(), "-intree-action", "bogus"}},
		{"registry-proxy-socket alone without bindings-env-output or intree-action=apply", []string{"-registry-proxy-socket", "/tmp/does-not-matter.sock"}},
		{"intree-bindings-env-output without intree-action=apply", []string{"-intree-bindings-env-output", filepath.Join(t.TempDir(), "intree-bindings.env")}},
		{"intree-bindings-env-output with intree-action=revert", []string{"-intree-work-dir", t.TempDir(), "-intree-action", "revert", "-intree-bindings-env-output", filepath.Join(t.TempDir(), "intree-bindings.env")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout bytes.Buffer
			rc := runBindRegistryWithDeps(c.args, &stdout,
				func(int) bool { return true },
				func(string, int) error { return nil },
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
