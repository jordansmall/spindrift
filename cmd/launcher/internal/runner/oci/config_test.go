package oci

import (
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// Issue #445 replaced a long positional-argument list with a single Config
// struct, so every OCI-relevant Config field must reach the adapter.
func TestNewOCI_UsesConfigFields(t *testing.T) {
	cfg := runner.Config{
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
		MountParams: runner.MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	r := New(cfg, "/pwd")
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("New did not return *ociAdapter")
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
		t.Errorf("New(cfg, pwd) fields = %+v, want %+v", got, want)
	}
}

// Issue #1274: "rancher" is the first runtime value that differs from the
// binary it invokes, "nerdctl".
func TestNewOCI_RancherAliasesToNerdctl(t *testing.T) {
	r := New(runner.Config{Runtime: "rancher"}, "/pwd")
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("New did not return *ociAdapter")
	}
	if a.cli != "nerdctl" {
		t.Errorf("cli = %q, want %q", a.cli, "nerdctl")
	}
}
