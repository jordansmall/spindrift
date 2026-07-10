package runner

// Config carries the subset of launcher config the runner package's
// constructors need to build an OCI or bwrap adapter. pwd is passed
// separately to NewOCI (a genuine per-invocation runtime dependency, not a
// config knob).
type Config struct {
	// Runtime selects the sandbox mechanism: "podman", "docker", or "bwrap".
	// For OCI adapters it also names the CLI binary.
	Runtime string

	// OCI image config (baked by nix wrapper; empty for bwrap).
	Image           string
	ImageArchive    string
	ImageDrv        string
	ImageTag        string
	NixBuilderImage string
	NixVolume       string
	FlakeImageAttr  string

	// OCI container network / resource caps.
	PodmanNetwork string
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

	// In-box mount targets declared by the selected Driver (ADR 0009; baked
	// by nix at wrap time), shared by the OCI and bwrap run adapters.
	// DriverSessionCacheDir is empty when the Driver declares no
	// session-state dir, in which case the driver-cache dir is never
	// mounted regardless of Box.DriverCacheDir.
	DriverSkillsDir       string
	DriverSessionCacheDir string
}
