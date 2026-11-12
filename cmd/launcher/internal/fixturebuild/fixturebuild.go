// Package fixturebuild resolves nix-built test fixtures: a flake output built
// from the checkout above the working directory, or the directory of fixture
// Connector .wasm files. It imports nothing from the launcher, so both seamtest
// and core's shared test Fake can use it.
package fixturebuild

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// connectorEnvVar names the dir holding the fixture Connector .wasm files.
	connectorEnvVar = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"
	// connectorFlakeAttr is the flake package that builds that dir.
	connectorFlakeAttr = "connector-wasm-fixtures"
)

// resolver carries the environment lookups resolve needs, so tests can
// drive the decision logic without real nix.
type resolver struct {
	lookPath func(string) (string, error)
	// build runs nix build with args and returns the resulting store path.
	build func(args ...string) (string, error)
	// flakeRoot locates the flake to build from.
	flakeRoot func() (string, error)
}

// resolve builds the output args names from the flake root, or returns a
// non-empty skipReason when nix is unavailable (not a failure). The env
// override named envVar is the caller's concern; hint completes the skip
// message's "set <envVar> to ...".
func (r resolver) resolve(envVar, hint string, args func(root string) []string) (path, skipReason string, err error) {
	if _, lerr := r.lookPath("nix"); lerr != nil {
		return "", fmt.Sprintf("%s is unset and nix is not on PATH; set %s to %s", envVar, envVar, hint), nil
	}
	root, err := r.flakeRoot()
	if err != nil {
		return "", "", err
	}
	out, err := r.build(args(root)...)
	if err != nil {
		return "", "", fmt.Errorf("nix build: %w", err)
	}
	return out, "", nil
}

// ResolveBuild builds a flake output with nix from the checkout above the
// working directory, for tests whose subject is a nix-built artifact other
// than the fixtures. args maps the flake root to nix build's arguments. It
// returns a non-empty skipReason, never an error, when nix is absent; the
// caller checks its own env override first (naming envVar) and should cache
// the result, since the build is slow.
func ResolveBuild(envVar, hint string, args func(root string) []string) (path, skipReason string, err error) {
	return resolver{lookPath: exec.LookPath, build: nixBuild, flakeRoot: workingDirFlakeRoot}.resolve(envVar, hint, args)
}

func workingDirFlakeRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return findFlakeRoot(wd)
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

// nixBuild runs `nix build --no-link --print-out-paths` with args and returns
// the single resulting store path.
func nixBuild(args ...string) (string, error) {
	cmd := exec.Command("nix", append([]string{"build", "--no-link", "--print-out-paths"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	// A multi-output derivation prints one path per line; the fixtures
	// derivations built here have a single output.
	path := strings.TrimSpace(string(out))
	if path == "" || strings.Contains(path, "\n") {
		return "", errors.New("nix build printed no single store path: " + path)
	}
	return path, nil
}

// ConnectorDir returns the directory of fixture Connector .wasm files:
// $SPINDRIFT_CONNECTOR_FIXTURES_DIR when set, else the nix-built
// connector-wasm-fixtures output. An unavailable nix is an error here, not a
// skip, because no core test may skip the Connector seam (ADR 0067). The
// answer is computed once per test binary, since the nix build is slow.
var ConnectorDir = sync.OnceValues(func() (string, error) {
	if dir := os.Getenv(connectorEnvVar); dir != "" {
		return dir, nil
	}
	built, skip, err := ResolveBuild(connectorEnvVar, "a directory of fixture Connector .wasm files", func(root string) []string {
		return []string{root + "#" + connectorFlakeAttr}
	})
	if skip != "" {
		return "", errors.New(skip)
	}
	return built, err
})

// WazeroCacheDir is the per-user wazero compilation cache directory, or "" when
// the user cache dir is unknown (no cache then).
func WazeroCacheDir() string {
	// Per-user, not os.TempDir: another user must not plant native code here.
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "spindrift", "wazero")
}
