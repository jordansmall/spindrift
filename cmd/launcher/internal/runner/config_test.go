package runner

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Issue #445 replaced a long positional-argument list with a single Config
// struct, so every OCI-relevant Config field must reach the adapter.
func TestNewOCI_UsesConfigFields(t *testing.T) {
	cfg := Config{
		Runtime:         "podman",
		Image:           "img:tag",
		ImageArchive:    "/nix/store/archive",
		ImageDrv:        "/nix/store/drv",
		ImageTag:        "img:tag2",
		NixBuilderImage: "builder@sha256:abc",
		NixVolume:       "vol",
		FlakeImageAttr:  ".#image",
		PodmanNetwork:   "none",
		NetworkMode:     "no-host-loopback",
		PidsLimit:       "256",
		MemoryLimit:     "2g",
		MountParams: MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	r := NewOCI(cfg, "/pwd")
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("NewOCI did not return *ociAdapter")
	}
	got := ociAdapter{
		cli:             a.cli,
		image:           a.image,
		imageArchive:    a.imageArchive,
		imageDrv:        a.imageDrv,
		imageTag:        a.imageTag,
		nixBuilderImage: a.nixBuilderImage,
		nixVolume:       a.nixVolume,
		flakeImageAttr:  a.flakeImageAttr,
		pwd:             a.pwd,
		mountParams:     a.mountParams,
		podmanNetwork:   a.podmanNetwork,
		networkMode:     a.networkMode,
		pidsLimit:       a.pidsLimit,
		memoryLimit:     a.memoryLimit,
	}
	want := ociAdapter{
		cli:             cfg.Runtime,
		image:           cfg.Image,
		imageArchive:    cfg.ImageArchive,
		imageDrv:        cfg.ImageDrv,
		imageTag:        cfg.ImageTag,
		nixBuilderImage: cfg.NixBuilderImage,
		nixVolume:       cfg.NixVolume,
		flakeImageAttr:  cfg.FlakeImageAttr,
		pwd:             "/pwd",
		mountParams:     cfg.MountParams,
		podmanNetwork:   cfg.PodmanNetwork,
		networkMode:     cfg.NetworkMode,
		pidsLimit:       cfg.PidsLimit,
		memoryLimit:     cfg.MemoryLimit,
	}
	if got != want {
		t.Errorf("NewOCI(cfg, pwd) fields = %+v, want %+v", got, want)
	}
}

// Issue #1274: "rancher" is the first runtime value that differs from the
// binary it invokes, "nerdctl".
func TestNewOCI_RancherAliasesToNerdctl(t *testing.T) {
	r := NewOCI(Config{Runtime: "rancher"}, "/pwd")
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("NewOCI did not return *ociAdapter")
	}
	if a.cli != "nerdctl" {
		t.Errorf("cli = %q, want %q", a.cli, "nerdctl")
	}
}

// The bwrap counterpart to the Config-struct constructor above (issue #445).
func TestNewBwrap_UsesConfigFields(t *testing.T) {
	cfg := Config{
		AgentFiles:      "/agent-files",
		AgentEnv:        "/agent-env",
		PasswdFile:      "/nix/store/abc-passwd/passwd",
		GroupFile:       "/nix/store/def-group/group",
		BakedPrefetch:   "prefetch-snippet",
		BwrapUnshareNet: true,
		NetworkMode:     "none",
		NixConfigFile:   "/nix/store/fake-hash-nix-conf/nix.conf",
		MountParams: MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	r := NewBwrap(cfg, "/pwd")
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("NewBwrap did not return *bwrapAdapter")
	}
	want := bwrapAdapter{
		agentFiles:         cfg.AgentFiles,
		agentEnv:           cfg.AgentEnv,
		passwdFile:         cfg.PasswdFile,
		groupFile:          cfg.GroupFile,
		bakedPrefetch:      cfg.BakedPrefetch,
		mountParams:        cfg.MountParams,
		unshareNet:         cfg.BwrapUnshareNet,
		networkMode:        cfg.NetworkMode,
		nixConfigFile:      cfg.NixConfigFile,
		nixVarSnapshotDir:  nixVarSnapshotDir("/pwd", closureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot: nixVarSnapshotRoot("/pwd"),
	}
	// Compare with reflect.DeepEqual over pointers, not !=: the mu/running
	// fields Kill uses (issue #649) are not comparable, and dereferencing
	// would copy the embedded sync.Mutex.
	if !reflect.DeepEqual(a, &want) {
		t.Errorf("NewBwrap(cfg) fields = %+v, want %+v", a, &want)
	}
}

// The bwrap build counterpart to the Config-struct constructor (issue #445).
func TestNewBwrapBuild_UsesConfigFields(t *testing.T) {
	cfg := Config{
		AgentFilesDrv:    "/files.drv",
		AgentEnvDrv:      "/env.drv",
		PasswdFileDrv:    "/passwd.drv",
		GroupFileDrv:     "/group.drv",
		NixConfigFileDrv: "/nix-config.drv",
	}
	r := NewBwrapBuild(cfg, "/pwd")
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBwrapBuild did not return *bwrapBuildAdapter")
	}
	want := bwrapBuildAdapter{
		agentFilesDrv:      cfg.AgentFilesDrv,
		agentEnvDrv:        cfg.AgentEnvDrv,
		passwdFileDrv:      cfg.PasswdFileDrv,
		groupFileDrv:       cfg.GroupFileDrv,
		nixConfigFileDrv:   cfg.NixConfigFileDrv,
		nixVarSnapshotDir:  nixVarSnapshotDir("/pwd", closureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot: nixVarSnapshotRoot("/pwd"),
		nixVarGeneration:   closureGeneration(cfg.ImageTag),
	}
	if *a != want {
		t.Errorf("NewBwrapBuild(cfg) fields = %+v, want %+v", *a, want)
	}
}

// The ImageTag fixture is a nix store path because lib/preambles.nix's bwrap
// branch renders it that way. Each closure gets a generation subdir named
// after its basename, replacing the flat path every closure collided on.
func TestNewBwrap_ImageTagScopesSnapshotDirToClosureGeneration(t *testing.T) {
	cfg := Config{ImageTag: "/nix/store/abc123-agent-closure"}
	r := NewBwrap(cfg, "/pwd")
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("NewBwrap did not return *bwrapAdapter")
	}
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot", "abc123-agent-closure")
	if a.nixVarSnapshotDir != want {
		t.Errorf("NewBwrap(cfg).nixVarSnapshotDir = %q, want %q", a.nixVarSnapshotDir, want)
	}
}

// The build side writes the snapshot the run side above mounts, so both must
// resolve to the same generation-scoped path for a given ImageTag.
func TestNewBwrapBuild_ImageTagScopesSnapshotDirToClosureGeneration(t *testing.T) {
	cfg := Config{ImageTag: "/nix/store/abc123-agent-closure"}
	r := NewBwrapBuild(cfg, "/pwd")
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBwrapBuild did not return *bwrapBuildAdapter")
	}
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot", "abc123-agent-closure")
	if a.nixVarSnapshotDir != want {
		t.Errorf("NewBwrapBuild(cfg).nixVarSnapshotDir = %q, want %q", a.nixVarSnapshotDir, want)
	}
}
