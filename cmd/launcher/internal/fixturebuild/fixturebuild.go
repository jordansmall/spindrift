// Package fixturebuild resolves nix-built test fixtures: a flake output built
// from the checkout above the working directory, or the directory of fixture
// Connector .wasm files. It imports nothing from the launcher, so both seamtest
// and core's shared test Fake can use it.
package fixturebuild

import (
	"errors"
	"fmt"
	"io"
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
	// connectorHostEnvVar names the connector-host binary, and connectorHostAttr
	// is the flake package that builds it (issue #7013).
	connectorHostEnvVar = "SPINDRIFT_CONNECTOR_HOST"
	connectorHostAttr   = "connector-host"
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

// builtArtifact is <out>/<rel> of the nix-built flake package attr, for a test
// artifact envVar did not name. An unavailable nix is an error, not a skip,
// because no core test may skip the Connector seam (ADR 0067). Callers read
// envVar themselves, by name, so launcher-env-coverage sees the read.
func builtArtifact(envVar, hint, attr, rel string) (string, error) {
	built, skip, err := ResolveBuild(envVar, hint, func(root string) []string {
		return []string{root + "#" + attr}
	})
	if skip != "" {
		return "", errors.New(skip)
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(built, rel), nil
}

// ConnectorDir returns the directory of fixture Connector .wasm files:
// $SPINDRIFT_CONNECTOR_FIXTURES_DIR when set, else the nix-built
// connector-wasm-fixtures output. The answer is computed once per test binary,
// since the nix build is slow.
var ConnectorDir = sync.OnceValues(func() (string, error) {
	if dir := os.Getenv(connectorEnvVar); dir != "" {
		return dir, nil
	}
	return builtArtifact(connectorEnvVar, "a directory of fixture Connector .wasm files", connectorFlakeAttr, "")
})

// ConnectorHost returns the connector-host binary: $SPINDRIFT_CONNECTOR_HOST
// when set, else <out>/bin/connector-host of the nix-built connector-host
// package, computed once per test binary like ConnectorDir.
var ConnectorHost = sync.OnceValues(func() (string, error) {
	if bin := os.Getenv(connectorHostEnvVar); bin != "" {
		return bin, nil
	}
	return builtArtifact(connectorHostEnvVar, "the connector-host binary", connectorHostAttr, filepath.Join("bin", "connector-host"))
})

// NewConnectorHostCmd is a fresh, unstarted connector-host child whose stderr
// goes to stderr. An exec.Cmd runs once, so each host needs its own.
func NewConnectorHostCmd(stderr io.Writer) (*exec.Cmd, error) {
	bin, err := ConnectorHost()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin)
	cmd.Stderr = stderr
	return cmd, nil
}

// testLogger is the part of testing.TB ConnectorHostCmd needs; naming it keeps
// the testing package out of this non-test file's imports.
type testLogger interface {
	Helper()
	Fatal(args ...any)
	Log(args ...any)
}

// ConnectorHostCmd is NewConnectorHostCmd with the host's stderr sent to the
// test log, failing t when the binary cannot be resolved.
func ConnectorHostCmd(t testLogger) *exec.Cmd {
	t.Helper()
	cmd, err := NewConnectorHostCmd(logWriter{t})
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

type logWriter struct{ t testLogger }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(string(p))
	return len(p), nil
}

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
