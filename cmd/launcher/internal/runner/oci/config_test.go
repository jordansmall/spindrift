package oci

import (
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
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
		NetworkMode:     "no-host-loopback",
		PidsLimit:       "256",
		MemoryLimit:     "2g",
		MountParams: runner.MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	r, err := New(cfg, runnertest.Lookup(map[string]string{"PODMAN_NETWORK": "none"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
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
		network:         a.network,
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
		network:         cfg.OCINetwork("none", noHostLoopbackNetwork(runner.BinaryFor(cfg.Runtime))),
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
	r, err := New(runner.Config{Runtime: "rancher"}, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("New did not return *ociAdapter")
	}
	if a.cli != "nerdctl" {
		t.Errorf("cli = %q, want %q", a.cli, "nerdctl")
	}
}

// PODMAN_NETWORK is a row knob: it reaches the adapter through the Lookup New
// is handed, not through runner.Config.
func TestNewOCI_ReadsPodmanNetworkFromLookup(t *testing.T) {
	cfg := runner.Config{Runtime: "podman", NetworkMode: runner.NetworkModeOpen}
	r, err := New(cfg, runnertest.Lookup(map[string]string{"PODMAN_NETWORK": "slirp4netns"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*ociAdapter).network.Arg; got != "slirp4netns" {
		t.Errorf("network.Arg = %q, want slirp4netns", got)
	}
	r, err = New(cfg, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*ociAdapter).network.Arg; got != "" {
		t.Errorf("network.Arg = %q, want empty with the knob unset", got)
	}
}
