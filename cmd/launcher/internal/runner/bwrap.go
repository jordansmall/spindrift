package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// bwrapSecrets is the set of box.Env keys whose values must not appear on the
// bwrap command line. They are delivered via the process environment instead
// so that ps/proc cannot expose them to other local users.
var bwrapSecrets = map[string]bool{
	"GH_TOKEN":                true,
	"CLAUDE_CODE_OAUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":       true,
}

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady is a no-op — store closures are realised by the build command.
type bwrapAdapter struct {
	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	bakedPrefetch string // baked prefetch snippet fed to the entrypoint
	promptDir     string // optional host path to bind-mount over /agent/prompts
	skillsDir     string // optional host path to bind-mount over /home/agent/.claude/skills
	unshareNet    bool   // when true, adds --unshare-net (isolates from host netns)
}

// NewBwrap constructs a bwrap adapter for the run command.
// EnsureReady is a no-op; call NewBwrapBuild for the build command.
// unshareNet adds --unshare-net to isolate from the host network namespace;
// when false, the sandbox shares the host netns (host-loopback reachable).
func NewBwrap(agentFiles, agentEnv, bakedPrefetch, promptDir, skillsDir string, unshareNet bool) Runner {
	return &bwrapAdapter{
		agentFiles:    agentFiles,
		agentEnv:      agentEnv,
		bakedPrefetch: bakedPrefetch,
		promptDir:     promptDir,
		skillsDir:     skillsDir,
		unshareNet:    unshareNet,
	}
}

// EnsureReady is a no-op for bwrap run: store closures are realised by
// `launcher build` (bwrapBuildAdapter.EnsureReady) before `run` is invoked.
func (a *bwrapAdapter) EnsureReady() error { return nil }

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
	if a.promptDir != "" {
		if info, err := os.Stat(a.promptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.promptDir)
			args = append(args, "--ro-bind", a.promptDir, "/agent/prompts")
		}
	}
	// Runtime mount takes precedence over baked skills; fall back to baked
	// skills when no runtime override is set.
	if a.skillsDir != "" {
		if info, err := os.Stat(a.skillsDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_SKILLS_DIR set; mounting %s over /home/agent/.claude/skills\n", a.skillsDir)
			args = append(args, "--ro-bind", a.skillsDir, "/home/agent/.claude/skills")
		}
	} else {
		bakedSkillsPath := filepath.Join(a.agentFiles, "home", "agent", ".claude", "skills")
		if info, err := os.Stat(bakedSkillsPath); err == nil && info.IsDir() {
			args = append(args, "--ro-bind", bakedSkillsPath, "/home/agent/.claude/skills")
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

// Run fans out a single issue into a bubblewrap sandbox.
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

	// cmd.Env = nil: the bwrap process inherits the launcher's full environment.
	// Without --clearenv, the sandbox also inherits it. Secrets (GH_TOKEN, auth
	// tokens) are therefore available inside the sandbox without appearing on argv.
	cmd := exec.Command("bwrap", a.buildArgs(etcDir, box)...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// Reap is a no-op for bwrap — sandboxes are ephemeral and exit when done.
func (a *bwrapAdapter) Reap(_ string) error { return nil }

// bwrapBuildAdapter implements Runner for the `launcher build` bwrap path.
// EnsureReady realises the agent store closures; Run is not supported.
type bwrapBuildAdapter struct {
	agentFilesDrv string // .drv path for agentFiles
	agentEnvDrv   string // .drv path for agentEnv
}

// NewBwrapBuild constructs a bwrap adapter for the build command.
// EnsureReady realises agent store closures via nix build.
func NewBwrapBuild(agentFilesDrv, agentEnvDrv string) Runner {
	return &bwrapBuildAdapter{
		agentFilesDrv: agentFilesDrv,
		agentEnvDrv:   agentEnvDrv,
	}
}

// EnsureReady realises the agent store closures via nix build. Nix is
// idempotent — if already realised this is fast. Real nix errors surface.
func (a *bwrapBuildAdapter) EnsureReady() error {
	fmt.Println("==> bwrap runner: realising agent store closures (no image build/load)")

	nixFiles := exec.Command("nix", "build", a.agentFilesDrv+"^*", "--no-link")
	nixFiles.Stdout = os.Stdout
	nixFiles.Stderr = os.Stderr
	if err := nixFiles.Run(); err != nil {
		return fmt.Errorf("nix build agent-files: %w", err)
	}

	nixEnv := exec.Command("nix", "build", a.agentEnvDrv+"^*", "--no-link")
	nixEnv.Stdout = os.Stdout
	nixEnv.Stderr = os.Stderr
	if err := nixEnv.Run(); err != nil {
		return fmt.Errorf("nix build agent-env: %w", err)
	}

	fmt.Println("==> done: agent store closures realised")
	return nil
}

// Run is not supported by the build adapter.
func (a *bwrapBuildAdapter) Run(_ Box) error {
	return fmt.Errorf("bwrap-build adapter: Run not supported (use bwrap run adapter)")
}

// Reap is a no-op for the build adapter.
func (a *bwrapBuildAdapter) Reap(_ string) error { return nil }
