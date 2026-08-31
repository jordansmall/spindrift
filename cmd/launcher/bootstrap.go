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

// errConfigInvalid is the sentinel bootstrap() wraps its own validate(c)
// error return with (issue #2568 slice 1), letting a caller distinguish "the
// loaded config failed validation" from any other bootstrap failure (a
// readiness check, the accumulation-repo seed, etc.) via errors.Is, without
// changing what validate(c) itself returns -- existing tests assert
// validate(c)'s raw error text directly and must keep passing unchanged.
var errConfigInvalid = errors.New("config invalid")

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
// resolution, the accumulation-lock seed, runner construction, a readiness
// check, the dispatch factory (including driver-cache setup), and settle.
// Config load+validate and the issueTracker/codeForge/gate prologue live in
// newGatedContext; see the seedConfig/gc split below for why the
// accumulation-lock seed still has to run outside that call. ensureReady
// selects EnsureReady() (build if absent, the default) over IsReady() (fail
// fast without building, --no-build) -- the one axis that varies per entry
// point. kind (dispatchKindWork or dispatchKindResearch, ADR 0022) selects
// the label family, waves blocker handling, and Settle implementation via
// applyDispatchKind — the other axis, carried by which subcommand launched.
// selfContained (issue #2202, --self-contained) is the research kind's
// no-repo sub-mode: set only by the research subcommand handler, false
// everywhere else. No step here can fail after the dispatch factory is
// constructed, so an error return never carries a launch context that still
// needs cleanup. The one thing that can still be outstanding on an early
// error return is the accumulation lock (issue #2441): it's acquired well
// before the factory exists, so a bare `return nil, err` from any step
// between acquisition and launchContext's construction would otherwise leak
// a held lock for the rest of the process. A single deferred release,
// registered right after acquisition, covers that whole window instead of
// relying on every such return site to remember it. Two other steps also
// stay inline here rather than moving into newGatedContext/newReadContext:
// the mutating registry-proxy-credential resolve (issue #2944 -- must run
// exactly once, after the gate walk's own validate peek has already
// succeeded) and the GH-token-refresh watch (starts a background goroutine
// that outlives this call, which neither constructor owns).
func bootstrap(ensureReady bool, kind string, selfContained bool) (lc *launchContext, err error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	// seedConfig backs the two steps below that must run before
	// newGatedContext's gate walk but don't depend on validate(c) having
	// succeeded first: MigrateLegacyLogDir needs only pwd, and
	// seedAccumulationRepoIfLocal must already hold the accumulation lock
	// by the time a gate can fail
	// (TestBootstrap_EarlyErrorAfterAccumLockAcquired_ReleasesLock).
	// newGatedContext below loads config again for gc.config; the two
	// loads agree because loadConfig is a deterministic function of env
	// vars and host git config.
	seedConfig := applyDispatchKind(loadConfig(), kind)
	seedConfig.selfContained = selfContained

	// validate(seedConfig) duplicates the validate(gc.config) call
	// newGatedContext makes below: seedAccumulationRepoIfLocal has a real
	// git side effect, which must not run ahead of config validation (an
	// invalid REPO_SLUG under CODE_FORGE=local must surface as validate's
	// own error, not a confusing git-push failure --
	// TestMainRun_Dispatch_MissingRepoSlugUnderLocalForge_ExitsConfigInvalid).
	// Nothing between here and newGatedContext's own validate(gc.config)
	// mutates env or config, so the two calls agree.
	if err := validate(seedConfig); err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}

	// One-time relocation (issue #2138): fold any legacy top-level logs/
	// left by an earlier spindrift into the new .spindrift/logs before any
	// host-side site reads or creates a log path this run.
	if err := dispatch.MigrateLegacyLogDir(pwd); err != nil {
		return nil, err
	}
	accumLock, err := seedAccumulationRepoIfLocal(seedConfig, pwd)
	if err != nil {
		return nil, err
	}
	if accumLock != nil {
		// Covers every early `return nil, err` between here and
		// launchContext's construction below (see the doc comment above):
		// once that construction succeeds, the final `return ..., nil`
		// leaves err nil, so this is a no-op and cleanup (below) becomes the
		// lock's sole owner from then on.
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

	// Resolved here, after newGatedContext's own validate(gc.config) peek
	// has already succeeded: resolution mutates env (os.Unsetenv on the
	// env-var form, see resolveRegistryProxyCredential) and must run
	// exactly once, so it can't run before that peek re-reads the same var.
	// validate(seedConfig) above already gives the "a bad credential fails
	// before the git push and network gates run" guarantee, since peek and
	// resolve share identical read/validate logic (credentialFromSource).
	if c.registryProxyUpstreamURL != "" {
		cred, err := resolveRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
		}
		c.registryProxyCredential = cred
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
	r := runnerForKind(c, rc, pwd)
	if ensureReady {
		if err := r.EnsureReady(); err != nil {
			return nil, err
		}
	} else if err := r.IsReady(); err != nil {
		return nil, err
	}

	lw := localloop.Wire(localloopConfig(c), it)
	f := newDispatchFactory(c, pwd, r, it, lw, cf)
	s := newSettle(c, it, lw, cf, gc.capabilities)

	return &launchContext{
		config:       c,
		pwd:          pwd,
		runner:       r,
		issueTracker: it,
		codeForge:    cf,
		factory:      f,
		settle:       s,
		cleanup: func() {
			f.Cleanup()
			// Held from the seed (seedAccumulationRepoIfLocal, above) through
			// the whole run — every Box this process dispatches, not just the
			// initial seed+mount — so a concurrent process can't seed/mount
			// the same Accumulation repo while this one still has it in use
			// (issue #2441). Release is a best-effort, process-exit-time
			// advisory unlock; the OS releases the underlying flock on
			// process exit regardless, so an error here has nowhere useful
			// to go and is ignored, same as other best-effort cleanup here.
			if accumLock != nil {
				_ = accumLock.Release()
			}
		},
	}, nil
}

// workSettle asserts that lc.settle satisfies settle.WorkSettler. Both
// callers (console, recover) always bootstrap with dispatchKindWork, so
// this always succeeds in practice; a clear panic beats a generic
// "interface conversion" one if that invariant is ever broken.
func (lc *launchContext) workSettle() settle.WorkSettler {
	ws, ok := lc.settle.(settle.WorkSettler)
	if !ok {
		panic("lc.settle does not implement settle.WorkSettler (bootstrap wiring bug)")
	}
	return ws
}

// seedAccumulationRepoIfLocal creates and seeds the bare Accumulation repo
// (ADR 0033) from pwd's checkout before any Box runs, when c.codeForge is
// "local" and the run isn't research's no-repo self-contained sub-mode —
// a no-op for github/git, which use no Accumulation repo, and for
// self-contained research (validate() in main.go rejects selfContained
// outside dispatchKindResearch, so this combination only arises there),
// which never clones or lands code (it posts one verdict comment and
// stops), so seeding for it would be pure waste and a needless new failure
// surface. Non-self-contained research still needs seeding: it clones and
// explores /repo in-box just like work does (agent/entrypoint.sh's
// clone_repo() under CODE_FORGE=local), so skipping it there left the
// clone with nothing to mount against (issue #2439). Wired into
// bootstrap's prologue (issue #1726) rather than left for the mount or
// landing forge to discover on demand: a defaulted-but-nonexistent
// AccumulationRepoDir otherwise makes candidateMount silently skip the
// /repo mount, and host-side landing then fails against a repo that was
// never created. c.codeForgeAccumulationRepoDir is already resolved to an
// absolute path by loadConfig, matching SeedAccumulationRepo's requirement.
//
// Also acquires and returns an exclusive, non-blocking AccumulationLock
// (issue #2441) on the Accumulation repo path before seeding it, so a
// second, independent `spindrift` process (e.g. a concurrent research and
// dispatch run) can't seed/mount the same repo at the same time — a
// corruption hazard the seed-then-push race alone didn't guard against.
// The lock is acquired before, not after, SeedAccumulationRepo runs, so a
// concurrent process's in-flight seed is what actually blocks or rejects a
// second attempt, rather than racing it. The caller (bootstrap) folds the
// returned lock's Release into the launch context's cleanup, holding it for
// the whole run rather than just this seed. Returns a nil lock for the
// no-op cases above, and releases the lock without returning it if
// SeedAccumulationRepo itself fails, so a failed seed never leaks a held
// lock.
func seedAccumulationRepoIfLocal(c config, pwd string) (*local.AccumulationLock, error) {
	if c.codeForge != "local" || c.selfContained {
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
	f := newDispatchFactory(rc, lc.pwd, lc.runner, it, lw, lc.codeForge)

	// newSettle(rc, ...) always takes the dispatchKindResearch branch and
	// returns NewResearchSettle/NewResearchSettleReadOnly, which never reads
	// its caps argument (settle.Config is a work-kind-only concern) -- so
	// there is nothing here worth resolving Capabilities against.
	s := newSettle(rc, it, lw, lc.codeForge, forge.Capabilities{})
	return it, f, s
}
