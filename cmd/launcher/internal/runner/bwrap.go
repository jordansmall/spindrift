package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// execCommand builds the *exec.Cmd for hardcoded-binary orchestration (nix,
// bwrap) that has no configurable CLI field to intercept. Tests swap this
// package-level seam to substitute a fake binary; production always uses the
// standard library's exec.Command unmodified.
var execCommand = exec.Command

// bwrapSecrets is the set of box.Env keys whose values must not appear on the
// bwrap command line. They are delivered via the process environment instead
// so that ps/proc cannot expose them to other local users.
var bwrapSecrets = map[string]bool{
	"GH_TOKEN":                true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":       true,
}

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady is a no-op — store closures are realized by the build command.
type bwrapAdapter struct {
	agentFiles      string // baked nix store path for agent files (/agent/…)
	agentEnv        string // baked nix store path for the agent env (PATH, SSL, …)
	bakedPrefetch   string // baked prefetch snippet fed to the entrypoint
	promptDir       string // optional host path to bind-mount over /agent/prompts
	skillsDir       string // optional host path to bind-mount over driverSkillsDir
	driverSkillsDir string // in-box skills bind target (Driver declaration, ADR 0009)
	// driverSessionCacheDir is the in-box bind target for the Driver's
	// session-state dir (Driver declaration, ADR 0009); empty when the
	// selected Driver declares no session-state dir, in which case
	// box.DriverCacheDir is never bound regardless of its value.
	driverSessionCacheDir string
	// codeForge is the CODE_FORGE knob value; accumulationRepoDir is the host
	// path to the bare Accumulation repo bound read-only at /repo when it is
	// "local" (ADR 0033, issue #1697). boxForgeAndIssueAccess is the
	// BOX_FORGE_AND_ISSUE_ACCESS knob value; see MountParams.
	codeForge              string
	accumulationRepoDir    string
	boxForgeAndIssueAccess string
	// issueTracker and localIssuesDir gate the read-only /issues mount
	// (ADR 0032); see MountParams.
	issueTracker   string
	localIssuesDir string
	unshareNet     bool // when true, adds --unshare-net (isolates from host netns)

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
		agentFiles:             cfg.AgentFiles,
		agentEnv:               cfg.AgentEnv,
		bakedPrefetch:          cfg.BakedPrefetch,
		promptDir:              cfg.PromptDir,
		skillsDir:              cfg.SkillsDir,
		driverSkillsDir:        cfg.DriverSkillsDir,
		driverSessionCacheDir:  cfg.DriverSessionCacheDir,
		codeForge:              cfg.CodeForge,
		accumulationRepoDir:    cfg.AccumulationRepoDir,
		boxForgeAndIssueAccess: cfg.BoxForgeAndIssueAccess,
		issueTracker:           cfg.IssueTracker,
		localIssuesDir:         cfg.LocalIssuesDir,
		unshareNet:             cfg.BwrapUnshareNet,
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
		PromptDir:              a.promptDir,
		SkillsDir:              a.skillsDir,
		DriverSkillsDir:        a.driverSkillsDir,
		DriverSessionCacheDir:  a.driverSessionCacheDir,
		CodeForge:              a.codeForge,
		AccumulationRepoDir:    a.accumulationRepoDir,
		BoxForgeAndIssueAccess: a.boxForgeAndIssueAccess,
		IssueTracker:           a.issueTracker,
		LocalIssuesDir:         a.localIssuesDir,
	}, box)
}

// buildArgs constructs the bwrap command-line arguments for the given box.
// etcDir is the temp directory holding the synthesised /etc/passwd and /etc/group.
// Secret env vars (GH_TOKEN, auth tokens) are intentionally excluded from argv;
// they reach the sandbox via inherited process environment (no --clearenv).
func (a *bwrapAdapter) buildArgs(etcDir string, box Box) []string {
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
	if !a.unshareNet {
		if _, err := os.Stat("/etc/resolv.conf"); err == nil {
			args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	}
	args = append(args, "--ro-bind", a.agentFiles+"/agent", "/agent")
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
	// buildMountSpecs only covers the runtime skills override; the image's
	// own baked skills (no host-side equivalent for OCI, so this fallback
	// stays bwrap-only) fill in only when no runtime override was requested
	// at all. An override that was requested but doesn't resolve to a
	// directory yields no skills mount — it must not silently fall through
	// to the baked skills.
	if a.skillsDir == "" && a.driverSkillsDir != "" {
		bakedSkillsPath := filepath.Join(a.agentFiles, a.driverSkillsDir)
		if spec, ok := candidateMount(bakedSkillsPath, a.driverSkillsDir, true); ok {
			args = append(args, "--ro-bind", spec.Source, spec.Target)
		}
	}
	// --clearenv is intentionally absent: secrets (GH_TOKEN, auth tokens) reach
	// the sandbox by inheriting the launcher's process environment. Values on
	// argv are visible in ps/proc, so secrets must not appear there.
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
	if a.unshareNet {
		unshareFlags = append(unshareFlags, "--unshare-net")
	}
	args = append(args, unshareFlags...)
	args = append(args, "--", "/agent/entrypoint.sh")
	return args
}

// resolvedRunEnv returns the process environment the bwrap child should
// inherit: ambient with its GH_TOKEN entry (if any) replaced by
// box.Env["GH_TOKEN"] -- the value dispatchConfig's ResolveEnv chain
// computed, which reflects a BOX_GH_TOKEN override (ADR 0016, issue #380)
// when the operator set one. buildArgs's --setenv loop skips GH_TOKEN
// (bwrapSecrets) to keep it off argv, and bwrap has no --clearenv, so
// without this substitution the sandbox would inherit ambient's GH_TOKEN
// unconditionally, silently ignoring any override. BOX_GH_TOKEN itself is
// always stripped from ambient, present or not: it's a real var on the
// launcher's own process environment whenever the operator sets one, and
// lib/env-schema.nix's boxGhToken entry is deliberately boxEnv=false -- it
// must never reach the Box under its own name, only ever as GH_TOKEN's
// substituted value above. A missing "GH_TOKEN" key in box.Env (every
// caller outside production, and any future BoxEnvVars config that drops
// it) leaves ambient's own GH_TOKEN untouched.
func resolvedRunEnv(ambient []string, boxEnv map[string]string) []string {
	token, hasOverride := boxEnv["GH_TOKEN"]
	out := make([]string, 0, len(ambient)+1)
	for _, kv := range ambient {
		if strings.HasPrefix(kv, "BOX_GH_TOKEN=") {
			continue
		}
		if hasOverride && strings.HasPrefix(kv, "GH_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	if hasOverride {
		out = append(out, "GH_TOKEN="+token)
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

	// The bwrap process inherits the launcher's full environment (with
	// GH_TOKEN substituted per resolvedRunEnv). Without --clearenv, the
	// sandbox also inherits it. Secrets (GH_TOKEN, auth tokens) are
	// therefore available inside the sandbox without appearing on argv.
	cmd := execCommand("bwrap", a.buildArgs(etcDir, box)...)
	cmd.Env = resolvedRunEnv(os.Environ(), box.Env)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return err
	}
	a.trackRunning(box.Name, cmd.Process)
	defer a.untrackRunning(box.Name)
	return cmd.Wait()
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
