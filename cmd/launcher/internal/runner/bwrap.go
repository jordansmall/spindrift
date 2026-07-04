package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BwrapConfig carries the baked config the bwrap adapter needs.
type BwrapConfig struct {
	// AgentFiles is the realised store path containing the agent tree (/agent).
	AgentFiles string
	// AgentEnv is the realised store path containing the agent's runtime env.
	AgentEnv string
	// BakedPrefetch is the shell snippet baked into the image to warm caches.
	BakedPrefetch string
	// AgentFilesDrv is the derivation path for the agent-files closure.
	// EnsureReady realises it via nix build.
	AgentFilesDrv string
	// AgentEnvDrv is the derivation path for the agent-env closure.
	// EnsureReady realises it via nix build.
	AgentEnvDrv string
	// SpindriftPromptDir, if non-empty and a directory, is bind-mounted over the
	// baked prompt so the agent uses the caller's custom template.
	SpindriftPromptDir string
}

type bwrapAdapter struct {
	cfg BwrapConfig
	cmd commandFunc
}

// NewBwrap returns a bwrap Runner for the given config.
func NewBwrap(cfg BwrapConfig) Runner {
	return &bwrapAdapter{cfg: cfg, cmd: exec.Command}
}

// EnsureReady realises the agent store closures so the bwrap sandbox can exec them.
func (a *bwrapAdapter) EnsureReady() error {
	fmt.Println("==> bwrap runner: realising agent store closures (no image build/load)")
	if err := a.realise(a.cfg.AgentFilesDrv); err != nil {
		return err
	}
	if err := a.realise(a.cfg.AgentEnvDrv); err != nil {
		return err
	}
	fmt.Println("==> done: agent store closures realised")
	return nil
}

func (a *bwrapAdapter) realise(drv string) error {
	cmd := a.cmd("nix", "build", drv+"^*", "--no-link")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Run launches the Box in a bubblewrap sandbox and blocks until it exits.
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
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	args = append(args, "--ro-bind", a.cfg.AgentFiles+"/agent", "/agent")
	if a.cfg.SpindriftPromptDir != "" {
		if info, err := os.Stat(a.cfg.SpindriftPromptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.cfg.SpindriftPromptDir)
			args = append(args, "--ro-bind", a.cfg.SpindriftPromptDir, "/agent/prompts")
		}
	}
	args = append(args,
		"--clearenv",
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", a.cfg.AgentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", a.cfg.AgentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", a.cfg.AgentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.cfg.BakedPrefetch,
	)
	for k, v := range box.Env {
		args = append(args, "--setenv", k, v)
	}
	args = append(args,
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--", "/agent/entrypoint.sh",
	)

	cmd := a.cmd("bwrap", args...)
	stdout := box.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := box.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Reap is a no-op for bwrap: bubblewrap has no persistent named sandboxes.
func (a *bwrapAdapter) Reap(_ string) error {
	return nil
}
