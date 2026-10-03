// Package seamtest is the shared helper library for Go seam tests: tests that
// exercise a Go component against a fixture rendered by Nix (ADR 0058, issue
// #4280).
//
// Fixtures resolve from the directory named by SPINDRIFT_SEAM_FIXTURES_DIR
// first. When it is unset, Dir builds the flake's seam-fixtures output with
// nix; with no nix on PATH the test skips instead of failing, so a plain
// checkout without nix stays green. The package is not build-tagged, so its
// own unit tests run under plain `go test ./...`.
//
// # Launcher smoke set as the deletion gate
//
// TestSeamLauncherSmokeMatrix in cmd/launcher/launcher_seam_integration_test.go
// is the gate for deleting the launcher-side bats files (tests/run-*.bats,
// tests/build.bats): it must be green, with a case for every dispatch kind
// (work, research, butler) and runtime (podman, docker, bwrap) the rendered
// input document distinguishes, before any of them goes. A new kind or
// runtime distinction in the document earns a new case there, and
// TestSeamSmokeCatchesDroppedKnob keeps the matrix honest by asserting a
// dropped document knob fails a check.
package seamtest

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// EnvVar names the directory holding the nix-rendered fixture files.
const EnvVar = "SPINDRIFT_SEAM_FIXTURES_DIR"

// flakeAttr is the flake output whose store path holds every fixture.
const flakeAttr = "seam-fixtures"

// fixturesJSON is the single source of truth for fixture names: the Nix side
// reads this same file to pin the fixtures derivation's file set to it.
//
//go:embed fixtures.json
var fixturesJSON []byte

var fixtureNames = func() []string {
	var names []string
	if err := json.Unmarshal(fixturesJSON, &names); err != nil {
		panic("seamtest: parse fixtures.json: " + err.Error())
	}
	return names
}()

// resolver carries the environment lookups resolveDir needs, so tests can
// drive the decision logic without real nix.
type resolver struct {
	lookPath func(string) (string, error)
	// build runs nix build for the given flake reference and returns the
	// resulting store path.
	build func(flakeRef string) (string, error)
	// flakeRoot locates the flake to build from.
	flakeRoot func() (string, error)
}

// resolveDir returns the fixtures directory, or a non-empty skipReason when
// nix is unavailable (not a failure). The env override is Dir's concern.
func (r resolver) resolveDir() (dir, skipReason string, err error) {
	if _, lerr := r.lookPath("nix"); lerr != nil {
		return "", fmt.Sprintf("%s is unset and nix is not on PATH; set %s to a directory of rendered fixtures", EnvVar, EnvVar), nil
	}
	root, err := r.flakeRoot()
	if err != nil {
		return "", "", err
	}
	out, err := r.build(root + "#" + flakeAttr)
	if err != nil {
		return "", "", fmt.Errorf("build %s: %w", flakeAttr, err)
	}
	return out, "", nil
}

// findFlakeRoot returns the nearest ancestor of start (inclusive) holding a
// flake.nix. go test runs each package with its own directory as cwd.
func findFlakeRoot(start string) (string, error) {
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no flake.nix found above %s", start)
		}
		dir = parent
	}
}

func nixBuild(flakeRef string) (string, error) {
	cmd := exec.Command("nix", "build", "--no-link", "--print-out-paths", flakeRef)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	// A multi-output derivation prints one path per line; the fixtures
	// derivation has a single output.
	path := strings.TrimSpace(string(out))
	if path == "" || strings.Contains(path, "\n") {
		return "", errors.New("nix build printed no single store path: " + path)
	}
	return path, nil
}

type buildResult struct {
	dir, skipReason string
	err             error
}

// buildOnce caches the nix-build resolution for the test binary's
// lifetime: the nix build is slow and its answer cannot change mid-run.
var buildOnce = sync.OnceValue(func() buildResult {
	r := resolver{
		lookPath: exec.LookPath,
		build:    nixBuild,
		flakeRoot: func() (string, error) {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			return findFlakeRoot(wd)
		},
	}
	dir, skip, err := r.resolveDir()
	return buildResult{dir, skip, err}
})

// Dir returns the fixtures directory: $SPINDRIFT_SEAM_FIXTURES_DIR when set,
// else the nix-built seam-fixtures output. It skips the test when neither is
// available and fails it when the nix build fails.
func Dir(tb testing.TB) string {
	tb.Helper()
	if v := os.Getenv(EnvVar); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			tb.Fatalf("seamtest: resolve %s=%q: %v", EnvVar, v, err)
		}
		tb.Logf("seamtest: fixtures from %s: %s", EnvVar, abs)
		return abs
	}
	res := buildOnce()
	switch {
	case res.skipReason != "":
		tb.Skip(res.skipReason)
	case res.err != nil:
		tb.Fatalf("seamtest: %v", res.err)
	}
	tb.Logf("seamtest: fixtures from nix build: %s", res.dir)
	return res.dir
}

// Path returns the absolute path of the named fixture, failing the test when
// name is not a registered fixture.
func Path(tb testing.TB, name string) string {
	tb.Helper()
	if !slices.Contains(fixtureNames, name) {
		tb.Fatalf("seamtest: %q is not a registered fixture (see fixtures.json)", name)
	}
	return filepath.Join(Dir(tb), name)
}
