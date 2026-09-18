package runner

// NETWORK_MODE knob values (issue #2562), shared by the OCI and bwrap adapters
// and the launcher's runtime gate in cmd/launcher/main.go.
const (
	NetworkModeOpen           = "open"
	NetworkModeNoHostLoopback = "no-host-loopback"
	NetworkModeNone           = "none"

	// NetworkModeHost is a bwrap-only opt-out (issue #2666) that restores
	// sharing the host's network namespace. It has no OCI rendering:
	// oci.go's networkArg() default case treats it like "open".
	NetworkModeHost = "host"
)

// Config carries the subset of launcher config the runner constructors need.
// pwd is a per-invocation runtime dependency, so NewOCI takes it separately.
type Config struct {
	// Runtime is one of ValidValues ("podman", "docker", "rancher", or
	// "bwrap"). For OCI adapters it also names the CLI binary (via BinaryFor).
	Runtime string

	// OCI image config, baked by the nix wrapper and empty for bwrap, except
	// ImageTag and FlakeImageAttr, which are dual-purpose (issue #2667): the
	// OCI image's content-hash tag and flake attr, or the bundled bwrap agent
	// closure's loaded output path and flake attr.
	Image           string
	ImageArchive    string
	ImageDrv        string
	ImageTag        string
	NixBuilderImage string
	NixVolume       string
	FlakeImageAttr  string

	// PodmanNetwork is the raw --network escape hatch; NetworkMode is the
	// NETWORK_MODE knob. nix eval-rejects setting both (lib/mkHarness.nix
	// networkModeCoherenceOk), but the adapters still pick a deterministic
	// winner (raw) since Go cannot observe that invariant.
	PodmanNetwork string
	NetworkMode   string
	PidsLimit     string
	MemoryLimit   string

	// bwrap agent closure paths (bwrap only).
	AgentFiles      string
	AgentEnv        string
	PasswdFile      string
	GroupFile       string
	AgentFilesDrv   string // .drv path; realized by `launcher build`
	AgentEnvDrv     string // .drv path; realized by `launcher build`
	PasswdFileDrv   string // .drv path; realized by `launcher build`
	GroupFileDrv    string // .drv path; realized by `launcher build`
	BakedPrefetch   string
	BwrapUnshareNet bool
	// NixConfigFile is the baked store path for /etc/nix/nix.conf (ADR 0042);
	// empty when the Consumer's nixInBox knob is off.
	NixConfigFile string
	// NixConfigFileDrv is its .drv path; realized by `launcher build`.
	NixConfigFileDrv string
	// SyscallFilterPath is the baked store path to the compiled BPF
	// syscall-filter file (issue #2670). Unlike NixConfigFile it always
	// builds, whatever the nixInBox knob says.
	SyscallFilterPath string
	// SyscallFilterDrv is its .drv path; realized by `launcher build`.
	SyscallFilterDrv string
	// NixStoreWritable makes the bwrap adapter overlay /nix/store with an
	// ephemeral tmpfs upper instead of binding it read-only (ADR 0042). It does
	// nothing unless NixConfigFile is set: nix is not on PATH otherwise.
	NixStoreWritable bool

	// MountParams carries the mount-gating facts the run adapters share; the
	// build adapters ignore them. DriverSessionCacheDir is ADR 0009;
	// AccumulationRepoDir and BoxForgeAndIssueAccess are issue #1697. Embedded,
	// so call sites keep reaching its fields as cfg.PromptDir.
	MountParams
}
