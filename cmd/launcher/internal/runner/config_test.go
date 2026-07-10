package runner

import "testing"

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
		DriverSkillsDir:       "/home/agent/.claude/skills",
		DriverSessionCacheDir: "/home/agent/.claude/projects",
		PodmanNetwork:         "none",
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
		driverSkillsDir:       a.driverSkillsDir,
		driverSessionCacheDir: a.driverSessionCacheDir,
		podmanNetwork:         a.podmanNetwork,
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
		driverSkillsDir:       cfg.DriverSkillsDir,
		driverSessionCacheDir: cfg.DriverSessionCacheDir,
		podmanNetwork:         cfg.PodmanNetwork,
		pidsLimit:             cfg.PidsLimit,
		memoryLimit:           cfg.MemoryLimit,
	}
	if got != want {
		t.Errorf("NewOCI(cfg, pwd) fields = %+v, want %+v", got, want)
	}
}

// TestNewBwrap_UsesConfigFields verifies NewBwrap builds its adapter fields
// from Config instead of a positional-argument list.
func TestNewBwrap_UsesConfigFields(t *testing.T) {
	cfg := Config{
		AgentFiles:            "/agent-files",
		AgentEnv:              "/agent-env",
		BakedPrefetch:         "prefetch-snippet",
		PromptDir:             "/prompts",
		SkillsDir:             "/skills",
		DriverSkillsDir:       "/home/agent/.claude/skills",
		DriverSessionCacheDir: "/home/agent/.claude/projects",
		BwrapUnshareNet:       true,
	}
	r := NewBwrap(cfg)
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("NewBwrap did not return *bwrapAdapter")
	}
	want := bwrapAdapter{
		agentFiles:            cfg.AgentFiles,
		agentEnv:              cfg.AgentEnv,
		bakedPrefetch:         cfg.BakedPrefetch,
		promptDir:             cfg.PromptDir,
		skillsDir:             cfg.SkillsDir,
		driverSkillsDir:       cfg.DriverSkillsDir,
		driverSessionCacheDir: cfg.DriverSessionCacheDir,
		unshareNet:            cfg.BwrapUnshareNet,
	}
	if *a != want {
		t.Errorf("NewBwrap(cfg) fields = %+v, want %+v", *a, want)
	}
}

// TestNewBwrapBuild_UsesConfigFields verifies NewBwrapBuild builds its
// adapter fields from Config instead of a positional-argument list.
func TestNewBwrapBuild_UsesConfigFields(t *testing.T) {
	cfg := Config{AgentFilesDrv: "/files.drv", AgentEnvDrv: "/env.drv"}
	r := NewBwrapBuild(cfg)
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBwrapBuild did not return *bwrapBuildAdapter")
	}
	want := bwrapBuildAdapter{agentFilesDrv: cfg.AgentFilesDrv, agentEnvDrv: cfg.AgentEnvDrv}
	if *a != want {
		t.Errorf("NewBwrapBuild(cfg) fields = %+v, want %+v", *a, want)
	}
}
