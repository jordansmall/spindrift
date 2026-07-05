package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ociAdapter implements Runner for OCI container runtimes (podman or docker).
// podman and docker are one adapter differing only by CLI name.
type ociAdapter struct {
	cli             string // "podman" or "docker"
	image           string // tag to run (may be overridden by IMAGE env)
	imageArchive    string // baked nix store path to the OCI tarball
	imageDrv        string // baked .drv path; used by nix build for host realise
	imageTag        string // content-hash tag applied after loading
	nixBuilderImage string // fallback container image that carries nix
	nixVolume       string // named volume for /nix (incremental rebuilds)
	flakeImageAttr  string // nix flake attr for the image (.#packages.x.spindrift)
	pwd             string // $PWD; container-fallback mounts this as /workspace
	promptDir       string // optional host path to mount over /agent/prompts
}

// NewOCI constructs an OCI adapter. pwd is the working directory (used for the
// container-fallback path); promptDir is the optional SPINDRIFT_PROMPT_DIR.
func NewOCI(cli, image, imageArchive, imageDrv, imageTag, nixBuilderImage, nixVolume, flakeImageAttr, pwd, promptDir string) Runner {
	return &ociAdapter{
		cli:             cli,
		image:           image,
		imageArchive:    imageArchive,
		imageDrv:        imageDrv,
		imageTag:        imageTag,
		nixBuilderImage: nixBuilderImage,
		nixVolume:       nixVolume,
		flakeImageAttr:  flakeImageAttr,
		pwd:             pwd,
		promptDir:       promptDir,
	}
}

// EnsureReady checks that the OCI image is present; builds it if not.
// Uses `image inspect` (portable: docker has no `image exists` verb).
func (a *ociAdapter) EnsureReady() error {
	inspect := exec.Command(a.cli, "image", "inspect", a.image)
	inspect.Stdout = io.Discard
	inspect.Stderr = io.Discard
	if err := inspect.Run(); err == nil {
		return nil // image already present
	}
	fmt.Printf("==> image '%s' not found — building first\n", a.image)

	// 1. Try host build (nix build <drv>^* --no-link).
	nixBuild := exec.Command("nix", "build", a.imageDrv+"^*", "--no-link")
	nixBuild.Stdout = os.Stdout
	nixBuild.Stderr = os.Stderr
	if err := nixBuild.Run(); err == nil {
		fmt.Println("==> realised image derivation on the host")
		return a.loadImage(a.imageArchive)
	}

	// 2. Fall back to ephemeral nix container if the runtime is on PATH.
	if _, err := exec.LookPath(a.cli); err == nil {
		return a.buildInContainer()
	}

	// 3. Neither path is possible. Reachable only from `build`, which skips
	//    validate() (main.go) and so does not guarantee the runtime is on PATH;
	//    from `run` validate() already guaranteed it, making branch 2 succeed.
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
`, a.cli)
	return fmt.Errorf("cannot build image: no Linux builder and no container runtime")
}

func (a *ociAdapter) loadImage(archive string) error {
	fmt.Printf("==> loading spindrift image from %s\n", archive)
	load := exec.Command(a.cli, "load", "-i", archive)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("load failed: %w", err)
	}
	tag := exec.Command(a.cli, "tag", "spindrift:latest", a.imageTag)
	tag.Stdout = os.Stdout
	tag.Stderr = os.Stderr
	if err := tag.Run(); err != nil {
		return fmt.Errorf("tag failed: %w", err)
	}
	fmt.Printf("==> done: spindrift:latest + %s\n", a.imageTag)
	return nil
}

func (a *ociAdapter) buildInContainer() error {
	tar := filepath.Join(a.pwd, ".spindrift-image.tar")
	pathfile := ".spindrift-image-path"
	fmt.Printf("==> no host Linux builder; building the image inside a %s container\n", a.nixBuilderImage)
	fmt.Printf("    (reusing the '%s' volume for /nix so rebuilds are incremental)\n", a.nixVolume)

	shCmd := fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >%s && cp \"$(cat %s)\" .spindrift-image.tar",
		a.flakeImageAttr, pathfile, pathfile,
	)
	build := exec.Command(a.cli, "run", "--rm",
		"-v", a.nixVolume+":/nix",
		"-v", a.pwd+":/workspace",
		"-w", "/workspace",
		a.nixBuilderImage,
		"sh", "-euc", shCmd,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "==> container build failed — see the %s output above.\n", a.cli)
		_ = os.Remove(tar)
		_ = os.Remove(filepath.Join(a.pwd, pathfile))
		return fmt.Errorf("container build failed")
	}
	if err := a.loadImage(tar); err != nil {
		return err
	}
	_ = os.Remove(tar)
	_ = os.Remove(filepath.Join(a.pwd, pathfile))
	return nil
}

// containerIsRunning reports whether name is currently in the "running" state.
// Returns false when the container is absent, exited, or inspect fails — in all
// of those cases the caller may safely proceed with rm -f.
func (a *ociAdapter) containerIsRunning(name string) bool {
	out, err := exec.Command(a.cli, "inspect", "--format={{.State.Status}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "running"
}

// Run fans out a single issue into a podman/docker container.
func (a *ociAdapter) Run(box Box) error {
	// Reap any stale (exited or created) container from a prior interrupted run.
	// Never touch a running container — a concurrent launcher invocation may own it,
	// and a force-remove would destroy that run's work silently.
	if !a.containerIsRunning(box.Name) {
		reap := exec.Command(a.cli, "rm", "-f", box.Name)
		_ = reap.Run()
	}

	out := box.Output
	if out == nil {
		out = io.Discard
	}

	args := []string{"run", "--rm", "--name", box.Name}
	for k, v := range box.Env {
		args = append(args, "-e", k+"="+v)
	}
	if a.promptDir != "" {
		if info, err := os.Stat(a.promptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.promptDir)
			args = append(args, "-v", a.promptDir+":/agent/prompts:ro")
		}
	}
	args = append(args, a.image, "/agent/entrypoint.sh")

	cmd := exec.Command(a.cli, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// Reap removes a named container (best-effort). Never removes a running container.
func (a *ociAdapter) Reap(name string) error {
	if !a.containerIsRunning(name) {
		reap := exec.Command(a.cli, "rm", "-f", name)
		_ = reap.Run()
	}
	return nil
}
