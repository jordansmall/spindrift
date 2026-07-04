package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OCIConfig carries the baked config the OCI adapter needs.
type OCIConfig struct {
	// CLI is the container runtime command name: "podman" or "docker".
	CLI string
	// Image is the runtime image reference (content-hash tag or override).
	Image string
	// ImageArchive is the nix store path to the OCI tarball.
	ImageArchive string
	// ImageTag is the tag to apply after loading (e.g. "spindrift:<hash>").
	ImageTag string
	// ImageDrv is the nix derivation path for host-side builds.
	ImageDrv string
	// NixBuilderImage is the container image used for the container-fallback build.
	NixBuilderImage string
	// NixVolume is the named volume for the /nix cache inside the builder container.
	NixVolume string
	// FlakeImageAttr is the flake attribute to build inside the fallback container.
	FlakeImageAttr string
	// SpindriftPromptDir, if non-empty and a directory, is bind-mounted over the
	// baked prompt so the agent uses the caller's custom template.
	SpindriftPromptDir string
}

// commandFunc matches exec.Command's signature; swapped in unit tests.
type commandFunc func(name string, arg ...string) *exec.Cmd

type ociAdapter struct {
	cfg OCIConfig
	cmd commandFunc
}

// NewOCI returns an OCI Runner for the given config.
func NewOCI(cfg OCIConfig) Runner {
	return &ociAdapter{cfg: cfg, cmd: exec.Command}
}

// EnsureReady checks that the OCI image is present, building it if not.
// It uses "image inspect" — portable across podman and docker — as the
// presence check, fixing the podman-only "image exists" bug (#92).
func (a *ociAdapter) EnsureReady() error {
	// "image inspect" exits 0 when the image is present on both podman and
	// docker. "image exists" is podman-only and breaks with docker.
	check := a.cmd(a.cfg.CLI, "image", "inspect", a.cfg.Image)
	check.Stdout = nil
	check.Stderr = nil
	if check.Run() == nil {
		return nil
	}
	fmt.Printf("==> image '%s' not found — building first\n", a.cfg.Image)
	return a.buildImage()
}

// buildImage tries a host nix build, then an ephemeral container fallback.
func (a *ociAdapter) buildImage() error {
	// 1. Host build: nix build <drv>^* --no-link
	nixBuild := a.cmd("nix", "build", a.cfg.ImageDrv+"^*", "--no-link")
	nixBuild.Stdout = os.Stdout
	nixBuild.Stderr = os.Stderr
	if err := nixBuild.Run(); err == nil {
		fmt.Println("==> realised image derivation on the host")
		return a.loadImage(a.cfg.ImageArchive)
	}

	// 2. Container fallback.
	if _, err := exec.LookPath(a.cfg.CLI); err == nil {
		return a.buildInContainer()
	}

	// 3. Neither path works.
	fmt.Fprintf(os.Stderr, `==> cannot build the spindrift image.

The image is a Linux (OCI) derivation, and this host can neither realise it
directly nor fall back to a container build:

  * No Linux builder: 'nix build' could not realise the image. On macOS, enable
    nix-darwin's 'nix.linux-builder.enable = true;', or point nix at a remote
    Linux builder via 'nix.buildMachines' / '--builders'.

  * No container runtime: '%s' was not found on PATH. Install it (or set
    'runtime = "docker"' in your mkHarness call) so 'build' can build the image
    inside an ephemeral Nix container.

Run 'build' from your Consumer flake's directory.
`, a.cfg.CLI)
	return fmt.Errorf("cannot build image: no Linux builder and no container runtime")
}

func (a *ociAdapter) loadImage(archive string) error {
	fmt.Printf("==> loading spindrift image from %s\n", archive)
	load := a.cmd(a.cfg.CLI, "load", "-i", archive)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("load failed: %w", err)
	}
	tag := a.cmd(a.cfg.CLI, "tag", "spindrift:latest", a.cfg.ImageTag)
	tag.Stdout = os.Stdout
	tag.Stderr = os.Stderr
	if err := tag.Run(); err != nil {
		return fmt.Errorf("tag failed: %w", err)
	}
	fmt.Printf("==> done: spindrift:latest + %s\n", a.cfg.ImageTag)
	return nil
}

func (a *ociAdapter) buildInContainer() error {
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	tar := filepath.Join(pwd, ".spindrift-image.tar")
	pathfile := ".spindrift-image-path"
	fmt.Printf("==> no host Linux builder; building the image inside a %s container\n", a.cfg.NixBuilderImage)
	fmt.Printf("    (reusing the '%s' volume for /nix so rebuilds are incremental)\n", a.cfg.NixVolume)

	shCmd := fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >%s && cp \"$(cat %s)\" .spindrift-image.tar",
		a.cfg.FlakeImageAttr, pathfile, pathfile,
	)
	build := a.cmd(a.cfg.CLI, "run", "--rm",
		"-v", a.cfg.NixVolume+":/nix",
		"-v", pwd+":/workspace",
		"-w", "/workspace",
		a.cfg.NixBuilderImage,
		"sh", "-euc", shCmd,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "==> container build failed — see the %s output above.\n", a.cfg.CLI)
		_ = os.Remove(tar)
		_ = os.Remove(filepath.Join(pwd, pathfile))
		return fmt.Errorf("container build failed")
	}
	if err := a.loadImage(tar); err != nil {
		return err
	}
	_ = os.Remove(tar)
	_ = os.Remove(filepath.Join(pwd, pathfile))
	return nil
}

// Run launches the Box in an OCI container and blocks until it exits.
func (a *ociAdapter) Run(box Box) error {
	args := []string{"run", "--rm",
		"--name", box.Name,
	}
	for k, v := range box.Env {
		args = append(args, "-e", k+"="+v)
	}
	if a.cfg.SpindriftPromptDir != "" {
		if info, err := os.Stat(a.cfg.SpindriftPromptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.cfg.SpindriftPromptDir)
			args = append(args, "-v", a.cfg.SpindriftPromptDir+":/agent/prompts:ro")
		}
	}
	args = append(args, a.cfg.Image, "/agent/entrypoint.sh")

	cmd := a.cmd(a.cfg.CLI, args...)
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

// Reap removes any leftover container with the given name (best-effort).
func (a *ociAdapter) Reap(name string) error {
	reap := a.cmd(a.cfg.CLI, "rm", "-f", name)
	_ = reap.Run()
	return nil
}
