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

	"spindrift.dev/launcher/internal/registrymanifest"
)

// execCommand is the test seam for hardcoded-binary orchestration (nix, bwrap)
// that has no configurable CLI field to intercept.
var execCommand = exec.Command

// statResolvConf is the test seam for buildArgs' "does the host have
// /etc/resolv.conf to bind" check, so the resolv.conf-bind assertions do not
// depend on the test machine having that file (some nix build sandboxes do not).
var statResolvConf = func() error {
	_, err := os.Stat("/etc/resolv.conf")
	return err
}

// statHostNixDB is the test seam for snapshotStoreDB's hostNixDBPath preflight,
// so the missing-host-db assertion does not depend on the test machine having a
// real /nix/var/nix/db/db.sqlite.
var statHostNixDB = func() error {
	_, err := os.Stat(hostNixDBPath)
	return err
}

// lockRaceWindowHook runs between the os.OpenFile that opens a lock path and the
// syscall.Flock that follows, so a test can deterministically swap the path's
// inode inside the window lockedFDMatchesPath guards (issue #3005). No-op in
// production.
var lockRaceWindowHook = func() {}

// cgroupProvisionRaceWindowHook runs in Run between cmd.Start() and the
// cgroup.procs write, so a test can land a Reap inside the provisioning race
// window (see bwrapAdapter's provisioning field). No-op in production.
var cgroupProvisionRaceWindowHook = func() {}

// readSelfCgroup returns the launcher's own cgroup v2 path from
// /proc/self/cgroup's unified-hierarchy line ("0::<path>"). Tests swap this seam
// because /proc/self/cgroup is not writable in a test sandbox.
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

// cgroupFSRoot is the host's cgroup v2 mountpoint. Tests reassign it to a
// t.TempDir() to fake a writable delegated subtree.
var cgroupFSRoot = "/sys/fs/cgroup"

// writeCgroupLimit is os.WriteFile, swappable in tests to fail a single limit
// write (pids.max or memory.max). Scoped to the limit writes only: Run's
// cgroup.procs write stays a real os.WriteFile so tests can assert the PID
// actually landed.
var writeCgroupLimit = os.WriteFile

// homeAgentStagingDir is the in-box path bwrap ro-binds agentFiles' baked
// /home/agent subtree onto (issue #2843). It must be a fresh top-level path, not
// nested under /agent: /agent is already bound read-only by the time this mount
// is added, and bwrap cannot create a mountpoint inside a read-only bind.
const homeAgentStagingDir = "/home-agent-staged"

// offArgvKeys names the box.Env keys whose values must never appear on a runner
// process's argv: ps and /proc/<pid>/cmdline expose argv to any local user for
// the Box's whole lifetime, while the process's own environment is readable only
// by its owner. The test is "must never appear on argv", not "is a credential":
// ISSUE_TEXT is a private issue body, sensitive without being a secret.
var offArgvKeys = map[string]bool{
	"GH_TOKEN":                true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":       true,
	"OPENCODE_AUTH_CONTENT":   true,
	// Registry-proxy TCP fallback secret, a bearer-token-shaped credential
	// (issue #3111).
	"REGISTRY_PROXY_TCP_SECRET": true,
	"FORGEJO_TOKEN":             true,
	// The subject issue's injected body and comments (issue #3445). ADR 0032
	// keeps local issues private, and a private-repo GitHub body is sensitive.
	"ISSUE_TEXT": true,
}

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady delegates to IsReady rather than realizing anything itself, since
// the build command realizes the store closures.
type bwrapAdapter struct {
	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	passwdFile    string // baked nix store path for /etc/passwd
	groupFile     string // baked nix store path for /etc/group
	bakedPrefetch string // baked prefetch snippet fed to the entrypoint
	// nixConfigFile is the baked store path for /etc/nix/nix.conf (ADR 0042),
	// empty when the Consumer's nixInBox knob is off. That emptiness gates this
	// mount and nixVarSnapshotDir's together, since nix is not on PATH either
	// way. nixVarSnapshotDir stands in for /nix/var's overlay lower; it is always
	// computed, so IsReady checks its presence on disk, not its emptiness.
	nixConfigFile     string
	nixVarSnapshotDir string
	// nixVarSnapshotRoot is the pwd-derived root snapshotDirFor joins a
	// per-launch generation onto (box.ClosureGeneration, issue #2681).
	nixVarSnapshotRoot string
	// nixStoreWritable gives /nix/store the same overlay treatment as /nix/var
	// (ADR 0042): an ephemeral tmpfs upper, so paths built in the Box vanish with
	// the sandbox instead of touching host disk. buildArgs AND-gates it with
	// nixConfigFile, so true here alone does nothing when nixInBox is off.
	nixStoreWritable bool
	// mountParams passes this run's host-mount facts from Config to
	// buildMountSpecs unmodified. DriverSessionCacheDir is ADR 0009; the
	// CODE_FORGE=local mount specs are issue #1697.
	mountParams MountParams
	unshareNet  bool   // raw BWRAP_UNSHARE_NET knob; redundant with isolate-by-default, kept for defense in depth
	networkMode string // NETWORK_MODE knob; every value except the "host" opt-out isolates from the host netns (issue #2666)
	// pidsLimit is the PIDS_LIMIT knob (empty disables it). bwrap imposes no
	// process-count cap of its own, so the per-Box cgroup v2 pids.max control
	// file is the only containment mechanism (ADR 0042, provisionCgroup).
	pidsLimit string
	// memoryLimit is the MEMORY_LIMIT knob (empty disables it). It backs the
	// per-Box cgroup's memory.max control file (ADR 0042, provisionCgroup)
	// rather than any bwrap flag, and memory.max takes a raw byte count
	// (memoryLimitToBytes), unlike podman's --memory which accepts the suffix.
	memoryLimit string

	// syscallFilterPath is the baked store path to the compiled BPF syscall
	// filter (issue #2670). When non-empty, buildArgs emits "--seccomp
	// <seccompFilterFD>" and Run attaches the file via cmd.ExtraFiles. A failed
	// open at Run time warns and proceeds without the filter rather than
	// refusing to launch the Box (ADR 0042's degrade-don't-lie posture).
	syscallFilterPath string

	// mu guards running, the box-name to live-process map Kill consults (issue
	// #649). A bwrap sandbox is an unnamed child process with no daemon to query
	// by name, so Run tracks its own handle here for Terminate, the one caller
	// that reaches a live process from outside Run's goroutine.
	mu      sync.Mutex
	running map[string]*os.Process

	// provisioning refcounts box names between Run's beginProvisioning call and
	// the matching release (issue #2960): the window where a per-Box cgroup dir
	// exists but IsRunning still reads false, so a Reap landing there would
	// delete a mid-launch Box's dir. Guarded by mu; refcounted, not a set, so
	// two concurrent Runs for one name cannot release each other's guard.
	provisioning map[string]int
}

// nixVarSnapshotDir is the host-side directory standing in for /nix/var inside a
// bwrap Box's overlay lower (ADR 0042): the build command writes a VACUUMed
// snapshot of /nix/var/nix/db/db.sqlite to <dir>/nix/db/db.sqlite so the Box's
// nix trusts host-present store paths. generation scopes it to one agent-closure
// (issue #2680); empty means the flat pre-#2680 path.
func nixVarSnapshotDir(pwd, generation string) string {
	return filepath.Join(nixVarSnapshotRoot(pwd), generation)
}

// nixVarSnapshotRoot is the directory nixVarSnapshotDir nests generation subdirs
// under, and the sweep root reclaimStaleSnapshots RemoveAlls entries of. Derive
// it from pwd, never by filepath.Dir on an already-joined nixVarSnapshotDir:
// with an empty generation the flat path IS the snapshot dir, so its parent is
// .spindrift, home to unrelated siblings a sweep must never touch (issue #2680).
func nixVarSnapshotRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-var-snapshot")
}

// closureGeneration derives nixVarSnapshotDir's generation subdir from a
// bwrap-runtime Config.ImageTag. imageTag comes from an environment variable an
// untrusted source can influence and becomes a path component
// reclaimStaleSnapshots later os.RemoveAlls, hence safePathComponent. A rejected
// non-empty imageTag warns: it would otherwise silently disable stale reclaim.
func closureGeneration(imageTag string) string {
	gen := safePathComponent(imageTag)
	if imageTag != "" && gen == "" {
		fmt.Printf("==> bwrap runner: warning: generation source %q is not a usable generation label; falling back to the legacy flat nix-var snapshot path (stale-generation reclaim disabled)\n", truncateGenerationLabel(imageTag))
	}
	return gen
}

// generationLabelWarnLimit bounds how much of a rejected generation source
// closureGeneration's warning echoes. The value is untrusted text with no length
// bound of its own, so printing it whole would let a hostile one emit an
// arbitrarily long line into the launcher's log.
const generationLabelWarnLimit = 256

// truncateGenerationLabel cuts s on a rune boundary so the tail cannot become a
// mangled half-rune that %q would escape as \xNN noise.
func truncateGenerationLabel(s string) string {
	if len(s) <= generationLabelWarnLimit {
		return s
	}
	r := []rune(s)
	if len(r) <= generationLabelWarnLimit {
		return s
	}
	return string(r[:generationLabelWarnLimit]) + "..."
}

// NewAgentGeneration derives an AgentGeneration from closure, the just-realized
// tip agent-closure linkFarm's own store output path (issue #2682), NOT the
// agentFiles derivation. That linkFarm nests four children, so AgentFiles,
// AgentEnv, NixConfigFile and PrefetchFile are closure's "files", "env",
// "nix-config" and "prefetch" children (lib/mkHarness.nix, issue #2954).
func NewAgentGeneration(closure string) AgentGeneration {
	return AgentGeneration{
		AgentFiles:    filepath.Join(closure, "files"),
		AgentEnv:      filepath.Join(closure, "env"),
		NixConfigFile: filepath.Join(closure, "nix-config"),
		PrefetchFile:  filepath.Join(closure, "prefetch"),
		Generation:    safePathComponent(closure),
	}
}

// safePathComponent returns s unless it is unsafe as a single path component
// (empty, ".", "..", or a bare separator), in which case it returns "" so the
// caller falls back to a known-safe default. Shared by closureGeneration and
// snapshotDirFor (issue #2681) so both rejection paths stay in lockstep.
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

// snapshotLockPath spells the "<generation dir>.lock" convention: a sibling of
// the generation dir, never a file inside it, since buildArgs --overlay-src
// binds that dir into the sandbox and a lock file inside it would risk being
// swept up by the mount.
func snapshotLockPath(dir string) string {
	return dir + ".lock"
}

// lockSnapshotShared opens (creating if needed) and takes a blocking shared
// advisory flock on dir's snapshotLockPath. The path's inode is not stable: a
// concurrent sweepOrphanedLock can remove it between this open and the Flock,
// leaving a lock on an inode nothing resolves to, so verify identity afterwards
// (lockedFDMatchesPath) and retry against a fresh fd, bounded (issues #2680, #3005).
func lockSnapshotShared(dir string) (*os.File, error) {
	const maxAttempts = 100
	path := snapshotLockPath(dir)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		lockRaceWindowHook()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
			lf.Close()
			return nil, err
		}
		if lockedFDMatchesPath(lf, path) {
			return lf, nil
		}
		// lf's lock protects an inode nothing resolves to anymore. Drop it and
		// retry against whatever is at the path now.
		unlockSnapshot(lf)
	}
	return nil, fmt.Errorf("lockSnapshotShared: %s kept changing identity after locking across %d attempts", path, maxAttempts)
}

// lockedFDMatchesPath reports whether lf, an fd a caller just flocked, still
// identifies whatever currently sits at path. A successful flock proves the fd
// is locked, not that it still names path: a concurrent remove/recreate can win
// the window between the caller's open and its flock (issues #2680, #3005). Any
// stat failure counts as a mismatch.
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

// unlockSnapshot releases lf's flock and closes it; a nil lf is a no-op so
// callers can defer this unconditionally.
func unlockSnapshot(lf *os.File) {
	if lf == nil {
		return
	}
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	lf.Close()
}

// NewBwrap constructs a bwrap adapter for the run command from cfg and pwd (the
// launcher's own working directory); call NewBwrapBuild for the build command.
// By default (any cfg.NetworkMode except the "host" opt-out, issue #2666) the
// adapter isolates the sandbox into its own network namespace, with egress
// restored via a hardened pasta helper (ADR 0042), matching rootless podman.
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

// EnsureReady builds nothing for bwrap run: `launcher build` realizes the store
// closures first. It delegates to IsReady so the actionable snapshot-missing
// error fires on the default run/dispatch path too, not only on `--no-build`
// (issue #2664).
func (a *bwrapAdapter) EnsureReady() error { return a.IsReady() }

// IsReady checks that the nix-in-box snapshot `launcher build` writes is present
// when the Consumer's nixInBox knob is on (ADR 0042). Scoped to that one gap
// (issue #2664): buildArgs conditions no other mount on absence, so without this
// a missing snapshot surfaces only as a raw bwrap overlay mount failure.
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
	return buildMountSpecs(a.mountParams, box)
}

// RegistryProxyTransport always reports a unix Endpoint for bwrap: an
// unprivileged bwrap sandbox shares enough of the host mount namespace that a
// bind-mounted unix socket is always connectable from the guest, so there is
// nothing to probe (issue #3111). The Endpoint's path is unset; the caller mints
// the real per-Box socket path once it knows the transport decision.
func (a *bwrapAdapter) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	return registrymanifest.NewUnixEndpoint(""), false, nil
}

// isolateNet is the effective "cut off the host netns" decision (issue #2666,
// ADR 0042): every NetworkMode except the explicit "host" opt-out isolates,
// including the Go zero value and "no-host-loopback". The raw BwrapUnshareNet
// knob can only force isolation on, already the default, and is kept for defense
// in depth.
func (a *bwrapAdapter) isolateNet() bool {
	return a.unshareNet || a.networkMode != NetworkModeHost
}

// pastaPath reports whether the isolated network namespace gets egress restored
// via a pasta helper. networkMode="none" is the exception: fully offline, bare
// --unshare-net, no helper, no egress at all.
func (a *bwrapAdapter) pastaPath() bool {
	return a.isolateNet() && a.networkMode != NetworkModeNone
}

// pick returns override when non-empty, else baked. A ClosureGeneration with an
// empty field (a partially-populated override) falls back to baked rather than
// returning "": every caller appends a path segment, and an empty base would
// point at the sandbox's own root instead of a store closure (issue #2682).
func pick(override, baked string) string {
	if override != "" {
		return override
	}
	return baked
}

// agentFilesFor resolves the agent-closure store path box's launch should bind.
// Callers append "/agent" and "/home/agent" to the result.
func (a *bwrapAdapter) agentFilesFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.agentFiles
	}
	return pick(box.ClosureGeneration.AgentFiles, a.agentFiles)
}

// agentEnvFor resolves the agentEnv store path box's launch should bind for
// PATH/SSL_CERT_FILE/GIT_SSL_CAINFO. Callers append "/bin" and
// "/etc/ssl/certs/ca-bundle.crt" to the result.
func (a *bwrapAdapter) agentEnvFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.agentEnv
	}
	return pick(box.ClosureGeneration.AgentEnv, a.agentEnv)
}

// nixConfigFileFor resolves the nix.conf store path box's launch binds at
// /etc/nix/nix.conf.
func (a *bwrapAdapter) nixConfigFileFor(box Box) string {
	if box.ClosureGeneration == nil {
		return a.nixConfigFile
	}
	return pick(box.ClosureGeneration.NixConfigFile, a.nixConfigFile)
}

// prefetchFor resolves the PREFETCH value box's launch should --setenv.
// Deliberately not pick: PrefetchFile is a path, not the value, and prefetch's
// empty string is a legitimate swapped value (lib/mkHarness.nix's prefetch ? ""),
// not "unset". A read error falls back to a.bakedPrefetch with one diagnostic
// line, since an unreadable swapped prefetch child must never take down a Box.
func (a *bwrapAdapter) prefetchFor(box Box) string {
	if box.ClosureGeneration == nil || box.ClosureGeneration.PrefetchFile == "" {
		return a.bakedPrefetch
	}
	content, err := os.ReadFile(box.ClosureGeneration.PrefetchFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "==> bwrap hot-swap: prefetch file %q unreadable, falling back to baked value: %v\n", box.ClosureGeneration.PrefetchFile, err)
		return a.bakedPrefetch
	}
	return string(content)
}

// snapshotDirFor resolves the nix-var snapshot directory box's launch overlays
// onto /nix/var and locks for its lifetime (issue #2681). An empty or unsafe
// Generation falls back to the baked dir: joining a raw "" resolves to the root
// itself, which has no db.sqlite, and ".." escapes it. Joins onto
// a.nixVarSnapshotRoot directly, since nixVarSnapshotDir would re-append it.
func (a *bwrapAdapter) snapshotDirFor(box Box) string {
	if box.ClosureGeneration != nil {
		if gen := safePathComponent(box.ClosureGeneration.Generation); gen != "" {
			return filepath.Join(a.nixVarSnapshotRoot, gen)
		}
	}
	return a.nixVarSnapshotDir
}

// buildArgs constructs the bwrap command-line arguments for box. Secret env vars
// stay off argv and reach the sandbox via the inherited process environment (no
// --clearenv); see offArgvKeys. etcDir is only still needed for the synthesised
// /etc/resolv.conf on the pasta path, since passwd/group are baked store paths
// (issue #2663). Pasta is never part of this return value; see execTarget.
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
	// An empty nixConfigFile means nixInBox is off and nix is not on PATH, so
	// both nix mounts below are skipped together. When set, --overlay-src plus
	// --tmp-overlay gives a read-only lower (the VACUUMed db.sqlite snapshot,
	// ADR 0042) under an ephemeral tmpfs upper, so nix's own writes in the Box
	// (gcroots, profiles, WAL files) vanish instead of touching host disk.
	if a.nixConfigFile != "" {
		args = append(args, "--ro-bind", a.nixConfigFileFor(box), "/etc/nix/nix.conf")
		args = append(args, "--overlay-src", a.snapshotDirFor(box), "--tmp-overlay", "/nix/var")
	}
	if !isolateNet {
		if err := statResolvConf(); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	} else if a.pastaPath() {
		// Nothing else writes /etc/resolv.conf inside the guest, unlike the OCI
		// runner where podman supplies one; Run writes this file pointed at
		// pastaDNSForwardAddr before invoking bwrap.
		args = append(args, "--ro-bind", filepath.Join(etcDir, "resolv.conf"), "/etc/resolv.conf")
	}
	agentFiles := a.agentFilesFor(box)
	args = append(args, "--ro-bind", agentFiles+"/agent", "/agent")
	// The real /home/agent is a fresh writable tmpfs, so baked content (Claude
	// hooks, settings.json, opencode agent files) cannot be ro-bound there.
	// Stage it at a fresh top-level path instead: bwrap processes --ro-bind in
	// argv order and cannot fabricate a mountpoint inside the /agent bind made
	// read-only above (issue #2843). entrypoint.sh copies it in at startup.
	args = append(args, "--ro-bind", agentFiles+"/home/agent", homeAgentStagingDir)
	// buildMountSpecs computes the mount decisions (gates, existence guards,
	// operator messages) once and shares them with the OCI adapter; bwrap only
	// renders each spec into bind syntax. The driver-cache spec (issue #427) and
	// the CODE_FORGE=local outbox spec (ADR 0033, issue #1697) are the only
	// writable mounts it ever produces.
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
	// --clearenv is deliberately absent: the offArgvKeys subset of box.Env
	// reaches the sandbox through resolvedRunEnv, which Run sets as cmd.Env and
	// bwrap inherits. See offArgvKeys for why those keys stay off argv.
	agentEnv := a.agentEnvFor(box)
	args = append(args,
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.prefetchFor(box),
	)
	for k, v := range box.Env {
		if !offArgvKeys[k] {
			args = append(args, "--setenv", k, v)
		}
	}
	// bwrap is PID 1 in its own unshared PID namespace, so --die-with-parent's
	// PR_SET_PDEATHSIG kills the whole sandbox when bwrap's immediate OS parent
	// dies (issue #2669): the launcher on the direct-exec chain, but only pasta
	// on the pasta chain (see setDeathSignal's call site in Run).
	unshareFlags := []string{"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent"}
	// bwrap only unshares net itself for the fully offline networkMode=none case.
	// Every other isolating mode leaves that to pasta, which creates and
	// configures the fresh namespace (tap device, routes) for its COMMAND and
	// then execs it there; bwrap must inherit that namespace rather than
	// re-unshare a second, empty one on top of it (issue #2666).
	if isolateNet && !a.pastaPath() {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	// --seccomp only ever names seccompFilterFD, the one fd Run's cmd.ExtraFiles
	// attaches (issue #2670); an empty syscallFilterPath skips the flag.
	if a.syscallFilterPath != "" {
		args = append(args, "--seccomp", strconv.Itoa(seccompFilterFD))
	}
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// execTarget computes the top-level host-exec'd program and argv for box's bwrap
// invocation; pasta must be the outer process when pastaPath applies (see
// buildArgs' unshare-net comment). childExecsByName is true when that program
// execs its own child by bare argv name via execvp, so Run must forward a PATH:
// Go's exec.Command LookPath only ever resolves the top-level program itself.
func (a *bwrapAdapter) execTarget(etcDir string, box Box) (string, []string, bool) {
	bwrapArgs := a.buildArgs(etcDir, box)
	var program string
	var args []string
	if !a.pastaPath() {
		program, args = "bwrap", bwrapArgs
	} else {
		pastaArgs := append([]string{}, pastaHardenedFlags...)
		pastaArgs = append(pastaArgs, "--dns-forward", pastaDNSForwardAddr,
			// -f/--foreground is load-bearing: pasta's default is to fork into
			// the background and detach once the namespace is set up. Without
			// it, cmd.Start()/cmd.Wait() would track pasta's short-lived
			// detaching parent instead of the real bwrap child, breaking exit
			// codes, output capture, and the a.running process map.
			"-f", "--", "bwrap")
		pastaArgs = append(pastaArgs, bwrapArgs...)
		program, args = "pasta", pastaArgs
	}
	childExecsByName := program == "pasta"
	return program, args, childExecsByName
}

// pastaDNSForwardAddr is pasta's documented default IPv4 gateway address when it
// creates a namespace with no host default route visible, always true in this
// "run given command" unshare mode (pasta(1) NOTES). --dns-forward is a separate
// always-on rule scoped to port 53/853 traffic to this address, independent of
// --no-map-gw, so DNS works without reopening the host-loopback splice (ADR 0042).
const pastaDNSForwardAddr = "169.254.2.2"

// seccompFilterFD is the file descriptor number bwrap's --seccomp flag names.
// Go's exec.Cmd.ExtraFiles numbers extra fds from 3, and this is the only entry
// the bwrap adapter ever adds, so the number is fixed. It survives the
// pasta/bwrap exec chain: ExtraFiles produces a non-close-on-exec fd, preserved
// across every execve in that chain, and the outer wrappers never touch fd 3.
const seccompFilterFD = 3

// removeSeccompFlag strips a "--seccomp <fd>" pair back out of a flattened argv,
// used by Run when a.syscallFilterPath is set but the file failed to open.
// execTarget/buildArgs emit the flag from a.syscallFilterPath's emptiness alone
// and cannot know the open failed, so Run reconciles argv afterwards rather than
// threading the open result down through them.
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

// pastaHardenedFlags are the exact 5 flags ADR 0042 requires when a bwrap Box's
// exec target is wrapped with pasta to restore egress inside its isolated
// network namespace: no TCP/UDP port forwarding into the box and no
// gateway-address mapping, closing the host-loopback splice pasta's own defaults
// leave open.
var pastaHardenedFlags = []string{"-t", "none", "-T", "none", "-u", "none", "-U", "none", "--no-map-gw"}

// resolvedRunEnv returns the process environment the bwrap child inherits. It is
// an allowlist, not a denylist: os.Environ() is never read here, so nothing
// outside boxEnv reaches the sandbox this way, and only the offArgvKeys subset
// needs it (buildArgs delivers every other key on argv). TERM/LANG/TZ/TMPDIR and
// the proxy vars are excluded on the OCI runner's precedent: it forwards none.
func resolvedRunEnv(boxEnv map[string]string) []string {
	keys := make([]string, 0, len(offArgvKeys))
	for k := range offArgvKeys {
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

// memoryLimitToBytes converts a podman/docker-style unit-suffixed memory limit
// ("5g", "512m", "1024k"; bare digits are already bytes) to a raw byte count.
// cgroup v2's memory.max takes only a plain integer or the literal "max", unlike
// podman's --memory, so this has no OCI-adapter equivalent to reuse.
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
// creates, anchored at the outermost ancestor that delegates the controllers this
// adapter's limits need. Creation-time only: IsRunning/ListRunning/Reap read back
// via findCgroupDir instead, since a Box's creating process and the process later
// polling or reaping it are often launcher invocations with different anchors.
func (a *bwrapAdapter) cgroupDirForName(name string) (string, error) {
	parent, err := cgroupParentDir(a.cgroupControllers())
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, "spindrift-"+name), nil
}

// CgroupControllers returns the cgroup v2 controllers the given limits need:
// "memory" iff memoryLimit is set, "pids" iff pidsLimit is set.
// resolveCgroupAnchor rejects an ancestor missing one of these without
// over-rejecting one missing a controller nothing configured here needs.
// Exported so `spindrift doctor` resolves the same set and cannot drift (#3273).
func CgroupControllers(memoryLimit, pidsLimit string) []string {
	var want []string
	if memoryLimit != "" {
		want = append(want, "memory")
	}
	if pidsLimit != "" {
		want = append(want, "pids")
	}
	return want
}

func (a *bwrapAdapter) cgroupControllers() []string {
	return CgroupControllers(a.memoryLimit, a.pidsLimit)
}

// cgroupSubtreeControlHasAll reports whether dir's cgroup.subtree_control lists
// every controller in want. A missing or unreadable file (an undelegated
// ancestor, or a plain non-cgroupfs directory in tests) disqualifies the
// candidate rather than erroring, matching the walk's warn-and-degrade posture.
// An empty want is vacuously satisfied, though resolveCgroupAnchor bails first.
func cgroupSubtreeControlHasAll(dir string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return false
	}
	have := make(map[string]bool, len(want))
	for _, tok := range strings.Fields(string(raw)) {
		have[tok] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// cgroupDirWritable reports whether this process can create a subdirectory in
// dir, by making and immediately removing a throwaway one: ownership and mode
// alone do not settle it. A leftover probe from a launcher killed between the
// Mkdir and the Remove would otherwise disqualify a good directory, so ErrExist
// clears the stale dir and retries once, as ValidateCgroupDelegation does.
func cgroupDirWritable(dir string) bool {
	probe := filepath.Join(dir, fmt.Sprintf("spindrift-anchor-probe-%d", os.Getpid()))
	err := os.Mkdir(probe, 0o755)
	if errors.Is(err, os.ErrExist) {
		if rmErr := os.Remove(probe); rmErr == nil {
			err = os.Mkdir(probe, 0o755)
		}
	}
	if err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// resolveCgroupAnchor returns the OUTERMOST strict ancestor of self (down to
// self itself) that both lists every controller in want in its
// cgroup.subtree_control and accepts a throwaway Mkdir. Outermost first because
// cgroup v2 forbids enabling a controller on a cgroup holding processes, so the
// launcher's own subtree_control is typically empty even under a real delegation.
func resolveCgroupAnchor(self string, want []string) (string, bool) {
	if len(want) == 0 {
		return "", false
	}
	// A writable cgroupFSRoot means this process (root, or a hierarchy entirely
	// ours) could create a cgroup anywhere on the path, so there is no
	// delegation boundary to find and every candidate would pass the write
	// probe on permission alone.
	if cgroupDirWritable(cgroupFSRoot) {
		return "", false
	}

	// The unified hierarchy's root belongs to the init system and is never a
	// delegation target, so candidates start one level below it.
	var candidates []string
	acc := cgroupFSRoot
	for _, part := range strings.Split(self, "/") {
		if part == "" {
			continue
		}
		acc = filepath.Join(acc, part)
		candidates = append(candidates, acc)
	}

	// The subtree_control check runs before the write probe so a non-delegated
	// ancestor is never probed with a write.
	for _, dir := range candidates {
		if !cgroupSubtreeControlHasAll(dir, want) {
			continue
		}
		if !cgroupDirWritable(dir) {
			continue
		}
		return dir, true
	}
	return "", false
}

// cgroupParentDir is the shared seam the per-Box runner (provisionCgroup) and
// the doctor check (ValidateCgroupDelegation) both resolve their anchor through,
// so the two probe and later create under the same directory. Falling back to
// the launcher's own cgroup IS the degrade path (ADR 0042): it can still hold a
// Box's PID for tracking even though cgroup v2 refuses to let it carry limits.
func cgroupParentDir(want []string) (string, error) {
	self, err := readSelfCgroup()
	if err != nil {
		return "", err
	}
	if dir, ok := resolveCgroupAnchor(self, want); ok {
		return dir, nil
	}
	return filepath.Join(cgroupFSRoot, self), nil
}

// findCgroupDir searches the whole cgroupFSRoot tree, not just the calling
// process's own self-cgroup subtree, for a directory named "spindrift-"+name at
// any depth. That width is the point: a Box created under one invocation's
// anchor stays discoverable from another with a different self-cgroup path. A
// missing root, a walk error, or no match all degrade to ("", false).
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
// provisionCgroup. The three os.Remove calls on the control files are a plain
// unlink no-op on a real cgroupfs, where rmdir clears them with the subtree;
// they only do work against the plain directory standing in for cgroupfs in
// tests, where they are real files that would make the final rmdir ENOTEMPTY.
func removeCgroupDir(dir string) error {
	for _, f := range []string{"pids.max", "memory.max", "cgroup.procs"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	return os.Remove(dir)
}

// provisionCgroup creates a per-Box cgroup v2 subtree at the anchor
// cgroupDirForName resolves and writes pids.max/memory.max into it. Failing to
// create the dir means no usable delegation on this host, which ADR 0042 treats
// as expected: warn, return "", and let Run proceed unenforced. Once the dir
// exists it is kept, since a failed limit write leaves it usable for PID tracking.
func (a *bwrapAdapter) provisionCgroup(box Box) (dir string) {
	dir, err := a.cgroupDirForName(box.Name)
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: cgroup v2 delegation unavailable (%v); running box %q without cgroup resource containment\n", err, box.Name)
		return ""
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		fmt.Printf("==> bwrap runner: warning: could not create delegated cgroup %s (%v); running box %q without cgroup resource containment\n", dir, err, box.Name)
		return ""
	}
	if a.pidsLimit != "" {
		if err := writeCgroupLimit(filepath.Join(dir, "pids.max"), []byte(a.pidsLimit), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup pids.max (%v); box %q keeps cgroup tracking but runs without a process-count limit\n", err, box.Name)
		}
	}
	if a.memoryLimit != "" {
		bytesLimit, err := memoryLimitToBytes(a.memoryLimit)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not parse MEMORY_LIMIT %q (%v); box %q keeps cgroup tracking but runs without a memory limit\n", a.memoryLimit, err, box.Name)
		} else if err := writeCgroupLimit(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(bytesLimit, 10)), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup memory.max (%v); box %q keeps cgroup tracking but runs without a memory limit\n", err, box.Name)
		}
	}
	return dir
}

// Run launches a single issue into a bubblewrap sandbox.
func (a *bwrapAdapter) Run(box Box) error {
	// Only the synthesised /etc/resolv.conf lives here; passwd/group are baked
	// store paths (issue #2663).
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

	// Marked before provisionCgroup's mkdir, not after (see the provisioning
	// field). The deferred release covers every early return; the explicit one
	// past the cgroup.procs write unblocks Reap in the common case.
	releaseProvisioning := a.beginProvisioning(box.Name)
	defer releaseProvisioning()

	// Provisioned before Start so the dir and its limits exist by the time bwrap
	// is exec'd; moving the process in waits for the real PID. An empty
	// cgroupDir means provisionCgroup could not create the dir at all.
	cgroupDir := a.provisionCgroup(box)

	// Opened before cmd is built: a failed open must also drop the "--seccomp"
	// flag from argv, not just skip attaching ExtraFiles, or bwrap reads a
	// nonexistent fd 3 at startup and the whole Box launch fails over a
	// hardening nicety (issue #2670).
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
		// Strip the pair from the flattened argv rather than mutate a shallow
		// copy of *a: copying the struct copies its live sync.Mutex, which go
		// vet's copylocks flags and Run's concurrency makes a real hazard.
		execArgs = removeSeccompFlag(execArgs)
	}
	cmd := execCommand(program, execArgs...)
	// Pdeathsig kills this direct child (bwrap or pasta) the moment the launcher
	// dies, so a crashed launcher never leaves an orphaned Box. Separate from
	// bwrap's own --die-with-parent, which only protects bwrap against ITS
	// immediate parent: pasta, in the fork case, not the launcher two hops up.
	// setDeathSignal is a platform split, since Pdeathsig is Linux-only.
	setDeathSignal(cmd)
	cmd.Env = resolvedRunEnv(box.Env)
	if childExecsByName {
		// pasta execs "bwrap" by bare name via execvp, using its own process
		// environment's PATH rather than Go's LookPath, which resolved only the
		// top-level program. Without this the env carries no PATH at all and the
		// child exec fails with ENOENT. PATH is not an offArgvKeys value, so
		// forwarding it does not widen resolvedRunEnv's no-ambient-leak guarantee.
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	}
	if syscallFilterFile != nil {
		cmd.ExtraFiles = []*os.File{syscallFilterFile}
	}
	cmd.Stdout = out
	cmd.Stderr = out

	// A shared flock held for the life of the sandboxed process is how
	// reclaimStaleSnapshots detects a live Box still reading this generation.
	// Gated on nixConfigFile, the same condition buildArgs mounts the dir under.
	// Reclaim can still win the gap between the open and the blocking acquire, so
	// re-stat after locking: --overlay-src against a removed dir would not mount.
	var nixVarSnapshotLock *os.File
	if a.nixConfigFile != "" {
		// The same call buildArgs' --overlay-src bind uses, so the lock and stat
		// guard the exact directory this launch mounts.
		snapshotDir := a.snapshotDirFor(box)
		lf, err := lockSnapshotShared(snapshotDir)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); reclaim cannot detect box %q is reading this generation\n", snapshotLockPath(snapshotDir), err, box.Name)
		} else if _, statErr := os.Stat(snapshotDir); statErr != nil {
			unlockSnapshot(lf)
			if cgroupDir != "" {
				_ = os.Remove(cgroupDir)
			}
			return fmt.Errorf("nix-var snapshot %s no longer exists (reclaimed by a concurrent build?): %w", snapshotDir, statErr)
		} else {
			nixVarSnapshotLock = lf
		}
	}
	if err := cmd.Start(); err != nil {
		if cgroupDir != "" {
			_ = os.Remove(cgroupDir)
		}
		unlockSnapshot(nixVarSnapshotLock)
		return err
	}
	cgroupProvisionRaceWindowHook()
	if cgroupDir != "" {
		// Best-effort: the box is already running, so failing to move it in
		// means this Box runs unenforced, never that Run fails.
		pid := strconv.Itoa(cmd.Process.Pid)
		if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not move box %q into cgroup %s: %v\n", box.Name, cgroupDir, err)
		}
	}
	// Provisioning ends at the cgroup.procs write above, not at trackRunning
	// below; see the provisioning field for why.
	releaseProvisioning()
	a.trackRunning(box.Name, cmd.Process)
	defer a.untrackRunning(box.Name)
	// Deferred so the shared lock spans cmd.Wait()'s whole duration: releasing
	// earlier would let reclaimStaleSnapshots believe this generation is free
	// while the sandboxed process is still reading it. Flock releases on process
	// exit, so a crashed launcher needs no separate recovery path.
	defer unlockSnapshot(nixVarSnapshotLock)
	if cgroupDir != "" {
		// Deferred so cleanup runs after cmd.Wait() returns: the cgroup dir can
		// only be rmdir'd once no live process remains inside it (ADR 0042).
		defer func() {
			if err := removeCgroupDir(cgroupDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove cgroup %s: %v\n", cgroupDir, err)
			}
		}()
	}
	return asRunError(cmd.Wait())
}

// trackRunning records proc as the live process for name, so a concurrent Kill
// can find it.
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

// beginProvisioning marks name as mid-launch so Reap skips it; see the
// provisioning field for the race this closes. The returned release is wrapped
// in sync.Once because Run calls it explicitly after the cgroup.procs write and
// again via defer, and the two must not double-decrement the refcount out from
// under a second concurrent Run for the same name.
func (a *bwrapAdapter) beginProvisioning(name string) (release func()) {
	a.mu.Lock()
	if a.provisioning == nil {
		a.provisioning = map[string]int{}
	}
	a.provisioning[name]++
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.provisioning[name]--
			if a.provisioning[name] <= 0 {
				delete(a.provisioning, name)
			}
			a.mu.Unlock()
		})
	}
}

// Reap best-effort removes a leftover per-Box cgroup dir for name, e.g. one
// orphaned by a launcher that crashed before Run's deferred cleanup ran. It
// resolves the dir via findCgroupDir, so it cleans up after a different launcher
// invocation too, and never touches a running sandbox: Kill is the
// operator-driven counterpart. Every failure degrades to a silent nil return.
func (a *bwrapAdapter) Reap(name string) error {
	// a.mu is held across the provisioning check, IsRunning, findCgroupDir and
	// removeCgroupDir: dropping it in between would let a beginProvisioning
	// mkdir land right after the check and still get deleted. Neither callee
	// takes a.mu, so this cannot deadlock. It covers only this launcher process;
	// ADR 0042's "Amendment (issue #2960)" records the cross-process gap.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provisioning[name] > 0 {
		return nil
	}
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

// Kill sends SIGKILL to name's tracked live process, if Run has one under that
// name. A miss (already exited, or never launched) is not an error: Terminate's
// reap step is best-effort by design.
func (a *bwrapAdapter) Kill(name string) error {
	a.mu.Lock()
	proc := a.running[name]
	a.mu.Unlock()
	if proc == nil {
		return nil
	}
	// The process can finish between the map lookup above and this call, so
	// os.ErrProcessDone means it is already gone: a miss, not an error.
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// IsRunning reports whether name's per-Box cgroup still has a resident PID in
// its cgroup.procs, resolving the dir via findCgroupDir so a Box created by a
// different launcher invocation is found too. No cgroup tree and no dir both
// degrade to false. IsRunning itself never warns, since a poll loop calling it
// repeatedly would be noisy; provisionCgroup already warns once at launch.
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
// cgroupFSRoot and reports the live subset, so Console startup orphan detection
// (issue #651) finds Boxes started by a prior launcher invocation (issue #2669).
// Liveness is read from each candidate's own cgroup.procs, so a leftover empty
// dir is excluded. No cgroup v2 tree yields a nil slice and no error.
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
	agentFilesDrv string
	agentEnvDrv   string
	passwdFileDrv string
	groupFileDrv  string
	// nixConfigFileDrv is empty when the Consumer's nixInBox knob is off, which
	// gates both the extra nix-config closure realization and the store-DB
	// snapshot step below together (ADR 0042).
	nixConfigFileDrv string
	// syscallFilterDrv is unconditional in production (issue #2670) but guarded
	// like nixConfigFileDrv, so a zero-value adapter (a bare test struct
	// literal) never tries to realize an empty drv path.
	syscallFilterDrv string
	// nixVarSnapshotDir is the host-side directory snapshotStoreDB writes into,
	// the same directory the run adapter mounts.
	nixVarSnapshotDir string
	// nixVarSnapshotRoot and nixVarGeneration are kept as their own fields,
	// rather than re-derived from nixVarSnapshotDir by path surgery, so
	// reclaimStaleSnapshots' arguments cannot be misidentified (issue #2680).
	// An empty nixVarGeneration is the flat/legacy path, distinguishable here
	// from "the root itself" the way filepath.Dir/Base surgery could not be.
	nixVarSnapshotRoot string
	nixVarGeneration   string
}

// NewBwrapBuild constructs a bwrap adapter for the build command from cfg and
// pwd (the launcher's own working directory). EnsureReady realizes the agent
// store closures and, when nixInBox is on, snapshots the host nix store DB.
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
// realizes it from.
type closureSpec struct {
	label string
	drv   string
}

// EnsureReady realizes the agent store closures via nix build; nix is
// idempotent, so an already-realized closure is fast. When nixConfigFileDrv is
// set (nixInBox on) it also realizes the nix-config closure and snapshots the
// host nix store DB (ADR 0042) for the run adapter's /nix/var overlay to mount.
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
		// MkdirAll first: lockSnapshotShared opens dir+".lock", a sibling of dir,
		// so its parent must exist for O_CREATE, and on a fresh checkout the
		// root does not exist yet. A failure here degrades like a lock failure.
		if mkErr := os.MkdirAll(a.nixVarSnapshotRoot, 0o755); mkErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not create nix-var snapshot root %s (%v); a concurrent build cannot detect this generation is mid-write\n", a.nixVarSnapshotRoot, mkErr)
		}
		// Hold the same shared lock Run holds, here only for the write below: a
		// concurrent `launcher build` skips only its OWN keepGeneration, so from
		// its point of view this dir is one more stale generation and its
		// exclusive probe would RemoveAll it mid-VACUUM (issue #2680). Failing to
		// lock degrades only that protection; it must never fail the build.
		lf, lockErr := lockSnapshotShared(a.nixVarSnapshotDir)
		if lockErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire nix-var snapshot lock %s (%v); a concurrent build cannot detect this generation is mid-write\n", snapshotLockPath(a.nixVarSnapshotDir), lockErr)
		}
		err := a.snapshotStoreDB()
		unlockSnapshot(lf)
		if err != nil {
			return err
		}
		// An empty nixVarGeneration is the flat/legacy path: nixVarSnapshotDir IS
		// the root, so there are no sibling generations to sweep, and reclaiming
		// against the root would sweep unrelated siblings like .spindrift/accum.git.
		if a.nixVarGeneration == "" {
			fmt.Println("==> bwrap runner: nix-var snapshot is unscoped (no closure generation known); skipping stale-generation reclaim")
		} else if err := reclaimStaleSnapshots(a.nixVarSnapshotRoot, a.nixVarGeneration); err != nil {
			// Best-effort: an unreclaimed old generation wastes disk but leaves
			// the snapshot EnsureReady just produced perfectly usable.
			fmt.Printf("==> bwrap runner: warning: could not reclaim stale nix-var snapshots under %s: %v\n", a.nixVarSnapshotRoot, err)
		}
	}

	fmt.Println("==> done: agent store closures realized")
	return nil
}

// SnapshotGeneration writes the nix-var snapshot for a closure hot-swapped
// mid-run (ADR 0043, issue #2682), before binding it: Run's stat guard fails a
// Box naming a generation with no directory. It skips the vacuum when db.sqlite
// already exists, since a live Box may be reading it, and never reclaims: an
// idle Dispatch holds no flock on a generation it will launch against again.
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

// hostNixDBPath is the host's real, live nix store database, never a path inside
// a sandbox: snapshotStoreDB runs during `launcher build`, on the operator's own
// machine. It is the one Go call site that reaches into live host store metadata.
const hostNixDBPath = "/nix/var/nix/db/db.sqlite"

// snapshotStoreDB copies the host's live nix store database into
// a.nixVarSnapshotDir, compacting it in the same step (ADR 0042: ~302MB raw vs
// ~104MB compacted, and an overlay copy-up rewrites a file whole, so the
// compacted size is what lands in the Box's tmpfs upper on first touch). VACUUM
// INTO creates a file owned by the invoking uid, so no explicit chown is needed.
func (a *bwrapBuildAdapter) snapshotStoreDB() error {
	fmt.Println("==> bwrap runner: snapshotting host nix store DB (VACUUMed)")
	return vacuumStoreDBInto(a.nixVarSnapshotDir)
}

// vacuumStoreDBInto does the VACUUM INTO and backup-rename work against an
// explicit dir, so the run-time hot-swap caller (SnapshotGeneration) reuses it
// rather than reaching it only through a bwrapBuildAdapter's baked field (issue
// #2682). Writes dir/nix/db/db.sqlite, the layout every nixVarSnapshotDir caller
// expects.
func vacuumStoreDBInto(dir string) error {
	if err := statHostNixDB(); err != nil {
		return fmt.Errorf("host nix store db not found at %s: %w", hostNixDBPath, err)
	}

	dest := filepath.Join(dir, "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir nix-var-snapshot: %w", err)
	}

	// "VACUUM INTO" refuses to run when dest already exists, so move an existing
	// snapshot aside rather than deleting it outright: if the vacuum then fails
	// (disk full, host db locked), the rename below restores the previously
	// working snapshot instead of leaving `launcher run` with nothing (#2664).
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

	// "VACUUM INTO" uses sqlite's online-backup mechanism, so it handles a
	// concurrent nix-daemon write (WAL not yet checkpointed) that a plain file
	// copy could snapshot mid-write, and it compacts into the destination in the
	// same step. dest is escaped for a single-quoted SQL literal; sqlite3's
	// dot-commands are whitespace-tokenized, but a SQL statement on argv is not.
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
		// Best-effort: a leftover .bak wastes disk but does not break the
		// snapshot just written to dest.
		if err := os.Remove(backup); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not remove backup snapshot %s: %v\n", backup, err)
		}
	}
	return nil
}

// reclaimStaleSnapshots removes generation directories under root that are
// neither keepGeneration nor still referenced by a live Box. Liveness is a
// non-blocking exclusive Flock on the generation's sibling lock file, which
// fails while any Box holds the shared lock Run takes. It never removes the lock
// file itself; sweepOrphanedLock does that a pass later. Per-entry errors warn.
func reclaimStaleSnapshots(root, keepGeneration string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read nix-var-snapshot root %s: %w", root, err)
	}
	// Captured before any removal in this pass so sweepOrphanedLock can tell
	// "orphaned before this pass started" from "this pass just reclaimed it".
	// Directory entries sort before their "<name>.lock" sibling, so a live
	// post-removal os.Stat would always see the former as gone (issue #2680).
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
			// A live Box holds the shared lock, so leave this generation alone.
			lf.Close()
			continue
		}
		// See lockedFDMatchesPath's doc: only remove if lf still identifies
		// whatever currently sits at lockPath.
		if lockedFDMatchesPath(lf, lockPath) {
			if err := os.RemoveAll(genDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove stale nix-var snapshot %s: %v\n", genDir, err)
			}
		}
		unlockSnapshot(lf)
	}
	return nil
}

// sweepOrphanedLock removes entryName from root when it is a "<generation>.lock"
// whose generation dir was already absent from knownGenerations, since the main
// loop only considers directory entries and these would otherwise accumulate
// forever. knownGenerations, not a live os.Stat, is what makes it safe: a live
// check would also treat "this pass just removed it" as orphaned (issue #2680).
func sweepOrphanedLock(root, entryName, keepGeneration string, knownGenerations map[string]bool) {
	generation, ok := strings.CutSuffix(entryName, ".lock")
	if !ok || generation == keepGeneration {
		return
	}
	if knownGenerations[generation] {
		return // the generation dir was present at the start of this pass
	}
	lockPath := filepath.Join(root, entryName)
	lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	lockRaceWindowHook()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Still referenced (Run may be mid-race, about to discover its
		// generation dir is gone); leave it for a later reclaim pass.
		lf.Close()
		return
	}
	// See lockedFDMatchesPath's doc: only remove if lf still identifies
	// whatever currently sits at lockPath.
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

// Kill is a no-op for the build adapter: it never launches a box.
func (a *bwrapBuildAdapter) Kill(_ string) error { return nil }

// IsRunning always reports false: the build adapter never launches a box.
func (a *bwrapBuildAdapter) IsRunning(_ string) bool { return false }

// ListRunning always returns an empty list: the build adapter never launches a
// box.
func (a *bwrapBuildAdapter) ListRunning() ([]string, error) { return nil, nil }

// RegistryProxyTransport always reports a unix Endpoint, mirroring bwrapAdapter's
// own answer (issue #3111); the build adapter never launches a box to probe.
func (a *bwrapBuildAdapter) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	return registrymanifest.NewUnixEndpoint(""), false, nil
}
