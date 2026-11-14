package seamtest

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBwrapRegistered(t *testing.T) {
	if _, ok := fakes["bwrap"]; !ok {
		t.Fatal("no bwrap fake registered")
	}
}

func TestBwrapOutcomeGainsSetenvNonce(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=done\n"
	args := []string{"--ro-bind", "/a", "/b", "--setenv", "RUN_NONCE", "abc", "/agent/entrypoint.sh"}
	code, out, _ := runBwrap(t, BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []PodmanRun{{Exit: 2, Outcome: line}}}, args...)
	if want := "SPINDRIFT_OUTCOME issue=1 status=done nonce=abc\n"; code != 2 || out != want {
		t.Errorf("exit %d, out %q; want 2, %q", code, out, want)
	}
	if _, out, _ = runBwrap(t, BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []PodmanRun{{Outcome: line}}}, "--setenv", "HOME", "/h"); out != line {
		t.Errorf("no RUN_NONCE: out = %q; want verbatim", out)
	}
}

func TestBwrapRunsIndexedPerInvocation(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	rec := filepath.Join(dir, "rec")
	WriteFakeConfigFile(t, dir, "bwrap", BwrapConfig{Record: rec, Runs: []PodmanRun{{Exit: 0}, {Exit: 3}}})
	var out, errb bytes.Buffer
	for i, want := range []int{0, 3, 0} {
		if code := bwrapFake([]string{"box", string(rune('a' + i))}, &out, &errb); code != want {
			t.Errorf("invocation %d: exit %d; want %d", i, code, want)
		}
	}
	want := [][]string{{"box", "a"}, {"box", "b"}, {"box", "c"}}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

func TestBwrapOverlayProbeIsNotABoxRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	rec := filepath.Join(dir, "rec")
	line := "SPINDRIFT_OUTCOME issue=1 status=done\n"
	WriteFakeConfigFile(t, dir, "bwrap", BwrapConfig{Record: rec, Runs: []PodmanRun{{Exit: 3, Outcome: line}}})
	probe := []string{"--unshare-user", "--uid", "1000", "--gid", "1000", "--ro-bind", "/", "/", "--overlay-src", "/d", "--tmp-overlay", "/d", "--", "true"}
	box := []string{"--setenv", "RUN_NONCE", "n1", "/agent/entrypoint.sh"}
	var out, errb bytes.Buffer
	if code := bwrapFake(probe, &out, &errb); code != 0 || out.Len() != 0 {
		t.Errorf("probe: exit %d, out %q; want 0, empty", code, out.String())
	}
	out.Reset()
	if code, want := bwrapFake(box, &out, &errb), "SPINDRIFT_OUTCOME issue=1 status=done nonce=n1\n"; code != 3 || out.String() != want {
		t.Errorf("box: exit %d, out %q; want 3, %q", code, out.String(), want)
	}
	if got, want := ReadRecord(t, rec), [][]string{probe, box}; !reflect.DeepEqual(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

func TestBwrapMissingConfigExits(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(configEnv("bwrap"), "")
	var out, errb bytes.Buffer
	if code := bwrapFake([]string{"x"}, &out, &errb); code != fakeConfigExit {
		t.Errorf("exit %d; want %d", code, fakeConfigExit)
	}
}

func runBwrap(t *testing.T, cfg BwrapConfig, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	WriteFakeConfigFile(t, dir, "bwrap", cfg)
	var out, errb bytes.Buffer
	code = bwrapFake(args, &out, &errb)
	return code, out.String(), errb.String()
}

const sandboxHost = "/nix/store/x-connector-host"

func bwrapSandboxArgv(drop ...string) []string {
	args := []string{"--unshare-all", "--clearenv", "--cap-drop", "ALL", "--new-session", "--die-with-parent",
		"--proc", "/proc", "--dev", "/dev", "--ro-bind", sandboxHost, sandboxHost, "--seccomp", "3", "--", sandboxHost + "/bin/connector-host"}
	return slices.DeleteFunc(args, func(a string) bool { return slices.Contains(drop, a) })
}

func TestBwrapSandboxRunRefusesWeakenedIsolation(t *testing.T) {
	for name, argv := range map[string][]string{
		"share net":  slices.Insert(bwrapSandboxArgv(), 0, "--share-net"),
		"setenv":     slices.Insert(bwrapSandboxArgv(), 0, "--setenv", "A", "b"),
		"extra bind": slices.Insert(bwrapSandboxArgv(), 0, "--ro-bind", "/nix/store", "/nix/store"),
		"no unshare": bwrapSandboxArgv("--unshare-all"),
		"no seccomp": bwrapSandboxArgv("--seccomp", "3"),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), ConnectorHost: "/nonexistent/host"}
			code, out, stderr := runBwrap(t, cfg, argv...)
			if code == 0 || out != "" || !strings.Contains(stderr, "isolation") {
				t.Errorf("exit %d, stdout %q, stderr %q; want a non-zero exit and an isolation message", code, out, stderr)
			}
		})
	}
}

func TestBwrapSandboxRunCanBeRefused(t *testing.T) {
	cfg := BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), ConnectorHost: "/nonexistent/host",
		RefuseSandbox: "bwrap: No permissions to creating new namespace"}
	code, out, stderr := runBwrap(t, cfg, bwrapSandboxArgv()...)
	if code != 1 || out != "" || !strings.Contains(stderr, "No permissions to creating new namespace") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1 and the scripted message", code, out, stderr)
	}
}

func TestBwrapSandboxRunNeedsAHostPath(t *testing.T) {
	code, _, stderr := runBwrap(t, BwrapConfig{Record: filepath.Join(t.TempDir(), "rec")}, bwrapSandboxArgv()...)
	if code != fakeConfigExit || !strings.Contains(stderr, "connector_host") {
		t.Errorf("exit %d, stderr %q; want %d naming connector_host", code, stderr, fakeConfigExit)
	}
}

func TestBwrapSandboxRunsDoNotConsumeTheBoxScript(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	WriteFakeConfigFile(t, dir, "bwrap", BwrapConfig{Record: filepath.Join(dir, "rec"), ConnectorHost: "/nonexistent/host",
		RefuseSandbox: "no", Runs: []PodmanRun{{Exit: 3}}})
	var out, errb bytes.Buffer
	bwrapFake(bwrapSandboxArgv(), &out, &errb)
	if code := bwrapFake([]string{"--setenv", "HOME", "/h", "/agent/entrypoint.sh"}, &out, &errb); code != 3 {
		t.Errorf("first Box run exit %d; want the scripted 3", code)
	}
}

func TestUseBwrapPutsTheFakeOnPATH(t *testing.T) {
	rec := UseBwrap(t, BwrapConfig{RefuseSandbox: "no"})
	if err := exec.Command("bwrap", "--version").Run(); err != nil {
		t.Fatalf("bwrap: %v", err)
	}
	if got := ReadRecord(t, rec); len(got) != 1 || got[0][0] != "--version" {
		t.Errorf("record = %v; want the one call", got)
	}
}
