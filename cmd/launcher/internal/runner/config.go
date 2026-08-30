package runner

// NETWORK_MODE knob values (issue #2562), shared across the OCI and bwrap
// adapters and the launcher's own runtime gate (cmd/launcher/main.go) so the
// three literals aren't repeated as bare strings at each call site.
const (
	NetworkModeOpen           = "open"
	NetworkModeNoHostLoopback = "no-host-loopback"
	NetworkModeNone           = "none"

	// NetworkModeHost is a bwrap-only documented opt-out (issue #2666): it
	// restores the pre-#2666 behavior of sharing the host's network
	// namespace, which every other mode (and the zero value) no longer does
	// by default. It has no OCI rendering — oci.go's networkArg() switch's
	// `default: return ""` catches it the same as "open", which is fine and
	// out of scope for this issue.
	NetworkModeHost = "host"
)

// Config carries the subset of launcher config the runner package's
// constructors need to build an OCI or bwrap adapter. pwd is passed
// separately to NewOCI (a genuine per-invocation runtime dependency, not a
// config knob).
type Config struct {
	// Runtime selects the sandbox mechanism — one of ValidValues ("podman",
	// "docker", "rancher", or "bwrap"). For OCI adapters it also names the
	// CLI binary (via BinaryFor).
	Runtime string

	// OCI image config (baked by nix wrapper). Image/ImageArchive/ImageDrv/
	// NixBuilderImage/NixVolume are empty for bwrap (no image to load).
	// ImageTag/FlakeImageAttr are dual-purpose (issue #2667): the OCI
	// image's content-hash tag/flake attr for an OCI runtime, or the
	// bundled bwrap agent-closure's loaded output path/flake attr for
	// bwrap.
	Image           string
	ImageArchive    string
	ImageDrv        string
	ImageTag        string
	NixBuilderImage string
	NixVolume       string
	FlakeImageAttr  string

	// OCI container network / resource caps. NetworkMode is the NETWORK_MODE
	// knob ("open"/"no-host-loopback"/"none"); PodmanNetwork is the raw
	// --network escape hatch. nix eval-rejects setting both on a valid
	// Consumer flake (lib/mkHarness.nix networkModeCoherenceOk); the OCI and
	// bwrap adapters still resolve a deterministic precedence between them
	// (raw wins) since Go has no way to observe that invariant.
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
	// NixConfigFile is the baked nix store path for /etc/nix/nix.conf (ADR
	// 0042); empty when the Consumer's nixInBox knob is off.
	NixConfigFile string
	// NixConfigFileDrv is its .drv path; realized by `launcher build`
	// alongside the other bwrap agent store closures, when set.
	NixConfigFileDrv string
	// SyscallFilterPath is the baked nix store path to the compiled BPF
	// syscall-filter file (issue #2670), used by the bwrap run adapter.
	// Unlike NixConfigFile, this is unconditional: it always builds
	// regardless of the Consumer's nixInBox knob.
	SyscallFilterPath string
	// SyscallFilterDrv is its .drv path; realized by `launcher build`.
	SyscallFilterDrv string
	// NixStoreWritable gates whether the bwrap adapter overlays /nix/store
	// with an ephemeral tmpfs upper instead of binding it read-only (ADR
	// 0042). Has no effect unless NixConfigFile is also set: nix isn't even
	// on PATH in the Box otherwise.
	NixStoreWritable bool

	// Optional host overrides shared by the OCI and bwrap run adapters
	// (unused by the build adapters).
	PromptDir string
	SkillsDir string

	// DriverSessionCacheDir is the in-box mount target for the Driver's
	// session-state dir (ADR 0009; baked by nix at wrap time), shared by the
	// OCI and bwrap run adapters. Empty when the Driver declares no
	// session-state dir, in which case the driver-cache dir is never
	// mounted regardless of Box.DriverCacheDir.
	DriverSessionCacheDir string

	// HostMediatedRemote reports whether the active CODE_FORGE backend has no
	// writable remote to push to in-box at all (ADR 0033: CODE_FORGE=local);
	// AccumulationRepoDir is the host path to the bare Accumulation repo
	// mounted read-only at /repo when it is set (issue #1697).
	// OutboxRelayCapable reports whether the active CODE_FORGE backend gets
	// the outbox-relay treatment under BoxForgeAndIssueAccess=="read-only"
	// (issue #1918: true only for "github"). BoxForgeAndIssueAccess is the
	// BOX_FORGE_AND_ISSUE_ACCESS knob value ("read-write" or "read-only"),
	// which alongside HostMediatedRemote/OutboxRelayCapable gates the
	// writable /outbox mount.
	HostMediatedRemote     bool
	AccumulationRepoDir    string
	OutboxRelayCapable     bool
	BoxForgeAndIssueAccess string

	// HostMediatedIssueTracker and LocalIssuesDir gate the read-only /issues
	// mount (ADR 0032): only ISSUE_TRACKER=local reads its issues from the
	// Box.
	HostMediatedIssueTracker bool
	LocalIssuesDir           string
}
