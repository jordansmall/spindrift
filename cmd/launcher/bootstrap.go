package main

import (
	"errors"
	"fmt"
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

// errConfigInvalid wraps validate's own error (issue #2568) so a caller can tell
// a failed config validation from any other bootstrap failure via errors.Is.
// bootstrap never rewrites validate's message, so its text reaches stderr intact.
var errConfigInvalid = errors.New("config invalid")

// ghTokenRefreshInterval is how often bootstrap polls GH_TOKEN_REFRESH_FILE for
// a freshly minted token. An installation token lives about an hour (issue #1027).
const ghTokenRefreshInterval = 60 * time.Second

// launchContext bundles the wiring every top-level dispatch entry point shares,
// including the independently wired IssueTracker and CodeForge (ADR 0013).
// bootstrap is the only place that constructs one; tests build it directly with
// fakes to exercise subcommand logic without going through bootstrap.
type launchContext struct {
	config       config
	pwd          string
	runner       runner.Runner
	issueTracker forge.IssueTracker
	codeForge    forge.CodeForge
	// bootstrap copies capabilities out of gc at construction (issue #2946) so
	// reconcileAfterDispatch's callers need not each re-resolve it.
	capabilities forge.Capabilities
	factory      *dispatch.Factory
	settle       settle.Settler
	cleanup      func()
}

// bootstrap wires the prologue shared by run, dispatch, research, and recover.
// ensureReady picks EnsureReady() over IsReady() (--no-build); kind selects the
// label family, blocker handling, and Settle via applyDispatchKind (ADR 0022);
// selfContained is research's no-repo sub-mode (issue #2202). The accumulation
// lock is the one thing an early error return can leak, so a defer releases it.
func bootstrap(ensureReady bool, kind string, selfContained bool) (lc *launchContext, err error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// The two steps below must run before newGatedContext's gate walk: the seed
	// has to hold the accumulation lock by the time a gate can fail.
	// newGatedContext loads config again, and the two loads agree because
	// loadConfig is a deterministic function of env vars and host git config.
	seedConfig := applyDispatchKind(loadConfig(), kind)
	seedConfig.selfContained = selfContained

	// Duplicates newGatedContext's own validate below: the seed has a real git
	// side effect, and an invalid REPO_SLUG under CODE_FORGE=local must fail as
	// validate's error, not as a confusing git-push failure. Nothing in between
	// mutates env or config, so the two calls agree.
	if err := validate(seedConfig); err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}

	// One-time relocation (issue #2138): fold a legacy top-level logs/ into
	// .spindrift/logs before anything reads or creates a log path this run.
	if err := dispatch.MigrateLegacyLogDir(pwd); err != nil {
		return nil, err
	}
	accumLock, err := seedAccumulationRepoIfHostMediated(seedConfig, pwd)
	if err != nil {
		return nil, err
	}
	if accumLock != nil {
		// Covers every early error return below. Once launchContext is
		// constructed err is nil, so cleanup becomes the lock's sole owner.
		defer func() {
			if err != nil {
				_ = accumLock.Release()
			}
		}()
	}

	gc, err := newGatedContext(os.Stdout, kind, selfContained)
	if err != nil {
		return nil, err
	}
	// c diverges from gc.config below (registryProxyRoutes is set on c only),
	// which is safe only because gc itself is never read again.
	c := gc.config
	it := gc.issueTracker
	cf := gc.codeForge

	// Resolution mutates env (os.Unsetenv on the env-var form, see
	// credresolver.New) and must run exactly once per route, so it cannot run
	// before newGatedContext's validate peek re-reads the same vars (issue
	// #2944). validate(seedConfig) above still fails a bad credential ahead of
	// the git push and network gates, since peek and resolve share logic (#3139).
	routes, err := buildRegistryProxyRoutes(c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}
	c.registryProxyRoutes = routes

	// A run that outlives the minter's token lifetime would otherwise 401 at the
	// terminal gh calls (merge, label edits, final comment), so poll the file
	// the minter rewrites in place (issue #1027). No-op when unset.
	if c.ghTokenRefreshFile != "" {
		go tokenrefresh.Watch(c.ghTokenRefreshFile, ghTokenRefreshInterval, nil, func(v string) error {
			return os.Setenv("GH_TOKEN", v)
		})
	}

	rc := runnerConfig(c)
	r := runnerForKind(c, rc, pwd)
	if ensureReady {
		if err := r.EnsureReady(); err != nil {
			return nil, err
		}
	} else if err := r.IsReady(); err != nil {
		return nil, err
	}

	lw := localloop.Wire(localloopConfig(c), it)
	f := newDispatchFactory(c, pwd, r, it, lw, cf, gc.capabilities)
	s := newSettle(c, it, lw, cf, gc.capabilities)

	return &launchContext{
		config:       c,
		pwd:          pwd,
		runner:       r,
		issueTracker: it,
		codeForge:    cf,
		capabilities: gc.capabilities,
		factory:      f,
		settle:       s,
		cleanup: func() {
			f.Cleanup()
			// Held for the whole run rather than only the seed, so a
			// concurrent process cannot seed or mount the same Accumulation
			// repo while this one still uses it (issue #2441). The OS releases
			// the flock on process exit anyway, so a Release error is ignored.
			if accumLock != nil {
				_ = accumLock.Release()
			}
		},
	}, nil
}

// workSettle asserts that lc.settle satisfies settle.WorkSettler. Both callers
// bootstrap with dispatchKindWork, so a clear panic beats a generic interface
// conversion one if that invariant is ever broken.
func (lc *launchContext) workSettle() settle.WorkSettler {
	ws, ok := lc.settle.(settle.WorkSettler)
	if !ok {
		panic("lc.settle does not implement settle.WorkSettler (bootstrap wiring bug)")
	}
	return ws
}

// seedAccumulationRepoIfHostMediated seeds the bare Accumulation repo (ADR 0033)
// from pwd's checkout before any Box runs, holding an exclusive lock on it for
// the caller (issue #2441). A missing repo makes candidateMount silently skip
// the /repo mount, and host-side landing then fails (issue #1726); research
// needs the seed too unless it is self-contained (issue #2439).
func seedAccumulationRepoIfHostMediated(c config, pwd string) (*local.AccumulationLock, error) {
	row, _ := backendByName(c.codeForge)
	if !row.HostMediatedRemote || c.selfContained {
		return nil, nil
	}
	lock, err := local.AcquireAccumulationLock(c.codeForgeAccumulationRepoDir)
	if err != nil {
		return nil, err
	}
	if err := local.SeedAccumulationRepo(c.codeForgeAccumulationRepoDir, pwd, c.baseBranch); err != nil {
		_ = lock.Release()
		return nil, err
	}
	return lock, nil
}

// researchLaunchStack builds the research-kind tracker, dispatch factory, and
// settle the Console wires in alongside lc's work-kind stack (issue #1708, ADR
// 0022). A second bootstrap() call would repeat the readiness check and the
// driver-cache watch goroutine for an already-ready runner. The returned Factory
// owns its own driver-cache root, so the caller must call its Cleanup.
func researchLaunchStack(lc *launchContext) (forge.IssueTracker, *dispatch.Factory, settle.Settler) {
	rc := applyDispatchKind(lc.config, dispatchKindResearch)
	it := newIssueTracker(rc)
	lw := localloop.Wire(localloopConfig(rc), it)
	f := newDispatchFactory(rc, lc.pwd, lc.runner, it, lw, lc.codeForge, lc.capabilities)

	// newSettle takes the dispatchKindResearch branch here, and that branch
	// never reads its caps argument, so there is nothing worth resolving.
	s := newSettle(rc, it, lw, lc.codeForge, forge.Capabilities{})
	return it, f, s
}
