package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/driver/drivertest"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// A hermetic $HOME with the synthetic and stub Strategies' home-directory env
// vars unset (issue #3201): a run reaching a home-config write would otherwise
// land in the real ambient one.
func TestMain(m *testing.M) {
	if err := drivertest.UseClaudeConnector(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	home, err := os.MkdirTemp("", "bindregistry-cmd-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	// A handoff without a HeartbeatLog would otherwise write the Box's
	// /tmp/heartbeat.log, which a macOS Nix host shares across builds.
	defaultHeartbeatLog = filepath.Join(home, "heartbeat.log")
	for _, v := range []string{
		synthetic.Strategy.HomeConfig.HomeEnvVar,
		"STUB_ECOSYSTEM_HOME", "STUB_HOME_ECOSYSTEM_HOME", "STUB_REPO_AWARE_HOME", "STUB_HOME_HOME",
	} {
		os.Unsetenv(v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// lookPathFound stubs bindregistry.ResolveGate's LookPath dep to report socat
// found without touching PATH (issue #3141): the Nix test sandbox has no
// socat. Callers always inject a fake SpawnFunc, so the path is never run.
func lookPathFound(string) (string, error) { return "/fake/bin/socat", nil }

// lookPathMissing deterministically reports socat absent, rather than
// emptying $PATH, which depended on no other test having widened it.
func lookPathMissing(string) (string, error) { return "", exec.ErrNotFound }

// shortUnixSocketPath avoids t.TempDir(), whose path embeds the full subtest
// name and regularly blows AF_UNIX's 108-byte sun_path cap, failing
// net.Listen("unix", ...) with EINVAL.
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
// naming a unix endpoint (issue #3141).
func setUnixManifestEnv(t *testing.T, socketPath string, routes ...registrymanifest.Route) {
	t.Helper()
	m := registrymanifest.Manifest{Endpoint: registrymanifest.NewUnixEndpoint(socketPath), Routes: routes}
	encoded, err := registrymanifest.Encode(m)
	if err != nil {
		t.Fatalf("registrymanifest.Encode: %v", err)
	}
	t.Setenv(registrymanifest.EnvVar, encoded)
}

// setTCPManifestEnv is the TCP-endpoint counterpart (issue #3111's
// TCP-fallback transport).
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
// registrymanifest.Parse's ErrAbsent shape, rather than trusting the ambient
// environment to lack it.
func clearManifestEnv(t *testing.T) {
	t.Helper()
	t.Setenv(registrymanifest.EnvVar, "")
}

// forwarderPortStr is bindregistry.ForwarderPort (issue #3141's single port
// declaration) rendered once for the exact-wording assertions below.
var forwarderPortStr = strconv.Itoa(bindregistry.ForwarderPort)

// runSyntheticBind is runBindRegistryWithDeps over the kit's synthetic
// Strategy alone, with no real ecosystem's rendering in the way.
func runSyntheticBind(args []string, stdout io.Writer, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath bindregistry.LookPathFunc, timeout, pollInterval time.Duration) int {
	return runBindRegistryWithDeps(args, []ecosystem.Strategy{synthetic.Strategy}, stdout, probe, spawn, lookPath, timeout, pollInterval)
}

// rowsWith returns the synthetic Strategy followed by the stub Strategies, in that order.
func rowsWith(extra ...ecosystem.Strategy) []ecosystem.Strategy {
	return append([]ecosystem.Strategy{synthetic.Strategy}, extra...)
}

// exportStubRow is a Strategy exporting each named var, valued at the route's local
// URL.
func exportStubRow(name string, vars ...string) ecosystem.Strategy {
	return ecosystem.Strategy{
		Name: name,
		EnvExports: func(port int, prefix string, _ func(string) string, _ []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
			exports := make([]ecosystem.EnvExport, len(vars))
			for i, v := range vars {
				exports[i] = ecosystem.EnvExport{Name: v, Value: ecosystem.RouteLocalURL(prefix, port)}
			}
			return exports, nil
		},
	}
}

// syntheticHome points the synthetic Strategy's home-directory var at a fresh
// directory and returns it.
func syntheticHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(synthetic.Strategy.HomeConfig.HomeEnvVar, dir)
	return dir
}

// syntheticHomeConfigPath is where the synthetic Strategy's home config lands under home.
func syntheticHomeConfigPath(home string) string {
	return filepath.Join(home, synthetic.Strategy.HomeConfig.ConfigPath)
}

// syntheticHomeConfig is the synthetic Strategy's home config for a route prefix at
// the Forwarder's port, from the Strategy's own renderer.
func syntheticHomeConfig(prefix string) string {
	content, _ := synthetic.Strategy.HomeConfig.Render(bindregistry.ForwarderPort, prefix, nil)
	return content
}

// syntheticExport is the line the synthetic Strategy's env export renders to.
func syntheticExport(prefix string) string {
	return "export SYNTHETIC_REGISTRY='" + ecosystem.RouteLocalURL(prefix, bindregistry.ForwarderPort) + "'"
}

// mountSocketManifest mounts a fake unix socket and points the manifest at it,
// returning the socket path.
func mountSocketManifest(t *testing.T, routes ...registrymanifest.Route) string {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, routes...)
	return socketPath
}

// bindReady runs bindings mode over Strategies against a mounted socket whose probe
// reports ready, returning the exit code, stdout and the env file's path.
func bindReady(t *testing.T, rows []ecosystem.Strategy, routes ...registrymanifest.Route) (rc int, stdout, bindingsOut string) {
	t.Helper()
	mountSocketManifest(t, routes...)
	bindingsOut = filepath.Join(t.TempDir(), "bindings.env")
	var out bytes.Buffer
	rc = runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, rows, &out,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	return rc, out.String(), bindingsOut
}

// forwarderLineOf returns the success summary's "Forwarder up" line, or "".
func forwarderLineOf(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "==> registry proxy Forwarder up on ") {
			return line
		}
	}
	return ""
}

// At least one complete mode must be requested; -bindings-env-output alone
// suffices (issue #3141).
func TestRunBindRegistry_MissingFlagsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no flags at all", nil},
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

// Issue #3141: the launcher sets the manifest only when the registry proxy is
// genuinely enabled, so its absence is the "feature off" signal.
func TestBindRegistry_ManifestAbsentIsNoOp(t *testing.T) {
	clearManifestEnv(t)
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	probeCalled := false
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
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

// A manifest present but broken is distinct from ErrAbsent's empty string, so
// issue #3141's "present but unusable" branch warns rather than silently
// no-opping, and never reaches probe/spawn.
func TestBindRegistry_ManifestMalformedJSONWarnsAndSkipsBindings(t *testing.T) {
	t.Setenv(registrymanifest.EnvVar, "{not valid json")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	// The parse error text is reproduced rather than hardcoded, since err.Error()
	// is registrymanifest's own to define.
	_, parseErr := registrymanifest.Parse("{not valid json")
	wantWarning := "==> WARNING: REGISTRY_PROXY_MANIFEST is malformed: " + parseErr.Error() + " — synthetic will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_MANIFEST is malformed")
	}
}

// Issue #2931: every other bindings-mode test injects lookPathFound, leaving
// this branch uncovered. The lookPath check only gates the spawn path, so it must run
// after probe, not before.
func TestBindRegistry_SocatMissingWarnsAndSkipsBindings(t *testing.T) {
	socketPath := mountSocketManifest(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathMissing,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — synthetic will fall back to the public registry\n"
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
// writes bindings-env-output and the home config.
func TestBindRegistry_AlreadyListeningWritesBindings(t *testing.T) {
	mountSocketManifest(t, registrymanifest.Route{Prefix: "r0"})
	home := syntheticHome(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when probe already reports ready")
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	if want := syntheticExport("r0"); !strings.Contains(string(got), want) {
		t.Errorf("bindings env output = %q, want it to contain %q", got, want)
	}
	if strings.Contains(string(got), "FORWARDER_READY") {
		t.Errorf("bindings env output = %q, want it to not contain FORWARDER_READY (dead sentinel, nothing reads it)", got)
	}

	homeConfig, err := os.ReadFile(syntheticHomeConfigPath(home))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if want := syntheticHomeConfig("r0"); string(homeConfig) != want {
		t.Errorf("home config = %q, want %q", homeConfig, want)
	}
}

// Issue #3181: bindings mode collects exports by walking the Strategies; a
// stub Strategy's export reaching the file proves the walk, which a by-name
// call site would never see.
func TestBindRegistry_ExportsComeFromRowsWalk(t *testing.T) {
	syntheticHome(t)
	rc, stdout, bindingsOut := bindReady(t, rowsWith(exportStubRow("stub-ecosystem", "STUB_ECOSYSTEM_URL")), registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	want := `export STUB_ECOSYSTEM_URL='` + ecosystem.RouteLocalURL("r0", bindregistry.ForwarderPort) + `'`
	if !strings.Contains(string(got), want) {
		t.Errorf("bindings env output = %q, want it to contain %q (a stub row's export reaching the file proves a table walk, not by-name calls)", got, want)
	}
}

// Issue #3182, the same Strategy-generic contract for home-level config
// writes.
func TestBindRegistry_HomeConfigsComeFromRowsWalk(t *testing.T) {
	stubHome := t.TempDir()
	t.Setenv("STUB_ECOSYSTEM_HOME", stubHome)
	syntheticHome(t)
	rows := rowsWith(ecosystem.Strategy{
		Name: "stub-ecosystem",
		HomeConfig: &ecosystem.HomeConfig{
			HomeEnvVar:          "STUB_ECOSYSTEM_HOME",
			HomeRelativeDefault: ".stub-ecosystem",
			ConfigPath:          "stub.conf",
			Render: func(port int, prefix string, _ []registrymanifest.Route) (string, bool) {
				return "stub=" + strconv.Itoa(port) + "/" + prefix, true
			},
		},
	})

	rc, stdout, _ := bindReady(t, rows, registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
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

// Issue #3185, for the success summary's "bound to it via ..." fragments. One
// stub has a BindingEnvVar and one only a HomeConfig, covering both halves of
// the precedence.
func TestBindRegistry_SuccessSummaryFragmentsComeFromRowsWalk(t *testing.T) {
	stubHome := t.TempDir()
	t.Setenv("STUB_HOME_ECOSYSTEM_HOME", stubHome)
	syntheticHome(t)
	envStub := exportStubRow("stub-env-ecosystem", "STUB_ENV_REGISTRY")
	// The summary skips a BindingEnvVar Strategy whose var went unexported, so the
	// stub needs a renderer or it proves nothing about the walk.
	envStub.BindingEnvVar = "STUB_ENV_REGISTRY"
	rows := rowsWith(
		envStub,
		ecosystem.Strategy{
			Name: "stub-home-ecosystem",
			HomeConfig: &ecosystem.HomeConfig{
				HomeEnvVar:          "STUB_HOME_ECOSYSTEM_HOME",
				HomeRelativeDefault: ".stub-home-ecosystem",
				ConfigPath:          "stub.conf",
				Render: func(port int, prefix string, _ []registrymanifest.Route) (string, bool) {
					return "stub=" + strconv.Itoa(port) + "/" + prefix, true
				},
			},
		},
	)

	rc, stdout, _ := bindReady(t, rows, registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	wantEnvFragment := "stub-env-ecosystem bound to it via STUB_ENV_REGISTRY"
	if !strings.Contains(stdout, wantEnvFragment) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's BindingEnvVar reaching the summary proves a table walk)", stdout, wantEnvFragment)
	}
	wantHomeFragment := "stub-home-ecosystem bound to it via " + filepath.Join(stubHome, "stub.conf")
	if !strings.Contains(stdout, wantHomeFragment) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's resolved HomeConfig path reaching the summary proves the fallback branch of the walk)", stdout, wantHomeFragment)
	}
}

// Issue #3185, for the gate-unusable fallback warning: the ecosystem list must
// come from the walk, not a literal a new Strategy could be left out of.
func TestBindRegistry_UnusableGateFallbackNamesComeFromRowsWalk(t *testing.T) {
	rows := rowsWith(ecosystem.Strategy{Name: "stub-ecosystem"})

	t.Setenv(registrymanifest.EnvVar, "{not valid json")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-bindings-env-output", bindingsOut,
	}, rows, &stdout,
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is malformed")
			return 0, nil
		},
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if want := "synthetic and stub-ecosystem will fall back to the public registry"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's name reaching the fallback warning proves a table walk)", stdout.String(), want)
	}
}

// Mirrors the above for the sibling no-route-prefix fallback warning.
func TestBindRegistry_NoRoutePrefixFallbackNamesComeFromRowsWalk(t *testing.T) {
	rows := rowsWith(ecosystem.Strategy{Name: "stub-ecosystem"})

	rc, stdout, _ := bindReady(t, rows) // no routes at all
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if want := "synthetic and stub-ecosystem will fall back to the public registry"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's name reaching the fallback warning proves a table walk)", stdout, want)
	}
}

// exportNamesInFileOrder parses a rendered bindings env file into its export
// names in line order, which a strings.Contains assertion cannot see.
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

// The file's line order is the Strategies' order, each Strategy's exports
// contiguous; the stub names sort opposite to their position so a sorted file
// fails here.
func TestBindRegistry_ExportOrderFollowsRows(t *testing.T) {
	syntheticHome(t)
	rows := []ecosystem.Strategy{
		exportStubRow("stub-z", "STUB_Z_FIRST", "STUB_Z_SECOND"),
		synthetic.Strategy,
		exportStubRow("stub-a", "STUB_A_ONLY"),
	}

	rc, stdout, bindingsOut := bindReady(t, rows, registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	gotNames := exportNamesInFileOrder(t, string(got))
	wantNames := []string{"STUB_Z_FIRST", "STUB_Z_SECOND", "SYNTHETIC_REGISTRY", "STUB_A_ONLY"}
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
// everything to gate.manifest.Routes[0]. The distinctive non-"r0" prefix
// proves propagation from the manifest rather than a hardcoded default.
func TestBindRegistry_BindsToFirstRoutePrefixNotSecond(t *testing.T) {
	home := syntheticHome(t)

	rc, stdout, bindingsOut := bindReady(t, rowsWith(),
		registrymanifest.Route{Prefix: "first-route"},
		registrymanifest.Route{Prefix: "second-route"},
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got, err := os.ReadFile(bindingsOut)
	if err != nil {
		t.Fatalf("read bindings env output: %v", err)
	}
	if want := syntheticExport("first-route"); !strings.Contains(string(got), want) {
		t.Errorf("bindings env output = %q, want it to contain %q", got, want)
	}
	if strings.Contains(string(got), "second-route") {
		t.Errorf("bindings env output = %q, want it to never mention the second route's prefix", got)
	}

	homeConfig, err := os.ReadFile(syntheticHomeConfigPath(home))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if want := syntheticHomeConfig("first-route"); string(homeConfig) != want {
		t.Errorf("home config = %q, want %q", homeConfig, want)
	}
}

// Issue #3142: a live Forwarder with zero routes has no prefix to bind to, so
// it warns and skips rather than binding to the bare, 404ing
// "http://127.0.0.1:<port>/" URL. Defensive: the launcher always mints a route
// when it sets REGISTRY_PROXY_MANIFEST.
func TestBindRegistry_NoRoutesWarnsAndSkipsBindings(t *testing.T) {
	rc, stdout, bindingsOut := bindReady(t, rowsWith()) // no routes at all
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — synthetic will fall back to the public registry\n"
	if stdout != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout, wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when the manifest carries no route prefix")
	}
}

// The no-route-prefix guard's other defensive shape: a route whose Prefix is
// empty.
func TestBindRegistry_EmptyRoutePrefixWarnsAndSkipsBindings(t *testing.T) {
	rc, stdout, bindingsOut := bindReady(t, rowsWith(), registrymanifest.Route{UpstreamHost: "upstream.example"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantWarning := "==> WARNING: registry proxy manifest carries no route prefix — synthetic will fall back to the public registry\n"
	if stdout != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout, wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when routes[0].Prefix is empty")
	}
}

// Issue #2931: the Go port once dropped entrypoint.sh's success log line,
// leaving the success path printing nothing.
func TestBindRegistry_AlreadyListeningPrintsSuccessLines(t *testing.T) {
	home := syntheticHome(t)
	envStub := exportStubRow("stub-env-ecosystem", "STUB_ENV_REGISTRY")
	envStub.BindingEnvVar = "STUB_ENV_REGISTRY"

	rc, stdout, _ := bindReady(t, rowsWith(envStub), registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + forwarderPortStr + " — synthetic bound to it via " + syntheticHomeConfigPath(home) + " and stub-env-ecosystem bound to it via STUB_ENV_REGISTRY"
	if got := forwarderLineOf(stdout); got != wantForwarderLine {
		t.Errorf("Forwarder line = %q, want %q (stdout=%q)", got, wantForwarderLine, stdout)
	}
}

// Issues #3259 and #3260: a BindingEnvVar Strategy whose var the route leaves
// unexported must not appear in the summary, which would claim a binding that
// does not exist.
func TestBindRegistry_SummaryOmitsUnexportedBindingVars(t *testing.T) {
	home := syntheticHome(t)
	unexported := ecosystem.Strategy{
		Name:          "stub-unexported",
		BindingEnvVar: "STUB_UNEXPORTED_REGISTRY",
		EnvExports: func(int, string, func(string) string, []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
			return nil, nil
		},
	}

	rc, stdout, _ := bindReady(t, rowsWith(unexported), registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	wantForwarderLine := "==> registry proxy Forwarder up on 127.0.0.1:" + forwarderPortStr + " — synthetic bound to it via " + syntheticHomeConfigPath(home)
	if got := forwarderLineOf(stdout); got != wantForwarderLine {
		t.Errorf("Forwarder line = %q, want %q (stdout=%q)", got, wantForwarderLine, stdout)
	}
}

// Covers the $HOME/<HomeRelativeDefault> fallback branch itself: every other
// home-config test pins the Strategy's home var or empties both, so none runs
// the one branch every real deployment takes.
func TestBindRegistry_EmptyHomeEnvVarFallsBackToHomeRelativeDefault(t *testing.T) {
	t.Setenv(synthetic.Strategy.HomeConfig.HomeEnvVar, "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	rc, stdout, _ := bindReady(t, rowsWith(), registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got, err := os.ReadFile(syntheticHomeConfigPath(filepath.Join(home, synthetic.Strategy.HomeConfig.HomeRelativeDefault)))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if want := syntheticHomeConfig("r0"); string(got) != want {
		t.Errorf("home config = %q, want %q", got, want)
	}
}

// The home config write shares the env export write's all-or-nothing readiness
// gate: a Forwarder that never becomes ready must leave the Strategy's home
// directory untouched, not just bindings-env-output.
func TestBindRegistry_TimeoutSkipsHomeConfig(t *testing.T) {
	mountSocketManifest(t)
	home := syntheticHome(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if _, err := os.Stat(syntheticHomeConfigPath(home)); !os.IsNotExist(err) {
		t.Errorf("os.Stat(home config) err = %v, want a not-exist error when the Forwarder never becomes ready", err)
	}
}

// With a Strategy's home var and $HOME both unset, bindings mode must fail
// loud rather than resolve the home under the cwd or "/", which
// MkdirAll/WriteFile happily create as root while binding nothing anyone
// reads. Nor may it print a line claiming a binding or a live Forwarder when
// that later write failed (issue #2931).
func TestBindRegistry_EmptyHomeEnvVarAndHomeFailsLoud(t *testing.T) {
	t.Setenv(synthetic.Strategy.HomeConfig.HomeEnvVar, "")
	t.Setenv("HOME", "")

	envStub := exportStubRow("stub-env-ecosystem", "STUB_ENV_REGISTRY")
	envStub.BindingEnvVar = "STUB_ENV_REGISTRY"
	rc, stdout, _ := bindReady(t, rowsWith(envStub), registrymanifest.Route{Prefix: "r0"})
	if rc == 0 {
		t.Fatalf("bind-registry exit = 0, want non-zero when the home var and HOME are both empty (stdout=%q)", stdout)
	}
	if !strings.Contains(stdout, synthetic.Strategy.HomeConfig.HomeEnvVar) {
		t.Errorf("stdout = %q, want it to mention %s", stdout, synthetic.Strategy.HomeConfig.HomeEnvVar)
	}
	if strings.Contains(stdout, "bound to it via") {
		t.Errorf("stdout = %q, want it NOT to claim a binding when a later write (home resolve) failed", stdout)
	}
	if strings.Contains(stdout, "registry proxy Forwarder up on") {
		t.Errorf("stdout = %q, want it NOT to claim the registry proxy Forwarder is up when a later write (home resolve) failed", stdout)
	}
}

// Issue #2931: a Strategy's EnvExports warnings must not print before the
// fallible writes, or stdout claims an override that was never applied.
func TestBindRegistry_HomeResolveFailureOmitsRowWarnings(t *testing.T) {
	t.Setenv(synthetic.Strategy.HomeConfig.HomeEnvVar, "")
	t.Setenv("HOME", "")
	warnStub := exportStubRow("stub-warn-ecosystem", "STUB_WARN_REGISTRY")
	inner := warnStub.EnvExports
	warnStub.EnvExports = func(port int, prefix string, getenv func(string) string, routes []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		exports, _ := inner(port, prefix, getenv, routes)
		return exports, []string{"==> WARNING: overriding STUB_WARN_SETTING"}
	}

	rc, stdout, _ := bindReady(t, rowsWith(warnStub), registrymanifest.Route{Prefix: "r0"})
	if rc == 0 {
		t.Fatalf("bind-registry exit = 0, want non-zero when the home var and HOME are both empty (stdout=%q)", stdout)
	}
	if strings.Contains(stdout, "WARNING: overriding") {
		t.Errorf("stdout = %q, want it NOT to claim an override was applied when a later write (home resolve) failed", stdout)
	}
}

// Issue #3141's timeout warning names the manifest's endpoint. The small
// timeout/pollInterval keep this well under a second instead of the real 5s.
func TestBindRegistry_TimeoutWarnsAndSkipsBindings(t *testing.T) {
	socketPath := mountSocketManifest(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	spawnCalled := false
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
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

// Spawn returning an error, rather than a readiness timeout, also warns naming
// both the endpoint and the error (issue #3141).
func TestBindRegistry_ForwarderSpawnErrorWarnsNamingEndpoint(t *testing.T) {
	socketPath := mountSocketManifest(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, errors.New("boom") },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy Forwarder for endpoint unix://" + socketPath + " failed to start: boom — synthetic will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when spawn fails")
	}
}

// Issue #2931: an already-ready Forwarder must not fail bindings mode because
// socat has since left PATH. lookPath is wired to t.Fatal to prove it is never
// reached.
func TestBindRegistry_AlreadyListeningSkipsSocatCheck(t *testing.T) {
	mountSocketManifest(t, registrymanifest.Route{Prefix: "r0"})
	syntheticHome(t)

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	probeCalled := false
	spawnCalled := false
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { probeCalled = true; return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		func(string) (string, error) {
			t.Fatal("lookPath should not be called when probe already reports ready")
			return "", nil
		},
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
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

// Bindings mode's TCP-fallback transport (issue #3111, manifest-driven since
// #3141) must spawn via spawnHTTPForwarder, never the injected socket-shaped
// spawn, and write the same bindings and home config as socket mode, proving
// the downstream computation is transport-blind.
func TestBindRegistry_TCPTransportWritesBindings(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", registrymanifest.Route{Prefix: "r0"})
	home := syntheticHome(t)

	origSpawnHTTPForwarder := spawnHTTPForwarder
	var gotHost, gotSecret string
	var gotUpstreamPort, gotListenPort int
	spawnHTTPForwarder = func(upstreamHost string, upstreamPort int, secret string, listenPort int) (int, error) {
		gotHost, gotUpstreamPort, gotSecret, gotListenPort = upstreamHost, upstreamPort, secret, listenPort
		return 4242, nil
	}
	t.Cleanup(func() { spawnHTTPForwarder = origSpawnHTTPForwarder })

	// Not ready once forces a spawn, then ready lets the write proceed,
	// covering both paths in one test.
	probeCalls := 0
	probe := func(int) bool {
		probeCalls++
		return probeCalls > 1
	}

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		probe,
		func(string, int) (int, error) {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return 0, nil
		},
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
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
	if want := syntheticExport("r0"); !strings.Contains(string(got), want) {
		t.Errorf("bindings env output = %q, want it to contain %q", got, want)
	}

	homeConfig, err := os.ReadFile(syntheticHomeConfigPath(home))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if want := syntheticHomeConfig("r0"); string(homeConfig) != want {
		t.Errorf("home config = %q, want %q", homeConfig, want)
	}
}

// A TCP endpoint with REGISTRY_PROXY_TCP_SECRET unset warns and no-ops (exit
// 0) rather than failing the verb: the launcher always mints the secret with
// the endpoint, so a missing one is a real misconfiguration.
func TestBindRegistry_TCPTransportMissingSecretWarnsAndSkipsBindings(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")
	setTCPManifestEnv(t, "registry.example", "9443")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — synthetic will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
	if _, err := os.Stat(bindingsOut); err == nil {
		t.Error("bindings-env-output exists, want it untouched when REGISTRY_PROXY_TCP_SECRET is unset")
	}
}

// The TCP branch never reaches lookPath at all, unlike the unix branch, which
// gates it on the spawn path.
func TestBindRegistry_TCPTransportNeverChecksSocat(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "9443", registrymanifest.Route{Prefix: "r0"})
	syntheticHome(t)

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
	rc := runSyntheticBind([]string{
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if strings.Contains(stdout.String(), "socat") {
		t.Errorf("stdout = %q, want no socat-related warning on the TCP transport branch", stdout.String())
	}
	if _, err := os.Stat(bindingsOut); err != nil {
		t.Errorf("bindings-env-output not written: %v", err)
	}
}

// ParseEndpoint does not check that port is numeric, so a non-numeric port
// must warn rather than panic on the strconv.Atoi this guard exists for.
func TestBindRegistry_TCPManifestNonNumericPortWarns(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "s3cr3t")
	setTCPManifestEnv(t, "registry.example", "https")

	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-bindings-env-output", bindingsOut,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called with a non-numeric manifest port"); return false },
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called with a non-numeric manifest port")
			return 0, nil
		},
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:https has a non-numeric port — synthetic will fall back to the public registry\n"
	if stdout.String() != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantWarning)
	}
}

// AC1 for issue #3141: one -intree-action=apply invocation runs both in-tree
// apply modes and must probe/spawn the Forwarder exactly once. probe always
// reports "not ready", so a gate resolved per mode would spawn twice. The
// repo-aware Strategy is a stub: the synthetic Strategy has no repo-aware home
// config, and one carrying it is excluded from the in-tree rewrite.
func TestBindRegistry_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, synthetic.Strategy.InTreeConfigPath, intreeRegistriesContent)

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"})

	spawnCalls := 0
	var stdout bytes.Buffer
	rc := runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, rowsWith(intreeRepoAwareRow), &stdout,
		func(int) bool { return false }, // never ready
		func(string, int) (int, error) { spawnCalls++; return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	if spawnCalls != 1 {
		t.Errorf("spawn called %d times across one invocation running both in-tree apply modes, want exactly 1 (issue #3141's shared gate)", spawnCalls)
	}
	if got := strings.Count(stdout.String(), "did not start listening"); got != 2 {
		t.Errorf("stdout = %q, want the timeout warning exactly twice (once per mode -- in-tree rewrite, repo-aware home config -- from the one shared gate result), got %d", stdout.String(), got)
	}

	got, err := os.ReadFile(filepath.Join(dir, synthetic.Strategy.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != intreeRegistriesContent {
		t.Errorf("in-tree config changed, want byte-for-byte unchanged when the Forwarder never becomes ready")
	}
}

// newIntreeTestRepo returns a fresh, empty local git repo; no remote is needed,
// since skip-worktree and checkout are purely local operations.
func newIntreeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	return dir
}

// intreeRegistriesContent names upstream.example in the synthetic Strategy's
// `name = <url>` shape. ApplyInTreeBinding only string-replaces the upstream
// host, so one line covers every in-tree Strategy.
const intreeRegistriesContent = "main = https://upstream.example/\n"

// intreeRootRow is an in-tree Strategy whose config sits at the repo root, beside
// the synthetic Strategy's nested one.
var intreeRootRow = ecosystem.Strategy{Name: "stub-root", InTreeConfigPath: ".stub-registries"}

// intreeSecondRow is a second in-tree Strategy, so cross-Strategy tests see more than one
// sibling of the synthetic Strategy.
var intreeSecondRow = ecosystem.Strategy{Name: "stub-second", InTreeConfigPath: "stub-second.conf"}

// intreeRepoAwareRow binds through a repo-aware home config, which excludes it
// from the in-tree rewrite loop even though it names an InTreeConfigPath.
var intreeRepoAwareRow = ecosystem.Strategy{
	Name:             "stub-repo-aware",
	InTreeConfigPath: ".stub-repo-aware/registries",
	HomeConfig: &ecosystem.HomeConfig{
		HomeEnvVar:          "STUB_REPO_AWARE_HOME",
		HomeRelativeDefault: ".stub-repo-aware",
		ConfigPath:          "config",
		Render:              func(int, string, []registrymanifest.Route) (string, bool) { return "", false },
	},
	RepoAwareHomeConfig: renderRepoAware,
}

// writeTrackedIntreeFile commits relPath so the in-tree binding sees a
// tracked file.
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

// readIntreeFile returns the content of relPath under dir.
func readIntreeFile(t *testing.T, dir, relPath string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// intreeSkipWorktreeSet reports whether relPath's skip-worktree bit is set,
// reading the same "S " prefix bindregistry.skipWorktreeBitSet uses.
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

// intreeUpstreamRoute has prefix "r0", so a rewritten URL carries a "/r0"
// segment (buildIntreeHostRewrites, issue #3142).
var intreeUpstreamRoute = registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"}

// runIntree runs one in-tree action over Strategies with the default readiness
// timings.
func runIntree(action, dir string, rows []ecosystem.Strategy, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath bindregistry.LookPathFunc) (rc int, stdout string) {
	var out bytes.Buffer
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", action,
		"-intree-work-dir", dir,
	}, rows, &out, probe, spawn, lookPath,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	return rc, out.String()
}

// applyReady runs in-tree apply over Strategies against a Forwarder already reporting
// ready, so it never spawns.
func applyReady(dir string, rows []ecosystem.Strategy) (rc int, stdout string) {
	return runIntree("apply", dir, rows,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
	)
}

// wantRewritten fails unless the file at relPath no longer names the upstream
// host and carries the local Forwarder URL.
func wantRewritten(t *testing.T, dir, relPath, when string) {
	t.Helper()
	got := readIntreeFile(t, dir, relPath)
	if strings.Contains(got, "upstream.example") {
		t.Errorf("%s still mentions upstream.example %s: %s", relPath, when, got)
	}
	if !strings.Contains(got, "127.0.0.1:"+forwarderPortStr) {
		t.Errorf("%s missing rewritten local Forwarder URL %s: %s", relPath, when, got)
	}
}

// AC5, the all-or-nothing gate (issue #2932): with the Forwarder dead, apply
// must leave the file byte-for-byte unchanged and the skip-worktree bit unset.
func TestBindRegistry_IntreeApplyDeadForwarderLeavesFileUntouched(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t, intreeUpstreamRoute)

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, &stdout,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		20*time.Millisecond, 5*time.Millisecond,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want byte-for-byte unchanged:\ngot:  %q\nwant: %q", rel, got, intreeRegistriesContent)
	}
	if intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit set, want it unset when the Forwarder never became ready")
	}
}

func TestBindRegistry_IntreeApplyReadyRewritesAndHidesFromGit(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t, intreeUpstreamRoute)

	spawnCalled := false
	rc, stdout := runIntree("apply", dir, rowsWith(),
		func(int) bool { return true },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when probe already reports ready")
	}

	// Issue #3044: the already-ready short-circuit never spawns, so no PID line.
	if strings.Contains(stdout, "registry proxy Forwarder pid") {
		t.Errorf("stdout = %q, want no Forwarder pid line when probe already reports ready (nothing was spawned)", stdout)
	}

	wantRewritten(t, dir, rel, "after apply")
	if !intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// Issue #3142's buildIntreeHostRewrites filter: an empty-UpstreamHost route
// beside a real one is skipped, never reaching ApplyInTreeBinding's own
// empty-entry error, and the valid route's rewrite still applies.
func TestBindRegistry_IntreeApplySkipsRouteWithEmptyUpstreamHost(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t, intreeUpstreamRoute, registrymanifest.Route{Prefix: "r1", UpstreamHost: ""})

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantRewritten(t, dir, rel, "after apply with the valid route")
}

// When every route's UpstreamHost is empty, the result reads as "no route
// upstream host at all".
func TestBindRegistry_IntreeApplyAllRoutesEmptyUpstreamHostWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t, registrymanifest.Route{Prefix: "r0", UpstreamHost: ""})

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want untouched when every route's upstream host is empty", rel)
	}
	if want := "registry proxy manifest carries no route upstream host"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}

// Routes sharing one upstream host warn, and the generic "carries no route
// upstream host" warning must be suppressed: the manifest does carry one, it
// is just unusable for host-based rewriting.
func TestBindRegistry_IntreeApplyDuplicateUpstreamHostWarnsAndSuppressesGenericWarning(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t,
		registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"},
		registrymanifest.Route{Prefix: "r1", UpstreamHost: "upstream.example"},
	)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want untouched when every route's upstream host collides", rel)
	}

	want := "==> WARNING: registry proxy manifest routes r0, r1 share upstream host upstream.example — host-based in-tree rewriting cannot tell them apart, their in-tree registry rewrite is skipped, those ecosystems fall back to the public registry\n"
	if stdout != want {
		t.Errorf("stdout = %q, want exactly %q (generic no-upstream-host warning must be suppressed)", stdout, want)
	}
}

// Issue #3141: box's in-process apply passes no transport flag, so an unset
// manifest silently no-ops apply mode.
func TestBindRegistry_IntreeApplyManifestAbsentIsNoOp(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	clearManifestEnv(t)

	rc, stdout := runIntree("apply", dir, rowsWith(),
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_MANIFEST is absent")
			return 0, nil
		},
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want untouched when REGISTRY_PROXY_MANIFEST is absent", rel)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty when REGISTRY_PROXY_MANIFEST is absent (the one deliberate silence, per issue #3082)", stdout)
	}
}

// A present manifest means the registry proxy is genuinely configured, so a
// missing route upstream host warns rather than silently no-ops.
func TestBindRegistry_IntreeApplyEmptyUpstreamHostWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t) // no routes at all

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want untouched when the manifest carries no route upstream host", rel)
	}
	if want := "registry proxy manifest carries no route upstream host"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q (a present manifest means the proxy is genuinely configured, so a missing upstream host must warn)", stdout, want)
	}
}

// Issue #2932: the intree-apply path must print a socat-specific warning
// naming the endpoint rather than EnsureForwarderReady's generic "failed to
// start" one.
func TestBindRegistry_IntreeApplySocatMissingWarnsAndSkipsRewrite(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	socketPath := mountSocketManifest(t, intreeUpstreamRoute)

	probeCalled := false
	spawnCalled := false
	rc, stdout := runIntree("apply", dir, rowsWith(),
		func(int) bool { probeCalled = true; return false },
		func(string, int) (int, error) { spawnCalled = true; return 0, nil },
		lookPathMissing,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantWarning := "==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n" +
		"==> WARNING: registry proxy endpoint unix://" + socketPath + " is mounted but socat is not on PATH — the repo-aware registry binding is skipped, ecosystems fall back to the public registry\n"
	if stdout != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout, wantWarning)
	}
	if !probeCalled {
		t.Error("probe was never called, want it called to check whether a Forwarder is already listening before gating on socat")
	}
	if spawnCalled {
		t.Error("spawn was called, want it never called when socat is missing from PATH")
	}

	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want byte-for-byte unchanged when socat is missing from PATH:\ngot:  %q\nwant: %q", rel, got, intreeRegistriesContent)
	}
	if intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit set, want it unset when socat is missing from PATH")
	}
}

// Issue #3111's TCP-fallback transport in intree-apply mode: apply must spawn
// via spawnHTTPForwarder, never the injected socket-shaped spawn.
func TestBindRegistry_IntreeApplyTCPTransportRewritesFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
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

	// Not ready once, then ready: forces one spawn, then the apply proceeds.
	probeCalls := 0
	probe := func(int) bool {
		probeCalls++
		return probeCalls > 1
	}

	rc, stdout := runIntree("apply", dir, rowsWith(),
		probe,
		func(string, int) (int, error) {
			t.Fatal("the injected socket-shaped spawn must not be called on the TCP transport branch")
			return 0, nil
		},
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	if gotHost != "registry.example" || gotUpstreamPort != 9443 || gotSecret != "s3cr3t" || gotListenPort != bindregistry.ForwarderPort {
		t.Errorf("spawnHTTPForwarder called with (%q, %d, %q, %d), want (%q, %d, %q, %d)",
			gotHost, gotUpstreamPort, gotSecret, gotListenPort, "registry.example", 9443, "s3cr3t", bindregistry.ForwarderPort)
	}

	wantRewritten(t, dir, rel, "after apply")
	if !intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// The intree-apply counterpart of the missing-secret bindings test.
func TestBindRegistry_IntreeApplyTCPTransportMissingSecretWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	t.Setenv("REGISTRY_PROXY_TCP_SECRET", "")
	setTCPManifestEnv(t, "registry.example", "9443", intreeUpstreamRoute)

	rc, stdout := runIntree("apply", dir, rowsWith(),
		func(int) bool {
			t.Fatal("probe should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return false
		},
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called when REGISTRY_PROXY_TCP_SECRET is unset")
			return 0, nil
		},
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantWarning := "==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry\n" +
		"==> WARNING: registry proxy endpoint tcp://registry.example:9443 requires REGISTRY_PROXY_TCP_SECRET, which is not set — the repo-aware registry binding is skipped, ecosystems fall back to the public registry\n"
	if stdout != wantWarning {
		t.Errorf("stdout = %q, want %q", stdout, wantWarning)
	}

	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s changed, want untouched when REGISTRY_PROXY_TCP_SECRET is unset", rel)
	}
}

// Revert runs with no manifest set: it is a pure git operation that never
// consults REGISTRY_PROXY_MANIFEST or the probe/spawn deps.
func TestBindRegistry_IntreeRevertRestoresAppliedFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	clearManifestEnv(t)

	outcome, err := bindregistry.ApplyInTreeBinding(dir, synthetic.Strategy, []bindregistry.HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:" + forwarderPortStr}})
	if err != nil || outcome != bindregistry.ApplyApplied {
		t.Fatalf("ApplyInTreeBinding (setup) = (%v, %v), want (%v, nil)", outcome, err, bindregistry.ApplyApplied)
	}

	rc, stdout := runIntree("revert", dir, rowsWith(),
		func(int) bool { t.Fatal("probe should not be called on revert"); return false },
		func(string, int) (int, error) { t.Fatal("spawn should not be called on revert"); return 0, nil },
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
		t.Errorf("%s = %q, want restored to %q", rel, got, intreeRegistriesContent)
	}
	if intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit still set, want it cleared after revert")
	}
}

// The multi-Strategy happy path; the other intree happy-path tests exercise
// the synthetic Strategy only. A tracked repo-aware Strategy's config sits beside three
// in-tree Strategies and no pass may touch it: a repo-aware Strategy is not in
// InTreeBindings at all (issue #3201).
func TestBindRegistry_IntreeApplyAndRevertAcrossRows(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rows := rowsWith(intreeRootRow, intreeSecondRow, intreeRepoAwareRow)
	repoAwareRel := intreeRepoAwareRow.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, repoAwareRel, intreeRegistriesContent)
	relPaths := []string{synthetic.Strategy.InTreeConfigPath, intreeRootRow.InTreeConfigPath, intreeSecondRow.InTreeConfigPath}
	for _, rel := range relPaths {
		writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	}
	mountSocketManifest(t, intreeUpstreamRoute)

	assertRepoAwareUntouched := func() {
		t.Helper()
		if got := readIntreeFile(t, dir, repoAwareRel); got != intreeRegistriesContent {
			t.Errorf("%s = %q, want byte-for-byte unchanged (a repo-aware row binds via its home config, not the in-tree rewrite)", repoAwareRel, got)
		}
		if intreeSkipWorktreeSet(t, dir, repoAwareRel) {
			t.Errorf("%s skip-worktree bit set, want it never tagged", repoAwareRel)
		}
	}
	assertApplied := func(when string) {
		t.Helper()
		for _, rel := range relPaths {
			wantRewritten(t, dir, rel, when)
			if !intreeSkipWorktreeSet(t, dir, rel) {
				t.Errorf("%s skip-worktree bit not set %s", rel, when)
			}
		}
		assertRepoAwareUntouched()
	}

	rc, stdout := applyReady(dir, rows)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	assertApplied("after apply")

	rc, stdout = runIntree("revert", dir, rows,
		func(int) bool { return false },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
	)
	if rc != 0 {
		t.Fatalf("bind-registry revert exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	for _, rel := range relPaths {
		if got := readIntreeFile(t, dir, rel); got != intreeRegistriesContent {
			t.Errorf("%s = %q after revert, want restored to %q", rel, got, intreeRegistriesContent)
		}
		if intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit still set after revert", rel)
		}
	}
	assertRepoAwareUntouched()

	// Re-apply (AC4): the revert/re-apply around branch recovery must rewrite
	// and re-tag every Strategy.
	rc, stdout = applyReady(dir, rows)
	if rc != 0 {
		t.Fatalf("bind-registry re-apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	assertApplied("after re-apply")
}

// newIntreeUnmergedTestRepo leaves the synthetic Strategy's config genuinely
// unmerged (UU), replicating bindregistry's unexported newUnmergedTestRepo, so
// setting skip-worktree on it fails with exit 128 and ApplyInTreeBinding gets
// a genuine per-Strategy failure. Siblings stay tracked and clean.
func newIntreeUnmergedTestRepo(t *testing.T, siblings ...ecosystem.Strategy) string {
	t.Helper()
	dir := newIntreeTestRepo(t)

	for _, row := range siblings {
		writeTrackedIntreeFile(t, dir, row.InTreeConfigPath, intreeRegistriesContent)
	}

	rel := synthetic.Strategy.InTreeConfigPath
	full := filepath.Join(dir, rel)
	write := func(content string) {
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(intreeRegistriesContent)
	runGitCmd(t, dir, "add", rel)
	runGitCmd(t, dir, "commit", "-m", "add "+rel)
	base := strings.TrimSpace(runGitCmd(t, dir, "symbolic-ref", "--short", "HEAD"))

	runGitCmd(t, dir, "checkout", "-b", "feature")
	write("main = https://upstream.example/other/\n")
	runGitCmd(t, dir, "add", rel)
	runGitCmd(t, dir, "commit", "-m", "feature")

	runGitCmd(t, dir, "checkout", base)
	write("main = https://upstream.example/index2/\n")
	runGitCmd(t, dir, "add", rel)
	runGitCmd(t, dir, "commit", "-m", "base2")

	// The conflicting merge is the point, so a nonzero exit is desired here.
	if err := exec.Command("git", "-C", dir, "merge", "feature").Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", rel).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", rel, status, err)
	}

	return dir
}

// Regression: the apply loop once returned on the first Strategy error. A
// failing Strategy (the unmerged first one) must not stop the loop, and the
// overall apply must still report failure.
func TestBindRegistry_IntreeApplyPartialFailureDoesNotBlockSiblingRows(t *testing.T) {
	siblings := []ecosystem.Strategy{intreeRootRow, intreeSecondRow}
	dir := newIntreeUnmergedTestRepo(t, siblings...)
	failingRel := synthetic.Strategy.InTreeConfigPath
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith(siblings...))
	if rc != 1 {
		t.Fatalf("bind-registry exit = %d, want 1 when one row genuinely fails (stdout=%q)", rc, stdout)
	}

	if got := readIntreeFile(t, dir, failingRel); !strings.Contains(got, "upstream.example") {
		t.Errorf("%s rewritten despite the failing skip-worktree call: %s", failingRel, got)
	}
	if intreeSkipWorktreeSet(t, dir, failingRel) {
		t.Errorf("%s skip-worktree bit set, want it unset since the row genuinely failed", failingRel)
	}

	for _, row := range siblings {
		rel := row.InTreeConfigPath
		wantRewritten(t, dir, rel, "despite the first row failing")
		if !intreeSkipWorktreeSet(t, dir, rel) {
			t.Errorf("%s skip-worktree bit not set, want the first row's failure not to block its siblings", rel)
		}
	}
}

// Issue #3082: ApplyMissing must read differently from ApplyNoopContent; the
// two point an operator at different fixes (registry pinned outside the repo
// vs. wrong upstream host).
func TestBindRegistry_IntreeApplyMissingConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	want := "==> synthetic config .synthetic/registries not found — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
	if got := strings.Count(stdout, want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout, want, got)
	}
}

// Issue #2933's `[ -f ]` parity guard gets an operator-facing message (#3082).
func TestBindRegistry_IntreeApplyNotRegularConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, synthetic.Strategy.InTreeConfigPath), 0o755); err != nil {
		t.Fatalf("mkdir the config path as a directory: %v", err)
	}
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	want := "==> WARNING: synthetic config .synthetic/registries exists but is not a regular file — the in-tree registry rewrite is skipped, ecosystems fall back to the public registry"
	if got := strings.Count(stdout, want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout, want, got)
	}
}

// The ApplyUntracked warning survives the bool-to-ApplyOutcome change (#3082)
// unchanged.
func TestBindRegistry_IntreeApplyUntrackedConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	full := filepath.Join(dir, synthetic.Strategy.InTreeConfigPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(intreeRegistriesContent), 0o644); err != nil {
		t.Fatal(err)
	}
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	want := "==> WARNING: synthetic config .synthetic/registries exists but is not tracked by git — skipping the in-tree registry rewrite for it"
	if got := strings.Count(stdout, want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout, want, got)
	}
}

// Issue #2932's crash window: the bit can be tagged before content is
// rewritten, so "bit set" never proves convergence, and the warning must read
// differently from ApplyNoopContent's "nothing to do".
func TestBindRegistry_IntreeApplySkipWorktreeAlreadySetWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Strategy.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	runGitCmd(t, dir, "update-index", "--skip-worktree", "--", rel)
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	want := "==> WARNING: synthetic config .synthetic/registries already has the skip-worktree bit set — its content was not re-checked, so if a prior run crashed between tagging the bit and rewriting the content, it may still point at the real upstream while hidden from git status"
	if got := strings.Count(stdout, want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout, want, got)
	}
}

// ApplyNoopContent points at a different fix than ApplyMissing: the manifest's
// route upstream host is wrong.
func TestBindRegistry_IntreeApplyNoopContentWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, synthetic.Strategy.InTreeConfigPath, "main = https://other.example/\n")
	mountSocketManifest(t, intreeUpstreamRoute)

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	want := "==> WARNING: synthetic config .synthetic/registries no longer references upstream host upstream.example — the in-tree registry rewrite is skipped, verify the registry proxy manifest's route upstream host is set correctly"
	if got := strings.Count(stdout, want); got != 1 {
		t.Errorf("stdout = %q, want it to contain %q exactly once, got %d", stdout, want, got)
	}
}

// renderRepoAware is intreeRepoAwareRow's renderer: it echoes what the verb
// hands a repo-aware renderer into the home config, exports one placeholder
// per route and warns once, both only when the repo's own config is non-empty.
func renderRepoAware(port int, prefix string, routes []registrymanifest.Route, repoConfig string) (string, []ecosystem.EnvExport, []string) {
	var b strings.Builder
	b.WriteString("port=" + strconv.Itoa(port) + " prefix=" + prefix + " repo=" + strconv.Quote(repoConfig) + "\n")
	var exports []ecosystem.EnvExport
	for _, r := range routes {
		b.WriteString("route=" + r.Prefix + " path=" + r.Ecosystems.Path("stub-repo-aware") + "\n")
		if repoConfig != "" {
			exports = append(exports, ecosystem.EnvExport{Name: "STUB_REPO_AWARE_" + strings.ToUpper(r.Prefix) + "_TOKEN", Value: "placeholder"})
		}
	}
	var warnings []string
	if repoConfig != "" {
		warnings = []string{"==> WARNING: stub-repo-aware read its repo config for prefix " + prefix}
	}
	return b.String(), exports, warnings
}

// repoAwareSetup builds a repo (repoConfig tracked at the repo-aware
// Strategy's in-tree path unless empty), a ready manifest naming routes, and a
// home directory, returning the repo, the home and an env output path.
func repoAwareSetup(t *testing.T, repoConfig string, routes ...registrymanifest.Route) (dir, home, envOut string) {
	t.Helper()
	dir = newIntreeTestRepo(t)
	if repoConfig != "" {
		writeTrackedIntreeFile(t, dir, intreeRepoAwareRow.InTreeConfigPath, repoConfig)
	}
	mountSocketManifest(t, routes...)
	home = t.TempDir()
	t.Setenv(intreeRepoAwareRow.HomeConfig.HomeEnvVar, home)
	envOut = filepath.Join(t.TempDir(), "intree-bindings.env")
	return dir, home, envOut
}

// applyRepoAware runs in-tree apply over the repo-aware Strategy alone against a
// Forwarder already reporting ready.
func applyRepoAware(dir, envOut string) (rc int, stdout string) {
	var out bytes.Buffer
	rc = runBindRegistryWithDeps([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
		"-intree-bindings-env-output", envOut,
	}, []ecosystem.Strategy{intreeRepoAwareRow}, &out,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	return rc, out.String()
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// The end-to-end proof the verb reaches a Strategy's RepoAwareHomeConfig: the
// home config must match that renderer's own output for the same inputs.
func TestBindRegistry_IntreeApplyWritesRepoAwareHomeConfig(t *testing.T) {
	dir, home, envOut := repoAwareSetup(t, intreeRegistriesContent, intreeUpstreamRoute)

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got := readFileOrFail(t, filepath.Join(home, intreeRepoAwareRow.HomeConfig.ConfigPath))
	want, _, _ := renderRepoAware(bindregistry.ForwarderPort, "r0", []registrymanifest.Route{intreeUpstreamRoute}, intreeRegistriesContent)
	if got != want {
		t.Errorf("home config = %q, want %q", got, want)
	}

	// A repo-aware Strategy binds through its home config, so the tracked
	// repo file stays untouched.
	if repoConfig := readIntreeFile(t, dir, intreeRepoAwareRow.InTreeConfigPath); repoConfig != intreeRegistriesContent {
		t.Errorf("%s = %q, want byte-for-byte unchanged", intreeRepoAwareRow.InTreeConfigPath, repoConfig)
	}
}

// The other half of the same apply: the Strategy's exports reach the env output.
func TestBindRegistry_IntreeApplyWritesRepoAwareEnvOutput(t *testing.T) {
	dir, _, envOut := repoAwareSetup(t, intreeRegistriesContent, intreeUpstreamRoute)

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	want := "export STUB_REPO_AWARE_R0_TOKEN='placeholder'\n"
	if got := readFileOrFail(t, envOut); got != want {
		t.Errorf("intree bindings env output = %q, want %q", got, want)
	}
}

// The common case: a repo with no tracked config for the Strategy still gets its home
// config rendered from empty content, and exports nothing.
func TestBindRegistry_IntreeApplyNoRepoConfigWritesEmptyEnvOutputAndBaseConfig(t *testing.T) {
	dir, home, envOut := repoAwareSetup(t, "", intreeUpstreamRoute)

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got := readFileOrFail(t, filepath.Join(home, intreeRepoAwareRow.HomeConfig.ConfigPath))
	want, _, _ := renderRepoAware(bindregistry.ForwarderPort, "r0", []registrymanifest.Route{intreeUpstreamRoute}, "")
	if got != want {
		t.Errorf("home config = %q, want the empty-content render %q", got, want)
	}
	if envGot := readFileOrFail(t, envOut); envGot != "" {
		t.Errorf("intree bindings env output = %q, want empty (no repo config)", envGot)
	}
}

// Every route in the manifest reaches the renderer, and each route's exports
// reach the env output.
func TestBindRegistry_IntreeApplyPassesEveryRouteToRepoAwareRenderer(t *testing.T) {
	route1 := registrymanifest.Route{Prefix: "r1", UpstreamHost: "other.example"}
	dir, home, envOut := repoAwareSetup(t, intreeRegistriesContent, intreeUpstreamRoute, route1)

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got := readFileOrFail(t, filepath.Join(home, intreeRepoAwareRow.HomeConfig.ConfigPath))
	want, _, _ := renderRepoAware(bindregistry.ForwarderPort, "r0", []registrymanifest.Route{intreeUpstreamRoute, route1}, intreeRegistriesContent)
	if got != want {
		t.Errorf("home config = %q, want %q", got, want)
	}

	envGot := readFileOrFail(t, envOut)
	for _, wantExport := range []string{
		"export STUB_REPO_AWARE_R0_TOKEN='placeholder'",
		"export STUB_REPO_AWARE_R1_TOKEN='placeholder'",
	} {
		if !strings.Contains(envGot, wantExport) {
			t.Errorf("intree bindings env output = %q, want it to contain %q", envGot, wantExport)
		}
	}
}

// Without this end-to-end assertion, the repo-aware warning print loop in
// bindregistry.ApplyInTree could be deleted and nothing would fail.
func TestBindRegistry_IntreeApplyPrintsRepoAwareWarningsToStdout(t *testing.T) {
	dir, _, envOut := repoAwareSetup(t, intreeRegistriesContent, intreeUpstreamRoute)

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	want := "==> WARNING: stub-repo-aware read its repo config for prefix r0"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}

// multiBlockRoute carries a declaration block for each Strategy below plus one
// no Strategy knows. Every assertion using it is exact, so the unknown block
// must change neither the exit code nor a rendered byte.
func multiBlockRoute() registrymanifest.Route {
	return registrymanifest.Route{
		Prefix:       "r0",
		UpstreamHost: "upstream.example",
		Ecosystems: registryvocab.RouteEcosystems{
			"synthetic":       registryvocab.RouteDeclaration{"mirror-path": "/artifactory/synthetic-remote"},
			"stub-env":        registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/env-remote"},
			"stub-home":       registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/home-remote"},
			"stub-repo-aware": registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/repo-aware-remote"},
			"stub-unknown":    registryvocab.RouteDeclaration{registryvocab.RouteDeclarationPathKey: "/artifactory/unknown-remote"},
		},
	}
}

// pathEnvRow exports a var valued at the route's local URL plus the path its own
// block declares.
var pathEnvRow = ecosystem.Strategy{
	Name: "stub-env",
	EnvExports: func(port int, prefix string, _ func(string) string, routes []registrymanifest.Route) ([]ecosystem.EnvExport, []string) {
		return []ecosystem.EnvExport{{Name: "STUB_ENV_REGISTRY", Value: ecosystem.RouteLocalURL(prefix, port) + routes[0].Ecosystems.Path("stub-env")}}, nil
	},
}

// pathHomeRow renders a home config naming the path its own block declares.
var pathHomeRow = ecosystem.Strategy{
	Name: "stub-home",
	HomeConfig: &ecosystem.HomeConfig{
		HomeEnvVar:          "STUB_HOME_HOME",
		HomeRelativeDefault: ".stub-home",
		ConfigPath:          "stub.conf",
		Render: func(port int, prefix string, routes []registrymanifest.Route) (string, bool) {
			return "url = " + ecosystem.RouteLocalURL(prefix, port) + routes[0].Ecosystems.Path("stub-home") + "/\n", true
		},
	},
}

// The pre-clone half: each renderer binds to its own block's declared path. A
// shared source would give both the same path, or one of them none.
func TestBindRegistry_OneRouteBindsEachRowFromItsOwnBlock(t *testing.T) {
	syntheticHome(t)
	stubHome := t.TempDir()
	t.Setenv(pathHomeRow.HomeConfig.HomeEnvVar, stubHome)

	rc, stdout, bindingsOut := bindReady(t, rowsWith(pathEnvRow, pathHomeRow), multiBlockRoute())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	gotEnv := readFileOrFail(t, bindingsOut)
	wantEnv := syntheticExport("r0") + "\n" +
		"export STUB_ENV_REGISTRY='" + ecosystem.RouteLocalURL("r0", bindregistry.ForwarderPort) + "/artifactory/env-remote'\n"
	if gotEnv != wantEnv {
		t.Errorf("bindings env output = %q, want %q", gotEnv, wantEnv)
	}

	gotHome := readFileOrFail(t, filepath.Join(stubHome, pathHomeRow.HomeConfig.ConfigPath))
	wantHome := "url = " + ecosystem.RouteLocalURL("r0", bindregistry.ForwarderPort) + "/artifactory/home-remote/\n"
	if gotHome != wantHome {
		t.Errorf("stub home config = %q, want %q", gotHome, wantHome)
	}
}

// The repo-aware half over the same route: the renderer reads its own block
// among the others.
func TestBindRegistry_IntreeApplyRepoAwareRowReadsItsOwnBlock(t *testing.T) {
	dir, home, envOut := repoAwareSetup(t, intreeRegistriesContent, multiBlockRoute())

	rc, stdout := applyRepoAware(dir, envOut)
	if rc != 0 {
		t.Fatalf("bind-registry apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got := readFileOrFail(t, filepath.Join(home, intreeRepoAwareRow.HomeConfig.ConfigPath))
	want := "port=" + forwarderPortStr + " prefix=r0 repo=" + strconv.Quote(intreeRegistriesContent) + "\n" +
		"route=r0 path=/artifactory/repo-aware-remote\n"
	if got != want {
		t.Errorf("home config = %q, want %q", got, want)
	}
}

// A missing config means "this repo declares nothing", but an unreadable one
// must fail the run. The unreadable file is a directory, so os.ReadFile
// returns EISDIR without depending on a non-root user, which a Box does not
// guarantee.
func TestBindRegistry_IntreeApplyUnreadableRepoConfigFails(t *testing.T) {
	dir, _, envOut := repoAwareSetup(t, "", intreeUpstreamRoute)
	if err := os.MkdirAll(filepath.Join(dir, intreeRepoAwareRow.InTreeConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}

	rc, stdout := applyRepoAware(dir, envOut)
	if rc == 0 {
		t.Fatalf("bind-registry apply exit = %d, want non-zero when a repo config is unreadable (stdout=%q)", rc, stdout)
	}

	if want := "driver-exec bind-registry: read stub-repo-aware repo config:"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}

	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%s) err = %v, want the env output never written on a failed run", envOut, err)
	}
}

// -intree-bindings-env-output must be paired with -intree-action=apply, mirroring
// the other flag-pair validation errors in runBindRegistryWithDeps.
func TestBindRegistry_IntreeBindingsEnvOutputRequiresApply(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "intree-bindings.env")

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-intree-bindings-env-output", envOut,
	}, &stdout,
		func(int) bool { t.Fatal("probe should not be called on a validation error"); return false },
		func(string, int) (int, error) {
			t.Fatal("spawn should not be called on a validation error")
			return 0, nil
		},
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runSyntheticBind exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	want := "driver-exec bind-registry: -intree-bindings-env-output requires -intree-action=apply\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// Issue #3201: apply re-renders the repo-aware home configs from the repo's
// tracked config, and bindings mode would then re-render them from the base
// template in the same invocation, clobbering what apply wrote. No caller
// combines the two flags today; this guards against one starting to.
func TestBindRegistry_IntreeApplyWithBindingsEnvOutputRejected(t *testing.T) {
	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc == 0 {
		t.Fatalf("runSyntheticBind exit = 0, want non-zero (stdout=%q)", stdout.String())
	}
	want := "driver-exec bind-registry: -intree-action=apply and -bindings-env-output cannot be combined in one invocation — bindings mode would re-render the repo-aware rows' home configs from the base template and undo the apply\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// The -intree-work-dir/-intree-action pair and -intree-action value checks.
func TestBindRegistry_IntreeFlagValidation(t *testing.T) {
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
			rc := runSyntheticBind(c.args, &stdout,
				func(int) bool { return true },
				func(string, int) (int, error) { return 0, nil },
				lookPathFound,
				bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
			)
			if rc == 0 {
				t.Fatalf("runSyntheticBind exit = 0, want non-zero for %v (stdout=%q)", c.args, stdout.String())
			}
		})
	}
}

// Issue #3199: the lockfile scan reads the manifest only to decide on or off;
// it never resolves the Forwarder-readiness gate or spawns a Forwarder.
func TestBindRegistry_LockfileScanWarnsOnHit(t *testing.T) {
	dir := newIntreeTestRepo(t)
	lockfile := synthetic.Strategy.LockfileNames[0]
	writeTrackedIntreeFile(t, dir, lockfile, "source = \"http://127.0.0.1:"+forwarderPortStr+"/\"\n")
	setUnixManifestEnv(t, shortUnixSocketPath(t))

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runSyntheticBind exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: " + synthetic.Strategy.Classification + " lockfile " + lockfile + " still names the registry proxy Forwarder URL 127.0.0.1:" + forwarderPortStr + " — this will ship in the PR (issue #3199)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// -lockfile-scan-work-dir alone satisfies the "at least one mode" guard without
// requiring any other flag.
func TestBindRegistry_LockfileScanFlagAloneSatisfiesModeGuard(t *testing.T) {
	dir := newIntreeTestRepo(t)
	clearManifestEnv(t)

	var stdout bytes.Buffer
	rc := runSyntheticBind([]string{
		"-lockfile-scan-work-dir", dir,
	}, &stdout,
		func(int) bool { return true },
		func(string, int) (int, error) { return 0, nil },
		lookPathFound,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runSyntheticBind exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
}

// The command-injection fix on renderEnvExports: a value carrying shell
// metacharacters must round-trip through a real `source` unexpanded. The input
// flagged on issue #3259 is a host-rooted route's derived path, sourced from a
// repo's committed, therefore untrusted, registry config.
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
				{Name: "STUB_REGISTRY", Value: tc.value},
			})

			dir := t.TempDir()
			envFile := filepath.Join(dir, "env.sh")
			if err := os.WriteFile(envFile, []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(path, "-c", `source "$1"; printf '%s' "$STUB_REGISTRY"`, "--", envFile)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sourcing rendered exports failed: %v (output=%q)", err, out)
			}
			if got := string(out); got != tc.value {
				t.Errorf("sourced STUB_REGISTRY = %q, want unexpanded %q (rendered=%q)", got, tc.value, rendered)
			}
		})
	}
}
