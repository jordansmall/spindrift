package runner

import (
	"bytes"
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
	imageDrv        string // baked .drv path; used by nix build for host realize
	imageTag        string // content-hash tag applied after loading
	nixBuilderImage string // fallback container image that carries nix
	nixVolume       string // named volume for /nix (incremental rebuilds)
	flakeImageAttr  string // nix flake attr for the image (.#packages.x.agent-image)
	pwd             string // $PWD; container-fallback mounts this as /workspace
	promptDir       string // optional host path to mount over /agent/prompts
	skillsDir       string // optional host path to mount over /home/agent/.claude/skills
	podmanNetwork   string // optional --network value; empty omits the flag
	pidsLimit       string // --pids-limit value; empty disables the flag
	memoryLimit     string // --memory value; empty disables the flag
}

// NewOCI constructs an OCI adapter. pwd is the working directory (used for the
// container-fallback path); promptDir is the optional SPINDRIFT_PROMPT_DIR;
// skillsDir is the optional SPINDRIFT_SKILLS_DIR;
// podmanNetwork is the optional --network value (empty omits the flag).
// pidsLimit and memoryLimit set the --pids-limit and --memory flags; empty
// string omits the flag.
func NewOCI(cli, image, imageArchive, imageDrv, imageTag, nixBuilderImage, nixVolume, flakeImageAttr, pwd, promptDir, skillsDir, podmanNetwork, pidsLimit, memoryLimit string) Runner {
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
		skillsDir:       skillsDir,
		podmanNetwork:   podmanNetwork,
		pidsLimit:       pidsLimit,
		memoryLimit:     memoryLimit,
	}
}

// IsReady checks that the OCI image is already loaded without building.
// Returns a descriptive error if absent so the caller can fail fast.
func (a *ociAdapter) IsReady() error {
	inspect := exec.Command(a.cli, "image", "inspect", a.image)
	inspect.Stdout = io.Discard
	inspect.Stderr = io.Discard
	if err := inspect.Run(); err != nil {
		return fmt.Errorf("image absent; run `spindrift build`")
	}
	return nil
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

	// 1. Try host build; tee stderr so errors are visible AND inspectable.
	var hostStderr bytes.Buffer
	nixBuild := exec.Command("nix", "build", a.imageDrv+"^*", "--no-link")
	nixBuild.Stdout = os.Stdout
	nixBuild.Stderr = io.MultiWriter(os.Stderr, &hostStderr)
	if err := nixBuild.Run(); err == nil {
		fmt.Println("==> realized image derivation on the host")
		return a.loadImage(a.imageArchive)
	}

	// Host build failed: only fall back to the container for builder-missing
	// errors. A genuine derivation error is already printed to stderr above —
	// stop here so the real message is not buried by a slow, doomed retry.
	if !isNoBuilderError(hostStderr.String()) {
		return fmt.Errorf("nix build failed")
	}

	// 2. Fall back to ephemeral nix container if the runtime is on PATH.
	if _, err := exec.LookPath(a.cli); err == nil {
		return a.buildInContainer()
	}

	// 3. Neither path is possible. Reachable only from `build`, which skips
	//    validate() (main.go) and so does not guarantee the runtime is on PATH;
	//    from `run` validate() already guaranteed it, making branch 2 succeed.
	fmt.Fprintf(os.Stderr, `==> cannot build the spindrift image.

The image is a Linux (OCI) derivation, and this host can neither realize it
directly nor fall back to a container build:

  * No Linux builder: 'nix build' could not realize the image. On macOS, enable
    nix-darwin's 'nix.linux-builder.enable = true;', or point nix at a remote
    Linux builder via 'nix.buildMachines' / '--builders'.

  * No container runtime: '%s' was not found on PATH. Install it (or set
    'runtime = "docker"' in your mkHarness call) so 'build' can build the image
    inside an ephemeral Nix container.

Run 'build' from your Consumer flake's directory.
`, a.cli)
	return fmt.Errorf("cannot build image: no Linux builder and no container runtime")
}

// isDigestPinned reports whether image is pinned by digest (@sha256:…).
// Mutable tags like :latest return false; a digest reference is immutable.
func isDigestPinned(image string) bool {
	return strings.Contains(image, "@sha256:")
}

// isNoBuilderError reports whether nix stderr indicates a missing Linux
// builder rather than a genuine derivation error. Only builder-missing failures
// should trigger the container fallback; real errors must surface immediately.
func isNoBuilderError(stderr string) bool {
	return strings.Contains(stderr, "required to build") ||
		strings.Contains(stderr, "no build machines")
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
	// Stage under a temp dir so interruption never litters the consumer tree.
	tmpDir, err := os.MkdirTemp("", "spindrift-build-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if !isDigestPinned(a.nixBuilderImage) {
		fmt.Fprintf(os.Stderr, "==> WARNING: nixBuilderImage %q is not digest-pinned; use @sha256:… for supply-chain safety\n", a.nixBuilderImage)
	}
	fmt.Printf("==> no host Linux builder; building the image inside a %s container\n", a.nixBuilderImage)
	fmt.Printf("    (reusing the '%s' volume for /nix so rebuilds are incremental)\n", a.nixVolume)

	shCmd := fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >/build-output/image-path && cp \"$(cat /build-output/image-path)\" /build-output/image.tar",
		a.flakeImageAttr,
	)
	build := exec.Command(a.cli, "run", "--rm",
		"-v", a.nixVolume+":/nix",
		"-v", a.pwd+":/workspace",
		"-v", tmpDir+":/build-output",
		"-w", "/workspace",
		a.nixBuilderImage,
		"sh", "-euc", shCmd,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "==> container build failed — see the %s output above.\n", a.cli)
		return fmt.Errorf("container build failed")
	}
	return a.loadImage(filepath.Join(tmpDir, "image.tar"))
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

// buildRunArgs assembles the argument slice for `podman/docker run`. Separated
// from Run so the arg construction can be tested without exec.
func (a *ociAdapter) buildRunArgs(box Box) []string {
	args := []string{"run", "--name", box.Name}
	if a.podmanNetwork != "" {
		args = append(args, "--network", a.podmanNetwork)
	}
	for k, v := range box.Env {
		args = append(args, "-e", k+"="+v)
	}
	if a.promptDir != "" {
		if info, err := os.Stat(a.promptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", a.promptDir)
			args = append(args, "-v", a.promptDir+":/agent/prompts:ro")
		}
	}
	if a.skillsDir != "" {
		if info, err := os.Stat(a.skillsDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_SKILLS_DIR set; mounting %s over /home/agent/.claude/skills\n", a.skillsDir)
			args = append(args, "-v", a.skillsDir+":/home/agent/.claude/skills:ro")
		}
	}
	// Security hardening — always drop all capabilities and block privilege
	// escalation; these are unconditional so no consumer knob can silently
	// weaken the sandbox.
	args = append(args, "--cap-drop=all", "--security-opt=no-new-privileges")
	// Resource caps — configurable so consumers can tune without a rebuild.
	if a.pidsLimit != "" {
		args = append(args, "--pids-limit="+a.pidsLimit)
	}
	if a.memoryLimit != "" {
		args = append(args, "--memory="+a.memoryLimit)
	}
	args = append(args, a.image, "/agent/entrypoint.sh")
	return args
}

// reapOrphanedRebaseDirs removes leftover spindrift-rebase-* directories in root.
// These are created by forge.Rebase and cleaned up with defer; they become orphaned
// when the launcher is killed before the defer runs.
func reapOrphanedRebaseDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "spindrift-rebase-") {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err == nil {
			fmt.Printf("==> reaped orphaned rebase temp dir: %s\n", path)
		}
	}
}

// Run fans out a single issue into a podman/docker container.
func (a *ociAdapter) Run(box Box) error {
	// Reap any orphaned rebase temp dirs left by a prior killed launcher run.
	reapOrphanedRebaseDirs(os.TempDir())
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

	cmd := exec.Command(a.cli, a.buildRunArgs(box)...)
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if reapAfterSuccess(err) {
		_ = a.Reap(box.Name)
	}
	return err
}

// reapAfterSuccess reports whether the container should be reaped based on the
// error returned from cmd.Run. A nil error (clean exit) triggers a reap;
// any non-nil error retains the container so a human can recover locally.
func reapAfterSuccess(err error) bool {
	return err == nil
}

// Reap removes a named container (best-effort). Never removes a running container.
func (a *ociAdapter) Reap(name string) error {
	if !a.containerIsRunning(name) {
		reap := exec.Command(a.cli, "rm", "-f", name)
		_ = reap.Run()
	}
	return nil
}
