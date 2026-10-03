package seamtest

import (
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
