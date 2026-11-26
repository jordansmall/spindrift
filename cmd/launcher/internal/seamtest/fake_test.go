package seamtest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// captureTB records Fatalf instead of failing, so ReadRecord can run off the
// test goroutine; Goexit stops the reader the way a real Fatalf would. It
// covers only Fatalf, ReadRecord's one failure path: any other failing method
// reaches the real test from the wrong goroutine.
type captureTB struct {
	testing.TB
	failure string
}

func (c *captureTB) Helper() {}
func (c *captureTB) Fatalf(format string, args ...any) {
	c.failure = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// asyncRead is one ReadRecord running off the test goroutine. started closes
// just before the call; done closes once it has returned or failed.
type asyncRead struct {
	tb      *captureTB
	got     [][]string
	started chan struct{}
	done    chan struct{}
}

func readAsync(t testing.TB, path string) *asyncRead {
	r := &asyncRead{
		tb:      &captureTB{TB: t},
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(r.done)
		close(r.started)
		r.got = ReadRecord(r.tb, path)
	}()
	return r
}

func TestDispatch(t *testing.T) {
	saved := fakes
	t.Cleanup(func() { fakes = saved })
	fakes = map[string]func([]string) int{
		"tool": func(args []string) int { return 40 + len(args) },
	}
	ran := false
	runTests := func() int { ran = true; return 7 }

	if got := dispatch("tool", []string{"a", "b"}, runTests); got != 42 || ran {
		t.Fatalf("registered name: got %d, ran=%v; want 42, false", got, ran)
	}
	if got := dispatch("seamtest.test", nil, runTests); got != 7 || !ran {
		t.Fatalf("unregistered name: got %d, ran=%v; want 7, true", got, ran)
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	type cfg struct {
		Record string
		N      int
	}
	want := cfg{Record: "/x/y", N: 3}
	env := WriteFakeConfig(t, "podman", want)
	for k, v := range env {
		t.Setenv(k, v)
	}
	var got cfg
	if err := loadConfig("podman", &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v; want %+v", got, want)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	var c struct{}
	t.Setenv(configEnv("podman"), "")
	if err := loadConfig("podman", &c); err == nil {
		t.Error("unset env: want error")
	}
	t.Setenv(configEnv("podman"), filepath.Join(t.TempDir(), "missing.json"))
	if err := loadConfig("podman", &c); err == nil {
		t.Error("missing file: want error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnv("podman"), bad)
	if err := loadConfig("podman", &c); err == nil {
		t.Error("malformed file: want error")
	}
}

func TestAppendRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec")
	if got := ReadRecord(t, path); len(got) != 0 {
		t.Fatalf("missing file: got %v; want none", got)
	}
	first := []string{"run", "--name", "a b", "multi\nline"}
	second := []string{"rm", "-f", "x"}
	prior, err := appendRecord(path, first)
	if err != nil || len(prior) != 0 {
		t.Fatalf("first append: prior=%v err=%v", prior, err)
	}
	prior, err = appendRecord(path, second)
	if err != nil || !reflect.DeepEqual(prior, [][]string{first}) {
		t.Fatalf("second append: prior=%v err=%v", prior, err)
	}
	if got := ReadRecord(t, path); !reflect.DeepEqual(got, [][]string{first, second}) {
		t.Fatalf("ReadRecord = %v", got)
	}
}

// A poll that lands while a fake is mid-append must wait for the append, not
// parse its torn last line.
func TestReadRecordWaitsForAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`["run","a`); err != nil {
		t.Fatal(err)
	}
	r := readAsync(t, path)
	<-r.started
	select {
	case <-r.done:
		t.Fatalf("ReadRecord returned mid-append: failure=%q got=%v", r.tb.failure, r.got)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := f.WriteString("b\"]\n"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	<-r.done
	if r.tb.failure != "" {
		t.Fatalf("ReadRecord failed: %s", r.tb.failure)
	}
	if want := [][]string{{"run", "ab"}}; !reflect.DeepEqual(r.got, want) {
		t.Fatalf("ReadRecord = %v; want %v", r.got, want)
	}
}

func TestReadRecordRejectsBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec")
	if err := os.WriteFile(path, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := readAsync(t, path)
	<-r.done
	if !strings.Contains(r.tb.failure, "bad record") {
		t.Fatalf("failure = %q; want it to contain %q", r.tb.failure, "bad record")
	}
}

func TestInstallFakes(t *testing.T) {
	dir := InstallFakes(t, "podman")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(dir, "podman"))
	if err != nil || got != exe {
		t.Fatalf("symlink = %q, %v; want %q", got, err, exe)
	}
}

func TestLoadConfigFallsBackToWorkingDirFile(t *testing.T) {
	type cfg struct{ N int }
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(configEnv("bwrap"), "")
	var got cfg
	if err := loadConfig("bwrap", &got); err == nil {
		t.Fatal("no env and no file: want error")
	}
	WriteFakeConfigFile(t, dir, "bwrap", cfg{N: 5})
	if err := loadConfig("bwrap", &got); err != nil || got.N != 5 {
		t.Fatalf("fallback file: got %+v, err %v; want N=5", got, err)
	}
	// An explicit env var wins over the file.
	for k, v := range WriteFakeConfig(t, "bwrap", cfg{N: 9}) {
		t.Setenv(k, v)
	}
	if err := loadConfig("bwrap", &got); err != nil || got.N != 9 {
		t.Fatalf("env: got %+v, err %v; want N=9", got, err)
	}
}
