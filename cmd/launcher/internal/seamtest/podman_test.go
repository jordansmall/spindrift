package seamtest

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

// runPodman points the fake at cfg, runs it once with args and returns its
// exit status and streams.
func runPodman(t *testing.T, cfg PodmanConfig, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runOCIFake(t, "podman", cfg, args...)
}

func TestPodmanImageExists(t *testing.T) {
	for _, sub := range []string{"exists", "inspect"} {
		for _, present := range []bool{true, false} {
			rec := filepath.Join(t.TempDir(), "rec")
			code, _, _ := runPodman(t, PodmanConfig{Record: rec, ImagePresent: present}, "image", sub, "img")
			want := 1
			if present {
				want = 0
			}
			if code != want {
				t.Errorf("image %s present=%v: exit %d; want %d", sub, present, code, want)
			}
		}
	}
}

func TestPodmanOutcomeGainsRunNonce(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=done\n"
	cfg := PodmanConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []PodmanRun{
		{Outcome: line},
		{Outcome: "SPINDRIFT_OUTCOME issue=1 nonce=own\n"},
		{Outcome: line},
	}}
	_, out, _ := runPodman(t, cfg, "run", "-e", "A=b", "-e", "RUN_NONCE=abc", "img")
	if want := "SPINDRIFT_OUTCOME issue=1 status=done nonce=abc\n"; out != want {
		t.Errorf("out = %q; want %q", out, want)
	}
	_, out, _ = runPodman(t, cfg, "run", "-e", "RUN_NONCE=abc", "img")
	if out != cfg.Runs[1].Outcome {
		t.Errorf("own nonce rewritten: %q", out)
	}
	_, out, _ = runPodman(t, cfg, "run", "img")
	if out != line {
		t.Errorf("no RUN_NONCE in argv: out = %q; want verbatim", out)
	}
}

func TestPodmanRunsIndexedPerInvocation(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	cfg := PodmanConfig{Record: rec, Runs: []PodmanRun{
		{Exit: 0, Outcome: "SPINDRIFT_OUTCOME issue=1 status=done\n"},
		{Exit: 3},
	}}
	code, out, _ := runPodman(t, cfg, "run", "--name", "a")
	if code != 0 || out != cfg.Runs[0].Outcome {
		t.Errorf("run 0: exit %d, out %q", code, out)
	}
	// A non-run invocation between runs must not advance the index.
	if code, _, _ = runPodman(t, cfg, "rm", "-f", "a"); code != 0 {
		t.Errorf("rm: exit %d; want 0", code)
	}
	if code, out, _ = runPodman(t, cfg, "run", "--name", "b"); code != 3 || out != "" {
		t.Errorf("run 1: exit %d, out %q", code, out)
	}
	if code, out, _ = runPodman(t, cfg, "run", "--name", "c"); code != 0 || out != "" {
		t.Errorf("run past Runs: exit %d, out %q; want zero value", code, out)
	}
	want := [][]string{{"run", "--name", "a"}, {"rm", "-f", "a"}, {"run", "--name", "b"}, {"run", "--name", "c"}}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

func TestPodmanDefaults(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	for _, args := range [][]string{{"load", "-i", "x"}, {"tag", "a", "b"}, {"ps"}, {"machine", "inspect"}, {}} {
		if code, out, _ := runPodman(t, PodmanConfig{Record: rec}, args...); code != 0 || out != "" {
			t.Errorf("%v: exit %d, out %q; want 0, empty", args, code, out)
		}
	}
	// Container liveness probe: no such container.
	if code, _, _ := runPodman(t, PodmanConfig{Record: rec}, "inspect", "--format={{json .}}", "c"); code != 1 {
		t.Errorf("inspect: exit %d; want 1", code)
	}
}

func TestPodmanConfigError(t *testing.T) {
	t.Setenv(configEnv("podman"), "")
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := podmanFake("podman", []string{"run"}, &out, &errb); code != fakeConfigExit || errb.Len() == 0 {
		t.Errorf("exit %d, stderr %q; want %d and a message", code, errb.String(), fakeConfigExit)
	}
}

func runOCIFake(t *testing.T, tool string, cfg PodmanConfig, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	for k, v := range WriteFakeConfig(t, tool, cfg) {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code = podmanFake(tool, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDockerFakeRegisteredAndReadsOwnConfig(t *testing.T) {
	if _, ok := fakes["docker"]; !ok {
		t.Fatal("no docker fake registered")
	}
	rec := filepath.Join(t.TempDir(), "rec")
	cfg := PodmanConfig{Record: rec, ImagePresent: true, Runs: []PodmanRun{{Exit: 4, Outcome: "SPINDRIFT_OUTCOME issue=1 status=done\n"}}}
	if code, _, _ := runOCIFake(t, "docker", cfg, "image", "inspect", "img"); code != 0 {
		t.Errorf("image inspect: exit %d; want 0", code)
	}
	code, out, _ := runOCIFake(t, "docker", cfg, "run", "-e", "RUN_NONCE=n1", "img")
	if want := "SPINDRIFT_OUTCOME issue=1 status=done nonce=n1\n"; code != 4 || out != want {
		t.Errorf("run: exit %d, out %q; want 4, %q", code, out, want)
	}
	if got := ReadRecord(t, rec); len(got) != 2 {
		t.Errorf("record = %v; want 2 argvs", got)
	}
}

func TestDockerFakeIgnoresPodmanConfig(t *testing.T) {
	t.Setenv(configEnv("docker"), "")
	for k, v := range WriteFakeConfig(t, "podman", PodmanConfig{Record: filepath.Join(t.TempDir(), "rec")}) {
		t.Setenv(k, v)
	}
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	if code := podmanFake("docker", []string{"run"}, &out, &errb); code != fakeConfigExit {
		t.Errorf("exit %d; want %d (docker config unset)", code, fakeConfigExit)
	}
}
