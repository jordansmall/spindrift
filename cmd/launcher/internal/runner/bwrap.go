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
// bwrap) with no configurable CLI field to intercept; tests swap this seam.
var execCommand = exec.Command

// statResolvConf backs buildArgs' "does the host have /etc/resolv.conf to
// bind" check. Tests swap this seam: some nix build sandboxes have none.
var statResolvConf = func() error {
	_, err := os.Stat("/etc/resolv.conf")
	return err
}

// statHostNixDB backs snapshotStoreDB's "does hostNixDBPath exist" preflight.
// Tests swap this seam: the test machine may have no real store DB.
var statHostNixDB = func() error {
	_, err := os.Stat(hostNixDBPath)
	return err
}

// lockRaceWindowHook runs between the os.OpenFile of a lock path and the
// Flock(LOCK_EX) that follows it, so a test can deterministically swap the
// path's inode inside the window lockedFDMatchesPath guards. No-op in
// production.
var lockRaceWindowHook = func() {}

// readSelfCgroup returns the calling (launcher) process's own cgroup v2
// path, parsed from /proc/self/cgroup's unified-hierarchy line ("0::<path>").
// Tests swap this seam: /proc/self/cgroup isn't writable in a test sandbox.
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

// cgroupFSRoot is the host's cgroup v2 filesystem mountpoint. Tests reassign
// it to a t.TempDir() to fake a writable delegated subtree.
var cgroupFSRoot = "/sys/fs/cgroup"

// homeAgentStagingDir is the in-box path bwrap ro-binds agentFiles' baked
// /home/agent subtree onto. It must be a fresh top-level path, not nested
// under /agent: /agent is already bound read-only by the time this mount is
// added, and bwrap cannot create a mountpoint inside a read-only bind.
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
	// nixConfigFile is the baked nix store path for /etc/nix/nix.conf (ADR
	// 0042); empty when nixInBox is off, which gates both this mount and
	// nixVarSnapshotDir's together (nix isn't even on PATH then).
	// nixVarSnapshotDir is always computed, so its presence on disk — not its
	// own emptiness — is what IsReady checks.
	nixConfigFile     string
	nixVarSnapshotDir string
	// nixVarSnapshotRoot is the pwd-derived root snapshotDirFor joins a
	// per-launch generation (box.ClosureGeneration) onto.
	nixVarSnapshotRoot string
	// nixStoreWritable gives /nix/store the same overlay treatment as
	// /nix/var (ADR 0042): an ephemeral tmpfs upper over the host's real
	// store, so paths built in the Box vanish with the sandbox instead of
	// touching host disk. AND-gated with nixConfigFile in buildArgs — true
	// alone does nothing when nixInBox is off, since nix isn't on PATH then.
	nixStoreWritable bool
	// mountParams carries this run's host-mount facts straight through from
	// Config to buildMountSpecs, unmodified; see MountParams.
	mountParams MountParams
	unshareNet  bool   // raw BWRAP_UNSHARE_NET knob; can only force isolation on, already the default — kept for defense in depth
	networkMode string // NETWORK_MODE knob; every value except the "host" opt-out isolates from the host netns. "no-host-loopback" never legitimately reaches bwrap — nix eval-rejects it for a valid Consumer flake (lib/mkHarness.nix networkModeCoherenceOk).
	// pidsLimit and memoryLimit are the PIDS_LIMIT/MEMORY_LIMIT knobs (empty
	// disables). bwrap has no cap of its own for either: both are enforced
	// solely through the per-Box cgroup v2 control files (ADR 0042), and
	// memory.max needs a raw byte count unlike podman's --memory.
	pidsLimit   string
	memoryLimit string

	// syscallFilterPath is the baked nix store path to the compiled BPF
	// syscall filter. A failure to open it at Run time is a hardening gap,
	// not a safety blocker (ADR 0042's degrade-don't-lie posture): Run warns
	// and proceeds without the filter rather than refusing to launch the Box.
	syscallFilterPath string

	// mu guards running, the box-name -> live process map Kill consults —
	// bwrap sandboxes are unnamed child processes with no persistent daemon
	// to query by name, so Run tracks its own process handle here for the one
	// caller (Terminate) reaching a live one from outside Run's goroutine.
	mu      sync.Mutex
	running map[string]*os.Process
}

// nixVarSnapshotDir is the host-side directory that stands in for /nix/var
// inside a bwrap Box's overlay lower (ADR 0042): the build command writes an
// agent-owned, VACUUMed snapshot of the host's /nix/var/nix/db/db.sqlite to
// <dir>/nix/db/db.sqlite, and the run adapter overlays the whole directory
// onto /nix/var so the Box's nix trusts host-present store paths without
// re-substituting them. generation scopes the snapshot to the agent-closure
// it was taken against, so two closures coexist instead of colliding; an
// empty generation (no closure known, e.g. a test-constructed adapter) falls
// back to the flat path, since filepath.Join drops empty components.
func nixVarSnapshotDir(pwd, generation string) string {
	return filepath.Join(nixVarSnapshotRoot(pwd), generation)
}

// nixVarSnapshotRoot is the directory nixVarSnapshotDir nests generation
// subdirs under -- the sweep root reclaimStaleSnapshots RemoveAlls entries
// of. Callers needing the root must derive it from pwd this way, never by
// filepath.Dir/Base surgery on an already-joined nixVarSnapshotDir: that
// misidentifies the root when generation is "" (the flat path IS the
// snapshot dir, so its parent is .spindrift, home to unrelated siblings a
// sweep must never touch).
func nixVarSnapshotRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-var-snapshot")
}

// closureGeneration derives the generation subdir from a bwrap-runtime
// Config.ImageTag (the agent-closure's loaded store path). imageTag comes
// from an input-document artifact an untrusted source can influence, and the
// result becomes a path component inside a directory reclaimStaleSnapshots
// later os.RemoveAll's — hence safePathComponent.
func closureGeneration(imageTag string) string {
	return safePathComponent(imageTag)
}

// NewAgentGeneration derives an AgentGeneration for a Box launch from
// closure -- the tip agent-closure linkFarm's store output path (what
// freshness.Probe reports as res.TipTag under bwrap), NOT the agentFiles
// derivation directly. That linkFarm (lib/mkHarness.nix's agentClosure) nests
// the "files", "env" and "nix-config" children the fields point at.
// Generation uses safePathComponent, the same convention closureGeneration
// applies to a baked ImageTag, so hot-swapped and baked generations share one
// naming scheme.
func NewAgentGeneration(closure string) AgentGeneration {
	return AgentGeneration{
		AgentFiles:    filepath.Join(closure, "files"),
		AgentEnv:      filepath.Join(closure, "env"),
		NixConfigFile: filepath.Join(closure, "nix-config"),
		Generation:    safePathComponent(closure),
	}
}

// safePathComponent returns s unless it is unsafe to use as a single path
// component — empty, ".", "..", or a bare separator — in which case it
// returns "" so the caller can fall back to a known-safe default. Shared by
// closureGeneration and snapshotDirFor (which validates a Box-supplied
// AgentGeneration.Generation) so both rejection paths stay in lockstep.
func safePathComponent(s string) string {
	if s == "" {
		return ""
	}
	base := filepath.Base(s)
	if base == "." || base == ".." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

// snapshotLockPath is the one place the "<generation dir>.lock" naming
// convention is spelled: a sibling of the generation dir, never a file inside
// it, since the generation dir is what buildArgs --overlay-src binds into the
// sandbox. Shared by Run (shared lock for the life of the sandboxed process)
// and reclaimStaleSnapshots (exclusive-lock probe for "no live Box").
func snapshotLockPath(dir string) string {
	return dir + ".lock"
}

// lockSnapshotShared opens (creating if needed) and takes a blocking shared
// advisory flock on dir's snapshotLockPath. The lock file is NOT guaranteed to
// keep the same inode: sweepOrphanedLock removes it once a later reclaim pass
// sees no matching generation dir, and EnsureReady legitimately calls this
// before that dir exists. If such a sweep's LOCK_EX probe lands between the
// os.OpenFile and the Flock(LOCK_SH) below, it wins and removes the path while
// our Flock still succeeds (flock locks the open file description, not the
// path) -- leaving us holding a lock on nothing while a later pass O_CREATEs a
// fresh inode and locks it uncontested. Hence the verify-and-retry, bounded so
// a persistent adversary gets a clear error instead of an infinite loop.
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
		if lockedFDMatchesPath(lf, path) {
			return lf, nil
		}
		// The path was swapped or unlinked out from under lf between open and
		// lock -- lf's lock now protects an inode nothing resolves to. Drop
		// it and retry against whatever is at the path now.
		unlockSnapshot(lf)
	}
	return nil, fmt.Errorf("lockSnapshotShared: %s kept changing identity after locking across %d attempts", path, maxAttempts)
}

// lockedFDMatchesPath reports whether lf -- an fd a caller just flocked --
// still identifies whatever currently sits at path. A successful flock never
// licenses acting on path: a concurrent remove/recreate can win the window
// between open and flock, so flock success proves the fd is locked, not that
// it still names path (issue #3005). Any stat failure counts as a mismatch.
func lockedFDMatchesPath(lf *os.File, path string) bool {
	fdStat, err := lf.Stat()
	if err != nil {
		return false
	}
	pathStat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(fdStat, pathStat)
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
// (the launcher's own working directory). EnsureReady only checks readiness
// rather than realizing anything; call NewBwrapBuild for the build command.
// By default (any cfg.NetworkMode except the "host" opt-out) the adapter
// isolates the sandbox into its own network namespace, with egress restored
// via a hardened pasta helper (ADR 0042) — podman-rootless parity.
func NewBwrap(cfg Config, pwd string) Runner {
	return &bwrapAdapter{
		agentFiles:         cfg.AgentFiles,
		agentEnv:           cfg.AgentEnv,
		passwdFile:         cfg.PasswdFile,
		groupFile:          cfg.GroupFile,
		bakedPrefetch:      cfg.BakedPrefetch,
		nixConfigFile:      cfg.NixConfigFile,
		nixVarSnapshotDir:  nixVarSnapshotDir(pwd, closureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot: nixVarSnapshotRoot(pwd),
		nixStoreWritable:   cfg.NixStoreWritable,
		mountParams:        cfg.MountParams,
		unshareNet:         cfg.BwrapUnshareNet,
		networkMode:        cfg.NetworkMode,
		pidsLimit:          cfg.PidsLimit,
		memoryLimit:        cfg.MemoryLimit,
		syscallFilterPath:  cfg.SyscallFilterPath,
	}
}

// EnsureReady does not build anything for bwrap run: store closures are
// realized by `launcher build` before `run` is invoked. It delegates to
// IsReady so the actionable snapshot-missing error fires on the default
// run/dispatch path too, not only on `--no-build`.
func (a *bwrapAdapter) EnsureReady() error { return a.IsReady() }

// IsReady checks that the nix-in-box snapshot `launcher build` writes is
// present, when the Consumer's nixInBox knob is on (ADR 0042). The other
// closures (agentFiles/agentEnv/passwd/group) are deliberately not checked:
// buildArgs never conditions a mount on their absence the way it does
// nixVarSnapshotDir, whose absence would otherwise surface only as a raw
// bwrap overlay mount failure.
func (a *bwrapAdapter) IsReady() error {
	if a.nixConfigFile == "" {
		return nil
	}
	// Check the db.sqlite file itself, not just its parent dir: snapshotStoreDB
	// MkdirAlls <nixVarSnapshotDir>/nix/db before it writes db.sqlite, so a
	// dir-only check would report ready on a dir left by a failed snapshot.
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
	return buildMountSpecs(a.mountParams, box)
}

// isolateNet is the effective "cut off the host netns" decision (ADR 0042):
// every NetworkMode value except the explicit "host" opt-out isolates,
// including the Go zero value and "no-host-loopback" (nix eval-rejects the
// latter reaching bwrap in production; main.go's checkNetworkModeRuntimeGate
// backstops it). BwrapUnshareNet can only force isolation on, already the
// default outcome, but is kept for defense in depth.
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

// pick returns override if it's non-empty, else baked -- the shared shape
// behind agentFilesFor/agentEnvFor/nixConfigFileFor below. A partially
// populated ClosureGeneration (non-nil, empty field) falls back to baked
// rather than returning "" verbatim: every caller appends a path segment to
// this result, and an empty base would silently point at a root filesystem
// instead of a store closure.
func pick(override, baked string) string {
	if override != "" {
		return override
	}
	return baked
}

// agentFilesFor resolves the agent-closure store path box's launch binds.
// Callers append "/agent" and "/home/agent" to this result.
func (a *bwrapAdapter) agentFilesFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.agentFiles
	}
	return pick(box.ClosureGeneration.AgentFiles, a.agentFiles)
}

// agentEnvFor resolves the agentEnv store path box's launch binds for
// PATH/SSL_CERT_FILE/GIT_SSL_CAINFO. Callers append "/bin" and
// "/etc/ssl/certs/ca-bundle.crt" to this result.
func (a *bwrapAdapter) agentEnvFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.agentEnv
	}
	return pick(box.ClosureGeneration.AgentEnv, a.agentEnv)
}

// nixConfigFileFor resolves the nix.conf store path box's launch binds onto
// /etc/nix/nix.conf.
func (a *bwrapAdapter) nixConfigFileFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.nixConfigFile
	}
	return pick(box.ClosureGeneration.NixConfigFile, a.nixConfigFile)
}

// snapshotDirFor resolves the nix-var store-DB snapshot directory box's
// launch overlays onto /nix/var and locks for its lifetime. An empty or
// unsafe (".", "..", bare separator) Generation falls back to the baked dir:
// a raw "" would resolve to the root itself (which has no db.sqlite) and a
// raw ".." would escape it. Joins directly rather than via the
// nixVarSnapshotDir free function, which would double-append the root.
func (a *bwrapAdapter) snapshotDirFor(box Box) string {
	if box.ClosureGeneration != nil {
		if gen := safePathComponent(box.ClosureGeneration.Generation); gen != "" {
			return filepath.Join(a.nixVarSnapshotRoot, gen)
		}
	}
	return a.nixVarSnapshotDir
}

// buildArgs constructs the bwrap command-line arguments for the given box.
// etcDir is only needed for the synthesised /etc/resolv.conf (pastaPath
// only). Secret env vars are intentionally excluded from argv; they reach the
// sandbox via inherited process environment (no --clearenv). Pasta is never
// part of this return value -- see execTarget.
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
	// nixConfigFile empty means nixInBox is off (nix isn't even on PATH), so
	// both nix mounts below are skipped together rather than independently.
	// --overlay-src + --tmp-overlay gives a read-only lower (the VACUUMed
	// db.sqlite snapshot, ADR 0042) with an ephemeral tmpfs upper, so nix's
	// writes inside the Box (gcroots, profiles, WAL files) vanish with the
	// sandbox rather than touching host disk.
	if a.nixConfigFile != "" {
		args = append(args, "--ro-bind", a.nixConfigFileFor(box), "/etc/nix/nix.conf")
		args = append(args, "--overlay-src", a.snapshotDirFor(box), "--tmp-overlay", "/nix/var")
	}
	if !isolateNet {
		if err := statResolvConf(); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	} else if a.pastaPath() {
		// Nothing else writes /etc/resolv.conf inside the guest (unlike the
		// OCI runner, where podman supplies its own) -- Run writes this file
		// pointed at pastaDNSForwardAddr before invoking bwrap.
		args = append(args, "--ro-bind", filepath.Join(etcDir, "resolv.conf"), "/etc/resolv.conf")
	}
	agentFiles := a.agentFilesFor(box)
	args = append(args, "--ro-bind", agentFiles+"/agent", "/agent")
	// /home/agent above is a fresh writable tmpfs, so baked content (Claude
	// hooks, settings.json, opencode agent files) can't be ro-bound there
	// directly; stage it read-only at a fresh top-level path instead. It
	// cannot nest under /agent: bwrap processes binds in argv order and
	// cannot fabricate a mountpoint inside one already made read-only.
	// entrypoint.sh copies the staged content into /home/agent at startup.
	args = append(args, "--ro-bind", agentFiles+"/home/agent", homeAgentStagingDir)
	// Mount decisions (gates, existence guards, operator messages) are
	// computed once in buildMountSpecs, shared with the OCI adapter; bwrap
	// only renders each spec into its own bind syntax. The driver-cache spec
	// and the CODE_FORGE=local outbox spec (ADR 0033) are the only writable
	// mounts buildMountSpecs ever produces.
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
	// --clearenv is intentionally absent: values on argv are visible in
	// ps/proc, so the bwrapSecrets subset of box.Env reaches the sandbox
	// through the inherited process environment instead (resolvedRunEnv).
	agentEnv := a.agentEnvFor(box)
	args = append(args,
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.bakedPrefetch,
	)
	for k, v := range box.Env {
		if !bwrapSecrets[k] {
			args = append(args, "--setenv", k, v)
		}
	}
	// bwrap is PID 1 inside its own unshared PID namespace, so
	// --die-with-parent kills the whole sandbox when bwrap's immediate OS
	// parent dies -- the launcher on the direct-exec chain, but only pasta on
	// the pasta chain (see setDeathSignal's call site in Run for that gap).
	unshareFlags := []string{"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent"}
	// bwrap only unshares net itself for the fully offline networkMode=none
	// case. Every other isolating mode leaves this to pasta, which creates and
	// configures the fresh netns (tap device, routes) for its COMMAND then
	// execs it inside -- bwrap must inherit that configured namespace, not
	// re-unshare a second, empty one on top of it.
	if isolateNet && !a.pastaPath() {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	// --seccomp only ever names seccompFilterFD, the one fd Run's
	// cmd.ExtraFiles attaches; an empty syscallFilterPath skips the flag.
	if a.syscallFilterPath != "" {
		args = append(args, "--seccomp", strconv.Itoa(seccompFilterFD))
	}
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// execTarget computes the top-level host-exec'd program and argv for box's
// bwrap invocation. When pastaPath applies, pasta must be the outer process
// (see buildArgs' unshare-net comment for why); otherwise bwrap itself is the
// top-level program.
//
// The third return, childExecsByName, is true whenever the returned program
// will exec its own child by bare argv name via execvp, so Run must forward a
// PATH into its env for that resolution to succeed — Go's LookPath only
// resolves the top-level program. Deciding it here, where the chain is
// assembled, means a future wrapper added without updating the flag fails
// closed instead of silently inheriting forwarding.
func (a *bwrapAdapter) execTarget(etcDir string, box Box) (string, []string, bool) {
	bwrapArgs := a.buildArgs(etcDir, box)
	var program string
	var args []string
	if !a.pastaPath() {
		program, args = "bwrap", bwrapArgs
	} else {
		pastaArgs := append([]string{}, pastaHardenedFlags...)
		pastaArgs = append(pastaArgs, "--dns-forward", pastaDNSForwardAddr,
			// -f/--foreground is load-bearing: pasta otherwise forks into the
			// background once the namespace is set up, so cmd.Start()/Wait()
			// would track its short-lived detaching parent instead of the real
			// bwrap+entrypoint child, breaking exit-code propagation, output
			// capture, and the Kill()/Terminate() process map.
			"-f", "--", "bwrap")
		pastaArgs = append(pastaArgs, bwrapArgs...)
		program, args = "pasta", pastaArgs
	}
	childExecsByName := program == "pasta"
	return program, args, childExecsByName
}

// pastaDNSForwardAddr is pasta's documented default IPv4 gateway address when
// it creates a namespace with no host default route visible (always true
// here) -- see pasta(1) NOTES. Passed to --dns-forward so pasta itself, on
// the host, relays DNS queries to the host's real resolver. --dns-forward is
// scoped to port 53/853 traffic to this address, so it restores DNS without
// reopening the general host-loopback splice --no-map-gw closed (ADR 0042).
const pastaDNSForwardAddr = "169.254.2.2"

// seccompFilterFD is the file descriptor number bwrap's --seccomp flag names.
// It is fixed at 3 because this is the only entry the adapter ever adds to
// cmd.ExtraFiles, and it survives the pasta/bwrap exec chain unchanged
// (ExtraFiles fds are not close-on-exec, and no wrapper touches fd 3).
const seccompFilterFD = 3

// removeSeccompFlag strips a "--seccomp <fd>" pair back out of a flattened
// argv slice. Run needs it when syscallFilterPath is set but the file failed
// to open: buildArgs emits the flag from that path's emptiness alone, so Run
// reconciles argv with the real outcome afterward rather than threading the
// open result down through execTarget/buildArgs.
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

// pastaHardenedFlags are the flags ADR 0042 requires when a bwrap Box's exec
// target is wrapped with pasta: no TCP/UDP port forwarding into the box and
// no gateway-address mapping, closing the host-loopback splice pasta's own
// defaults leave open.
var pastaHardenedFlags = []string{"-t", "none", "-T", "none", "-u", "none", "-U", "none", "--no-map-gw"}

// resolvedRunEnv returns the process environment the bwrap child inherits: an
// allowlist, not a denylist. os.Environ() is never read here, so nothing
// outside boxEnv reaches the sandbox this way. boxEnv is the schema-driven
// allowlist (lib/env-schema.nix boxEnv=true names, resolved through
// dispatchConfig's ResolveEnv chain, including any BOX_GH_TOKEN two-actor
// override -- ADR 0016), plus launcher-synthesized keys. buildArgs' --setenv
// loop
// already delivers every boxEnv key on argv except the bwrapSecrets subset,
// so this function's only job is handing that subset over via the inherited
// environment instead (bwrap runs with no --clearenv). Note bwrapSecrets is
// not every secret boxEnv can carry -- FORGEJO_TOKEN, for one, still renders
// to argv. BOX_GH_TOKEN is never forwarded: env-schema marks it boxEnv=false,
// and any override was folded into boxEnv["GH_TOKEN"] upstream.
//
// TERM/LANG/LC_ALL/TZ/TMPDIR/proxy vars are deliberately excluded, on the
// precedent that the OCI runner has never forwarded ambient env at all and
// the same in-Box agent (Driver binary included) runs fine under it.
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
// suffix) to a raw byte count, which is all cgroup v2's memory.max accepts.
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

// cgroupDirForName computes the per-Box cgroup v2 directory provisionCgroup
// creates, under THIS process's own delegated cgroup -- the only subtree it
// has Mkdir permission in. Creation-time only: IsRunning/ListRunning/Reap
// read back via findCgroupDir instead, since a Box's creating process and the
// process later polling/reaping it are often different launcher invocations
// with different self-cgroup paths. Returns an error when the host has no
// cgroup v2 delegation; provisionCgroup turns that into a one-time warning.
func (a *bwrapAdapter) cgroupDirForName(name string) (string, error) {
	self, err := readSelfCgroup()
	if err != nil {
		return "", err
	}
	return filepath.Join(cgroupFSRoot, self, "spindrift-"+name), nil
}

// findCgroupDir searches the whole cgroupFSRoot tree, not just the calling
// process's own self-cgroup subtree, so IsRunning/ListRunning/Reap still find
// a Box created by a different launcher invocation -- a dropped SSH
// reconnect, a second console, a concurrent dogfood loop. A missing root, a
// walk error, or no match all degrade to ("", false) rather than an error,
// and a per-entry walk error (permission denied on another process's
// non-delegated subtree) is skipped, not fatal.
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
// provisionCgroup. The os.Remove calls on the control files are a no-op on a
// real cgroupfs (kernel interface nodes rmdir clears with the subtree); they
// only matter against the plain directory standing in for cgroupfs in tests,
// where they would otherwise make the final os.Remove(dir) fail ENOTEMPTY.
func removeCgroupDir(dir string) error {
	for _, f := range []string{"pids.max", "memory.max", "cgroup.procs"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	return os.Remove(dir)
}

// provisionCgroup attempts to create a per-Box cgroup v2 subtree under the
// launcher's own delegated cgroup, then writes pids.max/memory.max into it.
// Detection and creation are the same os.Mkdir call: whether the parent
// subtree is writable can only be learned by trying. Any failure — no unified
// cgroup v2 mount, a non-delegated parent, a malformed a.memoryLimit — means
// no usable delegation on this host, which ADR 0042 treats as expected and
// non-fatal: warn and report ok=false so Run proceeds without enforcement
// rather than refusing to launch or quietly shrinking the limits.
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
	// only); passwd/group are baked nix store paths.
	etcDir, err := os.MkdirTemp("", "spindrift-etc-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(etcDir)

	if a.pastaPath() {
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

	// Opened before cmd is built: a failed open must also drop the
	// "--seccomp" flag from argv, not just skip attaching ExtraFiles, or bwrap
	// reads a nonexistent fd 3 at startup and the whole Box launch fails over
	// a hardening nicety.
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
	program, execArgs, childExecsByName := a.execTarget(etcDir, box)
	if syscallFilterOpenFailed {
		// Strip the flag out of the flattened argv rather than mutate a
		// shallow copy of *a: that would copy the live sync.Mutex (go vet's
		// copylocks, and a real hazard since Run executes concurrently for
		// different boxes under MAX_PARALLEL).
		execArgs = removeSeccompFlag(execArgs)
	}
	cmd := execCommand(program, execArgs...)
	// Pdeathsig kills this direct child (bwrap or pasta) the moment the
	// launcher dies, so a crashed launcher never leaves an orphaned Box.
	// Separate from bwrap's own --die-with-parent, which only protects bwrap
	// against ITS immediate parent -- pasta, in the fork case, not the
	// launcher two hops up. setDeathSignal is a platform-split seam: Pdeathsig
	// is Linux-only and the launcher must still cross-compile for darwin.
	setDeathSignal(cmd)
	cmd.Env = resolvedRunEnv(box.Env)
	if childExecsByName {
		// The wrapper execs its child by bare name via execvp, using its own
		// environment's PATH -- and resolvedRunEnv is a secrets-only allowlist
		// carrying none, so without this the child exec fails with ENOENT even
		// though the wrapper started fine. PATH carries no secret, so
		// forwarding it doesn't widen the no-ambient-leak guarantee.
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	}
	if syscallFilterFile != nil {
		cmd.ExtraFiles = []*os.File{syscallFilterFile}
	}
	cmd.Stdout = out
	cmd.Stderr = out

	// nixVarSnapshotLock, when non-nil, holds a shared advisory flock on the
	// mounted generation's lock file for the life of the sandboxed process;
	// reclaimStaleSnapshots probes it exclusively to tell whether any live Box
	// still reads this generation, so no box->generation mapping is tracked
	// anywhere else. The acquire is blocking, so Run waits out a concurrent
	// reclaim -- but open and lock are two steps, leaving a window where
	// reclaim wins and RemoveAlls the generation dir in between. The re-stat
	// below closes it: exec'ing bwrap against a removed directory would break
	// --overlay-src's mount. Failing to open or lock at all only degrades
	// reclaim's ability to detect this box, so that path warns and proceeds.
	var nixVarSnapshotLock *os.File
	if a.nixConfigFile != "" {
		// Same call buildArgs' --overlay-src bind uses, so the lock/stat below
		// guards the exact directory this launch mounts.
		snapshotDir := a.snapshotDirFor(box)
		lf, err := lockSnapshotShared(snapshotDir)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); reclaim cannot detect box %q is reading this generation\n", snapshotLockPath(snapshotDir), err, box.Name)
		} else if _, statErr := os.Stat(snapshotDir); statErr != nil {
			unlockSnapshot(lf)
			if cgroupOK {
				_ = os.Remove(cgroupDir)
			}
			return fmt.Errorf("nix-var snapshot %s no longer exists (reclaimed by a concurrent build?): %w", snapshotDir, statErr)
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
		// Best-effort: the box process is already running, so a failure to
		// move it in must not fail Run -- it just means this Box runs outside
		// cgroup enforcement despite delegation being available.
		pid := strconv.Itoa(cmd.Process.Pid)
		if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not move box %q into cgroup %s: %v\n", box.Name, cgroupDir, err)
		}
	}
	a.trackRunning(box.Name, cmd.Process)
	defer a.untrackRunning(box.Name)
	// Deferred so the shared lock spans cmd.Wait()'s entire duration:
	// releasing it earlier would let reclaimStaleSnapshots believe this
	// generation is free while the sandboxed process is still reading it. A
	// launcher crash releases it automatically (fd closes on process exit),
	// so no separate crash-recovery path is needed.
	defer unlockSnapshot(nixVarSnapshotLock)
	if cgroupOK {
		// Deferred so cleanup runs after cmd.Wait() returns -- the cgroup dir
		// can only be rmdir'd once no live process remains inside it (ADR
		// 0042's strictly-ephemeral posture).
		defer func() {
			if err := removeCgroupDir(cgroupDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove cgroup %s: %v\n", cgroupDir, err)
			}
		}()
	}
	return asRunError(cmd.Wait())
}

// trackRunning records proc as the live process for name, so a concurrent
// Kill call can find it. A blank name is tracked like any other; Kill would
// then reach whichever box last ran nameless, which never happens in
// production (box.Name is always set).
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

// Reap best-effort removes a leftover per-Box delegated cgroup dir for name,
// e.g. one orphaned by a launcher that crashed before Run's deferred cleanup
// could rmdir it -- including one left by a different launcher invocation,
// since it resolves via findCgroupDir. It never touches a running sandbox;
// Kill is the operator-driven counterpart, per the Runner.Reap contract. No
// cgroup dir, no cgroup v2 tree, and any removal failure all return nil.
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
	// The process can finish between the map lookup and this Kill call --
	// os.ErrProcessDone is a miss like any other, not an error.
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// IsRunning reports whether a Box's per-name delegated cgroup still has a
// resident PID in its cgroup.procs. Best-effort and read-only: no cgroup v2
// tree, or no cgroup dir for this name, both degrade to false. Never warns,
// unlike the rest of ADR 0042's warn-and-proceed tiering -- a poll loop would
// make that noisy, and provisionCgroup already warns once at launch time.
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
// cgroupFSRoot -- not just the calling process's own delegated subtree -- and
// reports the subset that are actually live, so Console startup orphan
// detection finds Boxes started by a prior launcher invocation. No cgroup v2
// tree degrades to a nil slice and no error. Liveness comes from each
// candidate's own cgroup.procs, so a stale empty dir is excluded.
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
	// empty when nixInBox is off, which gates both the nix-config closure
	// realization and the store-DB snapshot step below together.
	nixConfigFileDrv string
	// syscallFilterDrv is the .drv path for the compiled BPF syscall filter.
	// Unconditional in production, but guarded like nixConfigFileDrv so a
	// zero-value adapter never tries to realize an empty drv path.
	syscallFilterDrv string
	// nixVarSnapshotDir is the host-side directory snapshotStoreDB writes
	// into (see nixVarSnapshotDir's doc comment).
	nixVarSnapshotDir string
	// nixVarSnapshotRoot and nixVarGeneration are the same pwd/generation
	// nixVarSnapshotDir was built from, kept as their own fields rather than
	// re-derived via filepath.Dir/Base so reclaimStaleSnapshots' arguments can
	// never be misidentified by path surgery on the already-joined dir.
	nixVarSnapshotRoot string
	nixVarGeneration   string
}

// NewBwrapBuild constructs a bwrap adapter for the build command from cfg and
// pwd (the launcher's own working directory). EnsureReady realizes agent
// store closures via nix build and, when nixInBox is on, snapshots the host
// nix store DB.
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
// realizes it from; the label names the failing closure in the wrapped error.
type closureSpec struct {
	label string
	drv   string
}

// EnsureReady realizes the agent store closures via nix build; nix is
// idempotent, so an already-realized closure is fast. When nixConfigFileDrv
// is set (nixInBox on), it also realizes the nix-config closure and snapshots
// the host nix store DB (ADR 0042) for the run adapter's overlay to mount.
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
		// Hold the same shared lock Run holds, here only for the write below: a
		// concurrent `launcher build` against a different closure skips only
		// *its own* keepGeneration, so from its point of view this dir looks
		// like any other unreferenced stale generation and its exclusive Flock
		// probe would RemoveAll it out from under the VACUUM INTO. A failure
		// to acquire only degrades protection against that narrow race and
		// must never fail the build.
		//
		// The lock file is a sibling of dir, so its parent must exist for
		// O_CREATE to succeed and on a fresh checkout nixVarSnapshotRoot does
		// not yet. MkdirAll first so the lock gets a real shot instead of
		// always degrading on the first build.
		if mkErr := os.MkdirAll(a.nixVarSnapshotRoot, 0o755); mkErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not create nix-var snapshot root %s (%v); a concurrent build cannot detect this generation is mid-write\n", a.nixVarSnapshotRoot, mkErr)
		}
		lf, lockErr := lockSnapshotShared(a.nixVarSnapshotDir)
		if lockErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); a concurrent build cannot detect this generation is mid-write\n", snapshotLockPath(a.nixVarSnapshotDir), lockErr)
		}
		err := a.snapshotStoreDB()
		unlockSnapshot(lf)
		if err != nil {
			return err
		}
		// nixVarGeneration == "" means the flat path: nixVarSnapshotDir IS the
		// root, so there are no sibling generations to sweep -- reclaiming
		// here would sweep the root's unrelated siblings (e.g.
		// .spindrift/accum.git) as if they were stale generations.
		if a.nixVarGeneration == "" {
			fmt.Println("==> bwrap runner: nix-var snapshot is unscoped (no closure generation known); skipping stale-generation reclaim")
		} else if err := reclaimStaleSnapshots(a.nixVarSnapshotRoot, a.nixVarGeneration); err != nil {
			// Best-effort: an unreclaimed old generation wastes disk but leaves
			// the snapshot just produced perfectly usable.
			fmt.Printf("==> bwrap runner: warning: could not reclaim stale nix-var snapshots under %s: %v\n", a.nixVarSnapshotRoot, err)
		}
	}

	fmt.Println("==> done: agent store closures realized")
	return nil
}

// SnapshotGeneration is the hot-swap counterpart to EnsureReady's build-time
// snapshot step: a hot-swap (ADR 0043) realizes a new agent closure mid-run
// without going through a bwrapBuildAdapter at all, so nothing has written a
// generation for it until this runs. Call it once per successful swap, after
// the closure is realized and before binding it (NewAgentGeneration) onto
// subsequent Box launches -- IsReady/Run's stat guard fails any Box naming a
// generation with no directory on disk. closurePath is the same store path
// NewAgentGeneration derives from, and generation is derived the identical
// way, so both name the same directory.
//
// Unlike EnsureReady, this never calls reclaimStaleSnapshots -- reclaim stays
// build-time-only. A Dispatch can Run a Box, finish it, and sit idle for
// minutes waiting on CI before launching another against the same generation;
// no flock is held during that gap and nothing tracks which generations an
// idle Dispatch references, so reclaiming here could delete one still needed.
// A hot-swapped run therefore accumulates generation directories until the
// next `launcher build` — ADR 0043's Consequences section records this as an
// accepted divergence, not an oversight.
//
// Idempotent per closure: a generation dir may already be
// --overlay-src-mounted by a live Box (e.g. a revert commit swapping back to
// a previously-seen closure), and vacuumStoreDBInto renames the existing
// db.sqlite aside, mutating a file a running Box is reading (forbidden by ADR
// 0043) -- so an existing db.sqlite skips the vacuum entirely.
//
// Failing to create the root or take the lock only degrades a concurrent
// build's ability to detect this generation is mid-write (warn, continue),
// but a vacuumStoreDBInto failure means the generation genuinely doesn't
// exist and propagates.
func SnapshotGeneration(pwd, closurePath string) error {
	generation := closureGeneration(closurePath)
	dir := nixVarSnapshotDir(pwd, generation)
	root := nixVarSnapshotRoot(pwd)

	dest := filepath.Join(dir, "nix", "db", "db.sqlite")
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("==> bwrap runner: nix-var snapshot for generation %s already exists; skipping vacuum\n", generation)
		return nil
	}

	fmt.Printf("==> bwrap runner: snapshotting host nix store DB (VACUUMed) for hot-swapped generation %s\n", generation)

	if mkErr := os.MkdirAll(root, 0o755); mkErr != nil {
		fmt.Printf("==> bwrap runner: warning: could not create nix-var snapshot root %s (%v); a concurrent build cannot detect this generation is mid-write\n", root, mkErr)
	}
	lf, lockErr := lockSnapshotShared(dir)
	if lockErr != nil {
		fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); a concurrent build cannot detect this generation is mid-write\n", snapshotLockPath(dir), lockErr)
	}
	err := vacuumStoreDBInto(dir)
	unlockSnapshot(lf)
	return err
}

// hostNixDBPath is the host's real, live nix store database — never a path
// inside a sandbox. snapshotStoreDB runs during `launcher build`, on the
// operator's (or CI job's) own machine.
const hostNixDBPath = "/nix/var/nix/db/db.sqlite"

// snapshotStoreDB copies the host's live nix store database into
// a.nixVarSnapshotDir, compacting it in the same step (ADR 0042: ~302MB raw
// vs ~104MB compacted — an overlay copy-up rewrites a file whole, so the
// compacted size is what lands in the Box's tmpfs upper on first touch). This
// is the one Go call site in the launcher that reaches into the host's live
// nix store metadata rather than a realized artifact. No chown is needed for
// ADR 0042's "agent-owned" requirement: "VACUUM INTO" always creates a fresh
// file owned by the invoking process, whatever hostNixDBPath's own owner is.
func (a *bwrapBuildAdapter) snapshotStoreDB() error {
	fmt.Println("==> bwrap runner: snapshotting host nix store DB (VACUUMed)")
	return vacuumStoreDBInto(a.nixVarSnapshotDir)
}

// vacuumStoreDBInto does the VACUUM INTO/backup-rename work behind both
// snapshotStoreDB (once per `launcher build`) and SnapshotGeneration (once
// per hot-swap), over an explicit dir rather than a bwrapBuildAdapter's own
// baked field. Writes dir/nix/db/db.sqlite, the destination layout every
// nixVarSnapshotDir caller expects.
func vacuumStoreDBInto(dir string) error {
	if err := statHostNixDB(); err != nil {
		return fmt.Errorf("host nix store db not found at %s: %w", hostNixDBPath, err)
	}

	dest := filepath.Join(dir, "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir nix-var-snapshot: %w", err)
	}

	// "VACUUM INTO" uses sqlite's online-backup mechanism internally, so it
	// handles a concurrent host nix-daemon write (WAL journal not yet
	// checkpointed) that a plain file copy could snapshot mid-write into a
	// truncated database — and compacts in the same step. dest is escaped for
	// a single-quoted SQL string literal; a dot-command would not survive a
	// space in dest (they are whitespace-tokenized), but a SQL statement
	// passed as sqlite3's third argv element does.
	//
	// "VACUUM INTO" refuses to run if dest already exists, so move any
	// existing snapshot aside rather than deleting it: if the vacuum then
	// fails (disk full, host db locked), the rename back below restores the
	// previously-working snapshot instead of leaving `launcher run` unable to
	// start from one destroyed for nothing.
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
		// Best-effort: a leftover .bak wastes disk but leaves the snapshot
		// just written to dest perfectly usable.
		if err := os.Remove(backup); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not remove backup snapshot %s: %v\n", backup, err)
		}
	}
	return nil
}

// reclaimStaleSnapshots removes generation directories under root that are
// neither keepGeneration (the one this build produced) nor still referenced
// by a live Box. Liveness is detected via Run's own shared lock: a
// non-blocking exclusive Flock on the sibling snapshotLockPath fails while
// any Box holds it, so no box->generation tracking is needed elsewhere. This
// loop removes only the generation dir, never its lock file -- that outlives
// its dir by at least one pass and is removed by sweepOrphanedLock. A root
// that doesn't exist yet is not an error, and every other step is
// best-effort per entry.
func reclaimStaleSnapshots(root, keepGeneration string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read nix-var-snapshot root %s: %w", root, err)
	}
	// Captured before any removal in this pass so sweepOrphanedLock can tell
	// "orphaned before this pass started" (safe to remove its lock) from
	// "this pass just reclaimed it" (must not). Directory entries sort before
	// their "<name>.lock" sibling, so a live os.Stat after removal would
	// always see the former as gone.
	knownGenerations := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			knownGenerations[entry.Name()] = true
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			sweepOrphanedLock(root, entry.Name(), keepGeneration, knownGenerations)
			continue
		}
		name := entry.Name()
		if name == keepGeneration {
			continue
		}
		genDir := filepath.Join(root, name)
		lockPath := snapshotLockPath(genDir)
		lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not open nix-var snapshot lock %s (%v); leaving stale generation %s in place\n", lockPath, err, name)
			continue
		}
		lockRaceWindowHook()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			// A live Box holds the shared lock -- leave this generation alone.
			lf.Close()
			continue
		}
		// Only remove if lf still identifies what sits at lockPath -- see
		// lockedFDMatchesPath.
		if lockedFDMatchesPath(lf, lockPath) {
			if err := os.RemoveAll(genDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove stale nix-var snapshot %s: %v\n", genDir, err)
			}
		}
		unlockSnapshot(lf)
	}
	return nil
}

// sweepOrphanedLock removes entryName from root if it's a "<generation>.lock"
// file whose generation dir was already absent from knownGenerations; without
// it these accumulate forever, since the main loop only considers directory
// entries. knownGenerations, not a live os.Stat, is what makes this safe: a
// live check would also treat "this pass just RemoveAll'd it" as orphaned,
// deleting a lock file whose generation was live at the start of the pass and
// breaking mutual exclusion for anyone holding it. Best-effort and silent on
// failure: a leftover lock file costs disk, never correctness.
func sweepOrphanedLock(root, entryName, keepGeneration string, knownGenerations map[string]bool) {
	generation, ok := strings.CutSuffix(entryName, ".lock")
	if !ok || generation == keepGeneration {
		return
	}
	if knownGenerations[generation] {
		return // generation dir was present at the start of this pass -- not orphaned
	}
	lockPath := filepath.Join(root, entryName)
	lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	lockRaceWindowHook()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Still referenced (e.g. Run is mid-race, about to discover its
		// generation dir is gone) -- leave it for a later reclaim pass.
		lf.Close()
		return
	}
	// Only remove if lf still identifies what sits at lockPath -- see
	// lockedFDMatchesPath.
	if lockedFDMatchesPath(lf, lockPath) {
		_ = os.Remove(lockPath)
	}
	unlockSnapshot(lf)
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
