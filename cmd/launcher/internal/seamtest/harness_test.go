package seamtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCapturesStreamsAndExitCode(t *testing.T) {
	res := Run(t, Cmd{Bin: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if res.Stdout != "out\n" || res.Stderr != "err\n" || res.ExitCode != 3 {
		t.Fatalf("got %+v", res)
	}
}

func TestRunGivesFreshHome(t *testing.T) {
	parent := os.Getenv("HOME")
	res := Run(t, Cmd{Bin: "sh", Args: []string{"-c", "printf %s \"$HOME\""}})
	if res.Stdout == "" || res.Stdout == parent {
		t.Fatalf("HOME = %q (parent %q); want a fresh dir", res.Stdout, parent)
	}
	if fi, err := os.Stat(res.Stdout); err != nil || !fi.IsDir() {
		t.Fatalf("HOME %q is not a directory: %v", res.Stdout, err)
	}
}

func TestRunComposesPath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	res := Run(t, Cmd{Bin: "sh", Args: []string{"-c", "printf %s \"$PATH\""}, PathDirs: []string{a, b}})
	want := a + string(os.PathListSeparator) + b + string(os.PathListSeparator)
	if !strings.HasPrefix(res.Stdout, want) {
		t.Fatalf("PATH = %q; want prefix %q", res.Stdout, want)
	}
	if !strings.HasSuffix(res.Stdout, os.Getenv("PATH")) {
		t.Fatalf("PATH = %q; want parent PATH kept as suffix", res.Stdout)
	}
}

func TestRunEnvOverridesAndEmpty(t *testing.T) {
	t.Setenv("SEAM_INHERITED", "yes")
	t.Setenv("SEAM_CLEARED", "set")
	res := Run(t, Cmd{
		Bin:  "sh",
		Args: []string{"-c", "printf '%s|%s|%s' \"$SEAM_INHERITED\" \"$SEAM_CLEARED\" \"$SEAM_NEW\""},
		Env:  map[string]string{"SEAM_CLEARED": "", "SEAM_NEW": "n"},
	})
	if res.Stdout != "yes||n" {
		t.Fatalf("env = %q; want yes||n", res.Stdout)
	}
}

func TestRunCleanEnv(t *testing.T) {
	t.Setenv("SEAM_INHERITED", "yes")
	t.Setenv("TMPDIR", t.TempDir())
	res := Run(t, Cmd{
		Bin:      "sh",
		Args:     []string{"-c", "printf '%s|%s|%s' \"$SEAM_INHERITED\" \"$SEAM_NEW\" \"$TMPDIR\""},
		Env:      map[string]string{"SEAM_NEW": "n"},
		CleanEnv: true,
	})
	if want := "|n|" + os.TempDir(); res.Stdout != want {
		t.Fatalf("env = %q; want %q", res.Stdout, want)
	}
}

func TestRunDir(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res := Run(t, Cmd{Bin: "sh", Args: []string{"-c", "pwd -P"}, Dir: dir})
	if strings.TrimSpace(res.Stdout) != dir {
		t.Fatalf("cwd = %q; want %q", res.Stdout, dir)
	}
}

func TestWantExit(t *testing.T) {
	Result{ExitCode: 2}.WantExit(t, 2)
}

func TestBuildAndRun(t *testing.T) {
	bin := Build(t, "./internal/seamtest/testdata/hello")
	if filepath.Base(bin) != "hello" {
		t.Fatalf("bin = %q; want name hello", bin)
	}
	res := Run(t, Cmd{Bin: bin})
	res.WantExit(t, 0)
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestFindModuleRoot(t *testing.T) {
	wd, _ := os.Getwd()
	root, err := findModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := findModuleRoot(t.TempDir()); err == nil {
		t.Fatal("want error outside the launcher module")
	}
}

// isolateBuilds swaps in a fresh memo table, shared dir and go-build stub,
// counting stub calls, and restores the real ones afterwards.
func isolateBuilds(t *testing.T, stub func(pkg string) error) *int {
	t.Helper()
	calls := new(int)
	fake := func(_, pkg, _ string) ([]byte, error) {
		*calls++
		if err := stub(pkg); err != nil {
			return []byte("boom output"), err
		}
		return nil, nil
	}
	buildState.Lock()
	oldDir, oldBuilds, oldGo := buildState.dir, buildState.builds, goBuild
	buildState.dir, buildState.builds, goBuild = t.TempDir(), map[string]builtBinary{}, fake
	buildState.Unlock()
	t.Cleanup(func() {
		buildState.Lock()
		buildState.dir, buildState.builds, goBuild = oldDir, oldBuilds, oldGo
		buildState.Unlock()
	})
	return calls
}

func TestBuildMemoizesPerPackage(t *testing.T) {
	calls := isolateBuilds(t, func(string) error { return nil })
	a := Build(t, "./cmd/foo")
	b := Build(t, "./cmd/foo")
	if a != b {
		t.Fatalf("paths differ: %q vs %q", a, b)
	}
	if *calls != 1 {
		t.Fatalf("go build ran %d times; want 1", *calls)
	}
	if filepath.Base(a) != "foo" {
		t.Fatalf("bin = %q; want name foo", a)
	}
	other := Build(t, "./other/foo")
	if other == a || filepath.Base(other) != "foo" || *calls != 2 {
		t.Fatalf("other = %q (calls %d); want a distinct path named foo", other, *calls)
	}
}

func TestBuildMemoizesFailure(t *testing.T) {
	calls := isolateBuilds(t, func(string) error { return errors.New("link failed") })
	for i := 0; i < 2; i++ {
		rec := &fatalRecorder{TB: t}
		func() {
			defer func() { _ = recover() }() // the recorder panics, like a real Fatalf
			Build(rec, "./bad")
		}()
		if !rec.fataled || !strings.Contains(rec.msg, "link failed") || !strings.Contains(rec.msg, "boom output") {
			t.Fatalf("call %d: fataled=%v msg=%q; want the build failure", i, rec.fataled, rec.msg)
		}
	}
	if *calls != 1 {
		t.Fatalf("go build ran %d times; want 1", *calls)
	}
}

func TestBuildWithoutSharedDirFails(t *testing.T) {
	calls := isolateBuilds(t, func(string) error { return nil })
	buildState.Lock()
	buildState.dir = ""
	buildState.Unlock()
	if _, err := buildBinary(t.TempDir(), "./x"); err == nil || !strings.Contains(err.Error(), "seamtest.Main") {
		t.Fatalf("err = %v; want a message naming seamtest.Main", err)
	}
	if *calls != 0 {
		t.Fatalf("go build ran %d times; want 0", *calls)
	}
}

func TestWithBuildDirRemovesDir(t *testing.T) {
	buildState.Lock()
	oldDir := buildState.dir
	buildState.Unlock()
	t.Cleanup(func() {
		buildState.Lock()
		buildState.dir = oldDir
		buildState.Unlock()
	})
	var inside string
	code := withBuildDir(func() int {
		buildState.Lock()
		inside = buildState.dir
		buildState.Unlock()
		if fi, err := os.Stat(inside); err != nil || !fi.IsDir() {
			t.Errorf("shared dir %q missing while tests run: %v", inside, err)
		}
		if err := os.WriteFile(filepath.Join(inside, "f"), nil, 0o644); err != nil {
			t.Error(err)
		}
		return 7
	})
	if code != 7 {
		t.Fatalf("code = %d; want runTests' status passed through", code)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("shared dir %q survives; stat err = %v", inside, err)
	}
}
