package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureImage verifies the OCI image exists, building and loading it if not.
func ensureImage(cfg *Config) error {
	if imageExists(cfg) {
		return nil
	}

	fmt.Printf("==> image '%s' not found — building first\n", cfg.ImageTag)

	// 1. Try host nix build.
	if err := nixBuild(cfg); err == nil {
		return loadImage(cfg, cfg.ImageArchive)
	}

	// 2. Fall back to an ephemeral container build if the runtime is present.
	if _, err := exec.LookPath(cfg.Runtime); err != nil {
		return failNoBuilder(cfg)
	}

	return buildInContainer(cfg)
}

func imageExists(cfg *Config) bool {
	return exec.Command(cfg.Runtime, "image", "exists", cfg.ImageTag).Run() == nil
}

func nixBuild(cfg *Config) error {
	return exec.Command("nix", "build", cfg.ImageDRV+"^*", "--no-link").Run()
}

func loadImage(cfg *Config, archive string) error {
	fmt.Printf("==> loading spindrift image from %s\n", archive)
	if err := exec.Command(cfg.Runtime, "load", "-i", archive).Run(); err != nil {
		return fmt.Errorf("loading image: %w", err)
	}
	if err := exec.Command(cfg.Runtime, "tag", "spindrift:latest", cfg.ImageTag).Run(); err != nil {
		return fmt.Errorf("tagging image: %w", err)
	}
	fmt.Printf("==> done: spindrift:latest + %s\n", cfg.ImageTag)
	return nil
}

// buildInContainer builds the image inside an ephemeral nix container and
// loads the resulting tarball via the host runtime.
func buildInContainer(cfg *Config) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	tarPath := filepath.Join(cwd, ".spindrift-image.tar")
	pathFile := ".spindrift-image-path"

	fmt.Printf("==> no host Linux builder; building the image inside a %s container\n", cfg.NixBuilderImage)
	fmt.Printf("    (reusing the '%s' volume for /nix so rebuilds are incremental)\n", cfg.NixVolume)

	buildCmd := fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >%s && cp \"$(cat %s)\" .spindrift-image.tar",
		cfg.FlakeImageAttr, pathFile, pathFile,
	)

	cmd := exec.Command(cfg.Runtime, "run", "--rm",
		"-v", cfg.NixVolume+":/nix",
		"-v", cwd+":/workspace",
		"-w", "/workspace",
		cfg.NixBuilderImage,
		"sh", "-euc", buildCmd,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		_ = os.Remove(tarPath)
		_ = os.Remove(filepath.Join(cwd, pathFile))
		return fmt.Errorf("==> container build failed — see the %s output above.", cfg.Runtime)
	}

	if err := loadImage(cfg, tarPath); err != nil {
		_ = os.Remove(tarPath)
		_ = os.Remove(filepath.Join(cwd, pathFile))
		return err
	}
	_ = os.Remove(tarPath)
	_ = os.Remove(filepath.Join(cwd, pathFile))
	return nil
}

func failNoBuilder(cfg *Config) error {
	return fmt.Errorf(`==> cannot build the spindrift image.

The image is a Linux (OCI) derivation, and this host can neither realise it
directly nor fall back to a container build:

  * No Linux builder: 'nix build' could not realise the image. On macOS, enable
    nix-darwin's 'nix.linux-builder.enable = true;', or point nix at a remote
    Linux builder via 'nix.buildMachines' / '--builders'.

  * No container runtime: '%s' was not found on PATH. Install it (or set
    'runtime = "docker"' in your mkHarness call) so 'build' can build the image
    inside an ephemeral Nix container.

Run 'build' from your Consumer flake's directory.`, strings.TrimSuffix(cfg.Runtime, "\n"))
}
