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
	"spindrift.dev/launcher/internal/registryvocab"
)

// A hermetic $HOME with $CARGO_HOME left unset (issue #3201): since cargo
// binds via RepoAwareHomeConfig, any -intree-action=apply run with a ready
// gate writes $CARGO_HOME/config.toml (or $HOME/.cargo/config.toml), which
// would otherwise land in the real ambient one. Tests that t.Setenv their own
// HOME/CARGO_HOME scope back to this default at teardown.
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

// lookPathFound stubs resolveRegistryProxyGate's lookPathFunc dep to report
// socat found without touching the real PATH (issue #3141's CI fix): the Nix
// go-test sandbox has no socat, which let this shared-gate test pass locally
// and fail in-box. Callers always inject a fake SpawnFunc too, so the returned
// path is never executed, only "found".
func lookPathFound(string) (string, error) { return "/fake/bin/socat", nil }

// lookPathMissing deterministically reports socat absent, replacing the old
// trick of emptying $PATH, which depended on no other test having widened PATH
// and on exec.LookPath being the thing actually consulted.
func lookPathMissing(string) (string, error) { return "", exec.ErrNotFound }

// shortUnixSocketPath avoids t.TempDir(), whose path embeds the full subtest
// name: AF_UNIX's sun_path is capped at 108 bytes on Linux, and t.TempDir()'s
// path here regularly blows past that, failing net.Listen("unix", ...) with
// EINVAL.
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
// naming a unix endpoint, the replacement for the deleted
// -registry-proxy-socket/-forwarder-port flags (issue #3141).
func setUnixManifestEnv(t *testing.T, socketPath string, routes ...registrymanifest.Route) {
	t.Helper()
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewUnixEndpoint(socketPath), Routes: routes}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)
}

// setTCPManifestEnv is the TCP-endpoint counterpart (issue #3111's TCP-fallback
// transport), replacing the deleted
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

// clearManifestEnv sets REGISTRY_PROXY_MANIFEST explicitly empty,
// registrymanifest.Parse's ErrAbsent shape, rather than relying on the ambient
// test environment happening to lack the var.
func clearManifestEnv(t *testing.T) {
	t.Helper()
	t.Setenv(registrymanifest.EnvVar, "")
}

// forwarderPortStr is bindregistry.ForwarderPort (issue #3141's single port
// declaration) rendered once for the exact-wording assertions below.
var forwarderPortStr = strconv.Itoa(bindregistry.ForwarderPort)

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

// At least one complete mode must be requested. -bindings-env-output alone is
// sufficient on its own since issue #3141, so it no longer appears among the
// error cases here.
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

// Issue #3141 replaced entrypoint.sh's `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ] ||
// return 0` short-circuit: the launcher sets the manifest only when the registry
// proxy is genuinely enabled, so its absence is the "feature off" signal.
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

// A manifest present but broken is distinct from ErrAbsent's empty-string case,
// so issue #3141's "manifest present but unusable" branch warns rather than
// silently no-opping, and never reaches probe/spawn.
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
	// The parse error text is reproduced rather than hardcoded, since err.Error()
	// is registrymanifest's own to define.
	_, parseErr := registrymanifest.Parse("{not valid json")
	wantWarning := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed: " + parseErr.Error() + " — cargo, npm, yarn, pnpm, go, and gradle will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_MANIFEST is malformed")
	}
}

// Issue #2931's blocking finding: this early-return branch had zero Go coverage,
// since every other test here injects lookPathFound. The lookPath check only
// gates the spawn path, so it must run after probe, not before.
func TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// The double-spawn-prevention path at the CLI-integration level: with probe
// reporting "already listening", spawn is never called and the ready path still
// writes bindings-env-output and the cargo config.toml.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, boundRoute("r0"))

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
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/r0/go'`,
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

// swapTable appends stub rows to ecosystem.Table for one test, preserving the
// load-bearing order of the real rows. The swap is package-level, so no test in
// this file may call t.Parallel.
func swapTable(t *testing.T, extra ...ecosystem.Row) {
	t.Helper()
	original := ecosystem.Table
	ecosystem.Table = append(append([]ecosystem.Row{}, original...), extra...)
	t.Cleanup(func() { ecosystem.Table = original })
}

// Issue #3181: bindings mode must collect exports by walking ecosystem.Table,
// not by naming individual ecosystems' renderers. A stub row's export reaching
// the written file proves the walk; a by-name call site would never see it.
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

// Issue #3182, the same row-generic contract for home-level config writes: a
// stub row's rendered file reaching disk proves the table walk, where a call
// site naming cargo/gradle directly would never write it.
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

// Issue #3185, for the success summary's per-ecosystem "bound to it via ..."
// fragments. Two stub rows, one with a BindingEnvVar and one with only a
// HomeConfig, prove both halves of the precedence a row-generic walk applies.
func TestRunBindRegistryWithDeps_SuccessSummaryFragmentsComeFromEcosystemTableWalk(t *testing.T) {
	stubHome := t.TempDir()
	swapTable(t,
		ecosystem.Row{
			Name: "stub-env-ecosystem",
			// The summary skips a BindingEnvVar row whose var
			// went unexported, so the stub needs a renderer or it
			// proves nothing about the walk.
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

// Issue #3185, for the gate-unusable fallback warning: the ecosystem list must
// come from walking ecosystem.Table, not a hand-maintained literal a new row
// could silently be left out of.
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

// Mirrors the above for the sibling no-route-prefix fallback warning.
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

// exportNamesInFileOrder parses a rendered bindings env file into its export
// names in line order, the property
// TestRunBindRegistryWithDeps_ExportOrderIsGoThenNpmFamily pins and a
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

// The rendered file's line order (go first, then the npm family) predates the
// ecosystem table and is independent of the table's own
// classification-precedence order, where npm precedes go, so a walk over Table
// itself would silently reverse it. Issue #3181's acceptance criterion is a
// byte-identical file: same names, same values, same order.
func TestRunBindRegistryWithDeps_ExportOrderIsGoThenNpmFamily(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, boundRoute("r0"))

	t.Setenv("CARGO_HOME", t.TempDir())
	t.Setenv("GRADLE_USER_HOME", t.TempDir())
	// An empty GO* snapshot is the no-exemption case, so GOSUMDB=off is exported
	// too: the widest go export set, and the one the pre-table call site rendered
	// first.
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

// Issue #3142: bindings mode has no per-ecosystem route mapping, so it binds
// everything to gate.manifest.Routes[0], preserving the pre-prefix routes[0]
// fallback semantics. The distinctive non-"r0" prefix proves real propagation
// from the manifest rather than a coincidental match on a hardcoded default.
func TestRunBindRegistryWithDeps_BindsToFirstRoutePrefixNotSecond(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath,
		boundRoute("artifactory-go"),
		boundRoute("artifactory-npm"),
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
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/artifactory-go/go'`,
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

// Issue #3142: a manifest with a live Forwarder but zero routes carries no
// prefix to bind to, so it warns and skips rather than binding every ecosystem
// to the bare, now-404ing "http://127.0.0.1:<port>/" URL. This is a defensive
// path: the launcher always mints at least one route whenever it sets
// REGISTRY_PROXY_MANIFEST at all.
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

// Mirrors TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings for the
// other defensive shape named in the same guard: a route present but its own
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

// Issue #2931's blocking finding: the Go port silently dropped the two success
// log lines agent/entrypoint.sh's
// phase_go_binding/phase_registry_proxy_forwarder printed, leaving the success
// path printing nothing to stdout.
func TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, boundRoute("r0"))

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

// Issue #3260: under a host-rooted route declaring a go path in its ecosystems
// block, the "==> go bound to it via GOPROXY=<url>" line must carry the
// full-path URL, not the bare-prefix guess the line used to hardcode.
func TestRunBindRegistryWithDeps_HostRootedDeclaredGoPathPrintsFullPathGoLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/artifactory/api/go/go-local"}},
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

// The other half of issue #3260: with no go path declared, GOPROXY renders no
// export at all, so the line claiming a binding must not print either.
func TestRunBindRegistryWithDeps_HostRootedNoDeclaredGoPathOmitsGoLine(t *testing.T) {
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

	if strings.Contains(stdout.String(), "go bound to it via GOPROXY=") {
		t.Errorf("stdout = %q, want it NOT to contain a go-bound-via-GOPROXY line (no go path declared)", stdout.String())
	}
}

// The summary half of the same rule. Under a host-rooted route declaring no
// paths, go's GOPROXY (issue #3260) and the npm family's three vars (issue
// #3259) all go unexported, so a summary still naming those rows would claim
// four bindings that do not exist.
func TestRunBindRegistryWithDeps_HostRootedSummaryOmitsUnexportedBindingVars(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
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

// The Gradle init script write under $GRADLE_USER_HOME/init.d/ mirrors the
// deleted entrypoint.sh phase_gradle_binding, gated the same all-or-nothing way
// the cargo config.toml write is.
func TestRunBindRegistryWithDeps_AlreadyListeningWritesGradleInitScript(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// Covers the $HOME/.gradle fallback branch itself, not just the both-unset guard
// around it: every other gradle test here either pins $GRADLE_USER_HOME or
// empties both, so none runs the one branch every real deployment takes, since
// $GRADLE_USER_HOME is never set outside this file.
func TestRunBindRegistryWithDeps_EmptyGradleUserHomeFallsBackToHomeGradle(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// The gradle init script write shares the cargo config.toml write's
// all-or-nothing readiness gate: a Forwarder that never becomes ready must leave
// $GRADLE_USER_HOME/init.d/ untouched, not just bindings-env-output.
func TestRunBindRegistryWithDeps_TimeoutSkipsGradleInitScript(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	setUnixManifestEnv(t, socketPath)

	gradleUserHome := t.TempDir()
	t.Setenv("GRADLE_USER_HOME", gradleUserHome)
	// Pinned so a regressed readiness gate falling through to the cargo home
	// resolve writes into a temp dir, not the real $HOME/.cargo/config.toml.
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

// With $GRADLE_USER_HOME and $HOME both unset, bindings mode must fail loud
// rather than resolve gradleUserHome to the literal "/.gradle", which
// MkdirAll/WriteFile happily create as root, claiming success while binding
// nothing anyone will read. Mirrors cargo's own both-unset guard; the bash this
// replaced ran under `set -u` and would have died on the unset $HOME expansion.
func TestRunBindRegistryWithDeps_EmptyGradleUserHomeAndHomeFailsLoud(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// With both $CARGO_HOME and $HOME unset, bindings mode must fail loud rather
// than silently resolve cargo's config.toml to a relative ".cargo" path under
// the process cwd. The bash this replaced ran under `set -u` and would have died
// on the unset $HOME expansion instead.
func TestRunBindRegistryWithDeps_EmptyCargoHomeAndHomeFailsLoud(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// Issue #3141's timeout warning names the manifest's endpoint. The small
// timeout/pollInterval keeps this test's wall-clock cost well under a second
// instead of eating the real 5s; production callers still use the real
// registryProxyForwarderTimeout/PollInterval constants unchanged.
func TestRunBindRegistryWithDeps_TimeoutWarnsAndSkipsBindings(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// EnsureForwarderReady's other failure mode, spawn returning an error rather
// than a readiness timeout, also warns naming both the endpoint and the error
// (issue #3141's "manifest present but unusable" branch).
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

// Issue #2931 finding: the "==> go bound to it via GOPROXY=" line used to print
// before the bindings-env-output write and the cargo home
// resolve/mkdir/config.toml write, so a failure in any of those left stdout
// falsely claiming Go was bound while the caller saw the nonzero exit and
// skipped sourcing entirely.
func TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoBoundLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// Issue #2931 finding: ComputeGoBindings' warning loop used to run before the
// fallible writes, so stdout could claim an override that was never applied.
// Unlike the sibling test above, this one sets GOSUMDB to a value
// ComputeGoBindings actually warns about, since blanking all five Go env vars
// never produces a warning to begin with.
func TestRunBindRegistryWithDeps_CargoHomeFailureOmitsGoWarningLine(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// Issue #2931 finding: the LookPath check used to run unconditionally before the
// readiness probe, so an already-ready Forwarder still failed bindings mode
// entirely if socat had since left PATH. lookPath is wired to t.Fatal here to
// prove it is never reached.
func TestRunBindRegistryWithDeps_AlreadyListeningSkipsSocatCheck(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	// Close unlinks the socket file by default, which would delete the ModeSocket
	// fixture this test needs to still exist on disk.
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

// Bindings mode's TCP-fallback transport (issue #3111 slice 8, manifest-driven
// since #3141) must spawn via spawnHTTPForwarder, never the injected
// socket-shaped spawn, and write the same bindings and cargo config the
// socket-mode success test asserts, proving the downstream computation really is
// transport-blind.
func TestRunBindRegistryWithDeps_TCPTransportWritesBindings(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", boundRoute("r0"))

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

	// Not-yet-listening once forces EnsureForwarderReady to call spawn, then ready
	// lets the bindings write proceed, covering both paths in one test.
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

	// Issue #3044: a spawned Forwarder's PID (4242 above) must reach stdout
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
		`export GOPROXY='http://127.0.0.1:` + forwarderPortStr + `/r0/go'`,
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

// A TCP endpoint with REGISTRY_PROXY_TCP_SECRET unset warns and no-ops (exit 0)
// rather than hard-failing the whole verb: the launcher always mints the secret
// together with the TCP endpoint, so a missing one is a real misconfiguration.
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

// The TCP branch never reaches lookPath at all, unlike the unix branch, which
// gates it on the spawn path.
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

// ParseEndpoint only checks that host and port are non-empty, not that port
// parses as a number, so a non-numeric port must warn rather than panic on the
// strconv.Atoi this internal-consistency guard exists for.
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

// AC1 for issue #3141: one -intree-action=apply invocation runs both in-tree
// apply modes and must probe/spawn the Forwarder exactly once across both. probe
// always reports "not ready", so a gate resolved once per mode (the pre-#3141
// shape) would call spawn twice. Issue #3201 split bindings mode out of this
// invocation, so it is no longer folded in here.
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

// newIntreeTestRepo returns a fresh, empty git repo: one local dir, since
// skip-worktree/checkout are purely local operations. Reuses the package's own
// shared runGitCmd helper from bundleout_cmd_test.go.
func newIntreeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	return dir
}

// intreeCargoConfigContent references upstream.example via sparse+https. The
// two-scheme ReplaceAll proof lives in intreebinding_test.go's
// TestApplyInTreeBindingRewritesTrackedFileBothSchemes, not here.
const intreeCargoConfigContent = "[source.crates-io]\nreplace-with = \"proxy\"\n\n[source.proxy]\nregistry = \"sparse+https://upstream.example/index/\"\n"

// ApplyInTreeBinding only string-replaces the upstream host and never parses the
// file's real syntax, so one line over https covers npm, yarn and pnpm alike.
const intreeNpmStyleConfigContent = "registry=https://upstream.example/\n"

// writeTrackedIntreeFile commits relPath so
// ApplyInTreeBinding/RevertInTreeBinding see a git-tracked file to operate on.
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

// listenOnFakeSocket opens and immediately closes, without unlinking, a real
// unix-socket file: the ModeSocket fixture isMountedSocket's check needs.
func listenOnFakeSocket(t *testing.T, socketPath string) {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix): %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
}

// intreeUpstreamRoute names upstream.example with prefix "r0", so a rewritten
// URL carries a "/r0" path segment (buildIntreeHostRewrites, issue #3142), which
// the exact-match content assertions below account for.
var intreeUpstreamRoute = registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"}

// AC5, the all-or-nothing gate (issue #2932 brief section 3): with the Forwarder
// dead, apply must leave the file byte-for-byte unchanged and the skip-worktree
// bit unset. No partial rewrite.
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

	// Issue #3044: the already-ready short-circuit never spawns, so there is no PID
	// to report and the stdout line must stay silent on this path.
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

// Issue #3142's buildIntreeHostRewrites filter: an empty-UpstreamHost route
// alongside a real one is silently skipped when building rewrites, never
// reaching ApplyInTreeBinding's own internal-consistency error for an empty
// entry, and the valid route's rewrite still applies.
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

// The other half of buildIntreeHostRewrites' filter from the caller side: a route
// whose UpstreamHost is empty is still "no route upstream host at all", since
// every rewrite candidate was filtered out.
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

// Blocking review finding on issue #3142: two routes naming the same UpstreamHost
// (legal, e.g. one Artifactory host fronting separate npm and cargo prefixes)
// cannot be told apart by ApplyInTreeBinding's host-only content match, so every
// rewrite for the shared host is dropped rather than the first kept, while a
// third route on a distinct host survives untouched.
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

// Non-blocking review finding on issue #3142: rewriteHostNames must not repeat a
// host appearing in more than one rewrite, and must preserve first-occurrence
// order rather than sorting.
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

// Both halves of the summary's skip rule: a BindingEnvVar row is named only when
// the rendered exports carry its var, while a HomeConfig row is named
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

// The degenerate case the skip rule newly makes reachable: empty prose is what
// lets the caller drop the separator rather than print a dangling one.
func TestBindingSummaryProse_EmptyWhenNothingBound(t *testing.T) {
	original := ecosystem.Table
	ecosystem.Table = []ecosystem.Row{{Name: "stub-unbound", BindingEnvVar: "STUB_UNBOUND_REGISTRY"}}
	t.Cleanup(func() { ecosystem.Table = original })

	if got := bindingSummaryProse(nil, nil); got != "" {
		t.Errorf("bindingSummaryProse(nothing bound) = %q, want %q", got, "")
	}
}

// joinProse's three cardinalities: one item bare, two joined on a bare "and" with
// no comma, three or more with an Oxford comma. Both bindings-mode fallback
// warnings and the repo-aware no-route warning rely on this shape to read as
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

// The caller side of the same review finding: the generic "carries no route
// upstream host" warning must be suppressed, since the manifest does carry one,
// it is just unusable for host-based rewriting.
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

// Issue #3141: entrypoint.sh's intree_binding_apply call site passes no transport
// flag at all, so an unset manifest silently no-ops apply mode, the replacement
// for the old empty -registry-proxy-socket no-op.
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

// A present manifest means the registry proxy is genuinely configured, so a
// missing route upstream host warns rather than silently no-ops.
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

// Review finding on issue #2932, mirroring
// TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings for the
// intree-apply path: apply must print a socat-specific warning naming the
// endpoint rather than falling through to EnsureForwarderReady's generic "failed
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

// Mirrors TestRunBindRegistryWithDeps_TCPTransportWritesBindings for intree-apply
// mode (issue #3111 slice 8, manifest-driven since #3141): apply must spawn via
// spawnHTTPForwarder, never the injected socket-shaped spawn.
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

// Mirrors
// TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings for
// intree-apply mode.
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

// Revert runs with no manifest set at all, since it is a pure git operation that
// never consults REGISTRY_PROXY_MANIFEST or the probe/spawn deps.
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

// The multi-row happy path a review finding called out as missing: every other
// intree test here exercises the first row only. A tracked cargo config sits
// alongside the three npm-family rows and must never be touched by any pass
// (issue #3201: cargo no longer participates in InTreeBindings at all).
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

	// Re-apply pass (AC4): the revert/re-apply sequence around branch recovery must
	// rewrite and re-tag all three rows, not just the apply/revert pair above.
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

// newIntreeUnmergedNpmTestRepo leaves .npmrc genuinely unmerged (UU), the same
// fixture shape as bindregistry's own unexported newUnmergedTestRepo in
// intreebinding_test.go, replicated here because that helper is unexported in a
// different package. `git update-index --skip-worktree` then fails with exit 128
// on .npmrc, giving ApplyInTreeBinding a genuine per-row failure.
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

	// A conflicting merge is the point of this fixture, so unlike runGitCmd's other
	// calls a nonzero exit here is the desired outcome, not a setup failure.
	if err := exec.Command("git", "-C", dir, "merge", "feature").Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", npmRel).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", npmRel, status, err)
	}

	return dir
}

// Regression test for a review finding: the apply loop used to return as soon as
// one row errored, aborting the rest. A genuinely failing row (unmerged .npmrc)
// must not stop the loop, and the overall apply must still report failure.
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

// Issue #3082's AC: ApplyMissing must read differently from ApplyNoopContent's
// "content no longer mentions the upstream host", since the two point an operator
// at different fixes (registry pinned outside the repo vs. wrong upstream host).
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

// Issue #2933's `[ -f ]` parity guard had no operator-facing message at all
// before #3082.
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

// Pins that the existing ApplyUntracked warning survives the
// bool-to-ApplyOutcome signature change unchanged: #3082 slice 2 only added the
// four other messages.
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

// Issue #2932's crash window: the bit can be tagged before content is rewritten,
// so "bit set" alone never proves the content converged, and the warning must
// read differently from ApplyNoopContent's "nothing to do".
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

// ApplyNoopContent is distinct from ApplyMissing's "file not found" and points an
// operator at a different fix: the manifest's route upstream host is wrong, not
// that the registry pin lives outside this file.
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

// The "private" registry's index host matches intreeUpstreamRoute's UpstreamHost,
// which is what every runBindRegistryRepoAwareHomeConfigs test below needs.
const intreeCargoNamedRegistryRepoConfig = "[registries.private]\n" +
	"index = \"sparse+https://upstream.example/private/index/\"\n"

// Issue #3201 replaced the in-tree cargo rewrite entirely. This is the end-to-end
// proof the verb wiring reaches ecosystem.CargoRepoAwareConfig at all: the
// rendered $CARGO_HOME/config.toml must match that renderer's own output for the
// same inputs.
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

	// Cargo no longer participates in the in-tree rewrite at all (issue #3201), so
	// the tracked repo file stays untouched.
	repoConfig, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(repoConfig) != intreeCargoNamedRegistryRepoConfig {
		t.Errorf(".cargo/config.toml = %q, want byte-for-byte unchanged", repoConfig)
	}
}

// The other half of the same apply: the placeholder token export the rewritten
// config.toml's [registries.spindrift-registry-proxy] needs bound, keyed to the
// reused proxy source name since route "r0" coincides with the manifest's own
// routes[0].Prefix.
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
	want := `export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R0_PRIVATE_TOKEN='` + ecosystem.CargoPlaceholderToken + "'\n"
	if string(got) != want {
		t.Errorf("intree bindings env output = %q, want %q", got, want)
	}
}

// The common case (issue #3201): a repo with no .cargo/config.toml declares no
// named registry, so the read-error-is-not-an-error path treats it as empty
// content and the render falls back to CargoConfigTOML's crates-io-only base.
func TestRunBindRegistryWithDeps_IntreeApplyNoRegistriesTableWritesEmptyEnvOutputAndBaseConfig(t *testing.T) {
	dir := newIntreeTestRepo(t)
	// No .cargo/config.toml at all; npm's is enough to give the repo a tracked
	// in-tree file and prove the run otherwise succeeds.
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

// Issue #3201's URL-to-source-name 1:1 constraint end-to-end: the "r0" route's
// registry shares the [source.spindrift-registry-proxy] stanza CargoConfigTOML
// already wrote and must not double it up, while the "r1" route's registry still
// gets its own distinct proxy source and placeholder export.
func TestRunBindRegistryWithDeps_IntreeApplyTwoRouteManifestDedupesReusedProxySource(t *testing.T) {
	dir := newIntreeTestRepo(t)
	repoConfig := intreeCargoNamedRegistryRepoConfig +
		"\n[registries.other-private]\n" +
		"index = \"sparse+https://other.example/idx/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", repoConfig)

	route0 := intreeUpstreamRoute
	route1 := registrymanifest.Route{Prefix: "r1", UpstreamHost: "other.example", Ecosystems: ecosystem.CargoRouteBlock("other-private")}

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
		`export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R0_PRIVATE_TOKEN='` + ecosystem.CargoPlaceholderToken + `'`,
		`export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R1_OTHER_PRIVATE_TOKEN='` + ecosystem.CargoPlaceholderToken + `'`,
	} {
		if !strings.Contains(string(envGot), wantExport) {
			t.Errorf("intree bindings env output = %q, want it to contain %q", envGot, wantExport)
		}
	}
}

// Issue #3201's warning parity at the verb boundary: the row value only returns
// warning strings, so without an end-to-end assertion the print loop in
// runBindRegistryRepoAwareHomeConfigs could be deleted and nothing would fail.
func TestRunBindRegistryWithDeps_IntreeApplyPrintsUndeclaredRegistryWarningToStdout(t *testing.T) {
	dir := newIntreeTestRepo(t)
	repoConfig := "[registries.undeclared]\n" +
		"index = \"sparse+https://upstream.example/undeclared/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", repoConfig)

	route := registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example", Ecosystems: ecosystem.CargoRouteBlock("declared")}

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

// The one manifest route issue #3404's acceptance criterion asks for: go, gradle
// and cargo each in their own [routes.ecosystems.<name>] block, the grammar of
// spec #3403's example, plus a "pypi" block no renderer in this Box knows. Every
// assertion below is an exact expected string, so that unknown block must change
// neither the exit code nor a rendered byte.
func threeEcosystemRoute() registrymanifest.Route {
	return registrymanifest.Route{
		Prefix:       "r0",
		UpstreamHost: "upstream.example",
		Ecosystems: registryvocab.RouteEcosystems{
			"go":     registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/api/go/go-remote"},
			"gradle": registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/maven-remote"},
			"cargo":  registryvocab.RouteDeclaration{ecosystem.CargoRouteRegistriesKey: registryvocab.StringsValue([]string{"internal"})},
			"pypi":   registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/pypi-remote"},
		},
	}
}

// The pre-clone half of issue #3404: each renderer reads its own block and binds
// to its own declared path. A shared source would give both the same path, or one
// of them none.
func TestRunBindRegistryWithDeps_OneRouteBindsGoAndGradleFromTheirOwnBlocks(t *testing.T) {
	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, threeEcosystemRoute())

	t.Setenv("CARGO_HOME", t.TempDir())
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

	port := strconv.Itoa(bindregistry.ForwarderPort)

	gotEnv, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	wantExport := `export GOPROXY='http://127.0.0.1:` + port + `/r0/artifactory/api/go/go-remote'`
	if !strings.Contains(string(gotEnv), wantExport) {
		t.Errorf("bindings env output = %q, want it to contain %q", gotEnv, wantExport)
	}

	gotScript, err := os.ReadFile(filepath.Join(gradleUserHome, "init.d", "spindrift-registry-proxy.init.gradle"))
	if err != nil {
		t.Fatalf("read gradle init script: %v", err)
	}
	wantURL := `def spindriftMavenUrl = "http://127.0.0.1:` + port + `/r0/artifactory/maven-remote/"`
	if !strings.Contains(string(gotScript), wantURL) {
		t.Errorf("gradle init script = %q, want it to contain %q", gotScript, wantURL)
	}
	if strings.Contains(string(gotScript), "installs no repository redirection") {
		t.Errorf("gradle init script = %q, want the real redirect script, not the inert fallback", gotScript)
	}
}

// The repo-aware half of issue #3404 over the same route: apply mode is the only
// one that reads the repo's .cargo/config.toml, so a bindings-mode invocation
// cannot reach the cargo renderer's registries filter at all.
func TestRunBindRegistryWithDeps_IntreeApplyBindsCargoFromSameThreeBlockRoute(t *testing.T) {
	dir := newIntreeTestRepo(t)
	repoConfig := "[registries.internal]\n" +
		"index = \"sparse+https://upstream.example/internal/index/\"\n"
	writeTrackedIntreeFile(t, dir, ".cargo/config.toml", repoConfig)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, threeEcosystemRoute())

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
	wantStanza := "[source.spindrift-upstream-internal]\n" +
		"registry = \"sparse+https://upstream.example/internal/index/\"\n" +
		"replace-with = \"spindrift-registry-proxy-r0-internal\"\n"
	if !strings.Contains(string(got), wantStanza) {
		t.Errorf("cargo config.toml = %q, want it to contain %q", got, wantStanza)
	}

	envGot, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read intree bindings env output: %v", err)
	}
	wantExport := `export CARGO_REGISTRIES_SPINDRIFT_REGISTRY_PROXY_R0_INTERNAL_TOKEN='` + ecosystem.CargoPlaceholderToken + `'`
	if !strings.Contains(string(envGot), wantExport) {
		t.Errorf("intree bindings env output = %q, want it to contain %q", envGot, wantExport)
	}

	// The declared name and the repo's own registry match, so either warning firing
	// would mean the filter came from somewhere other than this route's cargo block.
	if strings.Contains(stdout.String(), "==> WARNING: cargo registry") {
		t.Errorf("stdout = %q, want no cargo registry warning", stdout.String())
	}
}

// The repo-aware phase's non-ENOENT read-error branch (issue #3201): a missing
// config means "this repo declares no named registry", while an unreadable one
// means the plan was derived from nothing and the run must fail. The unreadable
// file is a directory, since os.ReadFile then returns EISDIR without depending on
// the test running as a non-root user, which a Box does not guarantee.
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

// -intree-bindings-env-output must be paired with -intree-action=apply, mirroring
// the other flag-pair validation errors in runBindRegistryWithDeps.
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

// Review finding on issue #3201: -intree-action=apply re-renders the repo-aware
// home configs from the repo's own tracked config, and bindings mode would then
// re-render the same rows from the base template in the same invocation,
// silently clobbering what apply just wrote. No caller combines the two flags
// today; this guards against one starting to.
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

// Mirrors TestRunBindRegistry_MissingFlagsErrors' style for the
// -intree-work-dir/-intree-action pair and the -intree-action value check.
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

// Pointing the output at a path whose parent directory doesn't exist forces
// WriteFile to fail past the Classify call, which can no longer itself return an
// error.
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

// Reviewer finding on issue #3142: a route whose UpstreamHost
// buildIntreeHostRewrites already dropped as a collision must not survive
// dropCollidedRoutes, since nothing on disk was ever rewritten to that route's
// LocalURL. Cargo's exports-side analogue is covered by ecosystem's own package
// tests (issue #3201).
func TestDropCollidedRoutes_SkipsRouteWithCollidedUpstreamHost(t *testing.T) {
	routes := []registrymanifest.Route{
		{Prefix: "r0", UpstreamHost: "shared.example", Ecosystems: ecosystem.CargoRouteBlock("collided-one")},
		{Prefix: "r1", UpstreamHost: "shared.example", Ecosystems: ecosystem.CargoRouteBlock("collided-two")},
		{Prefix: "r2", UpstreamHost: "distinct.example", Ecosystems: ecosystem.CargoRouteBlock("valid-registry")},
	}
	_, collisions := buildIntreeHostRewrites(routes, 9999)

	filtered := dropCollidedRoutes(routes, collisions)

	if len(filtered) != 1 || filtered[0].Prefix != "r2" {
		t.Errorf("filtered = %+v, want exactly one surviving route (r2/distinct.example)", filtered)
	}
}

// Issue #3199: this scan reads the manifest only to decide on or off. It never
// resolves the shared Forwarder-readiness gate and must never itself spawn a
// Forwarder, unlike bindings and intree-apply mode.
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

// A clean run never gets "scanned N lockfiles" chatter, and an off-dispatch never
// gets scanned regardless of what any tracked lockfile happens to contain.
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

// A malformed manifest must warn once and still exit 0, never fail the run over a
// manifest lockfile-scan mode isn't even trying to connect to.
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

// A scan error, here git ls-files failing because the work dir is not a repo,
// must warn and still exit 0.
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

// Issue #3199's clean-run invariant: no "scanned N lockfiles, all clean" chatter.
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

// -lockfile-scan-work-dir alone satisfies the "at least one mode" guard without
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

// Pins the fix for the command-injection finding on renderEnvExports: a value
// carrying shell metacharacters must round-trip through a real `source`
// unexpanded, byte-for-byte. The input flagged against issue #3259 is a
// host-rooted route's derived path, sourced from a repo's own committed,
// therefore untrusted, .npmrc.
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

// boundRoute is the manifest route the bindings-mode tests share: one route
// declaring a path per ecosystem, the shape that renders every binding var (see
// ecosystem.NpmFamilyBindings and ecosystem.ComputeGoBindings). An ecosystem the
// route declares nothing for binds nothing.
func boundRoute(prefix string) registrymanifest.Route {
	return registrymanifest.Route{
		Prefix:     prefix,
		Ecosystems: registryvocab.RouteEcosystems{"go": registryvocab.RouteDeclaration{"path": "/go"}},
		EnforcedPaths: []registryvocab.Subtree{
			{Ecosystem: "go", Path: "/go"},
			{Ecosystem: "npm", Path: "/"},
			{Ecosystem: "pnpm", Path: "/"},
			{Ecosystem: "yarn", Path: "/"},
		},
	}
}
