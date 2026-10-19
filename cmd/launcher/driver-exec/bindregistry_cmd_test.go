package main

import (
	"bytes"
	"errors"
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
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// A hermetic $HOME with every row's home-directory env var left unset (issue
// #3201): a run reaching a home-config write would otherwise land in the real
// ambient one. Tests that t.Setenv their own HOME or home-directory var scope
// back to this default at teardown.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "bindregistry-cmd-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	for _, row := range ecosystem.HomeConfigRows() {
		os.Unsetenv(row.HomeConfig.HomeEnvVar)
	}
	os.Unsetenv(synthetic.Row.HomeConfig.HomeEnvVar)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// lookPathFound stubs bindregistry.ResolveGate's LookPath dep to report
// socat found without touching the real PATH (issue #3141's CI fix): the Nix
// test sandbox has no socat, which let this shared-gate test pass locally
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

// runSyntheticBind is runBindRegistryWithRows over the kit's synthetic row
// alone: the generic behaviour of the verb, with no real ecosystem's rendering
// in the way.
func runSyntheticBind(args []string, stdout io.Writer, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath bindregistry.LookPathFunc, timeout, pollInterval time.Duration) int {
	return runBindRegistryWithRows(args, []ecosystem.Row{synthetic.Row}, stdout, probe, spawn, lookPath, timeout, pollInterval)
}

// rowsWith returns the synthetic row followed by the stub rows, in that order.
func rowsWith(extra ...ecosystem.Row) []ecosystem.Row {
	return append([]ecosystem.Row{synthetic.Row}, extra...)
}

// exportStubRow is a row exporting each named var, valued at the route's local
// URL.
func exportStubRow(name string, vars ...string) ecosystem.Row {
	return ecosystem.Row{
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

// syntheticHome points the synthetic row's home-directory var at a fresh
// directory and returns it.
func syntheticHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(synthetic.Row.HomeConfig.HomeEnvVar, dir)
	return dir
}

// syntheticHomeConfigPath is where the synthetic row's home config lands under home.
func syntheticHomeConfigPath(home string) string {
	return filepath.Join(home, synthetic.Row.HomeConfig.ConfigPath)
}

// syntheticHomeConfig is the synthetic row's home config for a route prefix at
// the Forwarder's port, from the row's own renderer.
func syntheticHomeConfig(prefix string) string {
	return synthetic.Row.HomeConfig.Render(bindregistry.ForwarderPort, prefix, nil)
}

// syntheticExport is the line the synthetic row's env export renders to.
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

// bindReady runs bindings mode over rows against a mounted socket whose probe
// reports ready, returning the exit code, stdout and the env file's path.
func bindReady(t *testing.T, rows []ecosystem.Row, routes ...registrymanifest.Route) (rc int, stdout, bindingsOut string) {
	t.Helper()
	mountSocketManifest(t, routes...)
	bindingsOut = filepath.Join(t.TempDir(), "bindings.env")
	var out bytes.Buffer
	rc = runBindRegistryWithRows([]string{
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

// At least one complete mode must be requested. -bindings-env-output alone is
// sufficient on its own since issue #3141, so it no longer appears among the
// error cases here.
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

// Issue #3141 replaced entrypoint.sh's `[ -S "$REGISTRY_PROXY_SOCKET_PATH" ] ||
// return 0` short-circuit: the launcher sets the manifest only when the registry
// proxy is genuinely enabled, so its absence is the "feature off" signal.
func TestRunBindRegistryWithDeps_ManifestAbsentIsNoOp(t *testing.T) {
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

// A manifest present but broken is distinct from ErrAbsent's empty-string case,
// so issue #3141's "manifest present but unusable" branch warns rather than
// silently no-opping, and never reaches probe/spawn.
func TestRunBindRegistryWithDeps_ManifestMalformedJSONWarnsAndSkipsBindings(t *testing.T) {
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

// Issue #2931's blocking finding: this early-return branch had zero coverage,
// since every other test here injects lookPathFound. The lookPath check only
// gates the spawn path, so it must run after probe, not before.
func TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings(t *testing.T) {
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
func TestRunBindRegistryWithDeps_AlreadyListeningWritesBindings(t *testing.T) {
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

// Issue #3181: bindings mode must collect exports by walking the rows, not by
// naming individual ecosystems' renderers. A stub row's export reaching the
// written file proves the walk; a by-name call site would never see it.
func TestRunBindRegistryWithRows_ExportsComeFromRowsWalk(t *testing.T) {
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

// Issue #3182, the same row-generic contract for home-level config writes: a
// stub row's rendered file reaching disk proves the walk, where a call site
// naming one ecosystem directly would never write it.
func TestRunBindRegistryWithRows_HomeConfigsComeFromRowsWalk(t *testing.T) {
	stubHome := t.TempDir()
	t.Setenv("STUB_ECOSYSTEM_HOME", stubHome)
	syntheticHome(t)
	rows := rowsWith(ecosystem.Row{
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

// Issue #3185, for the success summary's per-ecosystem "bound to it via ..."
// fragments. Two stub rows, one with a BindingEnvVar and one with only a
// HomeConfig, prove both halves of the precedence a row-generic walk applies.
func TestRunBindRegistryWithRows_SuccessSummaryFragmentsComeFromRowsWalk(t *testing.T) {
	stubHome := t.TempDir()
	t.Setenv("STUB_HOME_ECOSYSTEM_HOME", stubHome)
	syntheticHome(t)
	envStub := exportStubRow("stub-env-ecosystem", "STUB_ENV_REGISTRY")
	// The summary skips a BindingEnvVar row whose var went unexported, so the
	// stub needs a renderer or it proves nothing about the walk.
	envStub.BindingEnvVar = "STUB_ENV_REGISTRY"
	rows := rowsWith(
		envStub,
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
// come from walking the rows, not a hand-maintained literal a new row could
// silently be left out of.
func TestRunBindRegistryWithRows_UnusableGateFallbackNamesComeFromRowsWalk(t *testing.T) {
	rows := rowsWith(ecosystem.Row{Name: "stub-ecosystem"})

	t.Setenv(registrymanifest.EnvVar, "{not valid json")
	bindingsOut := filepath.Join(t.TempDir(), "bindings.env")

	var stdout bytes.Buffer
	rc := runBindRegistryWithRows([]string{
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
func TestRunBindRegistryWithRows_NoRoutePrefixFallbackNamesComeFromRowsWalk(t *testing.T) {
	rows := rowsWith(ecosystem.Row{Name: "stub-ecosystem"})

	rc, stdout, _ := bindReady(t, rows) // no routes at all
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	if want := "synthetic and stub-ecosystem will fall back to the public registry"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q (a stub row's name reaching the fallback warning proves a table walk)", stdout, want)
	}
}

// exportNamesInFileOrder parses a rendered bindings env file into its export
// names in line order, the property
// TestRunBindRegistryWithRows_ExportOrderFollowsRows pins and a
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

// The rendered file's line order is the rows' order, each row's exports staying
// contiguous; the stub names sort opposite to their position so a sorted file
// fails here.
func TestRunBindRegistryWithRows_ExportOrderFollowsRows(t *testing.T) {
	syntheticHome(t)
	rows := []ecosystem.Row{
		exportStubRow("stub-z", "STUB_Z_FIRST", "STUB_Z_SECOND"),
		synthetic.Row,
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
// everything to gate.manifest.Routes[0], preserving the pre-prefix routes[0]
// fallback semantics. The distinctive non-"r0" prefix proves real propagation
// from the manifest rather than a coincidental match on a hardcoded default.
func TestRunBindRegistryWithDeps_BindsToFirstRoutePrefixNotSecond(t *testing.T) {
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

// Issue #3142: a manifest with a live Forwarder but zero routes carries no
// prefix to bind to, so it warns and skips rather than binding every ecosystem
// to the bare, now-404ing "http://127.0.0.1:<port>/" URL. This is a defensive
// path: the launcher always mints at least one route whenever it sets
// REGISTRY_PROXY_MANIFEST at all.
func TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings(t *testing.T) {
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

// Mirrors TestRunBindRegistryWithDeps_NoRoutesWarnsAndSkipsBindings for the
// other defensive shape named in the same guard: a route present but its own
// Prefix field left empty.
func TestRunBindRegistryWithDeps_EmptyRoutePrefixWarnsAndSkipsBindings(t *testing.T) {
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

// Issue #2931's blocking finding: the port silently dropped the success log
// line agent/entrypoint.sh printed, leaving the success path printing nothing
// to stdout.
func TestRunBindRegistryWithDeps_AlreadyListeningPrintsSuccessLines(t *testing.T) {
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

// The summary half of issues #3259 and #3260: a BindingEnvVar row whose var the
// route leaves unexported must not appear in the summary, which would otherwise
// claim a binding that does not exist.
func TestRunBindRegistryWithDeps_SummaryOmitsUnexportedBindingVars(t *testing.T) {
	home := syntheticHome(t)
	unexported := ecosystem.Row{
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

// Covers the $HOME/<HomeRelativeDefault> fallback branch itself, not just the
// both-unset guard around it: every other home-config test here either pins the
// row's home var or empties both, so none runs the one branch every real
// deployment takes.
func TestRunBindRegistryWithDeps_EmptyHomeEnvVarFallsBackToHomeRelativeDefault(t *testing.T) {
	t.Setenv(synthetic.Row.HomeConfig.HomeEnvVar, "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	rc, stdout, _ := bindReady(t, rowsWith(), registrymanifest.Route{Prefix: "r0"})
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}

	got, err := os.ReadFile(syntheticHomeConfigPath(filepath.Join(home, synthetic.Row.HomeConfig.HomeRelativeDefault)))
	if err != nil {
		t.Fatalf("read home config: %v", err)
	}
	if want := syntheticHomeConfig("r0"); string(got) != want {
		t.Errorf("home config = %q, want %q", got, want)
	}
}

// The home config write shares the env export write's all-or-nothing readiness
// gate: a Forwarder that never becomes ready must leave the row's home
// directory untouched, not just bindings-env-output.
func TestRunBindRegistryWithDeps_TimeoutSkipsHomeConfig(t *testing.T) {
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

// With a row's home var and $HOME both unset, bindings mode must fail loud
// rather than resolve the home to a path under the cwd or "/", which
// MkdirAll/WriteFile happily create as root, claiming success while binding
// nothing anyone will read. The bash this replaced ran under `set -u` and would
// have died on the unset $HOME expansion. It must also not print a line
// claiming a binding or a live Forwarder when that later write failed (issue
// #2931).
func TestRunBindRegistryWithDeps_EmptyHomeEnvVarAndHomeFailsLoud(t *testing.T) {
	t.Setenv(synthetic.Row.HomeConfig.HomeEnvVar, "")
	t.Setenv("HOME", "")

	envStub := exportStubRow("stub-env-ecosystem", "STUB_ENV_REGISTRY")
	envStub.BindingEnvVar = "STUB_ENV_REGISTRY"
	rc, stdout, _ := bindReady(t, rowsWith(envStub), registrymanifest.Route{Prefix: "r0"})
	if rc == 0 {
		t.Fatalf("bind-registry exit = 0, want non-zero when the home var and HOME are both empty (stdout=%q)", stdout)
	}
	if !strings.Contains(stdout, synthetic.Row.HomeConfig.HomeEnvVar) {
		t.Errorf("stdout = %q, want it to mention %s", stdout, synthetic.Row.HomeConfig.HomeEnvVar)
	}
	if strings.Contains(stdout, "bound to it via") {
		t.Errorf("stdout = %q, want it NOT to claim a binding when a later write (home resolve) failed", stdout)
	}
	if strings.Contains(stdout, "registry proxy Forwarder up on") {
		t.Errorf("stdout = %q, want it NOT to claim the registry proxy Forwarder is up when a later write (home resolve) failed", stdout)
	}
}

// Issue #2931 finding: a row's EnvExports warnings used to print before the
// fallible writes, so stdout could claim an override that was never applied.
func TestRunBindRegistryWithDeps_HomeResolveFailureOmitsRowWarnings(t *testing.T) {
	t.Setenv(synthetic.Row.HomeConfig.HomeEnvVar, "")
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
// timeout/pollInterval keeps this test's wall-clock cost well under a second
// instead of eating the real 5s; production callers still use the real
// bindregistry.ForwarderReadyTimeout/PollInterval constants unchanged.
func TestRunBindRegistryWithDeps_TimeoutWarnsAndSkipsBindings(t *testing.T) {
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

// EnsureForwarderReady's other failure mode, spawn returning an error rather
// than a readiness timeout, also warns naming both the endpoint and the error
// (issue #3141's "manifest present but unusable" branch).
func TestRunBindRegistryWithDeps_ForwarderSpawnErrorWarnsNamingEndpoint(t *testing.T) {
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

// Issue #2931 finding: the LookPath check used to run unconditionally before the
// readiness probe, so an already-ready Forwarder still failed bindings mode
// entirely if socat had since left PATH. lookPath is wired to t.Fatal here to
// prove it is never reached.
func TestRunBindRegistryWithDeps_AlreadyListeningSkipsSocatCheck(t *testing.T) {
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

// Bindings mode's TCP-fallback transport (issue #3111 slice 8, manifest-driven
// since #3141) must spawn via spawnHTTPForwarder, never the injected
// socket-shaped spawn, and write the same bindings and home config the
// socket-mode success test asserts, proving the downstream computation really is
// transport-blind.
func TestRunBindRegistryWithDeps_TCPTransportWritesBindings(t *testing.T) {
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

	// Not-yet-listening once forces EnsureForwarderReady to call spawn, then ready
	// lets the bindings write proceed, covering both paths in one test.
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

// A TCP endpoint with REGISTRY_PROXY_TCP_SECRET unset warns and no-ops (exit 0)
// rather than hard-failing the whole verb: the launcher always mints the secret
// together with the TCP endpoint, so a missing one is a real misconfiguration.
func TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings(t *testing.T) {
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
func TestRunBindRegistryWithDeps_TCPTransportNeverChecksSocat(t *testing.T) {
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

// ParseEndpoint only checks that host and port are non-empty, not that port
// parses as a number, so a non-numeric port must warn rather than panic on the
// strconv.Atoi this internal-consistency guard exists for.
func TestRunBindRegistryWithDeps_TCPManifestNonNumericPortWarns(t *testing.T) {
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
// apply modes and must probe/spawn the Forwarder exactly once across both. probe
// always reports "not ready", so a gate resolved once per mode (the pre-#3141
// shape) would call spawn twice. Issue #3201 split bindings mode out of this
// invocation, so it is no longer folded in here. The repo-aware row is a stub:
// the synthetic row has no repo-aware home config, and a row carrying one is
// excluded from the in-tree rewrite.
func TestRunBindRegistryWithDeps_SharedGateSpawnsForwarderAtMostOnceAcrossBothModes(t *testing.T) {
	const registriesContent = "main = https://upstream.example/\n"
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, synthetic.Row.InTreeConfigPath, registriesContent)

	repoAware := ecosystem.Row{
		Name:             "stub-repo-aware",
		InTreeConfigPath: ".stub-repo-aware/registries",
		HomeConfig: &ecosystem.HomeConfig{
			HomeEnvVar:          "STUB_REPO_AWARE_HOME",
			HomeRelativeDefault: ".stub-repo-aware",
			ConfigPath:          "config",
			Render:              func(int, string, []registrymanifest.Route) string { return "" },
		},
		RepoAwareHomeConfig: func(int, string, []registrymanifest.Route, string) (string, []ecosystem.EnvExport, []string) {
			return "", nil, nil
		},
	}

	socketPath := shortUnixSocketPath(t)
	listenOnFakeSocket(t, socketPath)
	setUnixManifestEnv(t, socketPath, registrymanifest.Route{Prefix: "r0", UpstreamHost: "upstream.example"})

	spawnCalls := 0
	var stdout bytes.Buffer
	rc := runBindRegistryWithRows([]string{
		"-intree-action", "apply",
		"-intree-work-dir", dir,
	}, rowsWith(repoAware), &stdout,
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

	got, err := os.ReadFile(filepath.Join(dir, synthetic.Row.InTreeConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != registriesContent {
		t.Errorf("in-tree config changed, want byte-for-byte unchanged when the Forwarder never becomes ready")
	}
}

// newIntreeTestRepo returns a fresh, empty git repo: one local dir, since
// skip-worktree/checkout are purely local operations. Reuses the shared
// runGitCmd test helper.
func newIntreeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	return dir
}

// intreeRegistriesContent names upstream.example in the synthetic row's
// `name = <url>` shape. ApplyInTreeBinding only string-replaces the upstream
// host and never parses the file, so one line covers every in-tree row.
const intreeRegistriesContent = "main = https://upstream.example/\n"

// intreeRootRow is an in-tree row whose config sits at the repo root, beside
// the synthetic row's nested one.
var intreeRootRow = ecosystem.Row{Name: "stub-root", InTreeConfigPath: ".stub-registries"}

// intreeSecondRow is a second in-tree row, so cross-row tests see more than one
// sibling of the synthetic row.
var intreeSecondRow = ecosystem.Row{Name: "stub-second", InTreeConfigPath: "stub-second.conf"}

// intreeRepoAwareRow binds through a repo-aware home config, which excludes it
// from the in-tree rewrite loop even though it names an InTreeConfigPath.
var intreeRepoAwareRow = ecosystem.Row{
	Name:             "stub-repo-aware",
	InTreeConfigPath: ".stub-repo-aware/registries",
	HomeConfig: &ecosystem.HomeConfig{
		HomeEnvVar:          "STUB_REPO_AWARE_HOME",
		HomeRelativeDefault: ".stub-repo-aware",
		ConfigPath:          "config",
		Render:              func(int, string, []registrymanifest.Route) string { return "" },
	},
	RepoAwareHomeConfig: func(int, string, []registrymanifest.Route, string) (string, []ecosystem.EnvExport, []string) {
		return "", nil, nil
	},
}

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

// runIntree runs one in-tree action over rows with the default readiness
// timings.
func runIntree(action, dir string, rows []ecosystem.Row, probe bindregistry.ProbeFunc, spawn bindregistry.SpawnFunc, lookPath bindregistry.LookPathFunc) (rc int, stdout string) {
	var out bytes.Buffer
	rc = runBindRegistryWithRows([]string{
		"-intree-action", action,
		"-intree-work-dir", dir,
	}, rows, &out, probe, spawn, lookPath,
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	return rc, out.String()
}

// applyReady runs in-tree apply over rows against a Forwarder already reporting
// ready, so it never spawns.
func applyReady(dir string, rows []ecosystem.Row) (rc int, stdout string) {
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

// AC5, the all-or-nothing gate (issue #2932 brief section 3): with the Forwarder
// dead, apply must leave the file byte-for-byte unchanged and the skip-worktree
// bit unset. No partial rewrite.
func TestBindRegistry_IntreeApplyDeadForwarderLeavesFileUntouched(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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
	rel := synthetic.Row.InTreeConfigPath
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

	// Issue #3044: the already-ready short-circuit never spawns, so there is no PID
	// to report and the stdout line must stay silent on this path.
	if strings.Contains(stdout, "registry proxy Forwarder pid") {
		t.Errorf("stdout = %q, want no Forwarder pid line when probe already reports ready (nothing was spawned)", stdout)
	}

	wantRewritten(t, dir, rel, "after apply")
	if !intreeSkipWorktreeSet(t, dir, rel) {
		t.Error("skip-worktree bit not set, want it set after a successful apply")
	}
}

// Issue #3142's buildIntreeHostRewrites filter: an empty-UpstreamHost route
// alongside a real one is silently skipped when building rewrites, never
// reaching ApplyInTreeBinding's own internal-consistency error for an empty
// entry, and the valid route's rewrite still applies.
func TestBindRegistry_IntreeApplySkipsRouteWithEmptyUpstreamHost(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	mountSocketManifest(t, intreeUpstreamRoute, registrymanifest.Route{Prefix: "r1", UpstreamHost: ""})

	rc, stdout := applyReady(dir, rowsWith())
	if rc != 0 {
		t.Fatalf("bind-registry exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	wantRewritten(t, dir, rel, "after apply with the valid route")
}

// The other half of buildIntreeHostRewrites' filter from the caller side: a route
// whose UpstreamHost is empty is still "no route upstream host at all", since
// every rewrite candidate was filtered out.
func TestBindRegistry_IntreeApplyAllRoutesEmptyUpstreamHostWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

// The caller side of the same review finding: the generic "carries no route
// upstream host" warning must be suppressed, since the manifest does carry one,
// it is just unusable for host-based rewriting.
func TestBindRegistry_IntreeApplyDuplicateUpstreamHostWarnsAndSuppressesGenericWarning(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

// Issue #3141: box's in-process apply passes no transport
// flag at all, so an unset manifest silently no-ops apply mode, the replacement
// for the old empty -registry-proxy-socket no-op.
func TestBindRegistry_IntreeApplyManifestAbsentIsNoOp(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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
	rel := synthetic.Row.InTreeConfigPath
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

// Review finding on issue #2932, mirroring
// TestRunBindRegistryWithDeps_SocatMissingWarnsAndSkipsBindings for the
// intree-apply path: apply must print a socat-specific warning naming the
// endpoint rather than falling through to EnsureForwarderReady's generic "failed
// to start" warning.
func TestBindRegistry_IntreeApplySocatMissingWarnsAndSkipsRewrite(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

// Mirrors TestRunBindRegistryWithDeps_TCPTransportWritesBindings for intree-apply
// mode (issue #3111 slice 8, manifest-driven since #3141): apply must spawn via
// spawnHTTPForwarder, never the injected socket-shaped spawn.
func TestBindRegistry_IntreeApplyTCPTransportRewritesFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

	// Same "not-ready-once-then-ready" probe shape as the bindings-mode TCP
	// test: forces EnsureForwarderReady to call spawn once, then reports
	// ready so the apply proceeds.
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

// Mirrors
// TestRunBindRegistryWithDeps_TCPTransportMissingSecretWarnsAndSkipsBindings for
// intree-apply mode.
func TestBindRegistry_IntreeApplyTCPTransportMissingSecretWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

// Revert runs with no manifest set at all, since it is a pure git operation that
// never consults REGISTRY_PROXY_MANIFEST or the probe/spawn deps.
func TestBindRegistry_IntreeRevertRestoresAppliedFile(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, rel, intreeRegistriesContent)
	clearManifestEnv(t)

	outcome, err := bindregistry.ApplyInTreeBinding(dir, synthetic.Row, []bindregistry.HostRewrite{{UpstreamHost: "upstream.example", LocalURL: "http://127.0.0.1:" + forwarderPortStr}})
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

// The multi-row happy path a review finding called out as missing: every other
// intree test here exercises the first row only. A tracked repo-aware row's
// config sits alongside three in-tree rows and must never be touched by any pass
// (issue #3201: a repo-aware row does not participate in InTreeBindings at all).
func TestBindRegistry_IntreeApplyAndRevertAcrossRows(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rows := rowsWith(intreeRootRow, intreeSecondRow, intreeRepoAwareRow)
	repoAwareRel := intreeRepoAwareRow.InTreeConfigPath
	writeTrackedIntreeFile(t, dir, repoAwareRel, intreeRegistriesContent)
	relPaths := []string{synthetic.Row.InTreeConfigPath, intreeRootRow.InTreeConfigPath, intreeSecondRow.InTreeConfigPath}
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

	// Re-apply pass (AC4): the revert/re-apply sequence around branch recovery must
	// rewrite and re-tag every row, not just the apply/revert pair above.
	rc, stdout = applyReady(dir, rows)
	if rc != 0 {
		t.Fatalf("bind-registry re-apply exit = %d, want 0 (stdout=%q)", rc, stdout)
	}
	assertApplied("after re-apply")
}

// newIntreeUnmergedTestRepo leaves the synthetic row's config genuinely unmerged
// (UU), the same fixture shape as bindregistry's own unexported
// newUnmergedTestRepo in intreebinding_test.go, replicated here because that
// helper is unexported in a different package. `git update-index
// --skip-worktree` then fails with exit 128 on that file, giving
// ApplyInTreeBinding a genuine per-row failure. The sibling rows' configs are
// tracked and clean.
func newIntreeUnmergedTestRepo(t *testing.T, siblings ...ecosystem.Row) string {
	t.Helper()
	dir := newIntreeTestRepo(t)

	for _, row := range siblings {
		writeTrackedIntreeFile(t, dir, row.InTreeConfigPath, intreeRegistriesContent)
	}

	rel := synthetic.Row.InTreeConfigPath
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

	// A conflicting merge is the point of this fixture, so unlike runGitCmd's other
	// calls a nonzero exit here is the desired outcome, not a setup failure.
	if err := exec.Command("git", "-C", dir, "merge", "feature").Run(); err == nil {
		t.Fatal("git merge feature: succeeded, want a conflict")
	}

	status, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", rel).Output()
	if err != nil || !strings.HasPrefix(string(status), "UU ") {
		t.Fatalf("git status --porcelain %s = %q, err %v; want \"UU \" (unmerged)", rel, status, err)
	}

	return dir
}

// Regression test for a review finding: the apply loop used to return as soon as
// one row errored, aborting the rest. A genuinely failing row (the unmerged
// first one) must not stop the loop, and the overall apply must still report
// failure.
func TestBindRegistry_IntreeApplyPartialFailureDoesNotBlockSiblingRows(t *testing.T) {
	siblings := []ecosystem.Row{intreeRootRow, intreeSecondRow}
	dir := newIntreeUnmergedTestRepo(t, siblings...)
	failingRel := synthetic.Row.InTreeConfigPath
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

// Issue #3082's AC: ApplyMissing must read differently from ApplyNoopContent's
// "content no longer mentions the upstream host", since the two point an operator
// at different fixes (registry pinned outside the repo vs. wrong upstream host).
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

// Issue #2933's `[ -f ]` parity guard had no operator-facing message at all
// before #3082.
func TestBindRegistry_IntreeApplyNotRegularConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, synthetic.Row.InTreeConfigPath), 0o755); err != nil {
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

// Pins that the existing ApplyUntracked warning survives the
// bool-to-ApplyOutcome signature change unchanged: #3082 slice 2 only added the
// four other messages.
func TestBindRegistry_IntreeApplyUntrackedConfigWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	full := filepath.Join(dir, synthetic.Row.InTreeConfigPath)
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

// Issue #2932's crash window: the bit can be tagged before content is rewritten,
// so "bit set" alone never proves the content converged, and the warning must
// read differently from ApplyNoopContent's "nothing to do".
func TestBindRegistry_IntreeApplySkipWorktreeAlreadySetWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	rel := synthetic.Row.InTreeConfigPath
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

// ApplyNoopContent is distinct from ApplyMissing's "file not found" and points an
// operator at a different fix: the manifest's route upstream host is wrong, not
// that the registry pin lives outside this file.
func TestBindRegistry_IntreeApplyNoopContentWarns(t *testing.T) {
	dir := newIntreeTestRepo(t)
	writeTrackedIntreeFile(t, dir, synthetic.Row.InTreeConfigPath, "main = https://other.example/\n")
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

// intreeNpmStyleConfigContent is the real-row-shaped fixture the repo-aware
// tests below still use.
const intreeNpmStyleConfigContent = "registry=https://upstream.example/\n"

// The "private" registry's index host matches intreeUpstreamRoute's UpstreamHost,
// which is what every bindregistry.ApplyInTree's repo-aware half test below needs.
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
// bindregistry.ApplyInTree's repo-aware half could be deleted and nothing would fail.
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
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
				bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
			)
			if rc == 0 {
				t.Fatalf("runBindRegistryWithDeps exit = 0, want non-zero for %v (stdout=%q)", c.args, stdout.String())
			}
		})
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
		bindregistry.ForwarderReadyTimeout, bindregistry.ForwarderPollInterval,
	)
	if rc != 0 {
		t.Fatalf("runBindRegistryWithDeps exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	want := "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:" + forwarderPortStr + " — this will ship in the PR (issue #3199)\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
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
