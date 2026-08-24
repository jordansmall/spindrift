package runner

// NETWORK_MODE knob values (issue #2562), shared across the OCI and bwrap
// adapters and the launcher's own runtime gate (cmd/launcher/main.go) so the
// three literals aren't repeated as bare strings at each call site.
const (
	NetworkModeOpen           = "open"
	NetworkModeNoHostLoopback = "no-host-loopback"
	NetworkModeNone           = "none"
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

	// OCI image config (baked by nix wrapper; empty for bwrap).
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
	AgentFilesDrv   string // .drv path; realized by `launcher build`
	AgentEnvDrv     string // .drv path; realized by `launcher build`
	BakedPrefetch   string
	BwrapUnshareNet bool

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
