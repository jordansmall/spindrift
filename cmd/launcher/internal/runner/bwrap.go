package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// bwrapAdapter implements Runner for the daemonless bubblewrap sandbox.
// EnsureReady is a no-op — store closures are realised by the build command.
type bwrapAdapter struct {
	agentFiles    string // baked nix store path for agent files (/agent/…)
	agentEnv      string // baked nix store path for the agent env (PATH, SSL, …)
	bakedPrefetch string // baked prefetch snippet fed to the entrypoint
	promptDir     string // optional host path to bind-mount over /agent/prompts
}

// NewBwrap constructs a bwrap adapter for the run command.
// EnsureReady is a no-op; call NewBwrapBuild for the build command.
func NewBwrap(agentFiles, agentEnv, bakedPrefetch, promptDir string) Runner {
	return &bwrapAdapter{
		agentFiles:    agentFiles,
		agentEnv:      agentEnv,
		bakedPrefetch: bakedPrefetch,
		promptDir:     promptDir,
	}
}

// EnsureReady is a no-op for bwrap run: store closures are realised by
// `launcher build` (bwrapBuildAdapter.EnsureReady) before `run` is invoked.
func (a *bwrapAdapter) EnsureReady() error { return nil }

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
	args = append(args, "--ro-bind", a.agentFiles+"/agent", "/agent")
	if a.promptDir != "" {
		if info, err := os.Stat(a.promptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.promptDir)
			args = append(args, "--ro-bind", a.promptDir, "/agent/prompts")
		}
	}
	args = append(args,
		"--clearenv",
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", a.agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", a.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", a.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "PREFETCH", a.bakedPrefetch,
	)
	for k, v := range box.Env {
		args = append(args, "--setenv", k, v)
	}
	args = append(args,
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--", "/agent/entrypoint.sh",
	)

	out := box.Output
	if out == nil {
		out = io.Discard
	}

	cmd := exec.Command("bwrap", args...)
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
