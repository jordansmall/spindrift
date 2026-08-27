package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
// EnsureReady is a no-op — store closures are realized by the build command.
type bwrapAdapter struct {
	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	bakedPrefetch string // baked prefetch snippet fed to the entrypoint
	promptDir     string // optional host path to bind-mount over /agent/prompts
	skillsDir     string // optional host path to bind-mount over operatorSkillsDir (issue #2489)
	// driverSessionCacheDir is the in-box bind target for the Driver's
	// session-state dir (Driver declaration, ADR 0009); empty when the
	// selected Driver declares no session-state dir, in which case
	// box.DriverCacheDir is never bound regardless of its value.
	driverSessionCacheDir string
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
	unshareNet               bool   // raw BWRAP_UNSHARE_NET knob; when true, adds --unshare-net (isolates from host netns)
	networkMode              string // NETWORK_MODE knob; "none" also isolates from the host netns. "no-host-loopback" never reaches bwrap — nix eval-rejects it for a valid Consumer flake (lib/mkHarness.nix networkModeCoherenceOk).

	// mu guards running, the box-name -> live process map Kill (issue #649)
	// consults — bwrap sandboxes are unnamed child processes with no
	// persistent daemon IsRunning/Reap can query by name, so Run tracks its
	// own process handle here for the one caller (Terminate) that needs to
	// reach a live one from outside Run's own goroutine.
	mu      sync.Mutex
	running map[string]*os.Process
}

// NewBwrap constructs a bwrap adapter for the run command from cfg.
// EnsureReady is a no-op; call NewBwrapBuild for the build command.
// cfg.BwrapUnshareNet adds --unshare-net to isolate from the host network
// namespace; when false, the sandbox shares the host netns (host-loopback
// reachable).
func NewBwrap(cfg Config) Runner {
	return &bwrapAdapter{
		agentFiles:               cfg.AgentFiles,
		agentEnv:                 cfg.AgentEnv,
		bakedPrefetch:            cfg.BakedPrefetch,
		promptDir:                cfg.PromptDir,
		skillsDir:                cfg.SkillsDir,
		driverSessionCacheDir:    cfg.DriverSessionCacheDir,
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

// EnsureReady is a no-op for bwrap run: store closures are realized by
// `launcher build` (bwrapBuildAdapter.EnsureReady) before `run` is invoked.
func (a *bwrapAdapter) EnsureReady() error { return nil }

// IsReady is a no-op for bwrap: store closures are realized out-of-band by
// `spindrift build` and are either present or absent at bwrap invocation time.
func (a *bwrapAdapter) IsReady() error { return nil }

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

// buildArgs constructs the bwrap command-line arguments for the given box.
// etcDir is the temp directory holding the synthesised /etc/passwd and /etc/group.
// Secret env vars (GH_TOKEN, auth tokens) are intentionally excluded from argv;
// they reach the sandbox via inherited process environment (no --clearenv).
func (a *bwrapAdapter) buildArgs(etcDir string, box Box) []string {
	// isolateNet is the effective "cut off the host netns" decision: the raw
	// BWRAP_UNSHARE_NET escape hatch, or NETWORK_MODE=none (issue #2562).
	// Same shared invariant as oci.go's networkArg/config.go's Config doc
	// (raw wins whenever set) even though this is an OR rather than an
	// explicit "prefer raw" branch: BWRAP_UNSHARE_NET is bool-typed and only
	// ever forces isolation *on* (true) -- there's no raw value here that
	// forces it *off* against an isolating mode, unlike podman's
	// string-typed raw knob, which can render any --network value including
	// one that reopens what a mode would have closed. So OR-ing the two
	// reaches the same answer "raw wins" would for every combination Go can
	// observe. nix eval-rejects setting both on a valid Consumer flake
	// (lib/mkHarness.nix networkModeCoherenceOk), so this is defense in
	// depth for a case that can't actually occur there; it still needs a
	// deterministic answer here since Go has no way to observe that
	// invariant. NETWORK_MODE=no-host-loopback is not handled here on
	// purpose: bwrap's --unshare-net is all-or-nothing and can't express a
	// partial isolation, so this adapter has no rendering for it and falls
	// open (treats it like "open") if it ever arrives. RUNTIME is baked at
	// eval time while NETWORK_MODE is runtime-overridable -- main.go's
	// checkNetworkModeRuntimeGate is the actual backstop that keeps a
	// runtime override of NETWORK_MODE from reaching this adapter; see
	// TestBwrapArgs_NetworkModeNoHostLoopbackFailsOpen for the
	// characterization of what happens here if that gate is bypassed.
	isolateNet := a.unshareNet || a.networkMode == NetworkModeNone
	args := []string{
		"--ro-bind", "/nix/store", "/nix/store",
		"--tmpfs", "/tmp",
		"--tmpfs", "/work",
		"--tmpfs", "/home/agent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--dir", "/etc",
		"--ro-bind", filepath.Join(etcDir, "passwd"), "/etc/passwd",
		"--ro-bind", filepath.Join(etcDir, "group"), "/etc/group",
	}
	if !isolateNet {
		if err := statResolvConf(); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
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
	if isolateNet {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

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
	etcDir, err := os.MkdirTemp("", "spindrift-etc-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(etcDir)

	passwd := "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000:agent:/home/agent:/bin/bash\n"
	group := "root:x:0:\nagent:x:1000:\n"
	if err := os.WriteFile(filepath.Join(etcDir, "passwd"), []byte(passwd), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etcDir, "group"), []byte(group), 0o644); err != nil {
		return err
	}

	out := box.Output
	if out == nil {
		out = io.Discard
	}

	// The bwrap process's env is resolvedRunEnv(box.Env) -- the bwrapSecrets
	// subset of box.Env, not the launcher's own ambient environment. Without
	// --clearenv, the sandbox inherits it. Secrets (GH_TOKEN, auth tokens)
	// are therefore available inside the sandbox without appearing on argv.
	cmd := execCommand("bwrap", a.buildArgs(etcDir, box)...)
	cmd.Env = resolvedRunEnv(box.Env)
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
}

// NewBwrapBuild constructs a bwrap adapter for the build command from cfg.
// EnsureReady realizes agent store closures via nix build.
func NewBwrapBuild(cfg Config) Runner {
	return &bwrapBuildAdapter{
		agentFilesDrv: cfg.AgentFilesDrv,
		agentEnvDrv:   cfg.AgentEnvDrv,
	}
}

// EnsureReady realizes the agent store closures via nix build. Nix is
// idempotent — if already realized this is fast. Real nix errors surface.
func (a *bwrapBuildAdapter) EnsureReady() error {
	fmt.Println("==> bwrap runner: realizing agent store closures (no image build/load)")

	nixFiles := execCommand("nix", "build", a.agentFilesDrv+"^*", "--no-link")
	nixFiles.Stdout = os.Stdout
	nixFiles.Stderr = os.Stderr
	if err := nixFiles.Run(); err != nil {
		return fmt.Errorf("nix build agent-files: %w", err)
	}

	nixEnv := execCommand("nix", "build", a.agentEnvDrv+"^*", "--no-link")
	nixEnv.Stdout = os.Stdout
	nixEnv.Stderr = os.Stderr
	if err := nixEnv.Run(); err != nil {
		return fmt.Errorf("nix build agent-env: %w", err)
	}

	fmt.Println("==> done: agent store closures realized")
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
