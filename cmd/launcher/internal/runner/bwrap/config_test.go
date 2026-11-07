package bwrap

import (
	"path/filepath"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Issue #445 replaced a long positional-argument list with a single Config
// struct, so every bwrap-relevant Config field must reach the adapter; the
// bwrap artifacts reach it from the bwrap part of the Launcher input document.
func TestNewBwrap_ReadsArtifactsFromBwrapPart(t *testing.T) {
	cfg := runner.Config{
		NetworkMode: "none",
		MountParams: runner.MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	art := inputdoc.BwrapArtifacts{
		AgentFiles:       inputdoc.ValueOf("/agent-files"),
		AgentEnv:         inputdoc.ValueOf("/agent-env"),
		PasswdFile:       inputdoc.ValueOf("/nix/store/abc-passwd/passwd"),
		GroupFile:        inputdoc.ValueOf("/nix/store/def-group/group"),
		BakedPrefetch:    inputdoc.ValueOf("prefetch-snippet"),
		NixConfigFile:    inputdoc.ValueOf("/nix/store/fake-hash-nix-conf/nix.conf"),
		NixStoreWritable: inputdoc.FlagOf(true),
		SyscallFilter:    inputdoc.ValueOf("/nix/store/fff-filter.bpf"),
	}
	r, err := New(cfg, art, runnertest.Lookup(map[string]string{"BWRAP_UNSHARE_NET": "true"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("New did not return *bwrapAdapter")
	}
	want := bwrapAdapter{
		prepare:            newBuildAdapter(cfg, art, "/pwd"),
		agentFiles:         "/agent-files",
		agentEnv:           "/agent-env",
		passwdFile:         "/nix/store/abc-passwd/passwd",
		groupFile:          "/nix/store/def-group/group",
		bakedPrefetch:      "prefetch-snippet",
		mountParams:        cfg.MountParams,
		network:            cfg.BwrapNetwork(true),
		nixConfigFile:      "/nix/store/fake-hash-nix-conf/nix.conf",
		nixStoreWritable:   true,
		syscallFilterPath:  "/nix/store/fff-filter.bpf",
		nixVarSnapshotDir:  NixVarSnapshotDir("/pwd", ClosureGeneration("")),
		nixVarSnapshotRoot: NixVarSnapshotRoot("/pwd"),
		nixGCRootsRoot:     NixGCRootsRoot("/pwd"),
		generation:         ClosureGeneration(""),
	}
	// Compare with reflect.DeepEqual over pointers, not !=: the mu/running
	// fields Kill uses (issue #649) are not comparable, and dereferencing
	// would copy the embedded sync.Mutex.
	if !reflect.DeepEqual(a, &want) {
		t.Errorf("New(cfg) fields = %+v, want %+v", a, &want)
	}
}

// The Strategy entry hands New the bwrap part of the document; a document that
// selected another family carries none, which must read as empty artifacts
// rather than panic.
func TestStrategy_ToleratesNilBwrapPart(t *testing.T) {
	cfg := runner.Config{}
	for name, mk := range map[string]func() (runner.Runner, error){
		"New": func() (runner.Runner, error) {
			return Strategy.New(cfg, inputdoc.Artifacts{}, runnertest.Lookup(nil), "/pwd")
		},
		"NewBuild": func() (runner.Runner, error) {
			return Strategy.NewBuild(cfg, inputdoc.Artifacts{}, runnertest.Lookup(nil), "/pwd")
		},
	} {
		if r, err := mk(); err != nil || r == nil {
			t.Errorf("Strategy.%s with no bwrap part = (%v, %v), want a runner", name, r, err)
		}
	}
	if got := Strategy.PrerequisitesFor(cfg, inputdoc.Artifacts{}, runnertest.Lookup(nil)); len(got) == 0 {
		t.Error("Strategy.PrerequisitesFor with no bwrap part returned no rows")
	}
	art := inputdoc.Artifacts{Bwrap: &inputdoc.BwrapArtifacts{AgentFiles: inputdoc.ValueOf("/agent-files")}}
	r, err := Strategy.New(cfg, art, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*bwrapAdapter).agentFiles; got != "/agent-files" {
		t.Errorf("Strategy.New agentFiles = %q, want the bwrap part's /agent-files", got)
	}
}

// The bwrap build constructor reads the bwrap artifacts part (issue #445).
func TestNewBwrapBuild_ReadsArtifactsFromBwrapPart(t *testing.T) {
	art := inputdoc.BwrapArtifacts{
		AgentFilesDrv:    inputdoc.ValueOf("/files.drv"),
		AgentEnvDrv:      inputdoc.ValueOf("/env.drv"),
		PasswdFileDrv:    inputdoc.ValueOf("/passwd.drv"),
		GroupFileDrv:     inputdoc.ValueOf("/group.drv"),
		NixConfigFileDrv: inputdoc.ValueOf("/nix-config.drv"),
		SyscallFilterDrv: inputdoc.ValueOf("/filter.drv"),
	}
	r := NewBuild(runner.Config{}, art, "/pwd")
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBuild did not return *bwrapBuildAdapter")
	}
	want := bwrapBuildAdapter{
		agentFilesDrv:      "/files.drv",
		agentEnvDrv:        "/env.drv",
		passwdFileDrv:      "/passwd.drv",
		groupFileDrv:       "/group.drv",
		nixConfigFileDrv:   "/nix-config.drv",
		syscallFilterDrv:   "/filter.drv",
		nixVarSnapshotDir:  NixVarSnapshotDir("/pwd", ClosureGeneration("")),
		nixVarSnapshotRoot: NixVarSnapshotRoot("/pwd"),
		nixVarGeneration:   ClosureGeneration(""),
		nixGCRootsRoot:     NixGCRootsRoot("/pwd"),
	}
	if *a != want {
		t.Errorf("NewBuild(cfg) fields = %+v, want %+v", *a, want)
	}
}

// The ImageTag fixture is a nix store path because lib/runners/bwrap.nix
// fills IMAGE_TAG with the agent closure's store path. Each closure gets a
// generation subdir named after its basename, replacing the flat path every
// closure collided on.
func TestNewBwrap_ImageTagScopesSnapshotDirToClosureGeneration(t *testing.T) {
	art := inputdoc.BwrapArtifacts{ImageTag: inputdoc.ValueOf("/nix/store/abc123-agent-closure")}
	r, err := New(runner.Config{}, art, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("New did not return *bwrapAdapter")
	}
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot", "abc123-agent-closure")
	if a.nixVarSnapshotDir != want {
		t.Errorf("New(cfg).nixVarSnapshotDir = %q, want %q", a.nixVarSnapshotDir, want)
	}
}

// The build side writes the snapshot the run side above mounts, so both must
// resolve to the same generation-scoped path for a given ImageTag.
func TestNewBwrapBuild_ImageTagScopesSnapshotDirToClosureGeneration(t *testing.T) {
	art := inputdoc.BwrapArtifacts{ImageTag: inputdoc.ValueOf("/nix/store/abc123-agent-closure")}
	r := NewBuild(runner.Config{}, art, "/pwd")
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBuild did not return *bwrapBuildAdapter")
	}
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot", "abc123-agent-closure")
	if a.nixVarSnapshotDir != want {
		t.Errorf("NewBuild(cfg).nixVarSnapshotDir = %q, want %q", a.nixVarSnapshotDir, want)
	}
}

// RUNNER_KIND=bwrap with RUNTIME=podman is a supported pairing (issue #2538);
// the adapter must still isolate, whatever the runtime name says.
func TestNewBwrap_IsolatesUnderMismatchedRuntime(t *testing.T) {
	for _, rt := range []string{"podman", "docker", "rancher", ""} {
		cfg := runner.Config{Runtime: rt, NetworkMode: runner.NetworkModeOpen}
		r, err := New(cfg, inputdoc.BwrapArtifacts{}, runnertest.Lookup(nil), "/pwd")
		if err != nil {
			t.Fatal(err)
		}
		a, ok := r.(*bwrapAdapter)
		if !ok {
			t.Fatalf("New did not return *bwrapAdapter")
		}
		if !a.network.Isolate || !a.network.Pasta {
			t.Errorf("Runtime %q: network = %+v, want Isolate and Pasta", rt, a.network)
		}
	}
}

// testNet resolves mode through the shared plan, so a test states the
// NETWORK_MODE knob rather than hand-picking the Isolate/Pasta booleans.
func testNet(mode runner.NetworkMode) runner.BwrapNetwork {
	return runner.Config{NetworkMode: mode}.BwrapNetwork(false)
}

// BWRAP_UNSHARE_NET is a row knob: read through the Lookup New is handed, it
// can force isolation on even under NETWORK_MODE=host.
func TestNewBwrap_ReadsUnshareNetFromLookup(t *testing.T) {
	cfg := runner.Config{NetworkMode: runner.NetworkModeHost}
	r, err := New(cfg, inputdoc.BwrapArtifacts{}, runnertest.Lookup(map[string]string{"BWRAP_UNSHARE_NET": "true"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*bwrapAdapter).network; !got.Isolate {
		t.Errorf("network = %+v, want Isolate", got)
	}
	r, err = New(cfg, inputdoc.BwrapArtifacts{}, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*bwrapAdapter).network; got.Isolate {
		t.Errorf("network = %+v, want shared host network with the knob unset", got)
	}
}
