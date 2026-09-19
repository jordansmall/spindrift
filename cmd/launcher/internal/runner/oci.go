package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryprobe"
	"spindrift.dev/launcher/internal/unixsocket"
)

// ociAdapter implements Runner for OCI container runtimes; podman and docker
// are one adapter differing only by CLI name.
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
	// mountParams passes host-mount facts from Config to buildMountSpecs
	// unmodified. DriverSessionCacheDir is ADR 0009; the CODE_FORGE=local
	// mount specs are issue #1697.
	mountParams   MountParams
	podmanNetwork string // optional raw --network value; empty omits the flag
	networkMode   string // NETWORK_MODE knob ("open"/"no-host-loopback"/"none"/"")
	pidsLimit     string // --pids-limit value; empty disables the flag
	memoryLimit   string // --memory value; empty disables the flag
}

// NewOCI constructs an OCI adapter from cfg. pwd is the working directory the
// container-fallback build mounts as /workspace, a per-invocation dependency
// passed separately from cfg.
func NewOCI(cfg Config, pwd string) Runner {
	return &ociAdapter{
		cli:             BinaryFor(cfg.Runtime),
		image:           cfg.Image,
		imageArchive:    cfg.ImageArchive,
		imageDrv:        cfg.ImageDrv,
		imageTag:        cfg.ImageTag,
		nixBuilderImage: cfg.NixBuilderImage,
		nixVolume:       cfg.NixVolume,
		flakeImageAttr:  cfg.FlakeImageAttr,
		pwd:             pwd,
		mountParams:     cfg.MountParams,
		podmanNetwork:   cfg.PodmanNetwork,
		networkMode:     cfg.NetworkMode,
		pidsLimit:       cfg.PidsLimit,
		memoryLimit:     cfg.MemoryLimit,
	}
}

// IsReady reports whether the OCI image is already loaded, without building it.
func (a *ociAdapter) IsReady() error {
	inspect := exec.Command(a.cli, "image", "inspect", a.image)
	inspect.Stdout = io.Discard
	inspect.Stderr = io.Discard
	if err := inspect.Run(); err != nil {
		return fmt.Errorf("image absent; run `spindrift build`")
	}
	return nil
}

// EnsureReady checks that the OCI image is present and builds it if not. It
// inspects rather than asking `image exists`, which docker has no verb for.
func (a *ociAdapter) EnsureReady() error {
	inspect := exec.Command(a.cli, "image", "inspect", a.image)
	inspect.Stdout = io.Discard
	inspect.Stderr = io.Discard
	if err := inspect.Run(); err == nil {
		fmt.Printf("==> image '%s' already loaded\n", a.image)
		return nil
	}
	fmt.Printf("==> image '%s' not found — building first\n", a.image)

	// Tee stderr so a failure is both visible and inspectable below.
	var hostStderr bytes.Buffer
	nixBuild := execCommand("nix", "build", a.imageDrv+"^*", "--no-link")
	nixBuild.Stdout = os.Stdout
	nixBuild.Stderr = io.MultiWriter(os.Stderr, &hostStderr)
	if err := nixBuild.Run(); err == nil {
		fmt.Println("==> realized image derivation on the host")
		return a.loadImage(a.imageArchive)
	}

	// Only a missing builder justifies the container fallback: a genuine
	// derivation error is already on stderr, and a slow doomed retry buries it.
	if !isNoBuilderError(hostStderr.String()) {
		return fmt.Errorf("nix build failed")
	}

	if _, err := exec.LookPath(a.cli); err == nil {
		return a.buildInContainer()
	}

	// Reachable only from `build`, which skips main.go's validate(); under
	// `run` validate() already guaranteed the runtime, so the container
	// fallback above would have succeeded.
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

// isDigestPinned reports whether image is pinned by an immutable digest
// (@sha256:…) rather than a mutable tag like :latest.
func isDigestPinned(image string) bool {
	return strings.Contains(image, "@sha256:")
}

// isNoBuilderError reports whether nix stderr means a missing Linux builder
// rather than a genuine derivation error, which must be reported at once
// instead of triggering the container fallback.
func isNoBuilderError(stderr string) bool {
	return strings.Contains(stderr, "required to build") ||
		strings.Contains(stderr, "no build machines") ||
		strings.Contains(stderr, "Reason: platform mismatch")
}

// isTransientRegistryError reports whether stderr indicates a network hiccup
// reaching the registry rather than a genuine failure, so a caller can retry
// or skip instead of failing on a blip (issue #2015). Its only caller is the
// integration test, but it lives here untagged so checks-inbox unit-tests it
// without a real container runtime on PATH.
func isTransientRegistryError(stderr string) bool {
	for _, s := range []string{
		"i/o timeout",
		"no such host",
		"connection refused",
		"TLS handshake timeout",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// isRuntimeUnusableError reports whether stderr means the low-level runtime
// (crun/runc) failed to start the container at all. requireRealOCI only probes
// `<cli> info`, so a broken runtime ("crun: unknown version specified") slips
// past that gate; the integration probes skip on it rather than fail, since a
// runtime that cannot start a container can never hide a hardening regression.
func isRuntimeUnusableError(stderr string) bool {
	return strings.Contains(stderr, "OCI runtime error")
}

func (a *ociAdapter) loadImage(archive string) error {
	fmt.Printf("==> loading spindrift image from %s\n", archive)
	load := exec.Command(a.cli, "load", "-i", archive)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("load failed: %w", err)
	}
	// buildLayeredImage names the archive "<repo>:latest" where repo matches
	// a.imageTag's own repo, so re-tag from that derived source rather than a
	// hardcoded "spindrift:latest", which misses a driver-scoped archive.
	sourceTag := imageRepo(a.imageTag) + ":latest"
	tag := exec.Command(a.cli, "tag", sourceTag, a.imageTag)
	tag.Stdout = os.Stdout
	tag.Stderr = os.Stderr
	if err := tag.Run(); err != nil {
		return fmt.Errorf("tag failed: %w", err)
	}
	fmt.Printf("==> done: %s + %s\n", sourceTag, a.imageTag)
	return nil
}

// imageRepo returns the repo portion of an "<repo>:<tag>" reference, splitting
// on the LAST colon since a repo can itself contain one (a registry host:port
// prefix). A tag with no colon falls back to the default "spindrift" repo.
func imageRepo(imageTag string) string {
	i := strings.LastIndex(imageTag, ":")
	if i < 0 {
		return "spindrift"
	}
	return imageTag[:i]
}

// gitSafeDirectoryPrelude marks the bind-mounted /workspace safe so Nix's
// libgit2 dubious-ownership guard accepts a host repo owned by a UID other
// than container-root. printf writes the config directly because the builder
// image need not carry a git CLI (issue #2196).
const gitSafeDirectoryPrelude = `export HOME=/build-output/home; ` +
	`mkdir -p "$HOME"; ` +
	`printf '[safe]\n\tdirectory = *\n\tdirectory = /workspace\n' > "$HOME/.gitconfig"`

// containerBuildCmd assembles the `sh -euc` command the Nix builder container
// runs to build the image and stage it for the host. Split out of
// buildInContainer so tests can check it without spawning docker/podman.
func containerBuildCmd(flakeImageAttr string) string {
	return gitSafeDirectoryPrelude + "; " + fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >/build-output/image-path && cp \"$(cat /build-output/image-path)\" /build-output/image.tar",
		flakeImageAttr,
	)
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

	shCmd := containerBuildCmd(a.flakeImageAttr)
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

// IsRunning reports whether name is in the "running" state. Absent, exited, or
// a failed inspect all report false, and in each of those the caller may safely
// proceed with rm -f.
func (a *ociAdapter) IsRunning(name string) bool {
	out, err := exec.Command(a.cli, "inspect", "--format={{.State.Status}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "running"
}

// ListRunning returns the names of every running container under this runtime,
// for Console startup orphan detection (issue #651).
func (a *ociAdapter) ListRunning() ([]string, error) {
	out, err := exec.Command(a.cli, "ps", "--filter", "status=running", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// mountSpecs computes the host-to-box mounts for box, shared with the bwrap
// adapter; only the rendering below differs.
func (a *ociAdapter) mountSpecs(box Box) []MountSpec {
	return buildMountSpecs(a.mountParams, box)
}

// networkArg resolves `--network` from the raw podmanNetwork escape hatch and
// the NETWORK_MODE knob (issue #2562). Raw wins: mkHarness and main.go's
// checkNetworkModeRuntimeGate already reject both being set for a non-open
// mode. On docker/nerdctl "no-host-loopback" renders "bridge", their own
// default, so only podman's `pasta` without `--map-gw` denies the route.
func (a *ociAdapter) networkArg() string {
	if a.podmanNetwork != "" {
		return a.podmanNetwork
	}
	switch a.networkMode {
	case NetworkModeNoHostLoopback:
		if a.cli == "podman" {
			return "pasta"
		}
		return "bridge"
	case NetworkModeNone:
		return NetworkModeNone
	default:
		return ""
	}
}

// buildRunArgs assembles the argument slice for `podman/docker run`. Split out
// of Run so the arg construction can be tested without exec.
func (a *ociAdapter) buildRunArgs(box Box) []string {
	args := []string{"run", "--name", box.Name}
	if network := a.networkArg(); network != "" {
		args = append(args, "--network", network)
	}
	for k, v := range box.Env {
		if offArgvKeys[k] {
			// Bare "-e KEY" forwards KEY's value from the CLI process's own
			// environment, which ociRunEnv sets, so the value never lands in
			// argv, where ps/proc exposes it to any local user for the
			// container's whole lifetime (issue #3111 finding A).
			args = append(args, "-e", k)
			continue
		}
		args = append(args, "-e", k+"="+v)
	}
	// Mount decisions are computed once in buildMountSpecs, shared with the
	// bwrap adapter; OCI only renders each spec as a -v flag. The driver-cache
	// spec is scoped to the Driver's declared session-cache dir, never the
	// whole .claude, which would shadow the baked .claude/skills the image
	// ships.
	for _, m := range a.mountSpecs(box) {
		if m.Message != "" {
			fmt.Print(m.Message)
		}
		dst := m.Target
		if m.ReadOnly {
			dst += ":ro"
		}
		args = append(args, "-v", m.Source+":"+dst)
	}
	// A TCP-transport Box may need an explicit host-gateway mapping to resolve
	// TCPHost, which plain Linux docker will not resolve without it. Never
	// make it unconditional: a VM-backed runtime (Docker Desktop, Rancher
	// Desktop/Lima) resolves the name to the real host, and the mapping
	// overrides that with the in-VM bridge gateway (issue #3111).
	if box.RegistryProxy.TCPAddHost {
		args = append(args, "--add-host", box.RegistryProxy.Endpoint.Host()+":host-gateway")
	}
	// These two are unconditional, so no consumer knob can weaken the sandbox.
	args = append(args, "--cap-drop=all", "--security-opt=no-new-privileges")
	if a.pidsLimit != "" {
		args = append(args, "--pids-limit="+a.pidsLimit)
	}
	if a.memoryLimit != "" {
		args = append(args, "--memory="+a.memoryLimit)
	}
	args = append(args, a.image, "/agent/entrypoint.sh")
	return args
}

// probeSocketDir returns a fresh directory for the throwaway probe socket,
// preferring os.TempDir() but falling back to /tmp when that base would
// overflow AF_UNIX's sun_path limit (issue #3077), as macOS's per-user $TMPDIR
// under nix develop's nix-shell.XXXXXX/ prefix does. The fallback duplicates
// dispatch.registryProxySocketDir because runner cannot import dispatch.
func probeSocketDir() (string, error) {
	dir, err := os.MkdirTemp("", "spindrift-registry-probe-*")
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy probe dir: %w", err)
	}
	if !unixsocket.TooLong(filepath.Join(dir, "probe.sock")) {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove over-long registry proxy probe dir: %w", err)
	}
	// A too-long path from this fallback is net.Listen's error to raise, not
	// this function's.
	dir, err = os.MkdirTemp("/tmp", "spindrift-registry-probe-*")
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy probe dir under /tmp: %w", err)
	}
	return dir, nil
}

// hostGatewayHostname returns the hostname a Box resolves to reach the
// launcher's loopback interface over TCP (issue #3111). podman has its own
// convention; nerdctl follows docker's host.docker.internal.
func hostGatewayHostname(cli string) string {
	if cli == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// probeArgsFromRunArgs strips buildRunArgs' trailing "<image> <entrypoint>"
// pair off full, leaving the mount, network and hardening flags a throwaway
// probe reuses verbatim. It is the only place coupled to that trailing shape.
func probeArgsFromRunArgs(full []string) []string {
	return full[1 : len(full)-2]
}

// registryProbeEntrypoint replaces the image's own /bin/bash entrypoint for
// throwaway probe containers. Under bash the probe verb resolves to the Go
// binary on PATH and bash exits 126 reading an ELF file as a script, which is
// neither reserved verdict code, so RegistryProxyTransport would read every
// probe as an infrastructure failure and abort before any Box log exists.
const registryProbeEntrypoint = "driver-exec"

// registrySocketProbeArgs assembles the run args for a throwaway container
// that checks whether hostSocketPath reaches the guest as a connectable unix
// socket. Reusing buildRunArgs sandboxes the probe exactly like a real Box;
// --rm is added because a probe must leave no stopped container behind, unlike
// a real Box, which the caller reaps explicitly.
func (a *ociAdapter) registrySocketProbeArgs(hostSocketPath, containerName string) []string {
	box := Box{Name: containerName, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(hostSocketPath)}}
	full := a.buildRunArgs(box)
	args := append([]string{full[0], "--rm", "--entrypoint", registryProbeEntrypoint}, probeArgsFromRunArgs(full)...)
	return append(args, a.image, "probe-registry-socket", "-path", RegistryProxySocketTarget)
}

// registryTCPProbeArgs assembles the run args for a throwaway container that
// checks whether the launcher's TCP fallback listener at host:port is
// reachable from the guest over the --add-host host-gateway route (issue #3111
// finding B). Setting a TCP Endpoint on the throwaway Box fires buildRunArgs'
// own --add-host branch, wiring the probe like a real TCP-transport Box.
func (a *ociAdapter) registryTCPProbeArgs(host string, port int, containerName string, addHost bool) []string {
	box := Box{Name: containerName, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewTCPEndpoint(host, ""), TCPAddHost: addHost}}
	full := a.buildRunArgs(box)
	args := append([]string{full[0], "--rm", "--entrypoint", registryProbeEntrypoint}, probeArgsFromRunArgs(full)...)
	return append(args, a.image, "probe-registry-tcp", "-host", host, "-port", strconv.Itoa(port))
}

// registryProxyProbeTimeout bounds one throwaway probe container's start,
// probe and exit, never a real Box's runtime, so a wedged container daemon
// fails the probe in seconds instead of hanging every registry-proxy dispatch
// (issue #3111). A var, not a const, so tests can shorten it.
var registryProxyProbeTimeout = 30 * time.Second

// deniesHostLoopback reports whether networkMode denies a Box the host-loopback
// route: pasta without --map-gw blocks it, and "none" has no network at all.
func deniesHostLoopback(networkMode string) bool {
	return networkMode == NetworkModeNoHostLoopback || networkMode == NetworkModeNone
}

// RegistryProxyTransport reports this runtime's registry-proxy transport
// decision, reading the on-disk cache before a live probe that costs up to four
// throwaway containers (issue #3113). The cache replays only a verdict from
// probeRegistryProxyTransport under the same runtime+image+networkMode key, so
// dispatch and the doctor row (#3114) agree; an error is never cached.
func (a *ociAdapter) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	key := registryProbeCacheKey{runtime: a.cli, image: a.image, networkMode: a.networkMode}
	if endpoint, tcpAddHost, ok := loadRegistryProbeCache(a.pwd, key); ok {
		return endpoint, tcpAddHost, nil
	}

	endpoint, tcpAddHost, err := a.probeRegistryProxyTransport()
	if err != nil {
		return registrymanifest.Endpoint{}, false, err
	}
	// An unwritable .spindrift dir must degrade to probing every time, never
	// fail a dispatch that would otherwise have succeeded.
	_ = storeRegistryProbeCache(a.pwd, key, endpoint, tcpAddHost)
	return endpoint, tcpAddHost, nil
}

// runRegistrySocketProbe runs one throwaway probe container and reads its exit
// code as a verdict. hostSocketPath == "" is the control probe: with nothing to
// mount, candidateSocketMount drops the socket flag. It mints its own timeout
// and container name so socket and control probes each get a full budget. Only
// a reserved code returns nil; every other outcome wraps errProbeNoVerdict.
func (a *ociAdapter) runRegistrySocketProbe(hostSocketPath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), registryProxyProbeTimeout)
	defer cancel()

	containerName := fmt.Sprintf("spindrift-registry-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := a.registrySocketProbeArgs(hostSocketPath, containerName)
	out, err := exec.CommandContext(ctx, a.cli, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("registry proxy transport probe: %s: timed out after %s: %s: %w", a.cli, registryProxyProbeTimeout, out, errProbeNoVerdict)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case registryprobe.ExitCapable:
				return registryprobe.ExitCapable, nil
			case registryprobe.ExitIncapable:
				return registryprobe.ExitIncapable, nil
			default:
				return 0, fmt.Errorf("registry proxy transport probe: %s: probe container exited %d, want %d (capable) or %d (incapable) -- possible launcher/image version mismatch: %s: %w", a.cli, exitErr.ExitCode(), registryprobe.ExitCapable, registryprobe.ExitIncapable, out, errProbeNoVerdict)
			}
		}
		return 0, fmt.Errorf("registry proxy transport probe: %s: %w: %s: %w", a.cli, err, out, errProbeNoVerdict)
	}
	// Exit 0 is not registryprobe.ExitCapable: only that reserved code is the
	// capable verdict (issue #3120), so a clean exit is no verdict either.
	return 0, fmt.Errorf("registry proxy transport probe: %s: probe container exited 0, want %d (capable) or %d (incapable) -- possible launcher/image version mismatch: %w", a.cli, registryprobe.ExitCapable, registryprobe.ExitIncapable, errProbeNoVerdict)
}

// controlProbeNoSocket is the hostSocketPath that selects the control probe:
// with nothing to mount, candidateSocketMount skips the socket mount entirely.
const controlProbeNoSocket = ""

// probeRegistryProxyTransport probes the runtime live: it listens on a
// throwaway unix socket and reads a probe container's reserved exit code as the
// verdict. A no-verdict result triggers one control probe with nothing mounted,
// since some runtimes reject the socket mount before the container ever runs
// (Rancher Desktop with virtiofs exits 125 before start, issue #3466).
func (a *ociAdapter) probeRegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	probeDir, err := probeSocketDir()
	if err != nil {
		return registrymanifest.Endpoint{}, false, fmt.Errorf("registry proxy transport probe: %w", err)
	}
	defer os.RemoveAll(probeDir)

	probeSocketPath := filepath.Join(probeDir, "probe.sock")
	listener, err := net.Listen("unix", probeSocketPath)
	if err != nil {
		return registrymanifest.Endpoint{}, false, fmt.Errorf("registry proxy transport probe: listen on %s: %w", probeSocketPath, err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	exitCode, socketErr := a.runRegistrySocketProbe(probeSocketPath)
	if socketErr != nil {
		controlExit, controlErr := a.runRegistrySocketProbe(controlProbeNoSocket)
		if controlErr != nil {
			return registrymanifest.Endpoint{}, false, fmt.Errorf("%w; control probe without the socket mount also produced no verdict: %w", socketErr, controlErr)
		}
		if controlExit == registryprobe.ExitCapable {
			return registrymanifest.Endpoint{}, false, fmt.Errorf("registry proxy transport probe: %s: control probe without the socket mount unexpectedly reported capable (exit %d), which should be impossible with nothing mounted", a.cli, registryprobe.ExitCapable)
		}
		exitCode = controlExit
	}

	// The no-verdict branch above already returned, so exitCode is one of the
	// two reserved codes.
	if exitCode == registryprobe.ExitCapable {
		// Path is left unset; the caller mints the real per-Box socket path
		// once it knows the transport decision.
		return registrymanifest.NewUnixEndpoint(""), false, nil
	}
	if deniesHostLoopback(a.networkMode) {
		// Falling back silently would either leave a pasta Box unable to
		// reach the proxy with no diagnostic, or wire a host-loopback route
		// the operator's NETWORK_MODE explicitly denied (issue #3111
		// finding B).
		return registrymanifest.Endpoint{}, false, fmt.Errorf("registry proxy transport probe: %s: socket transport unavailable and NETWORK_MODE=%s denies the host-loopback route the TCP fallback requires", a.cli, a.networkMode)
	}
	host := hostGatewayHostname(a.cli)
	addHost, err := a.probeRegistryTCPReachable(host)
	if err != nil {
		return registrymanifest.Endpoint{}, false, err
	}
	// Port is left unset; the caller binds the real listener and learns the
	// ephemeral port after this call returns.
	return registrymanifest.NewTCPEndpoint(host, ""), addHost, nil
}

// probeRegistryTCPReachable reports whether host is reachable from a guest and
// which --add-host wiring gets it there. The runtime's own resolution is tried
// FIRST because the mapping is not additive: on a VM-backed runtime it replaces
// a working route to the real host with the in-VM bridge gateway. A no-verdict
// first sub-probe short-circuits, since that route was never tested (#3120).
func (a *ociAdapter) probeRegistryTCPReachable(host string) (bool, error) {
	withoutErr := a.probeRegistryTCPOnce(host, false)
	if withoutErr == nil {
		return false, nil
	}
	if errors.Is(withoutErr, errProbeNoVerdict) {
		return false, withoutErr
	}
	withErr := a.probeRegistryTCPOnce(host, true)
	if withErr == nil {
		return true, nil
	}
	if errors.Is(withErr, errProbeNoVerdict) {
		return false, withErr
	}
	return false, fmt.Errorf("registry proxy transport probe: %s: host %s is unreachable from the guest both with and without an --add-host host-gateway mapping; without: %v; with: %v", a.cli, host, withoutErr, withErr)
}

// errProbeNoVerdict is wrapped as the last %w of every error meaning the route
// was never actually tested, as opposed to tested and found unreachable.
// probeRegistryTCPReachable tells the two apart with errors.Is (issue #3120).
var errProbeNoVerdict = errors.New("no probe verdict")

// listenTCPProbe binds the throwaway TCP listener the probe container dials
// back into. A var, not a direct net.Listen call, so a test can force the bind
// to fail without starving the process of file descriptors (issue #3120).
var listenTCPProbe = func() (net.Listener, error) {
	return net.Listen("tcp", "0.0.0.0:0")
}

// probeRegistryTCPOnce runs one throwaway container verifying that the route to
// host actually reaches the launcher (issue #3111 finding B): a plain Linux
// docker bridge resolves host-gateway to the bridge IP, and a remote daemon
// runs on another machine entirely, so trusting the fallback unconfirmed would
// strand the Box. ExitIncapable is an error here: no fallback is left.
func (a *ociAdapter) probeRegistryTCPOnce(host string, addHost bool) error {
	listener, err := listenTCPProbe()
	if err != nil {
		// A listener that never bound means nothing was ever dialled: the
		// route is untested, not tested and unreachable (issue #3120).
		return fmt.Errorf("registry proxy transport probe: tcp-reachability sub-probe: listen: %w: %w", err, errProbeNoVerdict)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("registry proxy transport probe: tcp-reachability sub-probe: listener address %v is not a *net.TCPAddr: %w", listener.Addr(), errProbeNoVerdict)
	}

	ctx, cancel := context.WithTimeout(context.Background(), registryProxyProbeTimeout)
	defer cancel()

	containerName := fmt.Sprintf("spindrift-registry-tcp-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := a.registryTCPProbeArgs(host, tcpAddr.Port, containerName, addHost)
	out, err := exec.CommandContext(ctx, a.cli, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe timed out after %s: %s: %w", a.cli, registryProxyProbeTimeout, out, errProbeNoVerdict)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case registryprobe.ExitCapable:
				return nil
			case registryprobe.ExitIncapable:
				return fmt.Errorf("registry proxy transport probe: %s: host %s is not reachable from the guest (add-host=%t): %s", a.cli, host, addHost, out)
			default:
				return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe container exited %d, want %d (capable) or %d (incapable) -- possible launcher/image version mismatch: %s: %w", a.cli, exitErr.ExitCode(), registryprobe.ExitCapable, registryprobe.ExitIncapable, out, errProbeNoVerdict)
			}
		}
		return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe: %w: %s: %w", a.cli, err, out, errProbeNoVerdict)
	}
	// Exit 0 is not registryprobe.ExitCapable: only that reserved code is the
	// reachable verdict (issue #3120), so a clean exit is no verdict either.
	return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe container exited 0, want %d (capable) or %d (incapable) -- possible launcher/image version mismatch: %w", a.cli, registryprobe.ExitCapable, registryprobe.ExitIncapable, errProbeNoVerdict)
}

// reapOrphanedRebaseDirs removes leftover spindrift-rebase-* directories in
// root. forge.Rebase cleans these up with defer, which a killed launcher skips.
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

// ociRunEnv returns the environment the docker/podman CLI itself runs with:
// os.Environ() plus each offArgvKeys value present in boxEnv. The CLI needs its
// own ambient PATH to run at all, so this extends os.Environ() rather than
// replacing it as bwrap's allowlist-only resolvedRunEnv does. The appended
// values exist only for buildRunArgs' bare "-e KEY" entries to forward.
func ociRunEnv(boxEnv map[string]string) []string {
	keys := make([]string, 0, len(offArgvKeys))
	for k := range offArgvKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := os.Environ()
	for _, k := range keys {
		if v, ok := boxEnv[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Run launches a single issue into a podman/docker container.
func (a *ociAdapter) Run(box Box) error {
	reapOrphanedRebaseDirs(os.TempDir())
	// Never touch a running container: a concurrent launcher invocation may
	// own it, and a force-remove would destroy that run's work silently. A
	// running container would also collide on the name, so report
	// ErrAlreadyRunning instead of launching (issue #562).
	if a.IsRunning(box.Name) {
		return ErrAlreadyRunning
	}
	reap := exec.Command(a.cli, "rm", "-f", box.Name)
	_ = reap.Run()

	out := box.Output
	if out == nil {
		out = io.Discard
	}

	cmd := exec.Command(a.cli, a.buildRunArgs(box)...)
	cmd.Env = ociRunEnv(box.Env)
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if reapAfterSuccess(err) {
		_ = a.Reap(box.Name)
	}
	return asRunError(err)
}

// reapAfterSuccess reports whether to reap the container after cmd.Run. Any
// non-nil error retains it so a human can recover locally.
func reapAfterSuccess(err error) bool {
	return err == nil
}

// Reap removes a named container (best-effort). Never removes a running container.
func (a *ociAdapter) Reap(name string) error {
	if !a.IsRunning(name) {
		reap := exec.Command(a.cli, "rm", "-f", name)
		_ = reap.Run()
	}
	return nil
}

// Kill force-stops and removes name once confirmed to exist; `rm -f` stops a
// running container first, so Kill needs no running/exited distinction the way
// Reap's IsRunning guard does. A container that no longer exists is not an
// error, matching the Runner.Kill contract; that is the common settle-phase
// case, where reapAfterSuccess already removed the Box.
func (a *ociAdapter) Kill(name string) error {
	if err := exec.Command(a.cli, "inspect", name).Run(); err != nil {
		return nil
	}
	return exec.Command(a.cli, "rm", "-f", name).Run()
}
