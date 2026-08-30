package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
	"spindrift.dev/launcher/internal/ghapptoken"
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
// lifetime (issue #1027) gives ample slack for a minute-scale poll. Paired
// with ghAppRefreshInterval below: whichever process re-mints the file (an
// external CI minter, or bootstrap's own applyGHAppToken under local
// dispatch, issue #2867) re-mints well inside that ~1h margin, so a poll
// interval this much smaller than the re-mint cadence never observes a
// stale token.
const ghTokenRefreshInterval = 60 * time.Second

// ghAppRefreshInterval is how often applyGHAppToken's ghapptoken.Watch loop
// re-mints a fresh GitHub App installation token under local dispatch (issue
// #2867). Matches .github/actions/gh-token-refresher's own re-mint cadence
// (`sleep_secs=2700 # 45m`) — comfortably inside an installation token's ~1h
// lifetime (issue #1027), and well above ghTokenRefreshInterval's 60s poll
// so the poller reliably observes each re-mint promptly rather than missing
// it between polls.
const ghAppRefreshInterval = 45 * time.Minute

// ghAppTokenWatch mints a GitHub App installation token and starts its
// periodic re-mint loop; production wiring for ghapptoken.Watch. Swapped in
// tests to avoid a real network call — the same seam pattern as
// readonly_token_gate.go's ghTokenIntrospector var in this package.
var ghAppTokenWatch = ghapptoken.Watch

// applyGHAppToken mints an initial GitHub App installation token via
// ghAppTokenWatch when c.ghAppID is set (issue #2867), populating c.ghToken
// and the GH_TOKEN env var synchronously so validate(c)'s existing
// "gh-token" required-knob check (main.go) is satisfied without any ambient
// GH_TOKEN, and other code that reads c.ghToken directly (e.g.
// readonly_token_gate.go's boxToken == c.ghToken comparison) sees the
// current token too. Returns the token file ghAppTokenWatch's background
// loop is periodically rewriting; the caller (bootstrap) wires that file
// into c.ghTokenRefreshFile itself, and only after validate(c) has already
// run — so validateGHAppConfig's own mutual-exclusion check (called first,
// here, against the not-yet-mutated c.ghTokenRefreshFile) and
// launcherCrossKnobChecks' "gh-app-config" row (run inside validate(c))
// always see the operator's own raw GH_TOKEN_REFRESH_FILE, never the value
// this function's caller sets afterward.
//
// A no-op — ("", nil) — when c.ghAppID is unset, the pre-issue-#2867
// default; c is left untouched in that case. c is taken by pointer since a
// successful mint mutates c.ghToken in place.
func applyGHAppToken(c *config) (tokenFile string, err error) {
	if err := validateGHAppConfig(c.ghAppID, c.ghAppPrivateKeyFile, c.ghAppInstallationID, c.ghTokenRefreshFile); err != nil {
		return "", err
	}
	if c.ghAppID == "" {
		return "", nil
	}

	// A predictable path (e.g. PID-derived) lets an attacker on a shared host
	// pre-create the sibling ".tmp" path writeTokenFileAtomic renames from as
	// a symlink; os.WriteFile follows it and hands the attacker the token.
	// MkdirTemp's random suffix and 0700 mode close that off, matching the
	// repo's own randomized-temp idiom (dispatch/box.go's
	// "spindrift-registry-proxy-*").
	tokenDir, err := os.MkdirTemp("", "spindrift-gh-app-token-*")
	if err != nil {
		return "", err
	}
	// Every return below this point returns a non-nil err on failure (the
	// named result), so a single deferred cleanup covers every such path --
	// including the os.Setenv one two lines below the mint call, which used
	// to strand tokenDir (and ghAppTokenWatch's still-running re-mint
	// goroutine) because the old code only removed it on the mint call's own
	// error path.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tokenDir)
		}
	}()

	tokenFile = filepath.Join(tokenDir, "token")
	token, err := ghAppTokenWatch(context.Background(), ghapptoken.Config{
		AppID:          c.ghAppID,
		PrivateKeyFile: c.ghAppPrivateKeyFile,
		InstallationID: c.ghAppInstallationID,
	}, tokenFile, ghAppRefreshInterval, nil)
	if err != nil {
		return "", fmt.Errorf("mint GitHub App installation token: %w", err)
	}

	c.ghToken = token
	if err := os.Setenv("GH_TOKEN", token); err != nil {
		return "", err
	}
	return tokenFile, nil
}

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
// carried by which subcommand launched. selfContained (issue #2202,
// --self-contained) is the research kind's no-repo sub-mode: set only by the
// research subcommand handler, false everywhere else. No step here can fail
// after the dispatch factory is constructed, so an error return never
// carries a launch context that still needs cleanup. The one thing that can
// still be outstanding on an early error return is the accumulation lock
// (issue #2441): it's acquired well before the factory exists, so a bare
// `return nil, err` from any step between acquisition and launchContext's
// construction would otherwise leak a held lock for the rest of the
// process. A single deferred release, registered right after acquisition,
// covers that whole window instead of relying on every such return site to
// remember it.
func bootstrap(ensureReady bool, kind string, selfContained bool) (lc *launchContext, err error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	c := applyDispatchKind(loadConfig(), kind)
	c.selfContained = selfContained
	// Mint a GitHub App installation token (issue #2867) before validate(c)
	// runs, not after: a local-dispatch operator authenticating purely via
	// GH_APP_ID/GH_APP_PRIVATE_KEY_FILE/GH_APP_INSTALLATION_ID sets no
	// ambient GH_TOKEN at all, and validate(c)'s "gh-token" required-knob
	// check (main.go) needs c.ghToken populated by the time it runs. A
	// partial App config, or a full one combined with an explicit
	// GH_TOKEN_REFRESH_FILE, is rejected by applyGHAppToken's own
	// validateGHAppConfig call before any mint is attempted, so a confusing
	// "gh-token required" error can never mask the real, more specific
	// misconfiguration. c.ghTokenRefreshFile itself is deliberately left
	// unset here — wired in below, only once validate(c) has already run —
	// so validate(c)'s own "gh-app-config" row (checks.go) still sees the
	// operator's raw, not-yet-mutated GH_TOKEN_REFRESH_FILE.
	ghAppTokenFile, err := applyGHAppToken(&c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}
	if ghAppTokenFile != "" {
		// A live installation token now sits on disk. Every remaining early
		// `return nil, err` below -- validate(c) immediately following, the
		// registry-proxy credential resolve, MigrateLegacyLogDir, the
		// Accumulation seed, EnsureReady/IsReady, and the four read-only
		// gates -- would otherwise strand it there indefinitely, since
		// nothing else on that failure path removes it (mirrors the
		// accumLock defer below). A no-op once the final `return
		// &launchContext{...}, nil` sets err back to nil: from that point,
		// launchContext.cleanup (below) becomes the token dir's sole owner.
		defer func() {
			if err != nil {
				_ = os.RemoveAll(filepath.Dir(ghAppTokenFile))
			}
		}()
	}
	if err := validate(c); err != nil {
		return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
	}
	if ghAppTokenFile != "" {
		c.ghTokenRefreshFile = ghAppTokenFile
	}
	if c.registryProxyUpstreamURL != "" {
		cred, err := resolveRegistryProxyCredential(c.registryProxyCredentialFile, c.registryProxyCredentialEnv)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errConfigInvalid, err)
		}
		c.registryProxyCredential = cred
	}
	// One-time relocation (issue #2138): fold any legacy top-level logs/
	// left by an earlier spindrift into the new .spindrift/logs before any
	// host-side site reads or creates a log path this run.
	if err := dispatch.MigrateLegacyLogDir(pwd); err != nil {
		return nil, err
	}
	accumLock, err := seedAccumulationRepoIfLocal(c, pwd)
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

	// A run that outlives GH_TOKEN_REFRESH_FILE's minter's token lifetime
	// (issue #1027) would otherwise 401 at the terminal gh calls (merge,
	// label edits, final comment): keep GH_TOKEN current for the rest of
	// the process by polling the file its minter rewrites in place. That
	// minter is either external (CI's gh-token-refresher action, the
	// pre-issue-#2867 case) or, when GH_APP_ID is set, applyGHAppToken's own
	// ghAppTokenWatch goroutine above (issue #2867, local dispatch) — this
	// poll loop is the single consumer either way, per ghapptoken.Watch's
	// own doc comment. No-op when unset (true by default, and also whenever
	// applyGHAppToken was itself a no-op above) — GH_TOKEN then stays
	// whatever the ambient environment set it to for the whole run, as
	// before.
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

	it := newIssueTracker(c)
	cf := newCodeForge(c, local.SanitizedParent{}, it)
	if err := checkReadOnlyCapabilityGate(c); err != nil {
		return nil, err
	}
	if err := checkNetworkModeRuntimeGate(c); err != nil {
		return nil, err
	}
	if _, err := checkReadOnlyTokenGate(c, ghTokenIntrospector, os.Stdout); err != nil {
		return nil, err
	}
	if _, err := checkReadOnlyForgejoTokenGate(c, os.Stdout); err != nil {
		return nil, err
	}
	lw := localloop.Wire(localloopConfig(c), it)
	f := newDispatchFactory(c, pwd, r, it, lw, cf)
	s := newSettle(c, it, lw, cf)

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
			// applyGHAppToken's minted token directory (issue #2867):
			// nothing else on this process's exit path removes it, unlike
			// the CI equivalent's runner-temp file ("removed when the job
			// ends", gh-token-refresher/action.yml), so a local dispatch run
			// would otherwise leave a live installation token readable on
			// disk indefinitely. Only ever removes a directory this process
			// itself created via MkdirTemp above -- never an
			// externally-supplied GH_TOKEN_REFRESH_FILE, which
			// ghAppTokenFile is empty for.
			if ghAppTokenFile != "" {
				_ = os.RemoveAll(filepath.Dir(ghAppTokenFile))
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
	s := newSettle(rc, it, lw, lc.codeForge)
	return it, f, s
}
