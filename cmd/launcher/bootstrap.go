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
// independently-wired IssueTracker and CodeForge (ADR 0013), the dispatch
// factory (with its driver cache), settle, and the driver-cache cleanup hook.
// bootstrap is the only place that constructs one; tests build it directly
// with fakes (and a spy cleanup) to exercise subcommand logic without going
// through bootstrap.
type launchContext struct {
	config       config
	pwd          string
	runner       runner.Runner
	issueTracker forge.IssueTracker
	codeForge    forge.CodeForge
	factory      *dispatch.Factory
	settle       settle.Settler
	cleanup      func()
}

// bootstrap wires the prologue shared by run, the selective `dispatch <nums>`
// path, and recover: working-dir resolution, config load+validate, runner
// construction, a readiness check, the forge client, the dispatch factory
// (including driver-cache setup), and settle. ensureReady selects
// EnsureReady() (build if absent, the default) over IsReady() (fail fast
// without building, --no-build) -- the one axis that varies per entry point.
// No step here can fail after the dispatch factory is constructed, so an
// error return never carries a launch context that still needs cleanup.
func bootstrap(ensureReady bool) (*launchContext, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	c := loadConfig()
	if err := validate(c); err != nil {
		return nil, err
	}

	rc := runnerConfig(c)
	var r runner.Runner
	if c.runtime == "bwrap" {
		r = runner.NewBwrap(rc)
	} else {
		r = runner.NewOCI(rc, pwd)
	}
	if ensureReady {
		if err := r.EnsureReady(); err != nil {
			return nil, err
		}
	} else if err := r.IsReady(); err != nil {
		return nil, err
	}

	it := newIssueTracker(c)
	cf := newCodeForge(c)
	f := newDispatchFactory(c, pwd, r)
	s := newSettle(c, it, cf)

	return &launchContext{
		config:       c,
		pwd:          pwd,
		runner:       r,
		issueTracker: it,
		codeForge:    cf,
		factory:      f,
		settle:       s,
		cleanup:      f.Cleanup,
	}, nil
}
