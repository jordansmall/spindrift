package seamtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveDirSkipsWithoutNix(t *testing.T) {
	r := resolver{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		build:    func(string) (string, error) { t.Error("build called"); return "", nil },
	}
	dir, skip, err := r.resolveDir()
	if dir != "" || err != nil || !strings.Contains(skip, EnvVar) {
		t.Fatalf("got (%q, %q, %v); want skip naming %s", dir, skip, err, EnvVar)
	}
}

func TestResolveDirBuildsWithNix(t *testing.T) {
	var ref string
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "/repo", nil },
		build:     func(f string) (string, error) { ref = f; return "/nix/store/x-fx", nil },
	}
	dir, skip, err := r.resolveDir()
	if dir != "/nix/store/x-fx" || skip != "" || err != nil {
		t.Fatalf("got (%q, %q, %v)", dir, skip, err)
	}
	if ref != "/repo#seam-fixtures" {
		t.Fatalf("flake ref = %q", ref)
	}
}

func TestResolveDirBuildError(t *testing.T) {
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "/repo", nil },
		build:     func(string) (string, error) { return "", errors.New("boom") },
	}
	_, _, err := r.resolveDir()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v; want build error surfaced", err)
	}
}

func TestResolveDirFlakeRootError(t *testing.T) {
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "", errors.New("no flake") },
		build:     func(string) (string, error) { t.Error("build called"); return "", nil },
	}
	if _, _, err := r.resolveDir(); err == nil {
		t.Fatal("want error")
	}
}

func TestFindFlakeRootFromRepoSubdir(t *testing.T) {
	root, err := findFlakeRoot(".")
	if err != nil {
		t.Skipf("not inside a flake checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "flake.nix")); err != nil {
		t.Fatalf("no flake.nix at returned root %q: %v", root, err)
	}
}

func TestDirEnvSet(t *testing.T) {
	t.Setenv(EnvVar, "/fx")
	if got := Dir(t); got != "/fx" {
		t.Fatalf("Dir = %q", got)
	}
	if got := Path(t, "check-contract.md"); got != "/fx/check-contract.md" {
		t.Fatalf("Path = %q", got)
	}
}

func TestDirEnvRelativeIsAbsolute(t *testing.T) {
	t.Setenv(EnvVar, "fx")
	want, err := filepath.Abs("fx")
	if err != nil {
		t.Fatal(err)
	}
	rec := &fatalRecorder{TB: t}
	if got := Dir(rec); got != want {
		t.Fatalf("Dir = %q; want %q", got, want)
	}
	if !strings.Contains(rec.logged, EnvVar) {
		t.Fatalf("log %q does not name %s", rec.logged, EnvVar)
	}
}

func TestPathRejectsUnknownName(t *testing.T) {
	t.Setenv(EnvVar, "/fx")
	// Fatalf on the real t would fail this test, so run it on a stub.
	rec := &fatalRecorder{TB: t}
	func() {
		defer func() { _ = recover() }()
		Path(rec, "nope.txt")
	}()
	if !rec.fataled || !strings.Contains(rec.msg, "nope.txt") {
		t.Fatalf("fataled=%v msg=%q", rec.fataled, rec.msg)
	}
}

// fatalRecorder captures Logf, and Fatalf, aborting the call like the real
// one, via panic.
type fatalRecorder struct {
	testing.TB
	fataled bool
	msg     string
	logged  string
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Logf(format string, args ...any) {
	f.logged += fmt.Sprintf(format, args...)
}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.fataled = true
	f.msg = fmt.Sprintf(format, args...)
	panic("fatal")
}

func TestFixtureNamesWellFormed(t *testing.T) {
	if len(fixtureNames) == 0 {
		t.Fatal("empty fixture list")
	}
	if !slices.IsSorted(fixtureNames) {
		t.Errorf("fixtures.json not sorted: %v", fixtureNames)
	}
	seen := map[string]bool{}
	for _, n := range fixtureNames {
		if seen[n] {
			t.Errorf("duplicate fixture %q", n)
		}
		seen[n] = true
	}
}
