package bwrap

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
)

// execCommand is the test seam for every binary this package execs: bwrap (or
// the wrapped program), sqlite3, the overlay-validate probe, and host nix.
var execCommand = exec.Command

// statResolvConf is the test seam for buildArgs' "does the host have
// /etc/resolv.conf to bind" check, so the resolv.conf-bind assertions do not
// depend on the test machine having that file (some nix build sandboxes do not).
var statResolvConf = func() error {
	_, err := os.Stat("/etc/resolv.conf")
	return err
}

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

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady prepares a cold closure generation itself (via prepare) rather
// than demanding `launcher build`; IsReady is the read-only check --no-build uses.
type bwrapAdapter struct {
	// prepare realizes this adapter's closure generation when EnsureReady finds
	// it cold; it shares the build adapter's steps rather than duplicating them.
	prepare bwrapBuildAdapter

	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	passwdFile    string // baked nix store path for /etc/passwd
	groupFile     string // baked nix store path for /etc/group
	bakedPrefetch string // baked prefetch snippet handed to the Box as PREFETCH
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
	// nixGCRootsRoot and generation locate the GC roots `launcher build`
	// registered for the baked generation; Run re-registers them when a
	// concurrent build's reclaim dropped them (issue #5996).
	nixGCRootsRoot string
	generation     string
	// nixStoreWritable gives /nix/store the same overlay treatment as /nix/var
	// (ADR 0042): an ephemeral tmpfs upper, so paths built in the Box vanish with
	// the sandbox instead of touching host disk. buildArgs AND-gates it with
	// nixConfigFile, so true here alone does nothing when nixInBox is off.
	nixStoreWritable bool
	// mountParams passes this run's host-mount facts from Config to
	// BuildMountSpecs unmodified. DriverSessionCacheDir is ADR 0009; the
	// CODE_FORGE=local mount specs are issue #1697.
	mountParams runner.MountParams
	network     runner.BwrapNetwork // from runner.Config.BwrapNetwork
	// pidsLimit is the PIDS_LIMIT knob (empty disables it). bwrap imposes no
	// process-count cap of its own, so the per-Box cgroup v2 pids.max control
	// file is the only containment mechanism (ADR 0042, provisionCgroup).
	pidsLimit string
	// memoryLimit is the MEMORY_LIMIT knob (empty disables it). It backs the
	// per-Box cgroup's memory.max control file (ADR 0042, provisionCgroup)
	// rather than any bwrap flag, and memory.max takes a raw byte count
	// (MemoryLimitToBytes), unlike podman's --memory which accepts the suffix.
	memoryLimit string

	// syscallFilterPath is the baked store path to the compiled BPF syscall
	// filter (issue #2670), passed as "--seccomp <seccompFilterFD>". A failed
	// open at Run time warns and proceeds without the filter (ADR 0042's
	// degrade-don't-lie posture).
	syscallFilterPath string

	// connectorHost is the store path of the static connector-host package
	// (binary at bin/connector-host), the one path a Connector sandbox binds.
	// connectorHostDrv realizes it when absent.
	connectorHost    string
	connectorHostDrv string

	// mu guards the maps below. running is the box-name to live-process map
	// Kill consults (issue #649): a bwrap sandbox is an unnamed child process
	// with no daemon to query by name, so Run tracks its own handle.
	mu      sync.Mutex
	running map[string]*os.Process

	// provisioning refcounts box names between beginProvisioning and the
	// matching handOff or release (issue #2960): while a per-Box cgroup dir's
	// cgroup.procs is still empty, provisionCgroup's leftover reclaim would
	// delete a sibling Run's mid-launch dir (issue #4164). Refcounted so two
	// concurrent Runs for one name cannot release each other's guard.
	provisioning map[string]int

	// prefetchFallbackLogged holds the PrefetchFile paths whose read-error
	// fallback prefetchFor has logged (issue #3356), so it logs once per
	// adapter, not per launch. Never pruned. Keyed on PrefetchFile, not
	// Generation, which is "" for an unsafe closure path and would collapse
	// distinct generations.
	prefetchFallbackLogged map[string]struct{}
}

// Strategy is the bwrap family's entry in the runner registry.
var Strategy = runner.Strategy{
	Name: runner.KindBwrap,
	New: func(cfg runner.Config, art inputdoc.Artifacts, l runner.KnobLookup, pwd string) (runner.Runner, error) {
		return New(cfg, bwrapPart(art), l, pwd)
	},
	NewBuild: func(cfg runner.Config, art inputdoc.Artifacts, _ runner.KnobLookup, pwd string) (runner.Runner, error) {
		return NewBuild(cfg, bwrapPart(art), pwd), nil
	},
	Prerequisites: func(cfg runner.Config, art inputdoc.Artifacts, l runner.KnobLookup) []runner.Prerequisite {
		return Prerequisites(cfg, bwrapPart(art), l)
	},
	PrepareHotSwap: func(art inputdoc.Artifacts, pwd, closure string) error {
		return SnapshotGeneration(bwrapPart(art), pwd, closure)
	},
	RawNetworkKnobs: RawNetworkKnobs,
}

// bwrapPart returns the bwrap family's artifacts, or the zero part when the
// document selected another RUNNER_KIND and carries none.
func bwrapPart(art inputdoc.Artifacts) inputdoc.BwrapArtifacts {
	if art.Bwrap == nil {
		return inputdoc.BwrapArtifacts{}
	}
	return *art.Bwrap
}

// New constructs a bwrap adapter for every runner-building command but build
// (NewBuild); the error is a malformed family knob read through l. Unless
// cfg.NetworkMode is the "host" opt-out (issue #2666), the sandbox gets its
// own network namespace with egress via a hardened pasta helper (ADR 0042),
// matching rootless podman.
func New(cfg runner.Config, art inputdoc.BwrapArtifacts, l runner.KnobLookup, pwd string) (runner.Runner, error) {
	net, err := network(cfg, l)
	if err != nil {
		return nil, err
	}
	generation := ClosureGeneration(art.ImageTag.String())
	return &bwrapAdapter{
		prepare:            newBuildAdapter(cfg, art, pwd),
		agentFiles:         art.AgentFiles.String(),
		agentEnv:           art.AgentEnv.String(),
		passwdFile:         art.PasswdFile.String(),
		groupFile:          art.GroupFile.String(),
		bakedPrefetch:      art.BakedPrefetch.String(),
		nixConfigFile:      art.NixConfigFile.String(),
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, generation),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
		generation:         generation,
		nixStoreWritable:   art.NixStoreWritable.Bool(),
		mountParams:        cfg.MountParams,
		network:            net,
		pidsLimit:          cfg.PidsLimit,
		memoryLimit:        cfg.MemoryLimit,
		syscallFilterPath:  art.SyscallFilter.String(),
		connectorHost:      art.ConnectorHost.String(),
		connectorHostDrv:   art.ConnectorHostDrv.String(),
	}, nil
}

// unscopedGenerationLabel names a run whose nix-var snapshot has no generation.
const unscopedGenerationLabel = "(unscoped)"

// EnsureReady prepares the closure generation this run needs, as the OCI
// adapter loads an image only when absent: a ready generation (see
// generationReady) gets an "already realized" line and no image_build pair; a
// cold one is realized under an image_build pair. IsReady backs --no-build.
func (a *bwrapAdapter) EnsureReady() error {
	if a.prepare.generationReady() {
		gen := a.prepare.nixVarGeneration
		if gen == "" {
			gen = unscopedGenerationLabel
		}
		fmt.Printf("==> bwrap runner: closure generation %s already realized\n", gen)
		return nil
	}
	if err := a.prepare.ensureReady(); err != nil {
		return err
	}
	a.prepare.reclaimStale()
	return nil
}

// IsReady checks that a nix-in-box snapshot (written by `launcher build` or
// EnsureReady) is present when the Consumer's nixInBox knob is on (ADR 0042).
// Scoped to that one gap (issue #2664): buildArgs conditions no other mount on
// absence, so without this a missing snapshot surfaces only as a raw bwrap
// overlay mount failure.
func (a *bwrapAdapter) IsReady() error {
	if a.nixConfigFile == "" {
		return nil
	}
	// Check db.sqlite itself: VacuumStoreDBInto creates its dir first, so a
	// dir-only check passes a failed snapshot's leftover (issue #2664). Any
	// existing snapshot passes with no host probe: a host DB that moved on must
	// not fail --no-build, and Run refreshes it (issue #6072).
	snapDir := newestSnapshotDir(a.nixVarSnapshotDir)
	if snapDir == "" {
		// A flat pre-#6072 snapshot counts as built; with none, this names the
		// expected path in the error below.
		snapDir = a.nixVarSnapshotDir
	}
	dbPath := snapshotDBPath(snapDir)
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
// the OCI adapter (BuildMountSpecs); only the rendering below differs.
func (a *bwrapAdapter) mountSpecs(box runner.Box) []runner.MountSpec {
	return runner.BuildMountSpecs(a.mountParams, box)
}

// RegistryProxyTransport always reports a unix Endpoint: a bind-mounted unix
// socket is always connectable from an unprivileged bwrap sandbox, so there is
// nothing to probe (issue #3111). The caller mints the socket path.
func (a *bwrapAdapter) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	return registrymanifest.NewUnixEndpoint(""), false, nil
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
func (a *bwrapAdapter) agentFilesFor(box runner.Box) string {
	if box.ClosureGeneration == nil {
		return a.agentFiles
	}
	return pick(box.ClosureGeneration.AgentFiles, a.agentFiles)
}

// agentEnvFor resolves the agentEnv store path box's launch should bind for
// /bin, PATH, SSL_CERT_FILE and GIT_SSL_CAINFO. Callers append "/bin" and
// "/etc/ssl/certs/ca-bundle.crt" to the result.
func (a *bwrapAdapter) agentEnvFor(box runner.Box) string {
	if box.ClosureGeneration == nil {
		return a.agentEnv
	}
	return pick(box.ClosureGeneration.AgentEnv, a.agentEnv)
}

// nixConfigFileFor resolves the nix.conf store path box's launch binds at
// /etc/nix/nix.conf.
func (a *bwrapAdapter) nixConfigFileFor(box runner.Box) string {
	if box.ClosureGeneration == nil {
		return a.nixConfigFile
	}
	return pick(box.ClosureGeneration.NixConfigFile, a.nixConfigFile)
}

// prefetchFor resolves the PREFETCH value box's launch should --setenv. Not
// pick: an empty prefetch is a legitimate swapped value (lib/mkHarness.nix's
// prefetch ? ""), not "unset". An unreadable swapped prefetch must never take
// down a Box, so a read error falls back to a.bakedPrefetch.
func (a *bwrapAdapter) prefetchFor(box runner.Box) string {
	if box.ClosureGeneration == nil || box.ClosureGeneration.PrefetchFile == "" {
		return a.bakedPrefetch
	}
	content, err := os.ReadFile(box.ClosureGeneration.PrefetchFile)
	if err != nil {
		file := box.ClosureGeneration.PrefetchFile
		a.mu.Lock()
		if a.prefetchFallbackLogged == nil {
			a.prefetchFallbackLogged = map[string]struct{}{}
		}
		_, seen := a.prefetchFallbackLogged[file]
		a.prefetchFallbackLogged[file] = struct{}{}
		a.mu.Unlock()
		if !seen {
			fmt.Fprintf(os.Stderr, "==> store-closure hot-swap: prefetch file %q unreadable, falling back to baked value: %v\n", file, err)
		}
		return a.bakedPrefetch
	}
	return string(content)
}

// snapshotDirFor resolves the nix-var snapshot generation directory box locks
// for its lifetime (issue #2681); the Box mounts the db-<V> dir under it that
// ensureRunSnapshot resolves (issue #6072). An empty or unsafe Generation falls
// back to the baked dir: "" would resolve to the root and ".." escape it.
func (a *bwrapAdapter) snapshotDirFor(box runner.Box) string {
	if gen := a.generationFor(box); gen != "" {
		return filepath.Join(a.nixVarSnapshotRoot, gen)
	}
	return a.nixVarSnapshotDir
}

// generationFor is the generation box launches under: its own override, else
// the adapter's baked one (empty on the flat path). The snapshot dir, Run's
// lock gate and root re-registration all key off it, so they cannot drift.
func (a *bwrapAdapter) generationFor(box runner.Box) string {
	if gen := boxGeneration(box); gen != "" {
		return gen
	}
	return a.generation
}

// boxGeneration is box's safe per-launch generation override, empty when it
// has none.
func boxGeneration(box runner.Box) string {
	if box.ClosureGeneration == nil {
		return ""
	}
	return runner.SafePathComponent(box.ClosureGeneration.Generation)
}

// reRootIfReclaimed re-registers box's GC roots when none exist. A concurrent
// build's reclaim can drop them between this generation's build and Run taking
// its lock; Run holds the shared lock here, so reclaim cannot drop them again.
// Best-effort like the build: an older launcher's generation has no roots dir.
func (a *bwrapAdapter) reRootIfReclaimed(box runner.Box) {
	gen := a.generationFor(box)
	if gen == "" || a.nixGCRootsRoot == "" {
		return
	}
	_, err := os.Stat(filepath.Join(a.nixGCRootsRoot, gen))
	if err == nil {
		return
	}
	if !os.IsNotExist(err) {
		fmt.Printf("==> bwrap runner: warning: could not check GC roots for generation %s (%v); not re-registering\n", gen, err)
		return
	}
	fmt.Printf("==> bwrap runner: GC roots for generation %s missing (reclaimed by a concurrent build?); re-registering\n", gen)
	registerMountedArtifactRoots(a.nixGCRootsRoot, gen, []string{a.agentFilesFor(box), a.agentEnvFor(box)}, a.passwdFile, a.groupFile, a.syscallFilterPath)
}

// buildArgs constructs the bwrap arguments for box; pasta is never part of
// them (see execTarget). etcDir holds only the pasta path's synthesised
// /etc/resolv.conf, since passwd/group are baked store paths (issue #2663).
// nixVarLower is the db-<V> dir Run resolved.
func (a *bwrapAdapter) buildArgs(etcDir string, box runner.Box, nixVarLower string) []string {
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
	// An empty nixConfigFile means nixInBox is off and nix is not on PATH. The
	// overlay puts an ephemeral tmpfs upper over the VACUUMed snapshot (ADR
	// 0042), so nix's writes in the Box (gcroots, profiles, WAL) never touch
	// host disk.
	if a.nixConfigFile != "" {
		args = append(args, "--ro-bind", a.nixConfigFileFor(box), "/etc/nix/nix.conf")
		args = append(args, "--overlay-src", nixVarLower, "--tmp-overlay", "/nix/var")
	}
	if !a.network.Isolate {
		if err := statResolvConf(); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	} else if a.network.Pasta {
		// Unlike OCI, where podman supplies one, nothing writes the guest's
		// resolv.conf; Run writes this file pointed at pastaDNSForwardAddr.
		args = append(args, "--ro-bind", filepath.Join(etcDir, "resolv.conf"), "/etc/resolv.conf")
	}
	agentEnv := a.agentEnvFor(box)
	// Shebangs (#!/bin/sh), libc system(), and passwd's login shell resolve
	// absolute /bin paths; the OCI image has /bin via buildEnv, bwrap's root
	// does not. Same agentEnv as PATH below so a generation swap moves both.
	args = append(args, "--ro-bind", agentEnv+"/bin", "/bin")
	agentFiles := a.agentFilesFor(box)
	args = append(args, "--ro-bind", agentFiles+"/agent", "/agent")
	// /home/agent is a writable tmpfs, so baked content is staged read-only
	// at a top-level path (bwrap cannot fabricate a mountpoint inside the
	// read-only /agent bind above) and the box program copies it in at
	// startup (issue #2843).
	args = append(args, "--ro-bind", agentFiles+"/home/agent", homeAgentStagingDir)
	// BuildMountSpecs owns every mount decision, shared with the OCI adapter;
	// bwrap only renders. Its only writable mounts are the driver cache (issue
	// #427) and /outbox (ADR 0033, issues #1697, #3957).
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
	// bwrap fabricates / as an agent-owned tmpfs (mode 755), so an unsandboxed nix
	// build's HOME=/homeless-shelter would be creatable and then trip nix's
	// "home directory exists" guard on every later build (issue #6009). Must stay
	// after every op that fabricates a top-level path: those need a writable /.
	args = append(args, "--chmod", "0555", "/")
	// No --clearenv: the OffArgvKeys subset of box.Env reaches the sandbox
	// through resolvedRunEnv, which Run sets as cmd.Env and bwrap inherits.
	args = append(args,
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.prefetchFor(box),
	)
	for k, v := range box.Env {
		if !runner.IsOffArgvKey(k) {
			args = append(args, "--setenv", k, v)
		}
	}
	// bwrap is PID 1 in its own unshared PID namespace, so --die-with-parent's
	// PR_SET_PDEATHSIG kills the whole sandbox when bwrap's immediate OS parent
	// dies (issue #2669): the launcher on the direct-exec chain, but only pasta
	// on the pasta chain (see setDeathSignal's call site in Run).
	unshareFlags := []string{"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent"}
	// bwrap unshares net itself only for networkMode=none. Otherwise pasta
	// creates and configures the namespace, and bwrap must inherit it rather
	// than unshare a second, empty one (issue #2666).
	if a.network.Isolate && !a.network.Pasta {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	// --seccomp only ever names seccompFilterFD, the one fd the filter is placed
	// on (issue #2670, #5982); an empty syscallFilterPath skips the flag.
	if a.syscallFilterPath != "" {
		args = append(args, "--seccomp", strconv.Itoa(seccompFilterFD))
	}
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// buildSandboxArgs renders bwrap's argv for the Connector sandbox: no network
// or env, and only the connector-host store path bound, read-only, where a Box
// binds the whole /nix/store. --seccomp is unconditional; the caller must place
// the filter on seccompFilterFD or refuse to start.
func (a *bwrapAdapter) buildSandboxArgs() []string {
	return []string{
		"--unshare-all", "--clearenv", "--cap-drop", "ALL", "--new-session", "--die-with-parent",
		"--proc", "/proc", "--dev", "/dev",
		"--ro-bind", a.connectorHost, a.connectorHost,
		"--seccomp", strconv.Itoa(seccompFilterFD),
		"--", a.connectorHost + "/bin/connector-host",
	}
}

// execTarget computes the top-level program and argv for box's bwrap
// invocation, pasta outermost when network.Pasta applies. childExecsByName is
// true when that program execvp's its child by bare name, so Run must forward
// a PATH: exec.Command's LookPath resolves only the top-level program.
//
// filterOpen is true when syscallFilterPath is set and opened. buildArgs emits
// --seccomp from the path alone, so an unopened filter has the flag stripped
// here, or bwrap reads a dead fd 3 and the launch fails (issue #2670).
func (a *bwrapAdapter) execTarget(etcDir string, box runner.Box, nixVarLower string, filterOpen bool) (string, []string, bool) {
	bwrapArgs := a.buildArgs(etcDir, box, nixVarLower)
	if !filterOpen {
		bwrapArgs = removeSeccompFlag(bwrapArgs)
	}
	var program string
	var args []string
	if !a.network.Pasta {
		program, args = "bwrap", bwrapArgs
	} else {
		pastaArgs := append(pastaFlags(), "--")
		if filterOpen {
			pastaArgs = append(pastaArgs, "sh", "-c", pastaFilterTrampoline, "_", a.syscallFilterPath)
		}
		pastaArgs = append(pastaArgs, "bwrap")
		pastaArgs = append(pastaArgs, bwrapArgs...)
		program, args = "pasta", pastaArgs
	}
	childExecsByName := program == "pasta"
	return program, args, childExecsByName
}

// pastaFlags returns every pasta flag that precedes the "--" and the wrapped
// command, shared with the integration tests' pasta probe so it cannot drift from
// production.
func pastaFlags() []string {
	flags := append([]string{}, pastaHardenedFlags...)
	return append(flags,
		// --config-net is namespace setup, not hardening: the pinned passt only
		// brings the tap interface UP and gives it an address and default route
		// when asked, otherwise the Box has no egress (issue #5981).
		"--config-net",
		"--dns-forward", pastaDNSForwardAddr,
		// -f is load-bearing: pasta otherwise detaches, and cmd.Wait() would
		// track its short-lived parent instead of the bwrap child, breaking
		// exit codes, output capture and a.running.
		"-f")
}

// pastaDNSForwardAddr is the address --dns-forward intercepts: pasta forwards
// port 53/853 traffic sent to it to the host's resolver, an always-on rule
// independent of --no-map-gw, so DNS works without reopening the host-loopback
// splice (ADR 0042). The guest's /etc/resolv.conf names this address.
const pastaDNSForwardAddr = "169.254.2.2"

// seccompFilterFD is the fd bwrap's --seccomp names: the adapter's only
// ExtraFiles entry, which Go numbers 3. pasta closes every inherited fd before
// its exec, so on the pasta chain execTarget's trampoline opens the path onto
// this fd after it instead (issue #5982).
const seccompFilterFD = 3

// pastaFilterTrampoline is the sh -c script run as pasta's COMMAND when the
// filter is open: $1 is the filter path, the rest is bwrap's argv. sh opens the
// filter onto seccompFilterFD itself (see there).
var pastaFilterTrampoline = fmt.Sprintf(`f=$1; shift; exec "$@" %d<"$f"`, seccompFilterFD)

// removeSeccompFlag strips a "--seccomp <fd>" pair out of a flattened argv.
func removeSeccompFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--seccomp" && i+1 < len(args) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// pastaHardenedFlags are the 5 flags ADR 0042 requires on pasta: no TCP/UDP
// port forwarding into the box and no gateway mapping, closing the
// host-loopback splice pasta's defaults leave open.
var pastaHardenedFlags = []string{"-t", "none", "-T", "none", "-u", "none", "-U", "none", "--no-map-gw"}

// resolvedRunEnv returns the process environment the bwrap child inherits: an
// allowlist of boxEnv's OffArgvKeys, never os.Environ(). TERM/LANG/TZ/TMPDIR
// and the proxy vars stay out, as the OCI runner forwards none.
func resolvedRunEnv(boxEnv map[string]string) []string {
	keys := runner.OffArgvKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := boxEnv[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// cgroupDirForName computes the per-Box cgroup v2 directory provisionCgroup
// creates, under the outermost ancestor delegating the needed controllers.
// Creation-time only: IsRunning and ListRunning walk the whole tree, since the
// creating and polling launchers often resolve different anchors.
func (a *bwrapAdapter) cgroupDirForName(name string) (string, error) {
	parent, err := cgroupParentDir(a.cgroupControllers())
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, "spindrift-"+name), nil
}

func (a *bwrapAdapter) cgroupControllers() []string {
	return CgroupControllers(a.memoryLimit, a.pidsLimit)
}

// CgroupControllers returns the cgroup v2 controllers the given limits need,
// so an anchor is never rejected for a controller nothing here needs.
// Exported so `spindrift doctor` resolves the same set (#3273).
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

// cgroupSubtreeControlHasAll reports whether dir's cgroup.subtree_control lists
// every controller in want. A missing or unreadable file disqualifies the
// candidate rather than erroring, matching the walk's warn-and-degrade posture.
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
	// A writable cgroupFSRoot (root, or a hierarchy entirely ours) has no
	// delegation boundary: every candidate would pass the write probe.
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

// cgroupParentDir is the anchor provisionCgroup and ValidateCgroupDelegation
// share, so the doctor probes where the runner creates. Falling back to the
// launcher's own cgroup is the degrade path (ADR 0042): it can still track a
// Box's PID though cgroup v2 refuses it limits.
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

// removeCgroupDir removes a per-Box cgroup v2 directory. The control-file
// removes are no-ops on a real cgroupfs; they only matter on the plain
// directory tests use, where those files would make rmdir ENOTEMPTY.
func removeCgroupDir(dir string) error {
	for _, f := range []string{"pids.max", "memory.max", "cgroup.procs"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	return os.Remove(dir)
}

// reclaimLeftoverCgroupDir removes dir, a per-Box cgroup that already existed at
// Mkdir, and reports whether it did. It does so only when nothing can own it:
// no PID in cgroup.procs, no other in-process Run provisioning the name (this
// Run's own entry makes the count 1), and no tracked sibling in running, live
// or awaiting its deferred removeCgroupDir. A launcher in another process is
// the documented gap (ADR 0042, issue #2960).
func (a *bwrapAdapter) reclaimLeftoverCgroupDir(name, dir string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provisioning[name] != 1 || a.running[name] != nil {
		return false
	}
	procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil || strings.TrimSpace(string(procs)) != "" {
		return false
	}
	return removeCgroupDir(dir) == nil
}

// provisionCgroup creates the cgroup v2 subtree for the Box or Connector
// sandbox called name at the anchor cgroupDirForName resolves and writes
// pids.max/memory.max (empty writes none) into it. Failing to create the dir
// means no usable delegation, which ADR 0042 treats as expected: warn, return
// "", run unenforced. A created dir is kept even on a failed limit write, for
// PID tracking. A crashed launcher's leftover empty dir is reclaimed rather
// than failing every retry on EEXIST (issue #4164).
func (a *bwrapAdapter) provisionCgroup(name, pidsLimit, memoryLimit string) (dir string) {
	dir, err := a.cgroupDirForName(name)
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: cgroup v2 delegation unavailable (%v); running %q without cgroup resource containment\n", err, name)
		return ""
	}
	err = os.Mkdir(dir, 0o755)
	if errors.Is(err, os.ErrExist) && a.reclaimLeftoverCgroupDir(name, dir) {
		err = os.Mkdir(dir, 0o755)
	}
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: could not create delegated cgroup %s (%v); running %q without cgroup resource containment\n", dir, err, name)
		return ""
	}
	if pidsLimit != "" {
		if err := writeCgroupLimit(filepath.Join(dir, "pids.max"), []byte(pidsLimit), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup pids.max (%v); %q keeps cgroup tracking but runs without a process-count limit\n", err, name)
		}
	}
	if memoryLimit != "" {
		bytesLimit, err := runner.MemoryLimitToBytes(memoryLimit)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: invalid MEMORY_LIMIT %q (%v); %q keeps cgroup tracking but runs without a memory limit\n", memoryLimit, err, name)
		} else if err := writeCgroupLimit(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(bytesLimit, 10)), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not write cgroup memory.max (%v); %q keeps cgroup tracking but runs without a memory limit\n", err, name)
		}
	}
	return dir
}

// startInCgroup starts newCmd's command cloned straight into cgroupDir where
// the kernel supports it, so pasta's and bwrap's forks never run outside the
// limits (issue #4815), then hands its process off under name. newCmd is a
// func because Start is one-shot and a refused CLONE_INTO_CGROUP retries on a
// fresh Cmd. A failed start removes cgroupDir.
func (a *bwrapAdapter) startInCgroup(name, cgroupDir string, newCmd func() *exec.Cmd, handOff func(*os.Process)) (*exec.Cmd, error) {
	// The fd only needs to outlive Start.
	var cgroupFile *os.File
	if cgroupDir != "" {
		if f, err := os.Open(cgroupDir); err == nil {
			cgroupFile = f
		} else {
			fmt.Printf("==> bwrap runner: warning: could not open cgroup %s (%v); %q starts outside it and is moved in after Start\n", cgroupDir, err, name)
		}
	}
	cmd := newCmd()
	intoCgroup := cgroupFile != nil && cgroupCloneSupported
	if intoCgroup {
		setCgroupClone(cmd, int(cgroupFile.Fd()))
	}
	err := cmd.Start()
	if err != nil && intoCgroup {
		fmt.Printf("==> bwrap runner: warning: could not start %q inside cgroup %s (%v); retrying and moving it in after Start\n", name, cgroupDir, err)
		cmd = newCmd()
		err = cmd.Start()
	}
	if cgroupFile != nil {
		cgroupFile.Close()
	}
	if err != nil {
		if cgroupDir != "" {
			_ = os.Remove(cgroupDir)
		}
		return nil, err
	}
	if cgroupDir != "" {
		// A no-op after CLONE_INTO_CGROUP; otherwise descendants forked before
		// this write escape the limits (issue #4815). Best-effort: failure runs this
		// Box unenforced, never fails Run.
		pid := strconv.Itoa(cmd.Process.Pid)
		if err := os.WriteFile(filepath.Join(cgroupDir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not move %q into cgroup %s: %v\n", name, cgroupDir, err)
		}
	}
	// Atomic even when the cgroup.procs write above failed; see beginProvisioning.
	handOff(cmd.Process)
	return cmd, nil
}

// Run launches a single issue into a bubblewrap sandbox. It returns
// runner.ErrAlreadyRunning, before touching anything, when a Box of that name is
// already live: tracked in this adapter (a sibling Run) or resident in a
// spindrift-<name> cgroup (another launcher's).
func (a *bwrapAdapter) Run(box runner.Box) error {
	if a.alreadyRunning(box.Name) {
		return runner.ErrAlreadyRunning
	}
	// Only the synthesised /etc/resolv.conf lives here; passwd/group are baked
	// store paths (issue #2663).
	etcDir, err := os.MkdirTemp("", "spindrift-etc-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(etcDir)

	if a.network.Pasta {
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
	// field). The deferred release covers every early return; handOff below
	// covers the launched case.
	handOff, releaseProvisioning := a.beginProvisioning(box.Name)
	defer releaseProvisioning()

	// Provisioned before Start, which clones the child straight into it. Empty
	// when provisionCgroup could not create the dir.
	cgroupDir := a.provisionCgroup(box.Name, a.pidsLimit, a.memoryLimit)

	// Shared flocks held for the sandboxed process's life are how
	// reclaimStaleSnapshots sees a live Box using this generation (snapshot dir
	// and GC roots) and how a mounted snapshot version escapes the sweep. A
	// launcher that passed readiness holds no lock until here, so reclaim may
	// have removed the generation first: recreate rather than fail. The Box
	// mounts the host DB's current version dir and holds its lock too, so it
	// sees every path valid at launch and no writer disturbs its lower (issue
	// #6072).
	var nixVarSnapshotLock, nixVarVersionLock *os.File
	// genDir is empty only for an adapter not built by New, where
	// SnapshotLockPath would be a cwd-relative ".lock". The flat path with
	// nixInBox off has no mount, no root and no sweep, so a lock would be litter.
	genDir := a.snapshotDirFor(box)
	nixVarLower := genDir
	if genDir != "" && (a.nixConfigFile != "" || a.generationFor(box) != "") {
		if a.nixConfigFile != "" {
			retireFlatSnapshot(genDir)
		}
		lf, lockErr := LockSnapshotShared(genDir)
		if lockErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire generation lock %s (%v); reclaim cannot detect box %q is using this generation\n", SnapshotLockPath(genDir), lockErr, box.Name)
		} else {
			nixVarSnapshotLock = lf
		}
		if a.nixConfigFile != "" {
			dir, vlf, ensureErr := a.ensureRunSnapshot(box, genDir)
			if ensureErr != nil {
				UnlockSnapshot(nixVarSnapshotLock)
				if cgroupDir != "" {
					_ = os.Remove(cgroupDir)
				}
				return ensureErr
			}
			nixVarLower, nixVarVersionLock = dir, vlf
			// A daemon reclaims only at bootstrap, so versions its later
			// registrations superseded are swept here, per Box.
			sweepStaleSnapshotVersions(genDir, nixVarLower)
		} else if lockErr == nil {
			a.reRootIfReclaimed(box)
		}
	}

	// Opened here even on the pasta chain, whose trampoline reopens it, so a
	// bad filter degrades at one point instead of killing the Box after pasta
	// exec'd (issue #2670).
	var syscallFilterFile *os.File
	if a.syscallFilterPath != "" {
		f, err := os.Open(a.syscallFilterPath)
		if err != nil {
			fmt.Printf("==> bwrap runner: warning: could not open syscall filter %s (%v); running box %q without seccomp hardening\n", a.syscallFilterPath, err, box.Name)
		} else {
			syscallFilterFile = f
			defer f.Close()
		}
	}
	program, execArgs, childExecsByName := a.execTarget(etcDir, box, nixVarLower, syscallFilterFile != nil)
	newCmd := func() *exec.Cmd {
		cmd := execCommand(program, execArgs...)
		// Pdeathsig kills this direct child the moment the launcher dies;
		// bwrap's own --die-with-parent only watches its immediate parent,
		// which on the pasta chain is pasta.
		setDeathSignal(cmd)
		cmd.Env = resolvedRunEnv(box.Env)
		if childExecsByName {
			// Without PATH pasta's execvp of "bwrap" fails with ENOENT. PATH is
			// not an OffArgvKeys value, so this does not widen resolvedRunEnv's
			// no-ambient-leak guarantee.
			cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
		}
		// Direct chain only; see seccompFilterFD.
		if syscallFilterFile != nil && !a.network.Pasta {
			cmd.ExtraFiles = []*os.File{syscallFilterFile}
		}
		cmd.Stdout = out
		cmd.Stderr = out
		return cmd
	}

	cmd, err := a.startInCgroup(box.Name, cgroupDir, newCmd, handOff)
	if err != nil {
		UnlockSnapshot(nixVarSnapshotLock)
		UnlockSnapshot(nixVarVersionLock)
		return err
	}
	defer a.untrackRunning(box.Name)
	// The locks span cmd.Wait(): released earlier, reclaim would sweep a
	// generation the Box still reads. flock releases on exit, so a crashed
	// launcher needs no recovery path.
	defer UnlockSnapshot(nixVarSnapshotLock)
	defer UnlockSnapshot(nixVarVersionLock)
	if cgroupDir != "" {
		// A cgroup dir can only be rmdir'd once no process remains (ADR 0042).
		defer func() {
			if err := removeCgroupDir(cgroupDir); err != nil {
				fmt.Printf("==> bwrap runner: warning: could not remove cgroup %s: %v\n", cgroupDir, err)
			}
		}()
	}
	return runner.AsRunError(cmd.Wait())
}

// ensureRunSnapshot resolves the snapshot dir box mounts as /nix/var's lower:
// the host DB's current version under genDir, written if absent, returned with
// its shared lock held for the Box's life. Call it holding the generation's
// shared lock when it could be taken. A generation dir a sibling's reclaim
// removed took its GC roots along, so they are re-registered first. The write
// takes no realize lock: the held locks keep reclaim out, and the
// install-if-absent link covers a concurrent writer.
func (a *bwrapAdapter) ensureRunSnapshot(box runner.Box, genDir string) (string, *os.File, error) {
	if _, err := os.Stat(genDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("==> bwrap runner: warning: could not check nix-var snapshot %s (%v); not re-registering GC roots\n", genDir, err)
	} else if err != nil {
		fmt.Printf("==> bwrap runner: nix-var snapshot %s was reclaimed before box %q launched; recreating it\n", genDir, box.Name)
		closureSide := []string{a.agentFiles, a.agentEnv, a.nixConfigFile}
		// Root the closure itself, as SnapshotGeneration does, so both writers keep
		// one root set per generation. NewAgentGeneration makes AgentFiles a child
		// of the closure.
		if g := box.ClosureGeneration; boxGeneration(box) != "" && g.AgentFiles != "" {
			closureSide = []string{filepath.Dir(g.AgentFiles)}
		}
		registerMountedArtifactRoots(a.nixGCRootsRoot, a.generationFor(box), closureSide, a.passwdFile, a.groupFile, a.syscallFilterPath)
	}
	dir, lf, err := prepareSnapshotVersion(genDir)
	if err != nil {
		return "", nil, fmt.Errorf("prepare nix-var snapshot under %s: %w", genDir, err)
	}
	return dir, lf, nil
}

// alreadyRunning reports whether a Box named name is live. A sibling Run still
// provisioning (not yet in running) is not covered: only a launched Box counts,
// so two concurrent Runs of one name that both pass this check can both launch.
func (a *bwrapAdapter) alreadyRunning(name string) bool {
	return a.tracked(name) || a.IsRunning(name)
}

// tracked reports whether Run has handed name's process off to running.
func (a *bwrapAdapter) tracked(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running[name] != nil
}

// untrackRunning drops name's tracked process once Run's Wait returns.
func (a *bwrapAdapter) untrackRunning(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.running, name)
}

// beginProvisioning marks name as mid-launch so provisionCgroup's leftover
// reclaim skips it. handOff records proc in running and drops the guard under
// one a.mu hold, so no observer sees the name in neither map; release is the
// early-return path. One sync.Once keeps the second of them from
// double-decrementing a concurrent same-name Run's refcount.
func (a *bwrapAdapter) beginProvisioning(name string) (handOff func(proc *os.Process), release func()) {
	a.mu.Lock()
	if a.provisioning == nil {
		a.provisioning = map[string]int{}
	}
	a.provisioning[name]++
	a.mu.Unlock()

	var once sync.Once
	// Caller holds a.mu.
	dropGuard := func() {
		a.provisioning[name]--
		if a.provisioning[name] <= 0 {
			delete(a.provisioning, name)
		}
	}
	handOff = func(proc *os.Process) {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.running == nil {
				a.running = map[string]*os.Process{}
			}
			a.running[name] = proc
			dropGuard()
		})
	}
	release = func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			dropGuard()
		})
	}
	return handOff, release
}

// RemoveHostTree removes path directly: bwrap maps the Box uid to the invoking
// user, so nothing the Box wrote is out of the user's reach.
func (a *bwrapAdapter) RemoveHostTree(path string) error { return os.RemoveAll(path) }

// Kill sends SIGKILL to name's tracked live process, else to everything in
// name's live cgroup, so a Box or Connector sandbox a previous launcher started
// is killed by name as the OCI runners do (the orphan sweep, Reclaim's reap).
// A miss (already exited, or never launched) is not an error: the reap is
// best-effort.
func (a *bwrapAdapter) Kill(name string) error {
	a.mu.Lock()
	proc := a.running[name]
	a.mu.Unlock()
	if proc == nil {
		return killCgroup(name)
	}
	// It can exit after the lookup: ErrProcessDone is a miss.
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// killCgroup SIGKILLs every process in each live cgroup for name, so an empty
// leftover dir under another anchor cannot stand in for the live one.
// cgroup.kill (Linux 5.14+) also takes descendants that forked meanwhile;
// without it cgroup.procs' pids are signalled one by one, failures collected
// so one unkillable pid does not spare the rest. A failed cgroup.kill write is
// reported only if the fallback also failed.
func killCgroup(name string) error {
	var errs []error
	for _, cg := range liveCgroups() {
		if cg.name != name {
			continue
		}
		var killErr error
		// Opened without O_CREATE: where cgroup.kill is absent (older kernels, a
		// tmp-dir stand-in) WriteFile would create a regular file and report success.
		if f, err := os.OpenFile(filepath.Join(cg.dir, "cgroup.kill"), os.O_WRONLY, 0); err == nil {
			_, err = f.WriteString("1")
			killErr = errors.Join(err, f.Close())
			if killErr == nil {
				continue
			}
		}
		var pidErrs []error
		for _, field := range strings.Fields(cg.procs) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 1 {
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				pidErrs = append(pidErrs, err)
			}
		}
		if len(pidErrs) > 0 {
			errs = append(errs, killErr)
			errs = append(errs, pidErrs...)
		}
	}
	return errors.Join(errs...)
}

// IsRunning reports whether name has a live per-Box cgroup, answered from
// ListRunning so an empty leftover dir under another anchor cannot mask a live
// one and the two agree by construction. Every failure degrades to
// false without a warning, since a poll loop calls it repeatedly.
func (a *bwrapAdapter) IsRunning(name string) bool {
	names, _ := a.ListRunning()
	return slices.Contains(names, name)
}

// ListRunning reports the live spindrift-* cgroups anywhere under cgroupFSRoot,
// so reconcile and the orphan sweep find Boxes and Connector sandboxes a prior
// launcher started (issue #2669). With no cgroup v2 tree it returns nil and no
// error.
func (a *bwrapAdapter) ListRunning() ([]string, error) {
	var names []string
	for _, cg := range liveCgroups() {
		names = append(names, cg.name)
	}
	return names, nil
}

// liveCgroup is one per-Box cgroup dir whose cgroup.procs was non-empty, with
// the pids read from it.
type liveCgroup struct{ name, dir, procs string }

// liveCgroups walks cgroupFSRoot for spindrift-* dirs and returns those whose
// own cgroup.procs is non-empty, so a leftover empty dir is excluded. It is the
// one lookup behind ListRunning, IsRunning and killCgroup.
func liveCgroups() []liveCgroup {
	var live []liveCgroup
	_ = filepath.WalkDir(cgroupFSRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
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
			live = append(live, liveCgroup{name: name, dir: path, procs: string(procs)})
		}
		return fs.SkipDir // box cgroup dirs are leaves
	})
	return live
}

// bwrapBuildAdapter implements Runner for the `launcher build` bwrap path.
// EnsureReady realizes the agent store closures; Run is not supported.
type bwrapBuildAdapter struct {
	agentFilesDrv string
	agentEnvDrv   string
	passwdFileDrv string
	groupFileDrv  string
	// nixConfigFileDrv is empty when nixInBox is off, gating the nix-config
	// realization and the store-DB snapshot together (ADR 0042).
	nixConfigFileDrv string
	// syscallFilterDrv is unconditional in production (issue #2670) but
	// guarded so a zero-value test adapter never realizes an empty drv path.
	syscallFilterDrv string
	// nixVarSnapshotDir is the host-side directory the snapshot writers nest db-<V>
	// dirs under; the run adapter mounts one of those as /nix/var's lower.
	nixVarSnapshotDir string
	// nixVarSnapshotRoot and nixVarGeneration are their own fields, not derived
	// from nixVarSnapshotDir by path surgery, so reclaimStaleSnapshots' arguments cannot be misidentified (issue #2680).
	// An empty nixVarGeneration is the flat/legacy path.
	nixVarSnapshotRoot string
	nixVarGeneration   string
	// Realized output paths of the closures above, plus the root dir under
	// which registerGenerationRoot pins them for this generation (issue #5995).
	// Separate string fields keep the adapter comparable.
	agentFiles        string
	agentEnv          string
	passwdFile        string
	groupFile         string
	nixConfigFile     string
	syscallFilterPath string
	nixGCRootsRoot    string
}

// NewBuild constructs a bwrap adapter for the build command.
func NewBuild(cfg runner.Config, art inputdoc.BwrapArtifacts, pwd string) runner.Runner {
	b := newBuildAdapter(cfg, art, pwd)
	return &b
}

// newBuildAdapter is shared with New, whose run adapter prepares a cold
// generation through the same steps.
func newBuildAdapter(cfg runner.Config, art inputdoc.BwrapArtifacts, pwd string) bwrapBuildAdapter {
	generation := ClosureGeneration(art.ImageTag.String())
	return bwrapBuildAdapter{
		agentFilesDrv:      art.AgentFilesDrv.String(),
		agentEnvDrv:        art.AgentEnvDrv.String(),
		passwdFileDrv:      art.PasswdFileDrv.String(),
		groupFileDrv:       art.GroupFileDrv.String(),
		nixConfigFileDrv:   art.NixConfigFileDrv.String(),
		syscallFilterDrv:   art.SyscallFilterDrv.String(),
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, generation),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixVarGeneration:   generation,
		agentFiles:         art.AgentFiles.String(),
		agentEnv:           art.AgentEnv.String(),
		passwdFile:         art.PasswdFile.String(),
		groupFile:          art.GroupFile.String(),
		nixConfigFile:      art.NixConfigFile.String(),
		syscallFilterPath:  art.SyscallFilter.String(),
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
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
	if err := a.ensureReady(); err != nil {
		return err
	}
	a.reclaimStale()

	fmt.Println("==> done: agent store closures realized")
	return nil
}

// reclaimStale drops old generations once this one is locked and rooted (and
// its snapshot in place, with nixInBox on). Best-effort: an unreclaimed
// generation wastes disk but leaves the one just prepared perfectly usable.
func (a *bwrapBuildAdapter) reclaimStale() {
	// On the flat path (no generation) nixVarSnapshotDir IS the root, so
	// reclaimStaleSnapshots would delete the flat snapshot's own contents; only
	// stale db-<V> versions are swept (none while a flat nix/ remains).
	if a.nixVarGeneration == "" {
		fmt.Println("==> bwrap runner: closure generation is unscoped (none known); skipping stale-generation reclaim")
		sweepStaleSnapshotVersions(a.nixVarSnapshotDir, currentVersionKeeper()(a.nixVarSnapshotDir))
		return
	}
	if err := reclaimStaleSnapshots(a.nixVarSnapshotRoot, a.nixGCRootsRoot, a.nixVarGeneration); err != nil {
		fmt.Printf("==> bwrap runner: warning: could not reclaim stale generations under %s: %v\n", a.nixVarSnapshotRoot, err)
	}
}

// ensureReady is the preparation both adapters share: realize the closures,
// lock and root the generation and, when nixInBox is on, write the snapshot
// if absent. It reports the image_build pair even when all is realized: nix is
// fast then, and the defer clears the pair on failure.
func (a *bwrapBuildAdapter) ensureReady() error {
	report.ImageBuild(report.ImageBuildStarted)
	defer report.ImageBuild(report.ImageBuildFinished)

	fmt.Println("==> bwrap runner: realizing agent store closures (no image build/load)")
	if err := a.realizeClosures(); err != nil {
		return err
	}
	return a.publishSnapshot()
}

func (a *bwrapBuildAdapter) realizeClosures() error {
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
	return nil
}

// publishSnapshot locks and roots the generation's artifacts and, when nixInBox
// is on, writes its store-DB snapshot for the host DB's current version (ADR
// 0042, issues #6072, #6101). An existing db.sqlite for that version is kept
// and not re-vacuumed, since a running Box may be reading it; a host GC moves
// the version key, so a refresh lands in a fresh dir instead.
func (a *bwrapBuildAdapter) publishSnapshot() error {
	// Guarded like Run's lock.
	if a.nixVarSnapshotDir != "" && (a.nixConfigFileDrv != "" || a.nixConfigFile != "" || a.nixVarGeneration != "") {
		// The lock file is nixVarSnapshotDir's sibling, so on a fresh checkout
		// its parent must be created first. A failure degrades like a lock failure.
		if mkErr := os.MkdirAll(a.nixVarSnapshotRoot, 0o755); mkErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not create nix-var snapshot root %s (%v); a concurrent build cannot detect this generation is mid-write\n", a.nixVarSnapshotRoot, mkErr)
		}
		// Concurrent cold starts, build and run alike, serialize their snapshot
		// write here. Unlocked is still safe: the loser's link-if-absent
		// install finds db.sqlite already there.
		if a.nixConfigFileDrv != "" && a.nixVarSnapshotRoot != "" {
			rl, rlErr := lockRealize(a.nixVarSnapshotRoot, func() {
				fmt.Printf("==> bwrap runner: a sibling launcher is writing a nix-var snapshot under %s; waiting\n", a.nixVarSnapshotRoot)
			})
			if rlErr != nil {
				fmt.Printf("==> bwrap runner: warning: could not acquire realize lock %s (%v); proceeding unlocked\n", RealizeLockPath(a.nixVarSnapshotRoot), rlErr)
			}
			defer UnlockSnapshot(rl)
		}
		if a.nixConfigFileDrv != "" {
			retireFlatSnapshot(a.nixVarSnapshotDir)
		}
		// A concurrent build skips only its own keepGeneration, so its exclusive
		// probe would drop this generation's roots (and, with nix-in-box,
		// RemoveAll the snapshot mid-VACUUM, #2680). Failing to lock must never
		// fail the build.
		lf, lockErr := LockSnapshotShared(a.nixVarSnapshotDir)
		if lockErr != nil {
			fmt.Printf("==> bwrap runner: warning: could not acquire generation lock %s (%v); a concurrent build cannot detect this generation is mid-write\n", SnapshotLockPath(a.nixVarSnapshotDir), lockErr)
		}
		defer UnlockSnapshot(lf)
	}
	// Registered while the lock, if taken, is held: a concurrent build's
	// reclaim must not drop this generation before its roots exist.
	registerMountedArtifactRoots(a.nixGCRootsRoot, a.nixVarGeneration,
		[]string{a.agentFiles, a.agentEnv, a.nixConfigFile}, a.passwdFile, a.groupFile, a.syscallFilterPath)
	if a.nixConfigFileDrv == "" {
		return nil
	}
	// The version lock is dropped on return: a Box takes it itself for its life.
	_, vlf, err := prepareSnapshotVersion(a.nixVarSnapshotDir)
	UnlockSnapshot(vlf)
	return err
}

// generationReady reports whether every realized store path exists and, with a
// nix-config drv, the snapshot for the host DB's current version is a file. A
// host DB that moved past the newest snapshot is cold (issue #6072).
func (a *bwrapBuildAdapter) generationReady() bool {
	for _, p := range []string{a.agentFiles, a.agentEnv, a.passwdFile, a.groupFile, a.nixConfigFile, a.syscallFilterPath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	if a.nixConfigFileDrv == "" {
		return true
	}
	dir, err := currentSnapshotDir(a.nixVarSnapshotDir)
	if err != nil {
		return false
	}
	info, err := os.Stat(snapshotDBPath(dir))
	return err == nil && !info.IsDir()
}

// reclaimStaleSnapshots removes generation directories under root that are
// neither keepGeneration nor held by a live Box: a non-blocking exclusive Flock
// on the generation's sibling lock fails while any Box holds Run's shared
// lock. A snapshot dir's lock file is left for sweepOrphanedLock a pass later.
// A surviving generation has its superseded version dirs swept. Per-entry
// errors warn.
//
// A generation's GC-root dir under gcRootsRoot drops with its snapshot, under
// the same lock; one with no snapshot (nix-in-box off, or a failed snapshot) is
// probed on the lock it would have had and takes that lock file with it.
func reclaimStaleSnapshots(root, gcRootsRoot, keepGeneration string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read nix-var-snapshot root %s: %w", root, err)
	}
	// Captured before any removal so sweepOrphanedLock can tell "orphaned
	// before this pass" from "this pass just reclaimed it": a dir sorts before
	// its "<name>.lock", so a live os.Stat would see it gone (issue #2680).
	knownGenerations := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			knownGenerations[entry.Name()] = true
		}
	}
	keepFor := currentVersionKeeper()
	for _, entry := range entries {
		if !entry.IsDir() {
			sweepOrphanedLock(root, entry.Name(), keepGeneration, knownGenerations)
			continue
		}
		name := entry.Name()
		if name != keepGeneration {
			reclaimGeneration(root, gcRootsRoot, name, keepLock)
		}
		// A no-op once reclaimGeneration removed the dir; otherwise the
		// generation lives on (kept, or a Box holds it) and so do its versions.
		genDir := filepath.Join(root, name)
		sweepStaleSnapshotVersions(genDir, keepFor(genDir))
	}
	rootEntries, err := os.ReadDir(gcRootsRoot)
	if err != nil && !os.IsNotExist(err) {
		fmt.Printf("==> bwrap runner: warning: could not read nix GC roots %s: %v\n", gcRootsRoot, err)
	}
	for _, entry := range rootEntries {
		name := entry.Name()
		if !entry.IsDir() || name == keepGeneration || knownGenerations[name] {
			continue
		}
		// sweepOrphanedLock already ran this pass, so a lock left here would sit
		// stranded until the next one.
		reclaimGeneration(root, gcRootsRoot, name, dropLock)
	}
	return nil
}

// lockDisposition says whether reclaimGeneration also removes the lock file.
type lockDisposition bool

const (
	keepLock lockDisposition = false
	dropLock lockDisposition = true
)

// reclaimGeneration removes a generation's snapshot dir and GC-root dir unless a
// Box holds the generation's shared lock. Either dir may already be absent.
// dropLock also removes the lock file, under the lock, once the roots are gone.
func reclaimGeneration(root, gcRootsRoot, name string, lock lockDisposition) {
	genDir := filepath.Join(root, name)
	lockPath := SnapshotLockPath(genDir)
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: could not open generation lock %s (%v); leaving stale generation %s in place\n", lockPath, err, name)
		return
	}
	lockRaceWindowHook()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// A live Box holds the shared lock, so leave this generation alone.
		lf.Close()
		return
	}
	defer UnlockSnapshot(lf)
	if !LockedFDMatchesPath(lf, lockPath) {
		return
	}
	if err := os.RemoveAll(genDir); err != nil {
		fmt.Printf("==> bwrap runner: warning: could not remove stale nix-var snapshot %s: %v\n", genDir, err)
		return // keep the roots: a snapshot with no root is the unsafe half-state
	}
	// Dropping the out-links is enough: nix's indirect roots then dangle and
	// stop counting.
	if gcRootsRoot != "" { // else filepath.Join would make gcDir cwd-relative
		gcDir := filepath.Join(gcRootsRoot, name)
		if err := os.RemoveAll(gcDir); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not remove stale nix GC roots %s: %v\n", gcDir, err)
			return
		}
	}
	if lock == dropLock {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("==> bwrap runner: warning: could not remove stale generation lock %s: %v\n", lockPath, err)
		}
	}
}

// currentVersionKeeper probes the host DB version once and returns, per
// generation dir, the version dir a reclaim pass spares: the host's current one,
// not the newest on disk (a GC that lowers max(id) leaves that one stale). A
// failed probe falls back to the newest complete db-<V> snapshot on disk, or ""
// when there is none; never a flat genDir.
func currentVersionKeeper() func(genDir string) string {
	v, err := hostStoreDBVersion()
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: could not read host nix store db version (%v); reclaim will keep each generation's newest existing snapshot\n", err)
		return newestSnapshotDir
	}
	return func(genDir string) string { return snapshotVersionDir(genDir, v) }
}

// sweepStaleSnapshotVersions removes the db-<V> version dirs under genDir that
// no Box mounts, sparing keep (issue #6072), the version the caller mounts or
// is about to and which may be unlocked in between. Every other version,
// incomplete ones included, and every dirless db-<V>.lock goes only under an
// exclusive non-blocking flock on its lock, which fails while a Box or a
// mid-VACUUM writer holds it. A bare-decimal V from a pre-#6101 launcher goes
// as superseded. A genDir holding a flat pre-#6072 "nix" dir is not swept at
// all (issue #6100): its db-<V> dirs sit inside an older Box's lower.
func sweepStaleSnapshotVersions(genDir, keep string) {
	if _, err := os.Lstat(flatSnapshotPath(genDir)); err == nil {
		return
	}
	entries, err := os.ReadDir(genDir)
	if err != nil {
		return
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if v, ok := strings.CutPrefix(e.Name(), snapshotVersionPrefix); ok && e.IsDir() && isSnapshotVersion(v) {
			dirs[e.Name()] = true
		}
	}
	for _, e := range entries {
		name := e.Name()
		if dirs[name] {
			if dir := filepath.Join(genDir, name); dir != keep {
				removeSnapshotVersion(dir, true)
			}
			continue
		}
		base, ok := strings.CutSuffix(name, ".lock")
		if !ok || dirs[base] || e.IsDir() {
			continue
		}
		if v, ok := strings.CutPrefix(base, snapshotVersionPrefix); ok && isSnapshotVersion(v) && filepath.Join(genDir, base) != keep {
			removeSnapshotVersion(filepath.Join(genDir, base), false)
		}
	}
}

// removeSnapshotVersion removes dir (when withDir) and its lock file under an
// exclusive non-blocking flock on that lock, leaving both if it is held.
func removeSnapshotVersion(dir string, withDir bool) {
	lockPath := SnapshotLockPath(dir)
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: could not open snapshot lock %s (%v); leaving stale snapshot version %s in place\n", lockPath, err, dir)
		return
	}
	lockRaceWindowHook()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return
	}
	defer UnlockSnapshot(lf)
	if !LockedFDMatchesPath(lf, lockPath) {
		return
	}
	if withDir {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not remove stale snapshot version %s: %v\n", dir, err)
			return
		}
	}
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("==> bwrap runner: warning: could not remove stale snapshot version lock %s: %v\n", lockPath, err)
	}
}

// sweepOrphanedLock removes entryName from root when it is a "<generation>.lock"
// whose generation dir was absent from knownGenerations, since the main loop
// skips non-directory entries and these would otherwise accumulate forever.
// knownGenerations, not a live os.Stat, makes it safe: a live check would also
// treat "this pass just removed it" as orphaned (issue #2680).
func sweepOrphanedLock(root, entryName, keepGeneration string, knownGenerations map[string]bool) {
	generation, ok := strings.CutSuffix(entryName, ".lock")
	if !ok || generation == keepGeneration {
		return
	}
	if knownGenerations[generation] {
		return
	}
	lockPath := filepath.Join(root, entryName)
	lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	lockRaceWindowHook()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Still referenced (Run may be mid-race); leave it for a later pass.
		lf.Close()
		return
	}
	if LockedFDMatchesPath(lf, lockPath) {
		_ = os.Remove(lockPath)
	}
	UnlockSnapshot(lf)
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

// RemoveHostTree removes path directly; the build adapter runs no Box.
func (a *bwrapBuildAdapter) RemoveHostTree(path string) error { return os.RemoveAll(path) }

// Run is not supported by the build adapter.
func (a *bwrapBuildAdapter) Run(_ runner.Box) error {
	return fmt.Errorf("bwrap-build adapter: Run not supported (use bwrap run adapter)")
}

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

// errNoConnectorSandbox is the build adapter's answer: it launches nothing.
var errNoConnectorSandbox = errors.New("the bwrap build adapter cannot start a Connector sandbox")

// StartConnectorSandbox starts connector-host alone under bwrap, tracked and
// cgrouped like a Box so Kill(name) and ListRunning see it. It fails closed on
// anything that would weaken isolation: a missing connector-host or syscall
// filter (unlike a Box, which only warns), and never uses pasta, so the host
// gets no network whatever the Box's mode is.
func (a *bwrapAdapter) StartConnectorSandbox(name string) (runner.ConnectorSandbox, error) {
	if a.connectorHost == "" {
		return runner.ConnectorSandbox{}, errors.New("no connector-host in the input document")
	}
	if a.syscallFilterPath == "" {
		return runner.ConnectorSandbox{}, errors.New("no syscall filter in the input document; refusing to start an unfiltered Connector sandbox")
	}
	if a.alreadyRunning(name) {
		return runner.ConnectorSandbox{}, runner.ErrAlreadyRunning
	}
	if _, err := os.Stat(a.connectorHost); err != nil {
		if a.connectorHostDrv == "" {
			return runner.ConnectorSandbox{}, fmt.Errorf("connector-host %s is absent and the input document has no derivation to build it", a.connectorHost)
		}
		if err := a.prepare.realize("connector-host", a.connectorHostDrv); err != nil {
			return runner.ConnectorSandbox{}, err
		}
	}
	filter, err := os.Open(a.syscallFilterPath)
	if err != nil {
		return runner.ConnectorSandbox{}, fmt.Errorf("open syscall filter: %w", err)
	}
	defer filter.Close()

	// Plain pipes, not cmd.StdoutPipe: startInCgroup's retry builds a second
	// Cmd that must reuse them. Once the parent's write end is closed a bwrap
	// that dies at startup reads as EOF, not a hang.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return runner.ConnectorSandbox{}, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return runner.ConnectorSandbox{}, err
	}
	stderr := &runner.BoundedWriter{}
	args := a.buildSandboxArgs()
	newCmd := func() *exec.Cmd {
		cmd := execCommand("bwrap", args...)
		setDeathSignal(cmd)
		setOwnProcessGroup(cmd) // terminal Ctrl-C must not reach the sandbox before settle (issue #7302)
		cmd.Env = []string{}    // non-nil: bwrap needs none, and nil would inherit the launcher's
		cmd.ExtraFiles = []*os.File{filter}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderr
		return cmd
	}

	handOff, release := a.beginProvisioning(name)
	defer release()
	cgroupDir := a.provisionCgroup(name, runner.SandboxPidsLimit, runner.SandboxMemoryLimit)
	cmd, err := a.startInCgroup(name, cgroupDir, newCmd, handOff)
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		return runner.ConnectorSandbox{}, fmt.Errorf("start bwrap: %w", err)
	}
	wait := sync.OnceValue(func() error {
		defer func() {
			// As cmd.StdoutPipe's own Wait does: its reader finishes first.
			stdoutR.Close()
			a.untrackRunning(name)
			if cgroupDir != "" {
				if err := removeCgroupDir(cgroupDir); err != nil {
					fmt.Printf("==> bwrap runner: warning: could not remove cgroup %s: %v\n", cgroupDir, err)
				}
			}
		}()
		return cmd.Wait()
	})
	return runner.ConnectorSandbox{Stdin: stdinW, Stdout: stdoutR, Wait: wait, Stderr: stderr.String}, nil
}

// StartConnectorSandbox fails closed; the build adapter launches nothing.
func (a *bwrapBuildAdapter) StartConnectorSandbox(string) (runner.ConnectorSandbox, error) {
	return runner.ConnectorSandbox{}, errNoConnectorSandbox
}
