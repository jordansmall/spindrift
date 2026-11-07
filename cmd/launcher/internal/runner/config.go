package runner

// NetworkMode is the NETWORK_MODE knob (issue #2562), shared by the OCI and
// bwrap adapters and the launcher's runtime gate in cmd/launcher/main.go.
type NetworkMode string

const (
	NetworkModeOpen           NetworkMode = "open"
	NetworkModeNoHostLoopback NetworkMode = "no-host-loopback"
	NetworkModeNone           NetworkMode = "none"

	// NetworkModeHost is a bwrap-only opt-out (issue #2666) that restores
	// sharing the host's network namespace. It has no OCI rendering:
	// Config.OCINetwork resolves it like "open".
	NetworkModeHost NetworkMode = "host"
)

// NetworkModes returns every non-zero NETWORK_MODE value, in the schema's
// choice order, in a fresh slice the caller may modify; a launcher test pins
// it to the schema. The zero value "" is not a choice: a directly built
// Config may leave it empty and each family renders it exactly as it renders
// open.
func NetworkModes() []NetworkMode {
	return []NetworkMode{
		NetworkModeOpen,
		NetworkModeNoHostLoopback,
		NetworkModeNone,
		NetworkModeHost,
	}
}

// DeniesHostLoopback reports whether m denies a Box the host-loopback
// route: pasta without --map-gw blocks it, and "none" has no network at all.
func (m NetworkMode) DeniesHostLoopback() bool {
	return m == NetworkModeNoHostLoopback || m == NetworkModeNone
}

// Config carries the subset of launcher config the runner constructors need.
// pwd is a per-invocation runtime dependency, so the adapter constructors take it separately.
type Config struct {
	// Runtime is one of ValidValues ("podman", "docker", "rancher", or
	// "bwrap"). For OCI adapters it also names the CLI binary (via BinaryFor).
	Runtime string

	// OCI image config, baked by the nix wrapper and empty for bwrap, except
	// ImageTag and FlakeImageAttr, which are dual-purpose (issue #2667): they
	// are filled from whichever runner family part the Launcher input document
	// carries, but only the OCI adapters read them from Config.
	Image           string
	ImageArchive    string
	ImageDrv        string
	ImageTag        string
	NixBuilderImage string
	NixVolume       string
	FlakeImageAttr  string

	// NetworkMode is the NETWORK_MODE knob. The raw PODMAN_NETWORK and
	// BWRAP_UNSHARE_NET escape hatches are family row knobs the adapters read,
	// not Config fields. nix eval rejects setting NETWORK_MODE together with
	// either raw knob (lib/mkHarness.nix networkModeCoherenceOk), but
	// Config.OCINetwork still picks a deterministic winner (raw) since Go
	// cannot observe that invariant.
	NetworkMode NetworkMode
	PidsLimit   string
	MemoryLimit string

	// MaxParallel is MAX_PARALLEL; only the podman-machine prerequisite reads
	// it, to size the podman machine.
	MaxParallel int

	// MountParams carries the mount-gating facts the run adapters share; the
	// build adapters ignore them. DriverSessionCacheDir is ADR 0009;
	// AccumulationRepoDir is issue #1697. Embedded, so call sites keep
	// reaching its fields as cfg.PromptDir.
	MountParams
}
