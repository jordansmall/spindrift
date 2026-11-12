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
// # Launcher smoke set
//
// TestSeamLauncherSmokeMatrix in cmd/launcher/launcher_seam_integration_test.go
// replaced the launcher-side bats files (tests/run-*.bats). Its smokeMatrix
// carries a case for every dispatch kind and runtime the rendered input
// document distinguishes; recover has none, since it runs no Box and its
// Box-facing fields mirror work's. A new kind or runtime distinction in the
// document earns a new case there, and TestSeamSmokeCatchesDroppedKnob keeps
// the matrix honest by asserting a dropped document knob fails a check.
//
// # Fake Driver
//
// Strategy is core's fake Driver. Its log grammar is the canonical Event
// itself, one JSON-encoded line each (see Line), plus the host-written
// spindrift_op lines every Pass log carries. It proves the shared readers work
// over the canonical grammar only, not the real claude decoder.
package seamtest

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
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

type buildResult struct {
	dir, skipReason string
	err             error
}

// buildOnce caches the nix-build resolution for the test binary's
// lifetime: the nix build is slow and its answer cannot change mid-run.
var buildOnce = sync.OnceValue(func() buildResult {

	dir, skip, err := fixturebuild.ResolveBuild(EnvVar, "a directory of rendered fixtures", func(root string) []string {
		return []string{root + "#" + flakeAttr}
	})
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
