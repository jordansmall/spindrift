package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
func nixVarSnapshotDir(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-var-snapshot")
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
		nixVarSnapshotDir:        nixVarSnapshotDir(pwd),
		nixStoreWritable:         cfg.NixStoreWritable,
		hostMediatedRemote:       cfg.HostMediatedRemote,
		accumulationRepoDir:      cfg.AccumulationRepoDir,
		outboxRelayCapable:       cfg.OutboxRelayCapable,
		boxForgeAndIssueAccess:   cfg.BoxForgeAndIssueAccess,
		hostMediatedIssueTracker: cfg.HostMediatedIssueTracker,
		localIssuesDir:           cfg.LocalIssuesDir,
		unshareNet:               cfg.BwrapUnshareNet,
		networkMode:              cfg.NetworkMode,
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
	unshareFlags := []string{"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts"}
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
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// execTarget computes the top-level host-exec'd program and argv for box's
// bwrap invocation. When pastaPath applies, pasta must be the outer process
// (see buildArgs' unshare-net comment for why); otherwise bwrap itself is the
// top-level program, unchanged.
func (a *bwrapAdapter) execTarget(etcDir string, box Box) (string, []string) {
	bwrapArgs := a.buildArgs(etcDir, box)
	if !a.pastaPath() {
		return "bwrap", bwrapArgs
	}
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
	return "pasta", pastaArgs
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

	// The bwrap process's env is resolvedRunEnv(box.Env) -- the bwrapSecrets
	// subset of box.Env, not the launcher's own ambient environment. Without
	// --clearenv, the sandbox inherits it. Secrets (GH_TOKEN, auth tokens)
	// are therefore available inside the sandbox without appearing on argv.
	program, execArgs := a.execTarget(etcDir, box)
	cmd := execCommand(program, execArgs...)
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
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return err
	}
	a.trackRunning(box.Name, cmd.Process)
	defer a.untrackRunning(box.Name)
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

// Reap is a no-op for bwrap — sandboxes are ephemeral and exit when done.
func (a *bwrapAdapter) Reap(_ string) error { return nil }

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

// IsRunning always reports false for bwrap: sandboxes are unnamed child
// processes, not persistent named containers, so there is nothing to collide
// with by name.
func (a *bwrapAdapter) IsRunning(_ string) bool { return false }

// ListRunning always returns an empty list for bwrap: sandboxes are
// unprivileged child processes with no daemon tracking them by name, so
// there is nothing for Console startup orphan detection (issue #651) to
// find, matching IsRunning's already-false.
func (a *bwrapAdapter) ListRunning() ([]string, error) { return nil, nil }

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
	// nixVarSnapshotDir is the host-side directory snapshotStoreDB writes
	// into (shared package-level convention with the run adapter's mount —
	// see nixVarSnapshotDir's doc comment).
	nixVarSnapshotDir string
}

// NewBwrapBuild constructs a bwrap adapter for the build command from cfg and
// pwd (the launcher's own working directory, mirroring NewBwrap's pwd
// parameter). EnsureReady realizes agent store closures via nix build and,
// when nixInBox is on, snapshots the host nix store DB.
func NewBwrapBuild(cfg Config, pwd string) Runner {
	return &bwrapBuildAdapter{
		agentFilesDrv:     cfg.AgentFilesDrv,
		agentEnvDrv:       cfg.AgentEnvDrv,
		passwdFileDrv:     cfg.PasswdFileDrv,
		groupFileDrv:      cfg.GroupFileDrv,
		nixConfigFileDrv:  cfg.NixConfigFileDrv,
		nixVarSnapshotDir: nixVarSnapshotDir(pwd),
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
