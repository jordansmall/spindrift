package main

import (
	"os"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// launchContext bundles the wiring shared by every top-level dispatch entry
// point (run, the selective `dispatch <nums>` path, recover): the loaded and
// validated config, the resolved working directory, the ready runner, the
// forge client, the dispatch factory (with its driver cache), and settle.
// bootstrap is the only place that constructs one; tests build it directly
// with fakes to exercise entry-point logic without going through bootstrap.
type launchContext struct {
	config  config
	pwd     string
	runner  runner.Runner
	forge   forge.Client
	factory *dispatch.Factory
	settle  settle.Settler
}

// bootstrap wires the prologue shared by run, the selective `dispatch <nums>`
// path, and recover: working-dir resolution, config load+validate, runner
// construction, a readiness check, the forge client, the dispatch factory
// (including driver-cache setup), and settle. ensureReady selects
// EnsureReady() (build if absent, the default) over IsReady() (fail fast
// without building, --no-build) -- the one axis that varies per entry point.
// The returned cleanup runs the dispatch factory's driver-cache cleanup;
// callers must run it on every exit path. Because os.Exit is only ever
// called in main, deferring it at the call site is always sufficient.
func bootstrap(ensureReady bool) (*launchContext, func(), error) {
	noopCleanup := func() {}

	pwd, err := os.Getwd()
	if err != nil {
		return nil, noopCleanup, err
	}

	c := loadConfig()
	if err := validate(c); err != nil {
		return nil, noopCleanup, err
	}

	r := newRunner(c, pwd)
	if ensureReady {
		if err := r.EnsureReady(); err != nil {
			return nil, noopCleanup, err
		}
	} else if err := r.IsReady(); err != nil {
		return nil, noopCleanup, err
	}

	fc := newForgeClient(c)
	f := newDispatchFactory(c, pwd, r)
	s := newSettle(c, fc)

	lc := &launchContext{
		config:  c,
		pwd:     pwd,
		runner:  r,
		forge:   fc,
		factory: f,
		settle:  s,
	}
	return lc, f.Cleanup, nil
}
