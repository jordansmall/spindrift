package seamtest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func runDriver(t *testing.T, cfg DriverConfig, args ...string) (code int, stdout string) {
	t.Helper()
	for k, v := range WriteFakeConfig(t, "claude", cfg) {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code = driverFake(args, &out, &errb)
	return code, out.String()
}

func TestDriverRegistered(t *testing.T) {
	if _, ok := fakes["claude"]; !ok {
		t.Fatal("no claude fake registered")
	}
}

func TestDriverRunsIndexedPerInvocation(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	cfg := DriverConfig{Record: rec, Runs: []DriverRun{{Stdout: "one\n"}, {Stdout: "two\n", Exit: 3}}}
	for _, want := range []struct {
		code int
		out  string
	}{{0, "one\n"}, {3, "two\n"}, {0, ""}} {
		if code, out := runDriver(t, cfg, "-p", "x"); code != want.code || out != want.out {
			t.Errorf("got exit %d %q; want %d %q", code, out, want.code, want.out)
		}
	}
}

func TestDriverRecordsArgv(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	cfg := DriverConfig{Record: rec}
	runDriver(t, cfg, "--resume", "abc", "-p", "go on")
	runDriver(t, cfg, "-p", "again")
	want := [][]string{{"--resume", "abc", "-p", "go on"}, {"-p", "again"}}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v; want %v", got, want)
	}
}

func TestDriverMissingConfigExits(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(configEnv("claude"), "")
	var out, errb bytes.Buffer
	if code := driverFake([]string{"x"}, &out, &errb); code != fakeConfigExit {
		t.Errorf("exit %d; want %d", code, fakeConfigExit)
	}
}

func TestDriverRunsItsShellBeforeWritingStdout(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "mark")
	cfg := DriverConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []DriverRun{{Sh: "echo ran >" + mark, Stdout: "out\n"}}}
	if code, out := runDriver(t, cfg, "-p", "x"); code != 0 || out != "out\n" {
		t.Fatalf("got exit %d %q; want 0 \"out\\n\"", code, out)
	}
	if b, err := os.ReadFile(mark); err != nil || string(b) != "ran\n" {
		t.Errorf("shell side effect = %q, %v; want \"ran\\n\"", b, err)
	}
}

func TestDriverShellFailureIsAConfigError(t *testing.T) {
	cfg := DriverConfig{Record: filepath.Join(t.TempDir(), "rec"), Runs: []DriverRun{{Sh: "exit 5", Stdout: "out\n"}}}
	if code, out := runDriver(t, cfg, "-p", "x"); code != fakeConfigExit || out != "" {
		t.Errorf("got exit %d %q; want %d and no stdout", code, out, fakeConfigExit)
	}
}
