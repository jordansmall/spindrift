package oci

import (
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Issue #445 replaced a long positional-argument list with a single Config
// struct, so every OCI-relevant Config field must reach the adapter; the
// image artifacts arrive through the OCI part of the input artifacts.
func TestNewOCI_UsesConfigAndArtifacts(t *testing.T) {
	cfg := runner.Config{
		Runtime:     "podman",
		NetworkMode: "no-host-loopback",
		PidsLimit:   "256",
		MemoryLimit: "2g",
		MountParams: runner.MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	art := inputdoc.OCIArtifacts{
		ImageArchive:    inputdoc.ValueOf("/nix/store/archive"),
		ImageDrv:        inputdoc.ValueOf("/nix/store/drv"),
		ImageTag:        inputdoc.ValueOf("img:tag2"),
		NixBuilderImage: inputdoc.ValueOf("builder@sha256:abc"),
		NixVolume:       inputdoc.ValueOf("vol"),
		FlakeImageAttr:  inputdoc.ValueOf(".#image"),
	}
	r, err := New(cfg, art, runnertest.Lookup(map[string]string{"PODMAN_NETWORK": "none"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*ociAdapter)
	if !ok {
		t.Fatalf("New did not return *ociAdapter")
	}
	got := ociAdapter{
		boxImg:          imageSpec{tag: a.boxImg.tag, archive: a.boxImg.archive, drv: a.boxImg.drv, flakeAttr: a.boxImg.flakeAttr},
		cli:             a.cli,
		nixBuilderImage: a.nixBuilderImage,
		nixVolume:       a.nixVolume,
		pwd:             a.pwd,
		mountParams:     a.mountParams,
		network:         a.network,
		pidsLimit:       a.pidsLimit,
		memoryLimit:     a.memoryLimit,
	}
	want := ociAdapter{
		boxImg:          imageSpec{tag: "img:tag2", archive: "/nix/store/archive", drv: "/nix/store/drv", flakeAttr: ".#image"},
		cli:             cfg.Runtime,
		nixBuilderImage: "builder@sha256:abc",
		nixVolume:       "vol",
		pwd:             "/pwd",
		mountParams:     cfg.MountParams,
		network:         cfg.OCINetwork("none", noHostLoopbackNetwork(runner.BinaryFor(cfg.Runtime))),
		pidsLimit:       cfg.PidsLimit,
		memoryLimit:     cfg.MemoryLimit,
	}
	if got != want {
		t.Errorf("New(cfg, art, pwd) fields = %+v, want %+v", got, want)
	}
}

// A direct binary invocation with no --input document resolves to the OCI
// family with no artifacts part; ociPart reads it as all-empty.
func TestStrategy_NilOCIPartIsZero(t *testing.T) {
	r, err := newRunner(runner.Config{Runtime: "podman"}, inputdoc.Artifacts{}, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a := r.(*ociAdapter)
	if a.boxImg.tag != "" || a.boxImg.archive != "" || a.boxImg.drv != "" ||
		a.nixBuilderImage != "" || a.nixVolume != "" || a.boxImg.flakeAttr != "" {
		t.Errorf("adapter artifacts = %+v, want all empty for a nil OCI part", a)
	}
}

// Issue #1274: "rancher" is the first runtime value that differs from the
// binary it invokes, "nerdctl".
func TestNewOCI_RancherAliasesToNerdctl(t *testing.T) {
	r, err := New(runner.Config{Runtime: "rancher"}, inputdoc.OCIArtifacts{}, runnertest.Lookup(nil), "/pwd")
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
	r, err := New(cfg, inputdoc.OCIArtifacts{}, runnertest.Lookup(map[string]string{"PODMAN_NETWORK": "slirp4netns"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*ociAdapter).network.Arg; got != "slirp4netns" {
		t.Errorf("network.Arg = %q, want slirp4netns", got)
	}
	r, err = New(cfg, inputdoc.OCIArtifacts{}, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*ociAdapter).network.Arg; got != "" {
		t.Errorf("network.Arg = %q, want empty with the knob unset", got)
	}
}
