package main

import (
	"os"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// ghTokenRefreshInterval is how often bootstrap polls GH_TOKEN_REFRESH_FILE
// (when set) for a freshly minted token. An installation token's ~1h
// lifetime (issue #1027) gives ample slack for a minute-scale poll.
const ghTokenRefreshInterval = 60 * time.Second

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
// path, research (and `research <nums>`), and recover: working-dir
// resolution, config load+validate, runner construction, a readiness check,
// the forge client, the dispatch factory (including driver-cache setup), and
// settle. ensureReady selects EnsureReady() (build if absent, the default)
// over IsReady() (fail fast without building, --no-build) -- the one axis
// that varies per entry point. kind (dispatchKindWork or
// dispatchKindResearch, ADR 0022) selects the label family, waves blocker
// handling, and Settle implementation via applyDispatchKind — the other axis,
// carried by which subcommand launched. No step here can fail after the
// dispatch factory is constructed, so an error return never carries a launch
// context that still needs cleanup.
func bootstrap(ensureReady bool, kind string) (*launchContext, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	c := applyDispatchKind(loadConfig(), kind)
	if err := validate(c); err != nil {
		return nil, err
	}
	if err := seedAccumulationRepoIfLocal(c, pwd); err != nil {
		return nil, err
	}

	// A run that outlives GH_TOKEN_REFRESH_FILE's minter's token lifetime
	// (issue #1027) would otherwise 401 at the terminal gh calls (merge,
	// label edits, final comment): keep GH_TOKEN current for the rest of
	// the process by polling the file an external minter rewrites in
	// place. No-op when unset (the default) — GH_TOKEN then stays whatever
	// the ambient environment set it to for the whole run, as before.
	if c.ghTokenRefreshFile != "" {
		go tokenrefresh.Watch(c.ghTokenRefreshFile, ghTokenRefreshInterval, nil, func(v string) error {
			return os.Setenv("GH_TOKEN", v)
		})
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
	cf := newCodeForge(c, local.SanitizedParent{})
	if err := checkReadOnlyCapabilityGate(c, cf, it); err != nil {
		return nil, err
	}
	lw := localloop.Wire(localloopConfig(c), it)
	f := newDispatchFactory(c, pwd, r, lw, cf)
	s := newSettle(c, it, lw, cf)

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

// seedAccumulationRepoIfLocal creates and seeds the bare Accumulation repo
// (ADR 0033) from pwd's checkout before any Box runs, when c.codeForge is
// "local" and the dispatch kind is work — a no-op for github/git, which use
// no Accumulation repo, and for research, which never mounts /repo or lands
// code (it posts one verdict comment and stops), so seeding for it would be
// pure waste and a needless new failure surface. Wired into bootstrap's
// prologue (issue #1726) rather than left for the mount or landing forge to
// discover on demand: a defaulted-but-nonexistent AccumulationRepoDir
// otherwise makes candidateMount silently skip the /repo mount, and
// host-side landing then fails against a repo that was never created.
// c.codeForgeAccumulationRepoDir is already resolved to an absolute path by
// loadConfig, matching SeedAccumulationRepo's requirement.
func seedAccumulationRepoIfLocal(c config, pwd string) error {
	if c.codeForge != "local" || c.dispatchKind == dispatchKindResearch {
		return nil
	}
	return local.SeedAccumulationRepo(c.codeForgeAccumulationRepoDir, pwd, c.baseBranch)
}

// researchLaunchStack builds the research-kind tracker, dispatch factory,
// and settle the Console wires in as its second launch stack alongside lc's
// own work-kind stack (issue #1708, ADR 0022) — reusing the same
// newIssueTracker/newDispatchFactory/newSettle helpers bootstrap's work-kind
// construction goes through, just with dispatchKindResearch applied, rather
// than a second bootstrap() call: lc's runner is already ready (EnsureReady
// already ran), so a second readiness check and driver-cache watch goroutine
// would be pure duplication. The returned Factory owns its own driver-cache
// root; the caller must arrange its own Cleanup call, same as lc.factory's.
func researchLaunchStack(lc *launchContext) (forge.IssueTracker, *dispatch.Factory, settle.Settler) {
	rc := applyDispatchKind(lc.config, dispatchKindResearch)
	it := newIssueTracker(rc)
	lw := localloop.Wire(localloopConfig(rc), it)
	f := newDispatchFactory(rc, lc.pwd, lc.runner, lw, lc.codeForge)
	s := newSettle(rc, it, lw, lc.codeForge)
	return it, f, s
}
