package runner

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestNewOCI_UsesConfigFields verifies NewOCI builds its adapter fields from
// a single Config struct instead of a long positional-argument list (issue
// #445): every OCI-relevant Config field must reach the constructed adapter.
func TestNewOCI_UsesConfigFields(t *testing.T) {
	cfg := Config{
		Runtime:               "podman",
		Image:                 "img:tag",
		ImageArchive:          "/nix/store/archive",
		ImageDrv:              "/nix/store/drv",
		ImageTag:              "img:tag2",
		NixBuilderImage:       "builder@sha256:abc",
		NixVolume:             "vol",
		FlakeImageAttr:        ".#image",
		PromptDir:             "/prompts",
		SkillsDir:             "/skills",
		DriverSessionCacheDir: "/home/agent/.claude/projects",
		PodmanNetwork:         "none",
		NetworkMode:           "no-host-loopback",
		PidsLimit:             "256",
		MemoryLimit:           "2g",
	}
	r := NewOCI(cfg, "/pwd")
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("NewOCI did not return *ociAdapter")
	}
	got := ociAdapter{
		cli:                   a.cli,
		image:                 a.image,
		imageArchive:          a.imageArchive,
		imageDrv:              a.imageDrv,
		imageTag:              a.imageTag,
		nixBuilderImage:       a.nixBuilderImage,
		nixVolume:             a.nixVolume,
		flakeImageAttr:        a.flakeImageAttr,
		pwd:                   a.pwd,
		promptDir:             a.promptDir,
		skillsDir:             a.skillsDir,
		driverSessionCacheDir: a.driverSessionCacheDir,
		podmanNetwork:         a.podmanNetwork,
		networkMode:           a.networkMode,
		pidsLimit:             a.pidsLimit,
		memoryLimit:           a.memoryLimit,
	}
	want := ociAdapter{
		cli:                   cfg.Runtime,
		image:                 cfg.Image,
		imageArchive:          cfg.ImageArchive,
		imageDrv:              cfg.ImageDrv,
		imageTag:              cfg.ImageTag,
		nixBuilderImage:       cfg.NixBuilderImage,
		nixVolume:             cfg.NixVolume,
		flakeImageAttr:        cfg.FlakeImageAttr,
		pwd:                   "/pwd",
		promptDir:             cfg.PromptDir,
		skillsDir:             cfg.SkillsDir,
		driverSessionCacheDir: cfg.DriverSessionCacheDir,
		podmanNetwork:         cfg.PodmanNetwork,
		networkMode:           cfg.NetworkMode,
		pidsLimit:             cfg.PidsLimit,
		memoryLimit:           cfg.MemoryLimit,
	}
	if got != want {
		t.Errorf("NewOCI(cfg, pwd) fields = %+v, want %+v", got, want)
	}
}

// TestNewOCI_RancherAliasesToNerdctl verifies runtime = "rancher" drives the
// OCI adapter's CLI binary as "nerdctl" — the first runtime value that
// differs from the binary it invokes (issue #1274).
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

// TestNewBwrap_UsesConfigFields verifies NewBwrap builds its adapter fields
// from Config instead of a positional-argument list.
func TestNewBwrap_UsesConfigFields(t *testing.T) {
	cfg := Config{
		AgentFiles:            "/agent-files",
		AgentEnv:              "/agent-env",
		PasswdFile:            "/nix/store/abc-passwd/passwd",
		GroupFile:             "/nix/store/def-group/group",
		BakedPrefetch:         "prefetch-snippet",
		PromptDir:             "/prompts",
		SkillsDir:             "/skills",
		DriverSessionCacheDir: "/home/agent/.claude/projects",
		BwrapUnshareNet:       true,
		NetworkMode:           "none",
		NixConfigFile:         "/nix/store/fake-hash-nix-conf/nix.conf",
	}
	r := NewBwrap(cfg, "/pwd")
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("NewBwrap did not return *bwrapAdapter")
	}
	want := bwrapAdapter{
		agentFiles:            cfg.AgentFiles,
		agentEnv:              cfg.AgentEnv,
		passwdFile:            cfg.PasswdFile,
		groupFile:             cfg.GroupFile,
		bakedPrefetch:         cfg.BakedPrefetch,
		promptDir:             cfg.PromptDir,
		skillsDir:             cfg.SkillsDir,
		driverSessionCacheDir: cfg.DriverSessionCacheDir,
		unshareNet:            cfg.BwrapUnshareNet,
		networkMode:           cfg.NetworkMode,
		nixConfigFile:         cfg.NixConfigFile,
		nixVarSnapshotDir:     nixVarSnapshotDir("/pwd", closureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot:    nixVarSnapshotRoot("/pwd"),
	}
	// reflect.DeepEqual over pointers, not !=: bwrapAdapter now also carries
	// the mu/running process-tracking fields Kill (issue #649) uses, which a
	// plain struct comparison can't handle (map[string]*os.Process is not
	// comparable) — comparing pointers instead of dereferenced values avoids
	// copying the embedded sync.Mutex.
	if !reflect.DeepEqual(a, &want) {
		t.Errorf("NewBwrap(cfg) fields = %+v, want %+v", a, &want)
	}
}

// TestNewBwrapBuild_UsesConfigFields verifies NewBwrapBuild builds its
// adapter fields from Config instead of a positional-argument list.
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

// TestNewBwrap_ImageTagScopesSnapshotDirToClosureGeneration verifies that a
// real closure ImageTag (as lib/preambles.nix's bwrap branch renders it — a
// nix store path like /nix/store/<hash>-agent-closure) scopes the run
// adapter's nixVarSnapshotDir to a generation subdir named after that
// closure's basename, rather than the shared flat path every closure used
// to collide on.
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

// TestNewBwrapBuild_ImageTagScopesSnapshotDirToClosureGeneration is the build
// adapter's counterpart to TestNewBwrap_ImageTagScopesSnapshotDirToClosureGeneration
// — the build side writes the snapshot the run side above mounts, so both
// must resolve to the same generation-scoped path for a given ImageTag.
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
