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
	"spindrift.dev/launcher/internal/registrymanifest"
)

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
		func(string, int) error { spawnCalled = true; return nil },
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
		func(string, int) error {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if want := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed:"; !strings.HasPrefix(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to start with %q", stdout.String(), want)
	}
	if want := "cargo, npm, pnpm, yarn, and gradle will fall back to the public registry"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
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
		func(string, int) error { spawnCalled = true; return nil },
		lookPathMissing,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
		func(string, int) error { spawnCalled = true; return nil },
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
		`export GOPROXY="http://127.0.0.1:` + forwarderPortStr + `/r0"`,
		`export npm_config_registry="http://127.0.0.1:` + forwarderPortStr + `/r0/"`,
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
	if want := bindregistry.CargoConfigTOML(bindregistry.ForwarderPort, "r0"); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
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
		func(string, int) error { return nil },
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
		`export GOPROXY="http://127.0.0.1:` + forwarderPortStr + `/artifactory-go"`,
		`export npm_config_registry="http://127.0.0.1:` + forwarderPortStr + `/artifactory-go/"`,
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
	if want := bindregistry.CargoConfigTOML(bindregistry.ForwarderPort, "artifactory-go"); string(cargoConfig) != want {
		t.Errorf("cargo config.toml = %q, want %q", cargoConfig, want)
	}

	gradleScript, err := os.ReadFile(filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	if want := bindregistry.GradleInitScript(bindregistry.ForwarderPort, "artifactory-go"); string(gradleScript) != want {
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
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
		func(string, int) error { return nil },
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
	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + forwarderPortStr + " — cargo bound to it via " + cargoHome + "/config.toml, npm bound to it via npm_config_registry, pnpm bound to it via pnpm_config_registry, yarn berry bound to it via YARN_NPM_REGISTRY_SERVER, and gradle bound to it via " + gradleUserHome + "/init.d/spindrift-registry-proxy.init.gradle"
	if !strings.Contains(stdout.String(), wantForwarderLine) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), wantForwarderLine)
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
		func(string, int) error { return nil },
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
	if want := bindregistry.GradleInitScript(bindregistry.ForwarderPort, "r0"); string(got) != want {
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
		func(string, int) error { return nil },
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
	if want := bindregistry.GradleInitScript(bindregistry.ForwarderPort, "r0"); string(got) != want {
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
		func(string, int) error { return nil },
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
		func(string, int) error { return nil },
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
		func(string, int) error { return nil },
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
		func(string, int) error { spawnCalled = true; return nil },
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
		func(string, int) error { return errors.New("boom") },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy Forwarder for endpoint unix://" + socketPath + " failed to start: boom — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
		func(string, int) error { return nil },
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
		func(string, int) error { return nil },
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
		func(string, int) error { spawnCalled = true; return nil },
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
	spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, listenPort int) error {
		gotHost, gotUpstreamPort, gotSecret, gotListenPort = upstreamHost, upstreamPort, secret, listenPort
		return nil
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
		func(string, int) error {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return nil
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

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotStr := string(got)
	for _, want := range []string{
		`export GOPROXY="http://127.0.0.1:` + forwarderPortStr + `/r0"`,
		`export npm_config_registry="http://127.0.0.1:` + forwarderPortStr + `/r0/"`,
	} {
		if !strings.Contains(gotStr, want) {
			t.Errorf("bindings env output = %q, want it to contain %q", gotStr, want)
		}
	}

	cargoConfig, err := os.ReadFile(filepath.Join(cargoHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cargo config: %v", err)
	}
	if want := bindregistry.CargoConfigTOML(bindregistry.ForwarderPort, "r0"); string(cargoConfig) != want {
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
		func(string, int) error {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
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
	spawnHTTPForwarder = func(string, int, string, int) error { return nil }
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
		func(string, int) error {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return nil
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
		func(string, int) error {
			t.Fatal("spawn should not be called with a non-numeric manifest port")
			return nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:https has a non-numeric port — cargo, npm, pnpm, yarn, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
}

// TestRunBindRegistryWithDeps_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes
// is the direct AC1 test (issue #3141): a single invocation running BOTH
// intree-apply mode and bindings mode (their entrypoint.sh call sites run at
// different points in main(), but the verb itself never assumed only one
// runs per invocation) must resolve REGISTRY_PROXY_MANIFEST and probe/spawn
// the Forwarder exactly once, sharing that one result across both modes --
// not once per mode. probe always reports "not ready" so EnsureForwarderReady
// would call spawn on every independent attempt; if the gate were resolved
// twice (once per mode, the pre-#3141 shape), spawn would be called twice.
func TestRunBindRegistryWithDeps_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"})

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalls := 0
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false }, // never ready
		func(string, int) error { spawnCalls++; return nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times across one invocation running both intree-apply and bindings mode, want exactly 1 (issue #3141's shared gate)", spawnCalls)
	}
	if got := strings.Count(stdout.String(), "did not start listening"); got != 2 {
		t.Errorf("stdout = %q, want the timeout warning exactly twice (once per mode, from the one shared gate result), got %d", stdout.String(), got)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeCargoConfigContent {
		t.Errorf("cargo config.toml changed, want byte-for-byte unchanged when the Forwarder never becomes ready")
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when the Forwarder never becomes ready")
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
		func(string, int) error { return nil },
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

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
		func(string, int) error { spawnCalled = true; return nil },
		lookPathFound,
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
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:"+forwarderPortStr+"/r0/index/") {
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

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
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

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
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

// TestRunBindRegistryWithDeps_IntreeApplyMultipleRegistriesWritesBothPlaceholders
// verifies a cargo config with two [registries.*] tables, both rewritten,
// produces both env export lines.
func TestRunBindRegistryWithDeps_IntreeApplyMultipleRegistriesWritesBothPlaceholders(t *testing.T) {
	dir := newIntreeTestRepo(t)
	content := "[registries.first-one]\n" +
		"index = \"sparse+https://upstream.example/first/index/\"\n\n" +
		"[registries.second-one]\n" +
		"index = \"sparse+https://upstream.example/second/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", content)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
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

// TestRunBindRegistryWithDeps_IntreeApplyTwoRouteManifestPerRoutePlaceholdersDeduped
// covers issue #3142's multi-route intree-apply: a two-route manifest with
// distinct prefixes, one route carrying an explicit CargoRegistries list and
// one relying on the ParseCargoRegistryNames fallback against its own
// prefixed LocalURL, produces one placeholder export per registry name --
// deduped when both routes would otherwise name the same registry, first
// (table-order) route's claim wins.
func TestRunBindRegistryWithDeps_IntreeApplyTwoRouteManifestPerRoutePlaceholdersDeduped(t *testing.T) {
	dir := newIntreeTestRepo(t)
	// "shared-registry" is claimed explicitly by route A (CargoRegistries)
	// but also has a [registries.shared-registry] table rewritten to route
	// B's own LocalURL -- proving A's explicit claim wins and B's fallback
	// scan doesn't double-export it. "fallback-registry" has no manifest
	// CargoRegistries anywhere, so it's only ever found via B's fallback
	// scan.
	content := "[registries.shared-registry]\n" +
		"index = \"sparse+https://host-b.example/shared/index/\"\n\n" +
		"[registries.fallback-registry]\n" +
		"index = \"sparse+https://host-b.example/fallback/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", content)

	routeA := registrymanifest.Route{Prefix: "r0", UpstreamHost: "host-a.example", CargoRegistries: []string{"shared-registry"}}
	routeB := registrymanifest.Route{Prefix: "r1", UpstreamHost: "host-b.example"}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, routeA, routeB)

	intreeBindingsOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", intreeBindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	got, err := os.ReadFile(intreeBindingsOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	want := "export CARGO_REGISTRIES_SHARED_REGISTRY_TOKEN=\"" + bindregistry.CargoPlaceholderToken + "\"\n" +
		"export CARGO_REGISTRIES_FALLBACK_REGISTRY_TOKEN=\"" + bindregistry.CargoPlaceholderToken + "\"\n"
	if string(got) != want {
		t.Errorf("intree bindings env output = %q, want %q (exactly one export per registry name, no duplicate)", got, want)
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)

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
		func(string, int) error { return nil },
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
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:"+forwarderPortStr+"/r0/index/") {
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
		func(string, int) error { return nil },
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
		func(string, int) error { return nil },
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
		func(string, int) error {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return nil
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
		func(string, int) error { return nil },
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
		func(string, int) error { spawnCalled = true; return nil },
		lookPathMissing,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n"
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", intreeUpstreamRoute)

	origSpawnHTTPForwarder := spawnHTTPForwarder
	var gotHost, gotSecret string
	var gotUpstreamPort, gotListenPort int
	spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, listenPort int) error {
		gotHost, gotUpstreamPort, gotSecret, gotListenPort = upstreamHost, upstreamPort, secret, listenPort
		return nil
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
		func(string, int) error {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return nil
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

	got, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "upstream.example") {
		t.Errorf("rewritten content still mentions upstream.example: %s", got)
	}
	if !strings.Contains(string(got), "sparse+http://127.0.0.1:"+forwarderPortStr+"/r0/index/") {
		t.Errorf("rewritten content missing expected rewrite: %s", got)
	}
	if !intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
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
		func(string, int) error {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return nil
		},
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n"
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	clearManifestEnv(t)

	cargoBinding := bindregistry.InTreeBindings()[0]
	outcome, err := bindregistry.ApplyInTreeBinding(dir, cargoBinding, []bindregistry.HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:" + forwarderPortStr}})
	if err != nil || outcome != bindregistry.ApplyApplied {
		t.Fatalf("ApplyInTreeBinding (setup) = (%v, %v), want (%v, nil)", outcome, err, bindregistry.ApplyApplied)
	}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "revert",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on revert"); return false },
		func(string, int) error { t.Fatal("spawn should not be called on revert"); return nil },
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
		t.Errorf("cargo config.toml = %q, want restored to %q", got, intreeCargoConfigContent)
	}
	if intreeSkipWorktreeSet(t, dir, ".cargo/config.toml") {
		t.Error("skip-worktree bit still set, want it cleared after revert")
	}
}

// TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllFourRows is the
// multi-row happy-path test a review finding called out as missing: with all
// four in-tree config files (cargo, npm, yarn, pnpm) tracked and present,
// apply must rewrite and skip-worktree-tag every one of them, and a
// following revert must restore every one of them -- not just the first
// row, the only shape every existing intree test here exercised.
func TestRunBindRegistryWithDeps_IntreeApplyAndRevertAllFourRows(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	writeTrackedIntreeFile(t, dir, ".npmrc", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, ".yarnrc.yml", intreeNpmStyleConfigContent)
	writeTrackedIntreeFile(t, dir, "pnpm-workspace.yaml", intreeNpmStyleConfigContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	relPaths := []string{".cargo/config.toml", ".npmrc", ".yarnrc.yml", "pnpm-workspace.yaml"}

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
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
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> cargo config .cargo/config.toml not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
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
	if err := os.MkdirAll(filepath.Join(dir, ".cargo", "config.toml"), 0o755); err != nil {
		t.Fatalf("mkdir config.toml as a directory: %v", err)
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
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo config .cargo/config.toml exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
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
	if err := os.MkdirAll(filepath.Join(dir, ".cargo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".cargo", "config.toml"), []byte(intreeCargoConfigContent), 0o644); err != nil {
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
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo config .cargo/config.toml exists but is not tracked by git — skipping the in-tree registry rewrite for it"
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", intreeCargoConfigContent)
	runGitCmd(t, dir, "update-index", "--skip-worktree", "--", ".cargo/config.toml")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo config .cargo/config.toml already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status"
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
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", "[source.crates-io]\nreplace-with = \"proxy\"\n\n[source.proxy]\nregistry = \"sparse+https://other.example/index/\"\n")

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) error { return nil },
		lookPathFound,
		registryProxyForwarderTimeout, registryProxyForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo config .cargo/config.toml no longer references upstream host upstream.example — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly"
	if got := strings.Count(stdout.String(), want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout.String(), want, got)
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
		func(string, int) error { t.Fatal("spawn should not be called on a validation error"); return nil },
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
				func(string, int) error { return nil },
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

// TestCargoRegistryExportsForRoutes_SkipsRouteWithCollidedUpstreamHost covers
// a reviewer finding on issue #3142: a route whose UpstreamHost
// buildIntreeHostRewrites already dropped as a collision must not still
// contribute its own manifest-declared CargoRegistries placeholders, since
// nothing on disk was ever rewritten to that route's LocalURL -- the
// collision means ApplyInTreeBinding skipped the rewrite for it entirely.
func TestCargoRegistryExportsForRoutes_SkipsRouteWithCollidedUpstreamHost(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example", CargoRegistries: []string{"collided-one"}},
		{Prefix: "r1", UpstreamHost: "shared.example", CargoRegistries: []string{"collided-two"}},
		{Prefix: "r2", UpstreamHost: "distinct.example", CargoRegistries: []string{"valid-registry"}},
	}
	_, collisions := buildIntreeHostRewrites(routes, 9999)

	var stdout bytes.Buffer
	exports := cargoRegistryExportsForRoutes(&stdout, routes, 9999, "", collisions)

	if len(exports) != 1 || exports[0].Name != bindregistry.CargoRegistryEnvVarName("valid-registry") {
		t.Errorf("exports = %+v, want exactly one export for valid-registry, none for the collided routes", exports)
	}
}

// TestCargoRegistryExportsForRoutes_WarnsForUndeclaredParsedRegistry covers a
// reviewer finding on issue #3142: a route with a non-empty declared
// CargoRegistries list still must be checked against the rewritten content's
// own [registries.*] tables, and a table naming this route's own LocalURL
// but absent from the declared list must produce a stdout warning naming the
// registry and the route's prefix -- not silently vanish. The declared
// list's exports themselves are unaffected: no merge, just a warning.
func TestCargoRegistryExportsForRoutes_WarnsForUndeclaredParsedRegistry(t *testing.T) {
	route := registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example", CargoRegistries: []string{"declared-registry"}}
	content := "[registries.undeclared-registry]\n" +
		"index = \"sparse+http://127.0.0.1:9999/r0/index/\"\n"

	var stdout bytes.Buffer
	exports := cargoRegistryExportsForRoutes(&stdout, []registrymanifest.Route{route}, 9999, content, nil)

	if len(exports) != 1 || exports[0].Name != bindregistry.CargoRegistryEnvVarName("declared-registry") {
		t.Errorf("exports = %+v, want exactly the declared-registry export, declared list stays outright-wins", exports)
	}
	want := "==> WARNING: cargo registry \"undeclared-registry\" is rewritten under route prefix \"r0\" but not declared in that route's cargo-registries"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}
