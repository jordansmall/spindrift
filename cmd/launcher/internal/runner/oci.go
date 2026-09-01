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

	"spindrift.dev/launcher/internal/registryproxy"
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
	// mountParams carries this run's host-mount facts straight through from
	// Config to buildMountSpecs, unmodified; see MountParams. DriverSessionCacheDir
	// is ADR 0009; the CODE_FORGE=local mount specs are issue #1697.
	mountParams   MountParams
	podmanNetwork string // optional raw --network value; empty omits the flag
	networkMode   string // NETWORK_MODE knob ("open"/"no-host-loopback"/"none"/""); see networkArg
	pidsLimit     string // --pids-limit value; empty disables the flag
	memoryLimit   string // --memory value; empty disables the flag
}

// NewOCI constructs an OCI adapter from cfg. pwd is the working directory
// (used for the container-fallback path) — a genuine per-invocation runtime
// dependency passed separately from cfg.
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
		fmt.Printf("==> image '%s' already loaded\n", a.image)
		return nil
	}
	fmt.Printf("==> image '%s' not found — building first\n", a.image)

	// 1. Try host build; tee stderr so errors are visible AND inspectable.
	var hostStderr bytes.Buffer
	nixBuild := execCommand("nix", "build", a.imageDrv+"^*", "--no-link")
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
		strings.Contains(stderr, "no build machines") ||
		strings.Contains(stderr, "Reason: platform mismatch")
}

// isTransientRegistryError reports whether stderr indicates a network hiccup
// reaching the registry (DNS, dial/TLS, or read/i/o timeout) rather than a
// genuine failure. Callers use this to retry or skip instead of failing hard
// on a blip (issue #2015). It only has a caller in oci_integration_test.go
// (//go:build integration) but lives here, untagged, so it still gets a
// plain unit test (TestIsTransientRegistryError in oci_test.go) that
// checks-inbox runs without needing a real container runtime on PATH.
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

// isRuntimeUnusableError reports whether stderr indicates the low-level OCI
// runtime (crun/runc) itself failed to start the container, rather than the
// container running and returning an unexpected result. requireRealOCI only
// probes `<cli> info`, so a runtime that is present and reports a reachable
// daemon but is actually broken (a version mismatch on a CI runner image:
// "OCI runtime error: crun: unknown version specified") slips past that gate
// and only surfaces at `run` time. Such a runtime is "not usable" in the sense
// ci.yml's integration step means, so the probes skip on it rather than fail
// hard — the same clean degradation the daemon-unreachable path already gives.
// A broken runtime can never start a container, so this is never a genuine
// hardening regression, which surfaces instead as wrong /proc output from a
// container that did start. Like isTransientRegistryError it only has an
// integration caller but lives here, untagged, for a plain unit test under
// checks-inbox.
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
	// The archive's buildLayeredImage name+tag is "<repo>:latest" where repo
	// matches a.imageTag's own repo (default "spindrift", or a driver-scoped
	// repo like "spindrift-opencode") — re-tag from that same source, not a
	// hardcoded "spindrift:latest", so a driver-scoped archive is found.
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

// imageRepo derives the repo portion of an "<repo>:<tag>" image reference —
// everything before the LAST colon, since a repo can itself contain a colon
// (e.g. a registry host:port prefix). Falls back to the default "spindrift"
// repo when imageTag has no colon at all (a degenerate/empty tag), rather
// than deriving an empty or nonsensical repo. Mirrors
// internal/freshness.imageRepo, which derives the same repo from the same
// kind of tag for the freshness probe's own tip-tag comparison.
func imageRepo(imageTag string) string {
	i := strings.LastIndex(imageTag, ":")
	if i < 0 {
		return "spindrift"
	}
	return imageTag[:i]
}

// gitSafeDirectoryPrelude sets a writable HOME under the /build-output mount
// and writes a global gitconfig marking /workspace (the bind-mounted host
// repo) safe, so upstream Nix's libgit2 dubious-ownership guard does not
// reject it when the host repo is owned by a UID different from
// container-root. Written directly via printf — no dependency on a `git`
// CLI being present in the builder image. Mirrors the safe.directory
// precedent in agent/entrypoint.sh:138-139, written directly rather than via
// `git config` (issue #2196).
const gitSafeDirectoryPrelude = `export HOME=/build-output/home; ` +
	`mkdir -p "$HOME"; ` +
	`printf '[safe]\n\tdirectory = *\n\tdirectory = /workspace\n' > "$HOME/.gitconfig"`

// containerBuildCmd assembles the `sh -euc` command run inside the Nix
// builder container to build the image and stage it for the host to pick
// up. Separated from buildInContainer so the command construction can be
// tested without spawning docker/podman — mirrors buildRunArgs.
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

// IsRunning reports whether name is currently in the "running" state.
// Returns false when the container is absent, exited, or inspect fails — in all
// of those cases the caller may safely proceed with rm -f.
func (a *ociAdapter) IsRunning(name string) bool {
	out, err := exec.Command(a.cli, "inspect", "--format={{.State.Status}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "running"
}

// ListRunning returns the names of every container currently in the
// "running" state under this runtime (podman/docker) — Console startup
// orphan detection (issue #651).
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

// mountSpecs computes the host-to-box mounts that apply for box, shared with
// the bwrap adapter (buildMountSpecs); only the rendering below differs.
func (a *ociAdapter) mountSpecs(box Box) []MountSpec {
	return buildMountSpecs(a.mountParams, box)
}

// networkArg resolves the effective `--network` value from the raw
// podmanNetwork escape hatch and the NETWORK_MODE knob (issue #2562). The
// raw knob wins whenever set: mkHarness's networkModeCoherenceOk eval assert
// (lib/mkHarness.nix) rejects setting both on a valid Consumer flake, and
// cmd/launcher/main.go's checkNetworkModeRuntimeGate backstops the same
// mutual exclusion against a runtime override (env var / CLI flag) of either
// knob past what that eval assert could see — so by the time a Box reaches
// this function, both being set is unreachable for a non-open NETWORK_MODE.
// An explicit NETWORK_MODE=open paired with a raw knob is a real, reachable
// case, though: checkNetworkModeRuntimeGate deliberately leaves it out of
// scope (Go can't distinguish "networkMode defaulted to open" from
// "networkMode was explicitly set to open" at that layer) and lets it reach
// here, where raw-wins resolves it. It still needs a deterministic answer
// here as defense-in-depth against the otherwise-unreachable non-open case,
// since Go has no way to
// observe that invariant locally. "no-host-loopback" resolves per CLI: plain
// `pasta` (no
// `--map-gw`) genuinely denies host-loopback on podman, but docker/nerdctl's
// "bridge" is just their own default network — byte-identical to what
// "open" already renders there (no `--network` flag falls back to the same
// default bridge) — and does not deny host-loopback by default, so on
// docker/nerdctl this is currently an inert-but-correct render, not a
// functional guarantee. "none" maps straight through. "open"/unset renders
// no flag at all.
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

// buildRunArgs assembles the argument slice for `podman/docker run`. Separated
// from Run so the arg construction can be tested without exec.
func (a *ociAdapter) buildRunArgs(box Box) []string {
	args := []string{"run", "--name", box.Name}
	if network := a.networkArg(); network != "" {
		args = append(args, "--network", network)
	}
	for k, v := range box.Env {
		if bwrapSecrets[k] {
			// Bare "-e KEY" (no value) tells docker/podman to forward KEY's
			// value from the CLI process's OWN environment instead -- ociRunEnv
			// puts it there via cmd.Env, so the value itself never lands in
			// argv, which ps/proc exposes to any local user for the
			// container's whole lifetime (issue #3111 finding A; mirrors
			// bwrap.go's bwrapSecrets/resolvedRunEnv treatment of the same
			// class of value).
			args = append(args, "-e", k)
			continue
		}
		args = append(args, "-e", k+"="+v)
	}
	// Mount decisions (gates, existence guards, operator messages) are
	// computed once in buildMountSpecs, shared with the bwrap adapter; OCI
	// only renders each spec into its own -v flag syntax. The driver-cache
	// spec has no host-side path to re-mount baked skills over, unlike
	// bwrap's agentFiles fallback — so it is scoped to the Driver's declared
	// session-cache dir, never the whole .claude, which would shadow the
	// baked .claude/skills the image ships.
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
	// A TCP-transport Box (issue #3111: the runtime can't carry a connectable
	// unix socket into the guest) may need an explicit host-gateway mapping to
	// resolve TCPHost — plain Linux docker does not resolve the name at all
	// without it. It is emphatically NOT unconditional: a VM-backed runtime
	// (Docker Desktop, Rancher Desktop/Lima) resolves the name to the real
	// host itself, and adding the mapping there overrides that with the in-VM
	// bridge gateway (172.17.0.1), which routes to the VM rather than to the
	// launcher — so forcing it breaks exactly the platform the TCP fallback
	// exists for. RegistryProxyTransport probes both ways and records the
	// answer on TCPAddHost. docker/podman/nerdctl all understand the literal
	// "host-gateway" sentinel value.
	if box.RegistryProxy.TCPAddHost {
		args = append(args, "--add-host", box.RegistryProxy.TCPHost+":host-gateway")
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

// probeSocketDir returns a fresh, unique directory for RegistryProxyTransport's
// throwaway probe socket, preferring os.TempDir() but falling back to /tmp
// when that base is already long enough that appending the probe socket's
// filename would overflow AF_UNIX's sun_path limit (issue #3077) -- the same
// class of failure dispatch.registryProxySocketDir already falls back for,
// macOS's per-user $TMPDIR nested under nix develop's own nix-shell.XXXXXX/
// prefix being the case that actually triggers it in practice. Duplicated
// here (rather than shared) because runner cannot import dispatch.
func probeSocketDir() (string, error) {
	dir, err := os.MkdirTemp("", "spindrift-registry-probe-*")
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy probe dir: %w", err)
	}
	if !registryproxy.TooLongForUnixSocket(filepath.Join(dir, "probe.sock")) {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove over-long registry proxy probe dir: %w", err)
	}
	// A too-long path from this fallback itself is net.Listen's error to
	// raise, not this function's -- mirrors registryProxySocketDir's own
	// matching comment.
	dir, err = os.MkdirTemp("/tmp", "spindrift-registry-probe-*")
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy probe dir under /tmp: %w", err)
	}
	return dir, nil
}

// hostGatewayHostname returns the hostname a Box resolves to reach the
// launcher's own loopback interface over TCP (issue #3111), when the
// configured runtime cannot carry a connectable unix socket into the guest.
// podman has its own convention distinct from docker's; nerdctl (including
// Rancher Desktop's containerd/nerdctl mode) follows docker's widely-adopted
// host.docker.internal, so it shares docker's branch here.
func hostGatewayHostname(cli string) string {
	if cli == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// probeArgsFromRunArgs strips buildRunArgs' trailing "<image> <entrypoint>"
// pair off full, leaving only the leading verb plus the mount/network/
// hardening flags a throwaway probe container reuses verbatim. The one place
// coupled to buildRunArgs' exact trailing-two-elements shape, shared by
// registrySocketProbeArgs and registryTCPProbeArgs so that shape only needs
// updating here if buildRunArgs' own trailing shape ever changes.
func probeArgsFromRunArgs(full []string) []string {
	return full[1 : len(full)-2]
}

// registryProbeEntrypoint is the image-entrypoint override every throwaway
// probe container runs under. The image's own Entrypoint is /bin/bash (see
// lib/image.nix), which a real Box relies on — it is launched as "<image>
// /agent/entrypoint.sh". A probe cannot append its verb the same way: bash
// would resolve "driver-exec" on PATH, find the Go binary, and try to
// interpret an ELF file as a shell script, exiting 126. That is neither the
// probe contract's 0 nor its 1, so RegistryProxyTransport would read every
// probe as an infrastructure failure and abort the dispatch before any Box —
// and therefore any Box log — exists. Replacing the entrypoint runs the
// binary directly instead.
const registryProbeEntrypoint = "driver-exec"

// registrySocketProbeArgs assembles the argument slice for a throwaway probe
// container that checks whether hostSocketPath is reachable from the guest as
// a connectable unix domain socket. It reuses buildRunArgs to render the same
// mount/network/hardening flags a real Box gets — so the probe is sandboxed
// identically — but replaces the image entrypoint with driver-exec and swaps
// the trailing "<image> /agent/entrypoint.sh" for the probe verb, and adds
// --rm right after "run" since a throwaway probe must never leave a stopped
// container behind (unlike a real Box, which the caller reaps explicitly).
func (a *ociAdapter) registrySocketProbeArgs(hostSocketPath, containerName string) []string {
	box := Box{Name: containerName, RegistryProxy: RegistryProxyLocation{SocketPath: hostSocketPath}}
	full := a.buildRunArgs(box)
	args := append([]string{full[0], "--rm", "--entrypoint", registryProbeEntrypoint}, probeArgsFromRunArgs(full)...)
	return append(args, a.image, "probe-registry-socket", "-path", registryProxySocketTarget)
}

// registryTCPProbeArgs assembles the argument slice for a throwaway probe
// container that checks whether the launcher's TCP registry-proxy fallback
// listener at host:port is actually reachable from the guest over the
// --add-host host-gateway route (issue #3111's review finding B). It reuses
// buildRunArgs the same way registrySocketProbeArgs does -- setting
// RegistryProxy.TCPHost on the throwaway Box makes buildRunArgs's own
// --add-host branch fire, so the probe container is wired identically to a
// real TCP-transport Box -- but overrides the image entrypoint and swaps the
// trailing "<image> /agent/entrypoint.sh" for the probe-registry-tcp verb
// instead of the socket one.
func (a *ociAdapter) registryTCPProbeArgs(host string, port int, containerName string, addHost bool) []string {
	box := Box{Name: containerName, RegistryProxy: RegistryProxyLocation{TCPHost: host, TCPAddHost: addHost}}
	full := a.buildRunArgs(box)
	args := append([]string{full[0], "--rm", "--entrypoint", registryProbeEntrypoint}, probeArgsFromRunArgs(full)...)
	return append(args, a.image, "probe-registry-tcp", "-host", host, "-port", strconv.Itoa(port))
}

// registryProxyProbeTimeout bounds a single registry-proxy capability probe:
// starting a throwaway container, running driver-exec probe-registry-socket
// inside it, and letting it exit. It bounds only that throwaway container's
// own start+probe+exit — not a real Box's runtime — so a wedged or
// still-starting container daemon fails this probe within seconds instead of
// hanging every registry-proxy dispatch indefinitely (issue #3111). A var,
// not a const, so tests can override it to a short duration rather than
// waiting out the real value.
var registryProxyProbeTimeout = 30 * time.Second

// deniesHostLoopback reports whether networkMode denies a Box a
// host-loopback route. NetworkModeNoHostLoopback denies it by network
// policy (podman's pasta with no --map-gw genuinely blocks the route);
// NetworkModeNone denies it by having no network at all. "open"/unset (and
// any other value) do not deny it.
func deniesHostLoopback(networkMode string) bool {
	return networkMode == NetworkModeNoHostLoopback || networkMode == NetworkModeNone
}

// RegistryProxyTransport probes the configured OCI runtime live: it listens
// on a fresh throwaway unix socket, launches a disposable container that
// mounts it at registryProxySocketTarget and runs `driver-exec
// probe-registry-socket`, and reads that container's own exit code as the
// verdict. driver-exec probe-registry-socket's own contract only ever exits 0
// (capable) or 1 (incapable) — exit 1 is the clean "incapable" answer,
// matching the AC that a mount-but-unconnectable socket degrades cleanly
// rather than crashing. Any other outcome — the probe container itself
// failing to run (docker/podman exit codes like 125/126/127), the runtime
// binary not starting at all, or the probe exceeding registryProxyProbeTimeout
// — is a genuine infrastructure failure and is returned as a Go error rather
// than silently downgrading a socket-capable host to the TCP transport. A
// clean "incapable" verdict is not itself the final answer, though: unless
// networkMode already denies the host-loopback route outright,
// probeRegistryTCPReachable runs a second live sub-probe (issue #3111 review
// finding B) confirming the TCP fallback's own --add-host host-gateway route
// actually works before this function ever reports the TCP transport as
// usable.
func (a *ociAdapter) RegistryProxyTransport() (bool, string, bool, error) {
	probeDir, err := probeSocketDir()
	if err != nil {
		return false, "", false, fmt.Errorf("registry proxy transport probe: %w", err)
	}
	defer os.RemoveAll(probeDir)

	probeSocketPath := filepath.Join(probeDir, "probe.sock")
	listener, err := net.Listen("unix", probeSocketPath)
	if err != nil {
		return false, "", false, fmt.Errorf("registry proxy transport probe: listen on %s: %w", probeSocketPath, err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), registryProxyProbeTimeout)
	defer cancel()

	containerName := fmt.Sprintf("spindrift-registry-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := a.registrySocketProbeArgs(probeSocketPath, containerName)
	out, err := exec.CommandContext(ctx, a.cli, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return false, "", false, fmt.Errorf("registry proxy transport probe: %s: timed out after %s: %s", a.cli, registryProxyProbeTimeout, out)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				if deniesHostLoopback(a.networkMode) {
					// The socket can't cross AND the network policy denies the
					// host-loopback route the TCP fallback would need --
					// falling back silently here would either leave a podman
					// pasta Box unable to reach the proxy with zero diagnostic,
					// or (on docker) actively wire a host-loopback route the
					// operator's NETWORK_MODE explicitly asked to deny (issue
					// #3111 finding B). Fail loudly instead.
					return false, "", false, fmt.Errorf("registry proxy transport probe: %s: socket transport unavailable and NETWORK_MODE=%s denies the host-loopback route the TCP fallback requires", a.cli, a.networkMode)
				}
				host := hostGatewayHostname(a.cli)
				addHost, err := a.probeRegistryTCPReachable(host)
				if err != nil {
					return false, "", false, err
				}
				return false, host, addHost, nil
			}
			return false, "", false, fmt.Errorf("registry proxy transport probe: %s: probe container exited %d: %s", a.cli, exitErr.ExitCode(), out)
		}
		return false, "", false, fmt.Errorf("registry proxy transport probe: %s: %w: %s", a.cli, err, out)
	}
	return true, "", false, nil
}

// probeRegistryTCPReachable determines whether host is reachable from a guest
// and, if so, which --add-host wiring gets it there, by running the live
// sub-probe below in each mode until one works. It reports the mode that
// succeeded so the real Box is launched with the wiring actually proved
// reachable.
//
// The runtime's own resolution is tried FIRST, and the explicit
// --add-host host-gateway mapping only as a fallback, because the mapping is
// not additive -- it overrides whatever the runtime would otherwise resolve
// the name to. On a VM-backed runtime (Docker Desktop, Rancher Desktop/Lima)
// the name already resolves to the real host, and the mapping replaces that
// with the in-VM bridge gateway (172.17.0.1), which routes to the VM rather
// than to the launcher: measured as `ok` without the flag and `connection
// refused` with it, on the same host, seconds apart. Preferring the mapping
// would therefore break every runtime the TCP fallback exists to serve, while
// preferring the runtime's own resolution costs a plain Linux docker host one
// extra failed sub-probe before it lands on the mapping it needs.
//
// Both modes failing is a hard error: there is no further transport to
// degrade to, and reporting the socket as unusable while silently wiring an
// unreachable proxy would strand the Box (falling through to the public
// registry, or hanging).
func (a *ociAdapter) probeRegistryTCPReachable(host string) (bool, error) {
	withoutErr := a.probeRegistryTCPOnce(host, false)
	if withoutErr == nil {
		return false, nil
	}
	withErr := a.probeRegistryTCPOnce(host, true)
	if withErr == nil {
		return true, nil
	}
	return false, fmt.Errorf("registry proxy transport probe: %s: host %s is unreachable from the guest both with and without an --add-host host-gateway mapping; without: %v; with: %v", a.cli, host, withoutErr, withErr)
}

// probeRegistryTCPOnce runs a single, independent throwaway-container
// probe (issue #3111 review finding B) verifying that the --add-host
// host-gateway route to host actually reaches the launcher: RegistryProxyTransport's
// first probe only proves the unix-socket transport is incapable -- it says
// nothing about whether the TCP fallback's own route actually works. A plain
// Linux docker bridge resolves host-gateway to the bridge IP (e.g.
// 172.17.0.1), not the launcher's loopback interface, and a remote-context
// docker/podman daemon runs on a different physical machine entirely where no
// bind address on the launcher host is reachable at all -- trusting the TCP
// fallback without confirming it is live would silently strand the Box with
// an unreachable proxy (falling through to the public registry, or hanging).
// It binds a throwaway TCP listener on every interface (mirroring the fix to
// dispatch's own registry-proxy listener bind), so the probe container's dial
// has something real to hit; launches a second disposable container running
// `driver-exec probe-registry-tcp` against it on a fresh timeout budget (the
// first probe's ctx may already be partially consumed); and reads that
// container's exit code as the verdict. Exit 0 is reachable (nil error); exit
// 1 is a clean but hard "not reachable" answer -- unlike the first probe's own
// exit-1 case, this is an error because there is no further fallback left to
// degrade to. Any other outcome (timeout, non-1 exit, exec failure) is also a
// hard error, matching how the first-stage probe already treats those.
func (a *ociAdapter) probeRegistryTCPOnce(host string, addHost bool) error {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("registry proxy transport probe: tcp-reachability sub-probe: listen: %w", err)
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
		return fmt.Errorf("registry proxy transport probe: tcp-reachability sub-probe: listener address %v is not a *net.TCPAddr", listener.Addr())
	}

	ctx, cancel := context.WithTimeout(context.Background(), registryProxyProbeTimeout)
	defer cancel()

	containerName := fmt.Sprintf("spindrift-registry-tcp-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := a.registryTCPProbeArgs(host, tcpAddr.Port, containerName, addHost)
	out, err := exec.CommandContext(ctx, a.cli, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe timed out after %s: %s", a.cli, registryProxyProbeTimeout, out)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				return fmt.Errorf("registry proxy transport probe: %s: host %s is not reachable from the guest (add-host=%t): %s", a.cli, host, addHost, out)
			}
			return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe container exited %d: %s", a.cli, exitErr.ExitCode(), out)
		}
		return fmt.Errorf("registry proxy transport probe: %s: tcp-reachability sub-probe: %w: %s", a.cli, err, out)
	}
	return nil
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

// ociRunEnv returns the process environment the docker/podman CLI itself
// should run with: the launcher's own os.Environ() plus each bwrapSecrets-
// listed key present in boxEnv, rendered as KEY=VALUE. Unlike bwrap's
// resolvedRunEnv (an allowlist-only environment for the sandboxed child),
// the docker/podman CLI process needs its own ambient environment (PATH,
// etc.) to run at all -- so this starts from os.Environ() rather than
// replacing it. The appended secrets exist here only so buildRunArgs's bare
// "-e KEY" entries have a same-process value to forward into the container;
// they never appear in the exec.Command args slice. Keys are sorted only for
// deterministic test output; the order is not otherwise load-bearing.
func ociRunEnv(boxEnv map[string]string) []string {
	keys := make([]string, 0, len(bwrapSecrets))
	for k := range bwrapSecrets {
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
	// Reap any orphaned rebase temp dirs left by a prior killed launcher run.
	reapOrphanedRebaseDirs(os.TempDir())
	// Reap any stale (exited or created) container from a prior interrupted run.
	// Never touch a running container — a concurrent launcher invocation may own it,
	// and a force-remove would destroy that run's work silently. A running
	// container also means launching would collide on the name; recognize
	// that as ErrAlreadyRunning instead of attempting the launch (issue #562).
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

// reapAfterSuccess reports whether the container should be reaped based on the
// error returned from cmd.Run. A nil error (clean exit) triggers a reap;
// any non-nil error retains the container so a human can recover locally.
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

// Kill force-stops and removes name, running or not, once confirmed to
// exist — `rm -f` on podman/docker stops a running container before
// removing it, so no running/exited distinction is needed the way Reap's
// IsRunning guard makes. A container that no longer exists at all (the
// common settle-phase case: the initial Box already exited successfully and
// Run's own reapAfterSuccess already removed it — CI watch and the merge
// gate never have a running Box) is not an error, matching the Runner.Kill
// contract; only a genuine removal failure on a container confirmed present
// is returned rather than swallowed.
func (a *ociAdapter) Kill(name string) error {
	if err := exec.Command(a.cli, "inspect", name).Run(); err != nil {
		return nil
	}
	return exec.Command(a.cli, "rm", "-f", name).Run()
}
