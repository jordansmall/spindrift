package runner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// execCommand builds the *exec.Cmd for hardcoded-binary orchestration (nix,
// bwrap) that has no configurable CLI field to intercept. Tests swap this
// package-level seam to substitute a fake binary; production always uses the
// standard library's exec.Command unmodified.
var execCommand = exec.Command

// statResolvConf backs the "does the host have /etc/resolv.conf to bind"
// check in buildArgs. Tests swap this seam so the resolv.conf-bind
// assertions don't depend on whether the CI runner's own filesystem happens
// to have /etc/resolv.conf (it doesn't inside some nix build sandboxes).
var statResolvConf = func() error {
	_, err := os.Stat("/etc/resolv.conf")
	return err
}

// statHostNixDB backs the "does hostNixDBPath exist" preflight check in
// snapshotStoreDB. Tests swap this package-level seam so the missing-host-db
// assertion doesn't depend on whether the machine running `go test` happens
// to have a real /nix/var/nix/db/db.sqlite.
var statHostNixDB = func() error {
	_, err := os.Stat(hostNixDBPath)
	return err
}

// lookPath backs the "is prlimit on PATH" check in execTarget. Tests swap
// this seam to fake prlimit's presence/absence deterministically, since the
// real answer depends on the host's own PATH (e.g. util-linux's prlimit is
// absent from this repo's own nix develop devShell).
var lookPath = exec.LookPath

// readSelfCgroup returns the calling (launcher) process's own cgroup v2
// path, parsed from /proc/self/cgroup's unified-hierarchy line ("0::<path>").
// Tests swap this seam to fake an ancestor path directly rather than writing
// a fake /proc/self/cgroup, which isn't writable in a test sandbox anyway.
var readSelfCgroup = func() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, nil
		}
	}
	return "", fmt.Errorf("no unified cgroup v2 line (0::...) in /proc/self/cgroup")
}

// cgroupFSRoot is the host's cgroup v2 filesystem mountpoint. Tests
// reassign this to a t.TempDir() to fake a writable delegated subtree
// without touching the real host cgroup filesystem.
var cgroupFSRoot = "/sys/fs/cgroup"

// homeAgentStagingDir is the fixed in-box path bwrap ro-binds agentFiles'
// baked /home/agent subtree onto (issue #2843). It must be a fresh
// top-level path, not nested under /agent: /agent is already bound
// read-only by the time this mount is added, and bwrap cannot create a
// new mountpoint inside an existing read-only bind.
const homeAgentStagingDir = "/home-agent-staged"

// bwrapSecrets is the set of box.Env keys whose values must not appear on the
// bwrap command line. They are delivered via the process environment instead
// so that ps/proc cannot expose them to other local users.
var bwrapSecrets = map[string]bool{
	"GH_TOKEN":                true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":       true,
	"OPENCODE_AUTH_CONTENT":   true,
}

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady delegates to IsReady rather than realizing anything itself —
// store closures are realized by the build command.
type bwrapAdapter struct {
	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	passwdFile    string // baked nix store path for /etc/passwd
	groupFile     string // baked nix store path for /etc/group
	bakedPrefetch string // baked prefetch snippet fed to the entrypoint
	promptDir     string // optional host path to bind-mount over /agent/prompts
	skillsDir     string // optional host path to bind-mount over operatorSkillsDir (issue #2489)
	// driverSessionCacheDir is the in-box bind target for the Driver's
	// session-state dir (Driver declaration, ADR 0009); empty when the
	// selected Driver declares no session-state dir, in which case
	// box.DriverCacheDir is never bound regardless of its value.
	driverSessionCacheDir string
	// nixConfigFile is the baked nix store path for /etc/nix/nix.conf (ADR
	// 0042); empty when the Consumer's nixInBox knob is off, which gates both
	// this mount and nixVarSnapshotDir's mount together (nix isn't even on
	// PATH in that case). nixVarSnapshotDir is the host-side directory
	// standing in for /nix/var's overlay lower (see nixVarSnapshotDir below);
	// it is always computed, so its presence on disk — not its own emptiness
	// — is what IsReady/EnsureReady actually check when nixConfigFile is set,
	// surfacing a missing snapshot as an actionable launcher-level error
	// before bwrap ever runs, rather than a raw bwrap mount failure.
	nixConfigFile     string
	nixVarSnapshotDir string
	// nixStoreWritable gates whether /nix/store itself gets the same
	// overlay treatment as /nix/var above (ADR 0042): an ephemeral tmpfs
	// upper over the host's real store, so paths built/substituted in the
	// Box land in the upper and vanish with the sandbox instead of ever
	// touching host disk. AND-gated with nixConfigFile in buildArgs — true
	// here alone does nothing when nixConfigFile is empty (nixInBox off),
	// since nix isn't on PATH in the Box in that case either.
	nixStoreWritable bool
	// hostMediatedRemote/outboxRelayCapable/accumulationRepoDir/
	// boxForgeAndIssueAccess gate the /repo and /outbox mounts (ADR 0033,
	// issue #1697; issue #1918); see MountParams.
	hostMediatedRemote     bool
	accumulationRepoDir    string
	outboxRelayCapable     bool
	boxForgeAndIssueAccess string
	// hostMediatedIssueTracker and localIssuesDir gate the read-only /issues
	// mount (ADR 0032); see MountParams.
	hostMediatedIssueTracker bool
	localIssuesDir           string
	unshareNet               bool   // raw BWRAP_UNSHARE_NET knob; forces network isolation on (redundant with the new isolate-by-default, kept for defense in depth — see buildArgs)
	networkMode              string // NETWORK_MODE knob; every value except the "host" opt-out (issue #2666) isolates from the host netns. "no-host-loopback" never legitimately reaches bwrap — nix eval-rejects it for a valid Consumer flake (lib/mkHarness.nix networkModeCoherenceOk).
	// pidsLimit is the PIDS_LIMIT knob (empty disables it, matching the OCI
	// adapter's own convention — oci.go's pidsLimit field). bwrap itself
	// imposes no process-count cap; execTarget wraps the whole exec target
	// with the external `prlimit --nproc` CLI tool to enforce one (ADR
	// 0042), rather than a raw syscall.Setrlimit in the launcher's own
	// process, which would be process-wide (shared by the whole thread
	// group, not scoped to one fork) and race against concurrent goroutine
	// Box launches under MAX_PARALLEL.
	pidsLimit string
	// memoryLimit is the MEMORY_LIMIT knob (empty disables it, same
	// convention as pidsLimit above and the OCI adapter's own memoryLimit
	// field). It backs the per-Box cgroup's memory.max control file (ADR
	// 0042, provisionCgroup) rather than any bwrap/prlimit flag — bwrap has
	// no per-process memory cap of its own, and memory.max needs a raw
	// byte count (memoryLimitToBytes), unlike podman's own --memory flag
	// which accepts the unit-suffixed string as-is.
	memoryLimit string

	// syscallFilterPath is the baked nix store path to the compiled BPF
	// syscall filter (issue #2670). When non-empty, buildArgs appends
	// "--seccomp <seccompFilterFD>" and Run opens the file and attaches it
	// via cmd.ExtraFiles so bwrap can read it. A failure to open it at Run
	// time is treated as a hardening gap, not a safety blocker (matches ADR
	// 0042's degrade-don't-lie posture for missing prlimit/cgroup) -- Run
	// warns and proceeds without the filter rather than refusing to launch
	// the Box.
	syscallFilterPath string

	// mu guards running, the box-name -> live process map Kill (issue #649)
	// consults — bwrap sandboxes are unnamed child processes with no
	// persistent daemon IsRunning/Reap can query by name, so Run tracks its
	// own process handle here for the one caller (Terminate) that needs to
	// reach a live one from outside Run's own goroutine.
	mu      sync.Mutex
	running map[string]*os.Process
}

// nixVarSnapshotDir is the host-side directory that stands in for /nix/var
// inside a bwrap Box's overlay lower (ADR 0042): the build command writes an
// agent-owned, VACUUMed snapshot of the host's own /nix/var/nix/db/db.sqlite
// to <dir>/nix/db/db.sqlite (bwrapBuildAdapter.EnsureReady, via
// snapshotStoreDB), and the run adapter overlays this whole directory onto
// /nix/var so the Box's nix trusts host-present store paths without
// re-substituting them. Shared, package-level, between the run adapter (this
// file, to mount) and the build adapter (snapshotStoreDB, to write) so the
// path convention lives in exactly one place.
//
// generation (see closureGeneration) nests the snapshot one directory level
// deeper, scoped to the agent-closure it was taken against (issue #2680), so
// two different closures get two different, coexisting directories instead
// of colliding on one shared path. An empty generation
// (no closure known, e.g. a bare test-constructed adapter) falls back to the
// pre-#2680 flat path — filepath.Join drops empty components, so this is the
// same call either way.
func nixVarSnapshotDir(pwd, generation string) string {
	return filepath.Join(nixVarSnapshotRoot(pwd), generation)
}

// nixVarSnapshotRoot is the directory nixVarSnapshotDir nests generation
// subdirs under -- the sweep root reclaimStaleSnapshots reads/RemoveAlls
// entries of. Factored out so callers that need the root (bwrapBuildAdapter,
// to reclaim stale generations) derive it the same way nixVarSnapshotDir
// does, from pwd directly, rather than by filepath.Dir/Base surgery on an
// already-joined nixVarSnapshotDir path -- surgery that misidentifies the
// root when generation is "" (issue #2680 review finding: the flat/legacy
// path IS the snapshot dir, not a subdir of it, so its parent is actually
// .spindrift, home to unrelated siblings a sweep must never touch).
func nixVarSnapshotRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-var-snapshot")
}

// closureGeneration derives the generation subdir nixVarSnapshotDir nests
// under from a bwrap-runtime Config.ImageTag (the bundled agent-closure's
// loaded nix store path, e.g. /nix/store/<hash>-agent-closure — see
// Config.ImageTag's doc comment). Empty imageTag (no closure known) returns
// "" rather than filepath.Base("")'s "." — "." is not a safe/intended
// directory name here, and "" is what makes nixVarSnapshotDir's empty-
// generation fallback kick in.
//
// imageTag is read from an environment variable / input-document artifact
// (getenvArtifact, cmd/launcher/inputdoc.go) that an untrusted source can
// influence, and the result becomes a path component threaded into a
// directory reclaimStaleSnapshots later os.RemoveAll's. filepath.Base alone
// doesn't guard against a hostile imageTag: filepath.Base("..") is "..",
// filepath.Base(".") is ".", filepath.Base("/") is the separator itself —
// any of those steer the eventual RemoveAll outside the intended snapshot
// root. Reject them the same way empty is rejected, falling back to the
// flat-path "".
func closureGeneration(imageTag string) string {
	if imageTag == "" {
		return ""
	}
	gen := filepath.Base(imageTag)
	if gen == "." || gen == ".." || gen == string(filepath.Separator) {
		return ""
	}
	return gen
}

// snapshotLockPath is the one place the "<generation dir>.lock" naming
// convention is spelled: a sibling of the generation dir itself, never a
// file inside it, since the generation dir is what buildArgs --overlay-src
// binds into the sandbox and a lock file living inside it would risk being
// swept up by that mount. Shared by Run (to acquire a shared lock for the
// life of the sandboxed process) and reclaimStaleSnapshots (to probe for an
// exclusive lock, i.e. no live Box holding the shared one).
func snapshotLockPath(dir string) string {
	return dir + ".lock"
}

// lockSnapshotShared opens (creating if needed) and takes a blocking shared
// advisory flock on dir's snapshotLockPath. The lock file is NOT guaranteed
// to stay the same inode for a generation's whole existence: sweepOrphanedLock
// (reclaimStaleSnapshots) removes it once a later pass's os.ReadDir no longer
// sees a matching generation dir -- deleting and recreating it without the
// guard below would let two callers each believe they hold "the" lock while
// actually flocking two different inodes that happen to share a name (issue
// #2680 review finding).
//
// EnsureReady, unlike Run, calls this before the generation dir exists --
// sweepOrphanedLock (a concurrent build's own reclaim pass) can therefore
// legitimately see this lock file with no matching generation dir yet and
// classify it as orphaned. If sweepOrphanedLock's LOCK_EX probe lands
// between this func's os.OpenFile and its Flock(LOCK_SH), it can win the
// exclusive lock on the not-yet-locked fd and os.Remove the path out from
// under it -- Flock still succeeds afterward (flock locks the open file
// description, not the path), so a naive caller would believe it holds a
// live lock on a path that actually resolves to nothing, letting a later
// pass O_CREATE a fresh inode there and win its own lock uncontested. So
// after locking, verify the fd still identifies whatever is currently at
// the path (fstat vs. a fresh os.Stat via os.SameFile) and retry against a
// freshly opened fd if not, bounded so a persistent adversary gets a clear
// error instead of an infinite loop.
func lockSnapshotShared(dir string) (*os.File, error) {
	const maxAttempts = 100
	path := snapshotLockPath(dir)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
			lf.Close()
			return nil, err
		}
		var fdStat, pathStat os.FileInfo
		if fdStat, err = lf.Stat(); err == nil {
			pathStat, err = os.Stat(path)
		}
		if err == nil && os.SameFile(fdStat, pathStat) {
			return lf, nil
		}
		// The path was swapped or unlinked out from under lf between open
		// and lock (e.g. by a concurrent sweepOrphanedLock) -- lf's lock is
		// now worthless, since it protects an inode nothing resolves to
		// anymore. Drop it and retry against whatever is at the path now.
		unlockSnapshot(lf)
	}
	return nil, fmt.Errorf("lockSnapshotShared: %s kept changing identity after locking across %d attempts", path, maxAttempts)
}

// unlockSnapshot releases lf's flock and closes it; lf == nil (lock
// acquisition itself failed, or was never attempted) is a no-op so callers
// can defer/call this unconditionally.
func unlockSnapshot(lf *os.File) {
	if lf == nil {
		return
	}
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	lf.Close()
}

// NewBwrap constructs a bwrap adapter for the run command from cfg and pwd
// (the launcher's own working directory, mirroring NewOCI's pwd parameter).
// EnsureReady delegates to IsReady, which checks readiness rather than
// realizing anything; call NewBwrapBuild for the build command. By
// default (any cfg.NetworkMode except the "host" opt-out, issue #2666) the
// resulting adapter isolates the sandbox into its own network namespace,
// with egress restored via a hardened pasta helper (ADR 0042) — podman-
// rootless parity. cfg.NetworkMode="host" and the raw cfg.BwrapUnshareNet
// knob are documented separately in buildArgs.
func NewBwrap(cfg Config, pwd string) Runner {
	return &bwrapAdapter{
		agentFiles:               cfg.AgentFiles,
		agentEnv:                 cfg.AgentEnv,
		passwdFile:               cfg.PasswdFile,
		groupFile:                cfg.GroupFile,
		bakedPrefetch:            cfg.BakedPrefetch,
		promptDir:                cfg.PromptDir,
		skillsDir:                cfg.SkillsDir,
		driverSessionCacheDir:    cfg.DriverSessionCacheDir,
		nixConfigFile:            cfg.NixConfigFile,
		nixVarSnapshotDir:        nixVarSnapshotDir(pwd, closureGeneration(cfg.ImageTag)),
		nixStoreWritable:         cfg.NixStoreWritable,
		hostMediatedRemote:       cfg.HostMediatedRemote,
		accumulationRepoDir:      cfg.AccumulationRepoDir,
		outboxRelayCapable:       cfg.OutboxRelayCapable,
		boxForgeAndIssueAccess:   cfg.BoxForgeAndIssueAccess,
		hostMediatedIssueTracker: cfg.HostMediatedIssueTracker,
		localIssuesDir:           cfg.LocalIssuesDir,
		unshareNet:               cfg.BwrapUnshareNet,
		networkMode:              cfg.NetworkMode,
		pidsLimit:                cfg.PidsLimit,
		memoryLimit:              cfg.MemoryLimit,
		syscallFilterPath:        cfg.SyscallFilterPath,
	}
}

// EnsureReady does not build anything for bwrap run: store closures are
// realized by `launcher build` (bwrapBuildAdapter.EnsureReady) before `run`
// is invoked. It delegates to IsReady so the same actionable
// snapshot-missing error fires on the default run/dispatch path too, not
// only on `--no-build` (bootstrap only calls IsReady there) — issue #2664.
func (a *bwrapAdapter) EnsureReady() error { return a.IsReady() }

// IsReady checks that the nix-in-box snapshot `launcher build` writes is
// present, when the Consumer's nixInBox knob is on (ADR 0042). Store
// closures otherwise (agentFiles/agentEnv/passwd/group) are realized
// out-of-band by `spindrift build` too, but buildArgs never conditions a
// mount on their absence the way it does nixVarSnapshotDir, so this check is
// scoped to that one gap (issue #2664): without it, a missing snapshot only
// surfaces as a raw bwrap overlay mount failure instead of an actionable
// launcher-level error.
func (a *bwrapAdapter) IsReady() error {
	if a.nixConfigFile == "" {
		return nil
	}
	// Check the db.sqlite file itself, not just its parent dir: snapshotStoreDB
	// MkdirAlls <nixVarSnapshotDir>/nix/db before it writes db.sqlite, so a
	// dir-only check would report ready on a dir left behind by a failed
	// snapshot (issue #2664).
	dbPath := filepath.Join(a.nixVarSnapshotDir, "nix", "db", "db.sqlite")
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("nix store snapshot not found at %s; run \"launcher build\" first", dbPath)
		}
		return fmt.Errorf("checking nix store snapshot at %s: %w", dbPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("nix store snapshot at %s is a directory, not a file; run \"launcher build\" first", dbPath)
	}
	return nil
}

// mountSpecs computes the host-to-box mounts that apply for box, shared with
// the OCI adapter (buildMountSpecs); only the rendering below differs.
func (a *bwrapAdapter) mountSpecs(box Box) []MountSpec {
	return buildMountSpecs(MountParams{
		PromptDir:                a.promptDir,
		SkillsDir:                a.skillsDir,
		DriverSessionCacheDir:    a.driverSessionCacheDir,
		HostMediatedRemote:       a.hostMediatedRemote,
		AccumulationRepoDir:      a.accumulationRepoDir,
		OutboxRelayCapable:       a.outboxRelayCapable,
		BoxForgeAndIssueAccess:   a.boxForgeAndIssueAccess,
		HostMediatedIssueTracker: a.hostMediatedIssueTracker,
		LocalIssuesDir:           a.localIssuesDir,
	}, box)
}

// isolateNet is the effective "cut off the host netns" decision (issue
// #2666, ADR 0042): every NetworkMode value except the explicit "host"
// opt-out isolates by default, including the Go zero value and
// "no-host-loopback" (nix eval-rejects the latter reaching bwrap in
// production; main.go's checkNetworkModeRuntimeGate backstops it). The raw
// BwrapUnshareNet knob can only ever force isolation on, already the
// default outcome, but is kept for defense in depth. See
// TestBwrapArgs_NetworkModeNoHostLoopbackDefaultsToIsolate.
func (a *bwrapAdapter) isolateNet() bool {
	return a.unshareNet || a.networkMode != NetworkModeHost
}

// pastaPath reports whether the isolated network namespace gets restored
// egress via a pasta helper. networkMode="none" is the deliberate exception:
// fully offline, bare --unshare-net, no helper, no egress at all (a Driver
// can't reach its Provider under it, documented elsewhere).
func (a *bwrapAdapter) pastaPath() bool {
	return a.isolateNet() && a.networkMode != NetworkModeNone
}

// buildArgs constructs the bwrap command-line arguments for the given box.
// The /etc/passwd and /etc/group binds source a.passwdFile/a.groupFile, baked
// nix store paths rather than a runner-written temp-dir copy (issue #2663).
// etcDir is only still needed for the synthesised /etc/resolv.conf (pastaPath
// only, see below) -- passwd/group no longer live there. Secret env vars
// (GH_TOKEN, auth tokens) are intentionally excluded from argv; they reach
// the sandbox via inherited process environment (no --clearenv). Pasta
// itself is never part of this return value -- see execTarget, which wraps
// bwrap's own argv with pasta as the outer process when pastaPath applies.
func (a *bwrapAdapter) buildArgs(etcDir string, box Box) []string {
	isolateNet := a.isolateNet()
	var args []string
	if a.nixConfigFile != "" && a.nixStoreWritable {
		args = append(args, "--overlay-src", "/nix/store", "--tmp-overlay", "/nix/store")
	} else {
		args = append(args, "--ro-bind", "/nix/store", "/nix/store")
	}
	args = append(args,
		"--tmpfs", "/tmp",
		"--tmpfs", "/work",
		"--tmpfs", "/home/agent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--dir", "/etc",
		"--ro-bind", a.passwdFile, "/etc/passwd",
		"--ro-bind", a.groupFile, "/etc/group",
	)
	// nixConfigFile empty means the Consumer's nixInBox knob is off (nix isn't
	// even on PATH in that case), so both nix-related mounts below are
	// skipped together rather than independently. When set: --overlay-src +
	// --tmp-overlay gives bwrap a read-only lower (the VACUUMed
	// /nix/var/nix/db/db.sqlite snapshot written by `launcher build`, ADR
	// 0042) with an ephemeral tmpfs upper, so nix's own writes inside the Box
	// (gcroots, profiles, WAL files) land in the upper and vanish with the
	// sandbox rather than touching host disk. The store itself (/nix/store,
	// rendered at the top of this function) is gated on nixConfigFile alone
	// plus one further AND with nixStoreWritable, not nixConfigFile alone
	// like the mounts below: with nixStoreWritable false (the default) it
	// stays a plain read-only bind even when nixConfigFile is set, and only
	// becomes an ephemeral tmpfs overlay — new store paths built/substituted
	// in the Box land in the upper and vanish on exit, never touching the
	// host's real store — when both are true.
	if a.nixConfigFile != "" {
		args = append(args, "--ro-bind", a.nixConfigFile, "/etc/nix/nix.conf")
		args = append(args, "--overlay-src", a.nixVarSnapshotDir, "--tmp-overlay", "/nix/var")
	}
	if !isolateNet {
		if err := statResolvConf(); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	} else if a.pastaPath() {
		// Nothing writes /etc/resolv.conf inside the guest otherwise (unlike
		// the OCI runner, where podman supplies its own) -- Run writes this
		// file pointed at pastaDNSForwardAddr before invoking bwrap.
		args = append(args, "--ro-bind", filepath.Join(etcDir, "resolv.conf"), "/etc/resolv.conf")
	}
	args = append(args, "--ro-bind", a.agentFiles+"/agent", "/agent")
	// The real /home/agent above is a fresh writable tmpfs, so baked content
	// (Claude hooks, settings.json, opencode agent files) can't be ro-bound
	// there directly; stage it read-only at a fresh top-level path instead.
	// It cannot nest under /agent: the --ro-bind above already bound /agent
	// read-only, and bwrap processes --ro-bind args in argv order, so it
	// cannot fabricate a new mountpoint inside a bind already made read-only
	// (issue #2843). entrypoint.sh copies the staged content into the
	// writable /home/agent at startup.
	args = append(args, "--ro-bind", a.agentFiles+"/home/agent", homeAgentStagingDir)
	// Mount decisions (gates, existence guards, operator messages) are
	// computed once in buildMountSpecs, shared with the OCI adapter; bwrap
	// only renders each spec into its own bind syntax. The driver-cache spec
	// (issue #427), scoped to the Driver's declared session-cache dir rather
	// than its parent so it can never shadow a sibling skills bind regardless
	// of order, and the CODE_FORGE=local outbox spec (ADR 0033, issue #1697)
	// are the only writable mounts buildMountSpecs ever produces.
	for _, m := range a.mountSpecs(box) {
		if m.Message != "" {
			fmt.Print(m.Message)
		}
		if !m.ReadOnly {
			// --dir creates the parent in the tmpfs as the sandbox user (uid
			// 1000), preventing bwrap from auto-fabricating it as root when
			// it processes the bind target (issue #447).
			args = append(args, "--dir", filepath.Dir(m.Target))
			args = append(args, "--bind", m.Source, m.Target)
			continue
		}
		args = append(args, "--ro-bind", m.Source, m.Target)
	}
	// --clearenv is intentionally absent: secrets (GH_TOKEN, auth tokens) reach
	// the sandbox via resolvedRunEnv(box.Env) below -- the bwrapSecrets subset
	// of the schema-driven box.Env -- which Run sets as cmd.Env and bwrap
	// inherits without --clearenv. Values on argv are visible in ps/proc, so
	// secrets must not appear there.
	args = append(args,
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", a.agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", a.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", a.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.bakedPrefetch,
	)
	for k, v := range box.Env {
		if !bwrapSecrets[k] {
			args = append(args, "--setenv", k, v)
		}
	}
	// bwrap is PID 1 inside its own unshared PID namespace, so
	// --die-with-parent's PR_SET_PDEATHSIG kills the whole sandbox when
	// bwrap's own immediate OS parent dies (issue #2669) -- the launcher
	// itself on the direct-exec chain, but only pasta on the pasta chain
	// (see setDeathSignal's call-site comment in Run for that gap).
	unshareFlags := []string{"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent"}
	// bwrap only unshares net itself for the fully offline networkMode=none
	// case. Every other isolating mode leaves this to pasta: pasta's own
	// documented behavior is to create and configure the fresh network
	// namespace (tap device, routes) for its COMMAND argument, then exec that
	// command inside the namespace it just built -- bwrap must inherit that
	// already-configured namespace via execTarget's pasta-as-outer-process
	// composition, not re-unshare a second, empty one on top of it (issue
	// #2666).
	if isolateNet && !a.pastaPath() {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	// --seccomp only ever names seccompFilterFD, the one fd Run's
	// cmd.ExtraFiles ever attaches (issue #2670); empty syscallFilterPath
	// (e.g. before the nix threading in a later slice populates it) skips
	// the flag entirely.
	if a.syscallFilterPath != "" {
		args = append(args, "--seccomp", strconv.Itoa(seccompFilterFD))
	}
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// execTarget computes the top-level host-exec'd program and argv for box's
// bwrap invocation. When pastaPath applies, pasta must be the outer process
// (see buildArgs' unshare-net comment for why); otherwise bwrap itself is the
// top-level program. When a.pidsLimit is set AND prlimit is found on PATH,
// the result of that decision is wrapped one level further out with
// `prlimit --nproc` (ADR 0042) — outside pasta, never between pasta and
// bwrap, so the process-count cap governs the whole
// pasta-plus-bwrap-plus-entrypoint tree pasta's own fork produces, not just
// the bwrap leaf. prlimit is an external util-linux CLI tool, not guaranteed
// present on every host (e.g. absent from this repo's own nix develop
// devShell) — matching provisionCgroup's degrade-don't-lie posture (issue
// #2668), a missing prlimit warns and proceeds unwrapped rather than
// crashing the Box launch over an unavailable resource-containment nicety.
func (a *bwrapAdapter) execTarget(etcDir string, box Box) (string, []string) {
	bwrapArgs := a.buildArgs(etcDir, box)
	var program string
	var args []string
	if !a.pastaPath() {
		program, args = "bwrap", bwrapArgs
	} else {
		pastaArgs := append([]string{}, pastaHardenedFlags...)
		pastaArgs = append(pastaArgs, "--dns-forward", pastaDNSForwardAddr,
			// -f/--foreground is load-bearing, not cosmetic: pasta's documented
			// default is to fork into the background and detach once the
			// namespace is set up. Without it, Go's cmd.Start()/cmd.Wait() would
			// track pasta's own short-lived detaching parent instead of the real
			// bwrap+entrypoint child, breaking exit-code propagation, stdout/
			// stderr capture, and the Kill()/Terminate() process map (a.running).
			"-f", "--", "bwrap")
		pastaArgs = append(pastaArgs, bwrapArgs...)
		program, args = "pasta", pastaArgs
	}
	if a.pidsLimit == "" {
		return program, args
	}
	if _, err := lookPath("prlimit"); err != nil {
		fmt.Printf("==> bwrap runner: warning: prlimit not found on PATH (%v); running box %q without process-count resource containment\n", err, box.Name)
		return program, args
	}
	prlimitArgs := append([]string{"--nproc=" + a.pidsLimit, "--", program}, args...)
	return "prlimit", prlimitArgs
}

// pastaDNSForwardAddr is pasta's own documented default IPv4 gateway address
// when it creates a namespace with no host default route visible (always
// true here, since pasta always creates a brand-new, empty netns in this
// "run given command" unshare mode) -- see pasta(1) NOTES ("Default gateways
// will be assigned as the link-local address 169.254.2.2 for IPv4 ...
// 169.254.2.1 [guest]"). Passed to --dns-forward so pasta itself (running on
// the host, with full host network access) relays DNS queries to whatever
// the host's real resolver is -- independent of --no-map-gw, which only
// disables the generic loopback-splice-on-any-port behavior; --dns-forward
// is a separate, always-on interception rule scoped to port 53/853 traffic
// to this address, restoring DNS without reopening the general host-loopback
// splice (ADR 0042).
const pastaDNSForwardAddr = "169.254.2.2"

// seccompFilterFD is the file descriptor number bwrap's own --seccomp
// flag argument names. Go's exec.Cmd.ExtraFiles numbers extra file
// descriptors sequentially starting at 3 (0/1/2 are stdin/stdout/stderr);
// this is the only entry the bwrap adapter ever adds to ExtraFiles, so
// the number is fixed at 3, and it survives the prlimit/pasta/bwrap exec
// chain unchanged (execTarget's outer wrappers never touch fd 3, and a
// non-close-on-exec fd -- which is what ExtraFiles produces -- is
// preserved across every execve() in that chain, not just the first).
const seccompFilterFD = 3

// removeSeccompFlag strips a "--seccomp <fd>" pair back out of a flattened
// argv slice, used by Run when a.syscallFilterPath is set but the file
// failed to open: execTarget/buildArgs decide whether to emit the flag
// purely from a.syscallFilterPath's own emptiness (they have no way to know
// the open failed), so Run reconciles argv with the real outcome afterward
// rather than threading the open result down through execTarget/buildArgs.
func removeSeccompFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--seccomp" && i+1 < len(args) {
			i++ // also drop the fd value that follows the flag
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// pastaHardenedFlags are the exact 5 flags ADR 0042 requires when a bwrap
// Box's exec target is wrapped with pasta to restore egress inside its
// isolated network namespace: no TCP/UDP port forwarding into the box and no
// gateway-address mapping, closing the host-loopback splice pasta's own
// defaults leave open.
var pastaHardenedFlags = []string{"-t", "none", "-T", "none", "-u", "none", "-U", "none", "--no-map-gw"}

// resolvedRunEnv returns the process environment the bwrap child should
// inherit. It is an allowlist, not a denylist: the launcher's own ambient
// environment (os.Environ()) is never read here, so nothing outside boxEnv
// can reach the sandbox this way. boxEnv is already the schema-driven
// allowlist (lib/env-schema.nix boxEnv=true names, resolved through
// dispatchConfig's ResolveEnv chain -- including any BOX_GH_TOKEN override,
// ADR 0016, issue #380 -- plus a fixed set of launcher-synthesized keys);
// buildArgs's --setenv loop already delivers every one of those keys to the
// sandbox on argv except the bwrapSecrets subset (GH_TOKEN,
// CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY, OPENCODE_AUTH_CONTENT), which
// it deliberately excludes so ps/proc can't expose them to other local
// users. bwrapSecrets is not every secret boxEnv can carry -- e.g.
// FORGEJO_TOKEN (lib/env-schema.nix) is secret=true and boxEnv=true but
// absent from bwrapSecrets, so it still renders to argv; that gap predates
// this function and is untouched by it. This function's sole remaining job
// is handing the bwrapSecrets subset to the sandbox via the inherited
// process environment instead (bwrap runs with no --clearenv). BOX_GH_TOKEN
// itself is never forwarded:
// it isn't a bwrapSecrets key, and lib/env-schema.nix's boxGhToken entry is
// boxEnv=false, so it's never a key in boxEnv to begin with -- by the time
// Run(box) is called, any BOX_GH_TOKEN override has already been folded
// into boxEnv["GH_TOKEN"] upstream (main.go's boxTokenResolver).
//
// TERM/LANG/LC_ALL/TZ/TMPDIR/proxy vars are deliberately not part of this
// allowlist: the OCI runner (oci.go buildRunArgs) is existing production
// precedent that none are load-bearing -- it has never forwarded ambient
// env at all (podman/docker don't inherit host env by default), and the
// same in-Box agent runs under it today without them. A sweep of
// agent/entrypoint.sh and every nix-rendered preamble found no read of any
// of them either, though that sweep covers the Box's shell surface, not
// the Driver binary's own env reads (ANTHROPIC_BASE_URL,
// NODE_EXTRA_CA_CERTS, proxy variables) -- the OCI precedent is what
// actually carries the claim for those.
func resolvedRunEnv(boxEnv map[string]string) []string {
	keys := make([]string, 0, len(bwrapSecrets))
	for k := range bwrapSecrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := boxEnv[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// memoryLimitToBytes converts a podman/docker-style unit-suffixed memory
// limit ("5g", "512m", "1024k"; bare digits already bytes; case-insensitive
// suffix) to a raw byte count. cgroup v2's memory.max control file, unlike
// podman's own --memory flag, takes only a plain integer (or the literal
// "max"), so this conversion has no OCI-adapter equivalent to reuse.
func memoryLimitToBytes(limit string) (int64, error) {
	if limit == "" {
		return 0, fmt.Errorf("empty memory limit")
	}
	mult := int64(1)
	numPart := limit
	switch limit[len(limit)-1] {
	case 'g', 'G':
		mult = 1024 * 1024 * 1024
		numPart = limit[:len(limit)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		numPart = limit[:len(limit)-1]
	case 'k', 'K':
		mult = 1024
		numPart = limit[:len(limit)-1]
	}
	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", limit, err)
	}
	return n * mult, nil
}

// cgroupDirForName computes the per-Box cgroup v2 directory path that
// provisionCgroup creates, under THIS calling process's own delegated
// cgroup (readSelfCgroup + cgroupFSRoot) -- the only subtree it has Mkdir
// permission in. It is creation-time-only: IsRunning/ListRunning/Reap read
// back via findCgroupDir instead (see its doc comment), since a Box's
// creating process and the process later polling/reaping it are often
// different launcher invocations with different self-cgroup paths. Returns
// an error when this host has no cgroup v2 delegation (readSelfCgroup
// fails); provisionCgroup turns that into a one-time warning.
func (a *bwrapAdapter) cgroupDirForName(name string) (string, error) {
	self, err := readSelfCgroup()
	if err != nil {
		return "", err
	}
	return filepath.Join(cgroupFSRoot, self, "spindrift-"+name), nil
}

// findCgroupDir searches the whole cgroupFSRoot tree (not just the calling
// process's own self-cgroup subtree) for a directory named "spindrift-"+name
// at any depth, and returns its path. This backs IsRunning/ListRunning/Reap
// so a Box created by one launcher invocation/session (via cgroupDirForName,
// under ITS self-cgroup path) is still discoverable by a read/cleanup call
// from a different invocation/session with a different self-cgroup path --
// e.g. a dropped SSH reconnect, a second console, or a concurrent dogfood
// loop. A missing cgroupFSRoot, a walk error, or no match anywhere all
// degrade to ("", false) rather than an error, matching the read paths'
// existing warn-never, degrade-sanely posture. A per-entry walk error (e.g.
// permission denied descending into some other process's non-delegated
// cgroup subtree) is skipped rather than aborting the whole walk.
func findCgroupDir(name string) (dir string, ok bool) {
	want := "spindrift-" + name
	_ = filepath.WalkDir(cgroupFSRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip this entry, keep walking the rest of the tree
		}
		if path == cgroupFSRoot {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() != want {
			return nil
		}
		dir, ok = path, true
		return filepath.SkipAll
	})
	return dir, ok
}

// removeCgroupDir removes a per-Box delegated cgroup v2 directory created by
// provisionCgroup. The three individual os.Remove calls on pids.max/
// memory.max/cgroup.procs are a plain unlink no-op on a real cgroupfs (its
// control files are kernel interface nodes that a real rmdir clears as part
// of removing the whole subtree, not files unlink can touch individually) --
// they only do real work against a plain directory standing in for cgroupfs
// in tests, where they're genuine files that would otherwise make the final
// os.Remove(dir) fail with ENOTEMPTY. Shared by Run's deferred cleanup (dir
// is empty: cmd.Wait() has already returned) and Reap (dir is expected to be
// empty: IsRunning's unsynchronized snapshot found no resident PID a moment
// earlier, not a guarantee held under a lock).
func removeCgroupDir(dir string) error {
	for _, f := range []string{"pids.max", "memory.max", "cgroup.procs"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	return os.Remove(dir)
}

// provisionCgroup attempts to create a per-Box cgroup v2 subtree under the
// launcher's own delegated cgroup (readSelfCgroup + cgroupFSRoot), then
// writes pids.max/memory.max into it. Detection and creation are the same
// os.Mkdir call rather than a separate probe-then-create step: whether the
// parent subtree is writable can only be learned by trying, and a distinct
// probe would just race this Mkdir for nothing. Any failure here — no
// unified cgroup v2 mount (cgroup v1/hybrid hosts), a non-delegated
// (read-only) parent, or a malformed a.memoryLimit — means no usable
// delegation on this host, which ADR 0042 treats as expected and
// non-fatal: it warns and reports ok=false so Run proceeds without cgroup
// enforcement rather than refusing to launch or quietly shrinking
// PidsLimit/MemoryLimit to compensate.
func (a *bwrapAdapter) provisionCgroup(box Box) (dir string, ok bool) {
	dir, err := a.cgroupDirForName(box.Name)
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: cgroup v2 delegation unavailable (%v); running box %q without cgroup resource containment\n", err, box.Name)
		return "", false
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		fmt.Printf("==> bwrap runner: warning: could not create delegated cgroup %s (%v); running box %q without cgroup resource containment\n", dir, err, box.Name)
		return "", false
	}
	if a.pidsLimit != "" {
		if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(a.pidsLimit), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup pids.max (%v); running box %q without cgroup resource containment\n", err, box.Name)
			_ = os.Remove(dir)
			return "", false
		}
	}
	if a.memoryLimit != "" {
		bytesLimit, err := memoryLimitToBytes(a.memoryLimit)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not parse MEMORY_LIMIT %q (%v); running box %q without cgroup resource containment\n", a.memoryLimit, err, box.Name)
			_ = os.Remove(dir)
			return "", false
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(bytesLimit, 10)), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup memory.max (%v); running box %q without cgroup resource containment\n", err, box.Name)
			_ = os.Remove(dir)
			return "", false
		}
	}
	return dir, true
}

// Run launches a single issue into a bubblewrap sandbox.
func (a *bwrapAdapter) Run(box Box) error {
	// etcDir is only needed for the synthesised /etc/resolv.conf (pastaPath
	// only) -- passwd/group are baked nix store paths now (issue #2663) and
	// no longer written here.
	etcDir, err := os.MkdirTemp("", "spindrift-etc-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(etcDir)

	if a.pastaPath() {
		// Nothing else writes /etc/resolv.conf into the guest for the bwrap
		// runtime; buildArgs ro-binds this file when pastaPath applies.
		resolvConf := "nameserver " + pastaDNSForwardAddr + "\n"
		if err := os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte(resolvConf), 0o644); err != nil {
			return err
		}
	}

	out := box.Output
	if out == nil {
		out = io.Discard
	}

	// Provisioned (and its pids.max/memory.max written) before Start, so the
	// control files exist by the time bwrap is exec'd; moving the process in
	// happens after Start below, once the real PID exists.
	cgroupDir, cgroupOK := a.provisionCgroup(box)

	// The bwrap process's env is resolvedRunEnv(box.Env) -- the bwrapSecrets
	// subset of box.Env, not the launcher's own ambient environment. Without
	// --clearenv, the sandbox inherits it. Secrets (GH_TOKEN, auth tokens)
	// are therefore available inside the sandbox without appearing on argv.
	// Opened here, before cmd is built, not after: a failed open must also
	// drop the "--seccomp" flag itself from argv, not just skip attaching
	// ExtraFiles -- otherwise bwrap tries to read a nonexistent fd 3 at its
	// own startup and the whole Box launch fails over a hardening nicety,
	// defeating the warn-and-proceed contract (issue #2670).
	var syscallFilterFile *os.File
	syscallFilterOpenFailed := false
	if a.syscallFilterPath != "" {
		f, err := os.Open(a.syscallFilterPath)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not open syscall filter %s (%v); running box %q without seccomp hardening\n", a.syscallFilterPath, err, box.Name)
			syscallFilterOpenFailed = true
		} else {
			syscallFilterFile = f
			defer f.Close()
		}
	}
	program, execArgs := a.execTarget(etcDir, box)
	if syscallFilterOpenFailed {
		// buildArgs (via execTarget) unconditionally appended "--seccomp
		// <fd>" from a.syscallFilterPath alone, before the open above was
		// known to fail; strip that pair back out of the flattened argv
		// rather than mutate a shallow copy of *a (which would copy the
		// live sync.Mutex embedded in bwrapAdapter -- go vet's copylocks
		// check, and a real hazard since Run executes concurrently for
		// different boxes under MAX_PARALLEL).
		execArgs = removeSeccompFlag(execArgs)
	}
	cmd := execCommand(program, execArgs...)
	// Pdeathsig kills this direct child (bwrap, pasta, or prlimit, whichever
	// execTarget resolved to) the moment the launcher itself dies, so a
	// killed/crashed launcher never leaves an orphaned Box running. This is
	// separate from bwrap's own --die-with-parent flag (buildArgs), which
	// only protects bwrap against ITS immediate OS parent -- pasta, in the
	// fork case, not the launcher two hops up. setDeathSignal is a
	// platform-split seam (bwrap_pdeathsig_linux.go /
	// bwrap_pdeathsig_other.go): syscall.SysProcAttr.Pdeathsig is Linux-only,
	// but the launcher binary itself must still cross-compile for darwin
	// (nix/checks/go.nix's launcher-cross-build).
	setDeathSignal(cmd)
	cmd.Env = resolvedRunEnv(box.Env)
	if program == "pasta" {
		// pasta is now the top-level process and execs "bwrap" itself (as a
		// bare name, positionally after its own flags -- see execTarget) via
		// its own execvp, using its own process environment's PATH, not Go's
		// exec.Command LookPath (which only resolved "pasta" itself, at
		// Command-construction time, against the launcher's ambient PATH).
		// Without this, pasta's env carries no PATH at all (resolvedRunEnv is
		// a secrets-only allowlist), so pasta's own child exec of "bwrap"
		// would fail with ENOENT even though pasta itself started fine --
		// the same class of bug this whole fix closes, one process hop over.
		// PATH carries no secret, so forwarding it here doesn't widen
		// resolvedRunEnv's documented no-ambient-leak guarantee for the
		// sandboxed child's own secrets.
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	}
	if syscallFilterFile != nil {
		cmd.ExtraFiles = []*os.File{syscallFilterFile}
	}
	cmd.Stdout = out
	cmd.Stderr = out

	// nixVarSnapshotLock, when non-nil, holds a shared advisory flock on
	// snapshotLockPath(a.nixVarSnapshotDir) for the life of the sandboxed
	// process. Gated on nixConfigFile, the same condition buildArgs uses to
	// decide whether to mount nixVarSnapshotDir at all -- nix-in-box off
	// means nothing is mounted, so there is nothing to protect.
	// reclaimStaleSnapshots attempts a non-blocking exclusive lock on this
	// same file to tell whether any live Box is still reading this
	// generation, without tracking box->generation mappings anywhere else.
	// lockSnapshotShared's Flock(LOCK_SH) call is blocking (no LOCK_NB), so
	// Run genuinely waits out a concurrent reclaim's exclusive hold rather
	// than racing past it -- but the open and the blocking lock acquire are
	// still two separate steps, leaving a window where reclaim can win the
	// exclusive lock and RemoveAll the generation dir in between. Once the
	// shared lock is actually held, Run re-stats the generation dir (below)
	// to close that window: proceeding to exec bwrap against a directory
	// reclaim has already removed would break --overlay-src's mount, so a
	// failed re-stat here returns an error instead. A failure to open or
	// lock the file in the first place only degrades reclaim's ability to
	// detect this box (ADR 0042's own degrade-don't-lie precedent) -- that
	// path alone must never fail Run, which is why it's warn-and-proceed.
	var nixVarSnapshotLock *os.File
	if a.nixConfigFile != "" {
		lf, err := lockSnapshotShared(a.nixVarSnapshotDir)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); reclaim cannot detect box %q is reading this generation\n", snapshotLockPath(a.nixVarSnapshotDir), err, box.Name)
		} else if _, statErr := os.Stat(a.nixVarSnapshotDir); statErr != nil {
			unlockSnapshot(lf)
			if cgroupOK {
				_ = os.Remove(cgroupDir)
			}
			return fmt.Errorf("nix-var snapshot %s no longer exists (reclaimed by a concurrent build?): %w", a.nixVarSnapshotDir, statErr)
		} else {
			nixVarSnapshotLock = lf
		}
	}
	if err := cmd.Start(); err != nil {
		if cgroupOK {
			_ = os.Remove(cgroupDir)
		}
		unlockSnapshot(nixVarSnapshotLock)
		return err
	}
	if cgroupOK {
		// Best-effort: the box process is already running by this point, so
		// a failure to move it in must not fail Run over it -- it just means
		// this Box runs outside cgroup enforcement despite delegation being
		// available.
		pid := strconv.Itoa(cmd.Process.Pid)
		if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not move box %q into cgroup %s: %v\n", box.Name, cgroupDir, err)
		}
	}
	a.trackRunning(box.Name, cmd.Process)
	defer a.untrackRunning(box.Name)
	// Deferred (unconditionally -- unlockSnapshot no-ops on a nil lock) so a
	// held shared lock spans cmd.Wait()'s entire duration and is released
	// only once it returns below -- releasing it any earlier would let
	// reclaimStaleSnapshots believe this generation is free while the
	// sandboxed process is still reading it. syscall.Flock releases
	// automatically if the launcher process itself dies before this defer
	// runs (fd closes on process exit), so no separate crash-recovery path
	// is needed here.
	defer unlockSnapshot(nixVarSnapshotLock)
	if cgroupOK {
		// Deferred so cleanup runs after cmd.Wait() below returns -- the
		// cgroup dir can only be rmdir'd once no live process remains
		// inside it (ADR 0042's strictly-ephemeral posture). The three
		// os.Remove calls on the individual control files are a plain
		// unlink no-op on a real cgroupfs (its pids.max/memory.max/
		// cgroup.procs are kernel interface nodes that a real rmdir clears
		// as part of removing the whole subtree, not files unlink can touch
		// individually) — they only do real work against a plain directory
		// standing in for cgroupfs in tests, where they're genuine files
		// that would otherwise make the final rmdir fail with ENOTEMPTY.
		defer func() {
			if err := removeCgroupDir(cgroupDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove cgroup %s: %v\n", cgroupDir, err)
			}
		}()
	}
	return asRunError(cmd.Wait())
}

// trackRunning records proc as the live process for name, so a concurrent
// Kill call can find it. A blank name (every call site but Terminate's ever
// scripts one — box.Name is always set in production) is tracked like any
// other; Kill on a blank name would then reach whichever box last ran
// nameless, which never happens outside tests.
func (a *bwrapAdapter) trackRunning(name string, proc *os.Process) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running == nil {
		a.running = map[string]*os.Process{}
	}
	a.running[name] = proc
}

// untrackRunning drops name's tracked process once Run's Wait returns.
func (a *bwrapAdapter) untrackRunning(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.running, name)
}

// Reap best-effort removes a leftover per-Box delegated cgroup dir (see
// provisionCgroup) for name, e.g. one orphaned by a launcher that crashed
// before Run's own deferred cleanup could rmdir it once the sandboxed
// process exited. It resolves the dir via findCgroupDir (searching the whole
// cgroupFSRoot tree) rather than this process's own self-cgroup, so it can
// clean up a Box left behind by a DIFFERENT, e.g. crashed, launcher
// invocation/session too. It never touches a running sandbox -- Kill is the
// operator-driven counterpart for that, per the Runner.Reap contract. No
// cgroup dir for name (never ran, already reaped, or Run's own deferred
// cleanup already removed it), no cgroup v2 tree to search, and any removal
// failure all degrade to a silent nil return rather than propagating an
// error, matching Reap's best-effort contract and the OCI adapter's own
// Reap.
func (a *bwrapAdapter) Reap(name string) error {
	if a.IsRunning(name) {
		return nil
	}
	dir, ok := findCgroupDir(name)
	if !ok {
		return nil
	}
	_ = removeCgroupDir(dir)
	return nil
}

// Kill sends SIGKILL to name's tracked live process, if Run currently has
// one running under that name. A miss (already exited, or never launched) is
// not an error — Terminate's reap step is best-effort by design.
func (a *bwrapAdapter) Kill(name string) error {
	a.mu.Lock()
	proc := a.running[name]
	a.mu.Unlock()
	if proc == nil {
		return nil
	}
	// The process can finish (and untrackRunning's deferred delete race
	// past the read above) between the map lookup and this Kill call --
	// os.ErrProcessDone means it's already gone, matching the "a miss is
	// not an error" contract exactly as much as a nil map entry does.
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// IsRunning reports whether a Box's per-name delegated cgroup (see
// provisionCgroup) still has a resident PID in its cgroup.procs. The dir is
// resolved via findCgroupDir, which searches the whole cgroupFSRoot tree
// rather than just this process's own self-cgroup subtree, so a Box created
// by a DIFFERENT launcher invocation/session is found too. This is a
// best-effort, read-only check: no cgroup v2 tree to search, or no cgroup
// dir for this name (never ran, already reaped, or the deferred cleanup in
// Run already removed it), both degrade to false rather than erroring,
// matching ADR 0042's warn-and-proceed tiering -- but IsRunning itself never
// warns, since a poll loop calling it repeatedly would make that noisy;
// provisionCgroup already warns once at launch time.
func (a *bwrapAdapter) IsRunning(name string) bool {
	dir, ok := findCgroupDir(name)
	if !ok {
		return false
	}
	procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(procs))) > 0
}

// ListRunning enumerates every spindrift-* cgroup dir anywhere under
// cgroupFSRoot -- not just the calling process's own delegated self-cgroup
// subtree -- and reports the subset that are actually live, so Console
// startup orphan detection (issue #651) can find Boxes started by a PRIOR,
// possibly different, launcher invocation/session (issue #2669). No cgroup
// v2 tree to search degrades to a nil slice and no error, matching
// IsRunning's own warn-never, degrade-sanely posture -- this is a read-only
// capability probe, not a hard failure. Liveness is checked directly against
// each candidate's own cgroup.procs (the walk callback already has the
// path, so there's no need to re-derive it through findCgroupDir/IsRunning
// per candidate), so a leftover empty/stale cgroup dir (crashed launcher,
// cleanup never ran, sandboxed process since exited) is excluded rather than
// reported as running.
func (a *bwrapAdapter) ListRunning() ([]string, error) {
	var names []string
	_ = filepath.WalkDir(cgroupFSRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip this entry, keep walking the rest of the tree
		}
		if path == cgroupFSRoot {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		name, ok := strings.CutPrefix(entry.Name(), "spindrift-")
		if !ok {
			return nil
		}
		procs, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err == nil && len(strings.TrimSpace(string(procs))) > 0 {
			names = append(names, name)
		}
		return fs.SkipDir // box cgroup dirs are leaves; no need to descend further
	})
	return names, nil
}

// bwrapBuildAdapter implements Runner for the `launcher build` bwrap path.
// EnsureReady realizes the agent store closures; Run is not supported.
type bwrapBuildAdapter struct {
	agentFilesDrv string // .drv path for agentFiles
	agentEnvDrv   string // .drv path for agentEnv
	passwdFileDrv string // .drv path for passwdFile
	groupFileDrv  string // .drv path for groupFile
	// nixConfigFileDrv is the .drv path for /etc/nix/nix.conf (ADR 0042);
	// empty when the Consumer's nixInBox knob is off, which gates both the
	// extra nix-config closure realization and the store-DB snapshot step
	// below together.
	nixConfigFileDrv string
	// syscallFilterDrv is the .drv path for the compiled BPF syscall filter
	// (issue #2670). Unconditional in production (see Config.SyscallFilterDrv),
	// but guarded the same way as nixConfigFileDrv below so a zero-value
	// adapter (e.g. an existing test's bare struct literal) never tries to
	// realize an empty drv path.
	syscallFilterDrv string
	// nixVarSnapshotDir is the host-side directory snapshotStoreDB writes
	// into (shared package-level convention with the run adapter's mount —
	// see nixVarSnapshotDir's doc comment).
	nixVarSnapshotDir string
	// nixVarSnapshotRoot and nixVarGeneration are the same pwd/generation
	// EnsureReady built nixVarSnapshotDir from, kept as their own fields
	// (rather than re-derived from nixVarSnapshotDir via filepath.Dir/Base)
	// so reclaimStaleSnapshots' root/keepGeneration arguments can never be
	// misidentified by path surgery on the already-joined dir (issue #2680
	// review finding). nixVarGeneration == "" is the flat/legacy path,
	// distinguishable here from "" meaning "root itself" the way Dir/Base
	// surgery could not.
	nixVarSnapshotRoot string
	nixVarGeneration   string
}

// NewBwrapBuild constructs a bwrap adapter for the build command from cfg and
// pwd (the launcher's own working directory, mirroring NewBwrap's pwd
// parameter). EnsureReady realizes agent store closures via nix build and,
// when nixInBox is on, snapshots the host nix store DB.
func NewBwrapBuild(cfg Config, pwd string) Runner {
	generation := closureGeneration(cfg.ImageTag)
	return &bwrapBuildAdapter{
		agentFilesDrv:      cfg.AgentFilesDrv,
		agentEnvDrv:        cfg.AgentEnvDrv,
		passwdFileDrv:      cfg.PasswdFileDrv,
		groupFileDrv:       cfg.GroupFileDrv,
		nixConfigFileDrv:   cfg.NixConfigFileDrv,
		syscallFilterDrv:   cfg.SyscallFilterDrv,
		nixVarSnapshotDir:  nixVarSnapshotDir(pwd, generation),
		nixVarSnapshotRoot: nixVarSnapshotRoot(pwd),
		nixVarGeneration:   generation,
	}
}

// closureSpec pairs a human-readable label with the .drv path EnsureReady
// realizes it from, so the same shape isn't respelled at each of the two call
// sites that build a []closureSpec (the fixed four closures and the
// conditionally-appended nix-config one).
type closureSpec struct {
	label string
	drv   string
}

// EnsureReady realizes the agent store closures via nix build. Nix is
// idempotent — if already realized this is fast. Real nix errors surface.
// When nixConfigFileDrv is set (Consumer's nixInBox knob is on), it also
// realizes the nix-config closure and snapshots the host nix store DB (ADR
// 0042) for the run adapter's /nix/var overlay to mount.
func (a *bwrapBuildAdapter) EnsureReady() error {
	fmt.Println("==> bwrap runner: realizing agent store closures (no image build/load)")

	closures := []closureSpec{
		{"agent-files", a.agentFilesDrv},
		{"agent-env", a.agentEnvDrv},
		{"passwd-file", a.passwdFileDrv},
		{"group-file", a.groupFileDrv},
	}
	if a.nixConfigFileDrv != "" {
		closures = append(closures, closureSpec{"nix-config", a.nixConfigFileDrv})
	}
	if a.syscallFilterDrv != "" {
		closures = append(closures, closureSpec{"syscall-filter", a.syscallFilterDrv})
	}
	for _, c := range closures {
		if err := a.realize(c.label, c.drv); err != nil {
			return err
		}
	}

	if a.nixConfigFileDrv != "" {
		if err := a.snapshotStoreDB(); err != nil {
			return err
		}
	}

	fmt.Println("==> done: agent store closures realized")
	return nil
}

// hostNixDBPath is the host's real, live nix store database — never a path
// inside a sandbox. snapshotStoreDB runs during `launcher build`, on the
// operator's (or CI job's) own machine.
const hostNixDBPath = "/nix/var/nix/db/db.sqlite"

// snapshotStoreDB copies the host's live nix store database into
// a.nixVarSnapshotDir, compacting it in the same step (ADR 0042: ~302MB raw
// vs ~104MB compacted — an overlay copy-up rewrites a file whole, so the
// compacted size is what actually lands in the Box's tmpfs upper on first
// touch). This is the one Go call site in the whole launcher that reaches
// into the host's live nix store metadata (as opposed to a nix-store-
// realized artifact). The resulting file's ownership is whatever uid runs
// `launcher build` (the operator, or the CI job), never root, regardless of
// hostNixDBPath's own ownership — sqlite3's "VACUUM INTO" always creates a
// fresh file owned by the invoking process (ADR 0042's "agent-owned"
// requirement), so no explicit chown is needed here.
func (a *bwrapBuildAdapter) snapshotStoreDB() error {
	fmt.Println("==> bwrap runner: snapshotting host nix store DB (VACUUMed)")

	if err := statHostNixDB(); err != nil {
		return fmt.Errorf("host nix store db not found at %s: %w", hostNixDBPath, err)
	}

	dest := filepath.Join(a.nixVarSnapshotDir, "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir nix-var-snapshot: %w", err)
	}

	// "VACUUM INTO" uses the same online-backup mechanism as sqlite's ".backup"
	// dot-command internally, so it correctly handles a concurrent host
	// nix-daemon write (WAL journal not yet checkpointed) that a plain file
	// copy could snapshot mid-write, producing a truncated/inconsistent
	// database — while also compacting into the destination in the same
	// step, so no separate VACUUM pass is needed. dest is escaped for
	// embedding in a single-quoted SQL string literal; sqlite3's dot-commands
	// are whitespace-tokenized (a bare ".backup <dest>" with a space in dest
	// breaks), but a SQL statement passed as sqlite3's third argv element is
	// not.
	//
	// "VACUUM INTO" refuses to run if dest already exists (e.g. a prior
	// `launcher build` on the same nixVarSnapshotDir), so move any existing
	// snapshot aside instead of deleting it outright: if VACUUM INTO then
	// fails (disk full, host db locked), the rename below lets us restore the
	// previously-working snapshot instead of leaving `launcher run` unable to
	// start from a snapshot that was destroyed for nothing (issue #2664
	// review finding).
	backup := dest + ".bak"
	hadBackup := false
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("move aside stale nix store db snapshot %s: %w", dest, err)
		}
		hadBackup = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat nix store db snapshot %s: %w", dest, err)
	}

	escapedDest := strings.ReplaceAll(dest, "'", "''")
	stmt := fmt.Sprintf("VACUUM INTO '%s';", escapedDest)
	vacuumInto := execCommand("sqlite3", hostNixDBPath, stmt)
	vacuumInto.Stdout = os.Stdout
	vacuumInto.Stderr = os.Stderr
	if err := vacuumInto.Run(); err != nil {
		wrapped := fmt.Errorf("sqlite3 vacuum-into nix store db snapshot: %w", err)
		if hadBackup {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("%w (additionally failed to restore previous snapshot from %s to %s: %v)", wrapped, backup, dest, restoreErr)
			}
		}
		return wrapped
	}

	if hadBackup {
		// Backup cleanup is best-effort: a leftover .bak file wastes disk but
		// doesn't break the snapshot VACUUM INTO just wrote to dest, so it's
		// not worth failing an otherwise-successful EnsureReady over.
		if err := os.Remove(backup); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not remove backup snapshot %s: %v\n", backup, err)
		}
	}
	return nil
}

// realize runs `nix build <drv>^* --no-link` for a single closure, wrapping
// any failure with label so the caller can tell which closure failed.
func (a *bwrapBuildAdapter) realize(label, drv string) error {
	cmd := execCommand("nix", "build", drv+"^*", "--no-link")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix build %s: %w", label, err)
	}
	return nil
}

// IsReady is a no-op for the build adapter.
func (a *bwrapBuildAdapter) IsReady() error { return nil }

// Run is not supported by the build adapter.
func (a *bwrapBuildAdapter) Run(_ Box) error {
	return fmt.Errorf("bwrap-build adapter: Run not supported (use bwrap run adapter)")
}

// Reap is a no-op for the build adapter.
func (a *bwrapBuildAdapter) Reap(_ string) error { return nil }

// Kill is a no-op for the build adapter — it never launches a box, so there
// is nothing to kill.
func (a *bwrapBuildAdapter) Kill(_ string) error { return nil }

// IsRunning always reports false for the build adapter: it never launches a
// box, so there is nothing to be running.
func (a *bwrapBuildAdapter) IsRunning(_ string) bool { return false }

// ListRunning always returns an empty list for the build adapter: it never
// launches a box, so there is nothing running to find.
func (a *bwrapBuildAdapter) ListRunning() ([]string, error) { return nil, nil }
