package seamtest

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

func runBwrap(t *testing.T, cfg BwrapConfig, args ...string) (code int, stdout string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	WriteFakeConfigFile(t, dir, "bwrap", cfg)
	var out, errb bytes.Buffer
	code = bwrapFake(args, &out, &errb)
	return code, out.String()
}

func TestBwrapRegistered(t *testing.T) {
	if _, ok := fakes["bwrap"]; !ok {
		t.Fatal("no bwrap fake registered")
	}
}

func TestBwrapOutcomeGainsSetenvNonce(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=done\n"
	args := []string{"--ro-bind", "/a", "/b", "--setenv", "RUN_NONCE", "abc", "/agent/entrypoint.sh"}
	code, out := runBwrap(t, BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []PodmanRun{{Exit: 2, Outcome: line}}}, args...)
	if want := "SPINDRIFT_OUTCOME issue=1 status=done nonce=abc\n"; code != 2 || out != want {
		t.Errorf("exit %d, out %q; want 2, %q", code, out, want)
	}
	if _, out = runBwrap(t, BwrapConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []PodmanRun{{Outcome: line}}}, "--setenv", "HOME", "/h"); out != line {
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
