package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/panicguard"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// errConfigInvalid wraps validate's own error (issue #2568) so a caller can tell
// a failed config validation from any other bootstrap failure via errors.Is.
// bootstrap never rewrites validate's message, so its text reaches stderr intact.
var errConfigInvalid = errors.New("config invalid")

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
	// merge settles work, recover and the Butler (whose SettleLedger falls to the
	// merge gate); verdict settles research. newSettlers sets exactly one.
	merge   settle.MergeSettler
	verdict *settle.ResearchSettle
	// terminated is the merge settler's own registry, and every marker writes
	// it: the Console's Terminate, the waves, and an abort's Reclaim. An abort
	// and a settle goroutine already polling CI then agree on an issue's fate
	// (#3521), and a Console recover gesture cannot replace it and strand later
	// marks where no settle looks (#3522). Under research no settler reads it
	// (the verdict arm mints a fresh one); the Console and waves still mark it.
	terminated *terminate.Registry
	cleanup    func()
}

// bootstrap wires the prologue shared by console, recover, dispatch, research,
// and butler. ensureReady picks EnsureReady() over IsReady() (--no-build); kind
// selects the label family, blocker handling, and Settle via applyDispatchKind
// (ADR 0022); stdout receives the gate walk's output; selfContained is
// research's no-repo sub-mode (issue #2202). The accumulation lock is the one
// thing an early error return can leak, so a defer releases it.
func bootstrap(stdout io.Writer, ensureReady bool, kind *dispatchkind.Descriptor, selfContained bool) (lc *launchContext, err error) {
	return bootstrapWith(stdout, ensureReady, kind, selfContained, false)
}

// bootstrapShadowed is bootstrap for a dispatch run (`dispatch`, `research`):
// a backend row asking for a read shadow gets its tracker and code forge
// layered before the settlers and factory capture them. lc.cleanup closes the
// shadows, so every path that defers it ends them.
func bootstrapShadowed(stdout io.Writer, ensureReady bool, kind *dispatchkind.Descriptor, selfContained bool) (*launchContext, error) {
	return bootstrapWith(stdout, ensureReady, kind, selfContained, true)
}

func bootstrapWith(stdout io.Writer, ensureReady bool, kind *dispatchkind.Descriptor, selfContained, shadowReads bool) (lc *launchContext, err error) {
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
	warnBoxAccessReadWrite(seedConfig, os.Stderr)

	// One-time relocation (issue #2138): fold a legacy top-level logs/ into
	// .spindrift/logs before anything reads or creates a log path this run.
	if err := dispatch.MigrateLegacyLogDir(pwd); err != nil {
		return nil, err
	}
	accumLock, err := seedAccumulationRepoIfHostSeeded(seedConfig, pwd)
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

	gc, err := newGatedContext(stdout, kind, selfContained)
	if err != nil {
		return nil, err
	}
	// c diverges from gc.config below (registryProxyRoutes is set on c only),
	// which is safe only because gc itself is never read again.
	c := gc.config
	it := gc.issueTracker
	cf := gc.codeForge
	caps := gc.capabilities
	var shadows *readShadows
	if shadowReads {
		shadows = newReadShadows(pwd, os.Stderr)
		it = shadows.wrapTracker(c, it)
		cf = shadows.wrapCodeForge(c, cf)
		caps = forge.ResolveCapabilities(cf, it, caps.ForgeDescriptor, caps.TrackerDescriptor)
		// Covers every early error return below, like accumLock's.
		defer func() {
			if err != nil {
				shadows.close()
			}
		}()
	}

	// Resolution mutates env (os.Unsetenv on the env-var form, see
	// credresolver.New) and must run exactly once per route, so it cannot run
	// before newGatedContext's validate peek re-reads the same vars (issue
	// #2944). validate(seedConfig) above still fails a bad credential ahead of
	// the git push and network gates, since peek and resolve share logic (#3139).
	routes, err := buildRegistryProxyRoutes(c, ecosystem.Registered())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}
	c.registryProxyRoutes = routes

	// A run that outlives the minter's token lifetime would otherwise 401 at the
	// terminal gh calls (merge, label edits, final comment), so poll the file
	// the minter rewrites in place (issue #1027). No-op when unset.
	if c.GhTokenRefreshFile != "" {
		panicguard.Go(func() {
			tokenrefresh.Watch(c.GhTokenRefreshFile, tokenrefresh.Interval, nil, func(v string) error {
				return os.Setenv("GH_TOKEN", v)
			})
		})
	}

	rc := runnerConfig(c)
	r, err := runnerForKind(c, rc, pwd)
	if err != nil {
		return nil, err
	}
	if ensureReady {
		if err := r.EnsureReady(); err != nil {
			return nil, err
		}
	} else if err := r.IsReady(); err != nil {
		return nil, err
	}

	lw := localloop.Wire(localloopConfig(c), it)
	mergeSettle, verdict, terminated, err := newSettlers(c.kind().Settle, c, it, lw, cf, caps)
	if err != nil {
		return nil, err
	}
	f := newDispatchFactory(c, pwd, r, it, lw, cf, caps)
	var merge settle.MergeSettler
	if mergeSettle != nil {
		merge = mergeSettle
	}

	return &launchContext{
		config:       c,
		pwd:          pwd,
		runner:       r,
		issueTracker: it,
		codeForge:    cf,
		capabilities: caps,
		factory:      f,
		merge:        merge,
		verdict:      verdict,
		terminated:   terminated,
		cleanup: func() {
			f.Cleanup()
			shadows.close()
			removeScratchMirrors()
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

// settler is the Settler the generic waves path (work and research dispatch)
// drives. It tests the concrete verdict pointer so a nil one never becomes a
// non-nil interface.
func (lc *launchContext) settler() settle.Settler {
	if lc.verdict != nil {
		return lc.verdict
	}
	return lc.merge
}

// hostSeedsMirror reports whether c's Box clones from a host-seeded mirror
// mounted at /repo (backend.ClonesFromMirror). Research needs it too, unless
// it is self-contained (issue #2439). The read-only decision is dispatch's own
// (effectiveBoxForgeAndIssueAccess), so launcher and Box agree per kind. A
// github or forgejo Box's per-Pass fetched mirror is a different arm of
// seedMirrorHook, gated on a forge.MirrorSeeder rather than the registry row.
func hostSeedsMirror(c config) bool {
	row, _ := backendkit.ByName(c.CodeForge)
	return backend.ClonesFromMirror(row.Descriptor, c.effectiveBoxForgeAndIssueAccess() == "read-only") && !c.selfContained
}

// seedAccumulationRepoIfHostSeeded makes the bare Accumulation repo (ADR 0033)
// from pwd's checkout at launch, holding an exclusive lock on it for the
// caller (issue #2441). Host-side landing needs the repo before any Box runs
// (issue #1726); each Pass then re-seeds the mirror through seedMirrorHook.
// Only a HostMediatedRemote forge has an Accumulation repo: a forge that merely
// seeds a mirror (git under read-only) lands on its remote.
func seedAccumulationRepoIfHostSeeded(c config, pwd string) (*local.AccumulationLock, error) {
	row, _ := backendkit.ByName(c.CodeForge)
	if !row.HostMediatedRemote || c.selfContained {
		return nil, nil
	}
	repoDir := c.hostRepoDir()
	lock, err := local.AcquireAccumulationLock(repoDir)
	if err != nil {
		return nil, err
	}
	if err := local.SeedAccumulationRepo(repoDir, pwd, c.BaseBranch); err != nil {
		_ = lock.Release()
		return nil, err
	}
	return lock, nil
}

// seedMirrorMu serialises every in-process Pass's seed of the one mirror,
// across parallel Boxes and across the work and research factories. It also
// guards scratchMirrors.
var seedMirrorMu sync.Mutex

// scratchMirrors are the per-process mirror dirs seedMirrorHook made for a
// forge with no HostRepoDir of its own; removeScratchMirrors deletes them.
var scratchMirrors []string

// removeScratchMirrors deletes every scratch mirror dir, on the launch
// context's cleanup.
func removeScratchMirrors() {
	seedMirrorMu.Lock()
	defer seedMirrorMu.Unlock()
	for _, d := range scratchMirrors {
		_ = os.RemoveAll(d)
	}
	scratchMirrors = nil
}

// newScratchMirrorDir makes a fresh dir under pwd's .spindrift for a mirror and
// returns the bare repo path inside it. It is not under /tmp: a podman machine
// on macOS shares $HOME with the VM, not /tmp, so only a path in the checkout
// is mountable. The caller holds seedMirrorMu.
func newScratchMirrorDir(pwd string) (string, error) {
	root := filepath.Join(pwd, ".spindrift")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	d, err := os.MkdirTemp(root, "mirror-*")
	if err != nil {
		return "", err
	}
	scratchMirrors = append(scratchMirrors, d)
	return filepath.Join(d, "repo.git"), nil
}

// seedMirrorHook is dispatch.Config.SeedMirror: it prepares the dir to mount
// read-only at /repo before each Pass, so a Box clones the base as it stands
// now rather than as of launch (issue #6896). Nil when the Box clones from its
// remote instead.
//
// Two arms. A host-seeded backend (local; git under read-only) re-seeds one
// shared mirror through the forge Strategy's SeedMirror: the Accumulation repo
// for a forge with a HostRepoDir, else a per-process scratch dir. It does not
// take the Accumulation lock, because bootstrap's flock stays held for the
// whole run and a second open file would conflict with it. Otherwise, a
// read-only Box (which has no forge credential to clone with) gets a fresh
// bare mirror the launcher fetches with its own credential through seeder
// (issue #6897). Read-write Boxes clone directly.
func seedMirrorHook(c config, pwd string, seeder forge.MirrorSeeder) func() (string, func(), error) {
	if hostSeedsMirror(c) {
		row, _ := backendkit.ByName(c.CodeForge)
		dir := c.hostRepoDir()
		return func() (string, func(), error) {
			seedMirrorMu.Lock()
			defer seedMirrorMu.Unlock()
			if dir == "" {
				d, err := newScratchMirrorDir(pwd)
				if err != nil {
					return "", nil, err
				}
				dir = d
			}
			return dir, func() {}, row.SeedMirror(backendShared(c), c.knobLookup, pwd, dir)
		}
	}
	// Effective access, not the adapter: a ReadOnlyBox kind under a read-write
	// knob still holds the read-write adapter, which also seeds.
	if c.selfContained || seeder == nil || c.effectiveBoxForgeAndIssueAccess() != "read-only" {
		return nil
	}
	return func() (string, func(), error) {
		// Under pwd, not /tmp: a podman machine on macOS shares only the
		// operator's tree, like the outbox (dispatch.OutboxDirFor).
		root := hostpaths.MirrorRoot(pwd)
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", nil, err
		}
		tmp, err := os.MkdirTemp(root, "pass-")
		if err != nil {
			return "", nil, err
		}
		dir := filepath.Join(tmp, "repo.git")
		if err := seeder.SeedMirror(dir); err != nil {
			_ = os.RemoveAll(tmp)
			return "", nil, fmt.Errorf("host-side fetch of the %s Target repo: %w", c.CodeForge, err)
		}
		return dir, func() { _ = os.RemoveAll(tmp) }, nil
	}
}

// researchLaunchStack builds the research-kind tracker, dispatch factory, and
// settle the Console wires in alongside lc's work-kind stack (issue #1708, ADR
// 0022). A second bootstrap() call would not merely repeat work: depending on
// config it would fail validation on registry credential env vars the first
// resolution already unset, fail on the run's own Accumulation-repo lock, or
// start a second GH_TOKEN refresh watcher and repeat the readiness check. The
// returned Factory owns its own driver-cache root, so the caller must call its
// Cleanup.
func researchLaunchStack(lc *launchContext) (forge.IssueTracker, *dispatch.Factory, *settle.ResearchSettle, error) {
	rc := applyDispatchKind(lc.config, dispatchkind.Research)
	it := newIssueTracker(rc)
	lw := localloop.Wire(localloopConfig(rc), it)
	f := newDispatchFactory(rc, lc.pwd, lc.runner, it, lw, lc.codeForge, lc.capabilities)

	// The SettleVerdict arm never reads its caps argument, so there is nothing
	// worth resolving.
	_, verdict, _, err := newSettlers(rc.kind().Settle, rc, it, lw, lc.codeForge, forge.Capabilities{})
	if err != nil {
		f.Cleanup()
		return nil, nil, nil, err
	}
	return it, f, verdict, nil
}
