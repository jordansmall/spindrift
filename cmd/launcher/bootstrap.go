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

// errConfigInvalid is the sentinel bootstrap() wraps its own validate(c) error
// return with, letting a caller distinguish "the loaded config failed
// validation" from any other bootstrap failure via errors.Is, without changing
// what validate(c) itself returns.
var errConfigInvalid = errors.New("config invalid")

// ghTokenRefreshInterval is how often bootstrap polls GH_TOKEN_REFRESH_FILE
// (when set) for a freshly minted token. An installation token's ~1h lifetime
// gives ample slack for a minute-scale poll.
const ghTokenRefreshInterval = 60 * time.Second

// launchContext bundles the wiring shared by every top-level dispatch entry
// point (run, the selective `dispatch <nums>` path, recover): config, working
// directory, the ready runner, the independently-wired IssueTracker and
// CodeForge (ADR 0013), the dispatch factory (with its driver cache), settle,
// and the driver-cache cleanup hook. bootstrap is the only place that
// constructs one; tests build it directly with fakes.
type launchContext struct {
	config       config
	pwd          string
	runner       runner.Runner
	issueTracker forge.IssueTracker
	codeForge    forge.CodeForge
	// capabilities is issueTracker's and codeForge's resolved
	// forge.Capabilities — reconcileAfterDispatch's callers reuse this one
	// resolution instead of each doing their own.
	capabilities forge.Capabilities
	factory      *dispatch.Factory
	settle       settle.Settler
	cleanup      func()
}

// bootstrap wires the prologue shared by run, `dispatch <nums>`, research, and
// recover: working-dir resolution, the accumulation-lock seed, runner
// construction, a readiness check, the dispatch factory (including its driver
// cache), and settle. Config load+validate and the issueTracker/codeForge/gate
// prologue live in newGatedContext; the seedConfig/gc split below says why the
// accumulation-lock seed still has to run outside that call.
//
// ensureReady picks EnsureReady() (build if absent, the default) over
// IsReady() (fail fast without building, --no-build). kind (ADR 0022) selects
// the label family, waves blocker handling, and Settle implementation via
// applyDispatchKind. selfContained (--self-contained) is the research kind's
// no-repo sub-mode, false everywhere else.
//
// No step here can fail after the dispatch factory is constructed, so an error
// return never carries a launch context that still needs cleanup. The one
// thing that can still be outstanding on an early error return is the
// accumulation lock, acquired well before the factory exists; one deferred
// release right after acquisition covers that whole window instead of relying
// on every return site to remember it. Two steps stay inline rather than
// moving into newGatedContext/newReadContext: the mutating
// registry-proxy-credential resolve (must run exactly once, after the gate
// walk's validate peek) and the GH-token-refresh watch (a background goroutine
// outliving this call, which neither constructor owns).
func bootstrap(ensureReady bool, kind string, selfContained bool) (lc *launchContext, err error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// seedConfig backs the two steps below that must run before
	// newGatedContext's gate walk but don't depend on validate(c) having
	// succeeded first: MigrateLegacyLogDir needs only pwd, and
	// seedAccumulationRepoIfHostMediated must already hold the accumulation
	// lock by the time a gate can fail. newGatedContext loads config again for
	// gc.config; the two loads agree because loadConfig is a deterministic
	// function of env vars and host git config.
	seedConfig := applyDispatchKind(loadConfig(), kind)
	seedConfig.selfContained = selfContained

	// Duplicates the validate(gc.config) call newGatedContext makes below:
	// seedAccumulationRepoIfHostMediated has a real git side effect, which must
	// not run ahead of config validation (an invalid REPO_SLUG under
	// CODE_FORGE=local must surface as validate's own error, not a confusing
	// git-push failure). Nothing between here and that call mutates env or
	// config, so the two agree.
	if err := validate(seedConfig); err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}

	// Fold any legacy top-level logs/ left by an earlier spindrift into
	// .spindrift/logs before any host-side site reads or creates a log path.
	if err := dispatch.MigrateLegacyLogDir(pwd); err != nil {
		return nil, err
	}
	accumLock, err := seedAccumulationRepoIfHostMediated(seedConfig, pwd)
	if err != nil {
		return nil, err
	}
	if accumLock != nil {
		// Covers every early `return nil, err` between here and
		// launchContext's construction below; once that succeeds err is nil,
		// so this is a no-op and cleanup becomes the lock's sole owner.
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
	// c is copied out of gc here and diverges below (registryProxyCredential
	// is set on c only) -- safe only because gc itself is never read again.
	c := gc.config
	it := gc.issueTracker
	cf := gc.codeForge

	// Resolved after newGatedContext's own validate(gc.config) peek has
	// succeeded: resolution mutates env (os.Unsetenv on the env-var form) and
	// must run exactly once, so it can't precede that peek's re-read of the
	// same var. validate(seedConfig) above already gives the "a bad credential
	// fails before the git push and network gates run" guarantee, since peek
	// and resolve share credentialFromSource.
	if c.registryProxyUpstreamURL != "" {
		cred, err := resolveRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
		}
		c.registryProxyCredential = cred
	}

	// A run that outlives the minter's token lifetime would otherwise 401 at
	// the terminal gh calls (merge, label edits, final comment): keep GH_TOKEN
	// current by polling the file an external minter rewrites in place. No-op
	// when unset — GH_TOKEN then stays whatever the ambient environment set.
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
			// Held from the seed through the whole run — every Box this
			// process dispatches, not just the initial seed+mount — so a
			// concurrent process can't seed/mount the same Accumulation repo
			// while this one still has it in use. Release is best-effort: the
			// OS drops the underlying flock on process exit regardless, so an
			// error here has nowhere useful to go.
			if accumLock != nil {
				_ = accumLock.Release()
			}
		},
	}, nil
}

// workSettle asserts that lc.settle satisfies settle.WorkSettler. Both callers
// (console, recover) always bootstrap with dispatchKindWork, so a clear panic
// beats a generic "interface conversion" one if that invariant ever breaks.
func (lc *launchContext) workSettle() settle.WorkSettler {
	ws, ok := lc.settle.(settle.WorkSettler)
	if !ok {
		panic("lc.settle does not implement settle.WorkSettler (bootstrap wiring bug)")
	}
	return ws
}

// seedAccumulationRepoIfHostMediated creates and seeds the bare Accumulation
// repo (ADR 0033) from pwd's checkout before any Box runs, when c.codeForge is
// "local" and the run isn't research's no-repo self-contained sub-mode — a
// no-op for github/git, and for self-contained research, which never clones or
// lands code. Non-self-contained research still needs seeding: it clones and
// explores /repo in-box just like work does. Seeding here rather than leaving
// it for the mount or landing forge to discover on demand matters because a
// defaulted-but-nonexistent AccumulationRepoDir makes candidateMount silently
// skip the /repo mount, and host-side landing then fails against a repo that
// was never created.
//
// Also acquires and returns an exclusive, non-blocking AccumulationLock on the
// repo path before seeding, so a second `spindrift` process can't seed/mount
// the same repo at once. Acquiring before, not after, SeedAccumulationRepo is
// what makes a concurrent in-flight seed block or reject a second attempt
// rather than race it. The caller folds Release into the launch context's
// cleanup, holding the lock for the whole run. Returns a nil lock for the
// no-op cases, and releases without returning it if SeedAccumulationRepo
// fails, so a failed seed never leaks a held lock.
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
// settle the Console wires in as its second launch stack alongside lc's own
// work-kind stack (ADR 0022) — reusing bootstrap's own construction helpers
// with dispatchKindResearch applied, rather than a second bootstrap() call:
// lc's runner is already ready, so a second readiness check and driver-cache
// watch goroutine would be pure duplication. The returned Factory owns its own
// driver-cache root; the caller must arrange its own Cleanup call.
func researchLaunchStack(lc *launchContext) (forge.IssueTracker, *dispatch.Factory, settle.Settler) {
	rc := applyDispatchKind(lc.config, dispatchKindResearch)
	it := newIssueTracker(rc)
	lw := localloop.Wire(localloopConfig(rc), it)
	f := newDispatchFactory(rc, lc.pwd, lc.runner, it, lw, lc.codeForge, lc.capabilities)

	// newSettle(rc, ...) always takes the dispatchKindResearch branch, which
	// never reads its caps argument (settle.Config is a work-kind-only
	// concern), so there is nothing worth resolving Capabilities against.
	s := newSettle(rc, it, lw, lc.codeForge, forge.Capabilities{})
	return it, f, s
}
