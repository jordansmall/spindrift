package bwrap

import (
	"path/filepath"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Issue #445 replaced a long positional-argument list with a single Config
// struct, so every bwrap-relevant Config field must reach the adapter.
func TestNewBwrap_UsesConfigFields(t *testing.T) {
	cfg := runner.Config{
		AgentFiles:    "/agent-files",
		AgentEnv:      "/agent-env",
		PasswdFile:    "/nix/store/abc-passwd/passwd",
		GroupFile:     "/nix/store/def-group/group",
		BakedPrefetch: "prefetch-snippet",
		NetworkMode:   "none",
		NixConfigFile: "/nix/store/fake-hash-nix-conf/nix.conf",
		MountParams: runner.MountParams{
			PromptDir:             "/prompts",
			SkillsDir:             "/skills",
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	r, err := New(cfg, runnertest.Lookup(map[string]string{"BWRAP_UNSHARE_NET": "true"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*bwrapAdapter)
	if !ok {
		t.Fatalf("New did not return *bwrapAdapter")
	}
	want := bwrapAdapter{
		prepare:            newBuildAdapter(cfg, "/pwd"),
		agentFiles:         cfg.AgentFiles,
		agentEnv:           cfg.AgentEnv,
		passwdFile:         cfg.PasswdFile,
		groupFile:          cfg.GroupFile,
		bakedPrefetch:      cfg.BakedPrefetch,
		mountParams:        cfg.MountParams,
		network:            cfg.BwrapNetwork(true),
		nixConfigFile:      cfg.NixConfigFile,
		nixVarSnapshotDir:  NixVarSnapshotDir("/pwd", ClosureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot: NixVarSnapshotRoot("/pwd"),
		nixGCRootsRoot:     NixGCRootsRoot("/pwd"),
		generation:         ClosureGeneration(cfg.ImageTag),
	}
	// Compare with reflect.DeepEqual over pointers, not !=: the mu/running
	// fields Kill uses (issue #649) are not comparable, and dereferencing
	// would copy the embedded sync.Mutex.
	if !reflect.DeepEqual(a, &want) {
		t.Errorf("New(cfg) fields = %+v, want %+v", a, &want)
	}
}

// The bwrap build counterpart to the Config-struct constructor (issue #445).
func TestNewBwrapBuild_UsesConfigFields(t *testing.T) {
	cfg := runner.Config{
		AgentFilesDrv:    "/files.drv",
		AgentEnvDrv:      "/env.drv",
		PasswdFileDrv:    "/passwd.drv",
		GroupFileDrv:     "/group.drv",
		NixConfigFileDrv: "/nix-config.drv",
	}
	r, err := NewBuild(cfg, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := r.(*bwrapBuildAdapter)
	if !ok {
		t.Fatalf("NewBuild did not return *bwrapBuildAdapter")
	}
	want := bwrapBuildAdapter{
		agentFilesDrv:      cfg.AgentFilesDrv,
		agentEnvDrv:        cfg.AgentEnvDrv,
		passwdFileDrv:      cfg.PasswdFileDrv,
		groupFileDrv:       cfg.GroupFileDrv,
		nixConfigFileDrv:   cfg.NixConfigFileDrv,
		nixVarSnapshotDir:  NixVarSnapshotDir("/pwd", ClosureGeneration(cfg.ImageTag)),
		nixVarSnapshotRoot: NixVarSnapshotRoot("/pwd"),
		nixVarGeneration:   ClosureGeneration(cfg.ImageTag),
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
	cfg := runner.Config{ImageTag: "/nix/store/abc123-agent-closure"}
	r, err := New(cfg, runnertest.Lookup(nil), "/pwd")
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
	cfg := runner.Config{ImageTag: "/nix/store/abc123-agent-closure"}
	r, err := NewBuild(cfg, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
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
		r, err := New(cfg, runnertest.Lookup(nil), "/pwd")
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
	r, err := New(cfg, runnertest.Lookup(map[string]string{"BWRAP_UNSHARE_NET": "true"}), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*bwrapAdapter).network; !got.Isolate {
		t.Errorf("network = %+v, want Isolate", got)
	}
	r, err = New(cfg, runnertest.Lookup(nil), "/pwd")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.(*bwrapAdapter).network; got.Isolate {
		t.Errorf("network = %+v, want shared host network with the knob unset", got)
	}
}
