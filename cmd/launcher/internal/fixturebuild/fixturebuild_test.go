package fixturebuild

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testEnvVar = "TEST_FIXTURES_DIR"

func TestResolveDirSkipsWithoutNix(t *testing.T) {
	r := resolver{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		build:    func(...string) (string, error) { t.Error("build called"); return "", nil },
	}
	dir, skip, err := r.resolve(testEnvVar, "a directory", func(root string) []string { return []string{root + "#attr"} })
	if dir != "" || err != nil || !strings.Contains(skip, testEnvVar) {
		t.Fatalf("got (%q, %q, %v); want skip naming %s", dir, skip, err, testEnvVar)
	}
}

func TestResolveDirBuildsWithNix(t *testing.T) {
	var ref []string
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "/repo", nil },
		build:     func(a ...string) (string, error) { ref = a; return "/nix/store/x-fx", nil },
	}
	dir, skip, err := r.resolve(testEnvVar, "a directory", func(root string) []string { return []string{root + "#attr"} })
	if dir != "/nix/store/x-fx" || skip != "" || err != nil {
		t.Fatalf("got (%q, %q, %v)", dir, skip, err)
	}
	if !slices.Equal(ref, []string{"/repo#attr"}) {
		t.Fatalf("flake ref = %q", ref)
	}
}

func TestResolveDirBuildError(t *testing.T) {
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "/repo", nil },
		build:     func(...string) (string, error) { return "", errors.New("boom") },
	}
	_, _, err := r.resolve(testEnvVar, "a directory", func(root string) []string { return []string{root + "#attr"} })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v; want build error surfaced", err)
	}
}

func TestResolveDirFlakeRootError(t *testing.T) {
	r := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "", errors.New("no flake") },
		build:     func(...string) (string, error) { t.Error("build called"); return "", nil },
	}
	if _, _, err := r.resolve(testEnvVar, "a directory", func(root string) []string { return []string{root + "#attr"} }); err == nil {
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

func TestResolveUsesCallerEnvVarHintAndArgs(t *testing.T) {
	skip := resolver{lookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, msg, err := skip.resolve("MY_BIN", "a my binary", nil)
	if err != nil || msg != "MY_BIN is unset and nix is not on PATH; set MY_BIN to a my binary" {
		t.Fatalf("got (%q, %v)", msg, err)
	}

	var got []string
	build := resolver{
		lookPath:  func(string) (string, error) { return "/bin/nix", nil },
		flakeRoot: func() (string, error) { return "/repo", nil },
		build:     func(a ...string) (string, error) { got = a; return "/nix/store/x", nil },
	}
	out, _, err := build.resolve("MY_BIN", "", func(root string) []string { return []string{"--inputs-from", root, "p#x"} })
	if err != nil || out != "/nix/store/x" || !slices.Equal(got, []string{"--inputs-from", "/repo", "p#x"}) {
		t.Fatalf("got (%q, %v), args %v", out, err, got)
	}
}
