package oci

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// The prompt-dir and skills-dir gates live only in BuildMountSpecs, never in
// an adapter file. The bwrap half lives in the bwrap package.
func TestAdapterRendersOnly_NoDuplicatedMountDecisions(t *testing.T) {
	runnertest.AssertNoMountDecisionMarkers(t, "oci.go")
}

// The same mount config reaches both backends by construction: both render the
// same BuildMountSpecs list. The bwrap half lives in the bwrap package.
func TestMountSpecs_RenderedByOCI(t *testing.T) {
	fx := runnertest.NewMountFixture(t)
	oci := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
		mountParams: runner.MountParams{
			PromptDir:             fx.PromptDir,
			SkillsDir:             fx.SkillsDir,
			DriverSessionCacheDir: runnertest.DriverSessionCacheTarget,
		},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: fx.CacheDir, Sockets: []runner.SocketMount{{Source: fx.ProxySocket, Target: runner.RegistryProxySocketTarget}}}

	fx.AssertRendered(t, "OCI", strings.Join(oci.buildRunArgs(box), " "), ":")
}

// ADR 0033, issue #1697: the Accumulation-repo and outbox mounts must reach
// the OCI backend the same way the other mounts do.
func TestLocalCodeForgeMounts_RenderedByOCI(t *testing.T) {
	repoDir := t.TempDir()
	outboxDir := t.TempDir()

	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: runner.MountParams{HostMediatedRemote: true, AccumulationRepoDir: repoDir},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice := oci.buildRunArgs(box)
	ociArgs := strings.Join(ociArgSlice, " ")

	if !slices.Contains(ociArgSlice, repoDir+":/repo:ro") {
		t.Errorf("OCI missing read-only /repo mount in args: %s", ociArgs)
	}
	if !slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI missing writable /outbox mount in args: %s", ociArgs)
	}
}

// Issue #3957: the writable /outbox mount reaches the OCI backend whenever
// Box.OutboxDir is set, whatever MountParams says, with no /repo mount since
// HostMediatedRemote is unset here.
func TestOutboxMount_RenderedByOCI(t *testing.T) {
	outboxDir := t.TempDir()

	oci := &ociAdapter{
		cli:   "podman",
		image: "spindrift:test",
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice := oci.buildRunArgs(box)
	ociArgs := strings.Join(ociArgSlice, " ")

	if !slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI missing writable /outbox mount in args: %s", ociArgs)
	}
	if strings.Contains(ociArgs, "/repo") {
		t.Errorf("unexpected /repo mount for HostMediatedRemote=false: oci=%s", ociArgs)
	}
}

// The Accumulation-repo dir is present on purpose, so this pins that the
// adapter does not leak the /repo mount through its own render path when
// HostMediatedRemote is false.
func TestLocalCodeForgeMounts_AbsentOnNonLocalOCI(t *testing.T) {
	repoDir := t.TempDir()

	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: runner.MountParams{AccumulationRepoDir: repoDir},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}}

	ociArgSlice := oci.buildRunArgs(box)

	if slices.Contains(ociArgSlice, repoDir+":/repo:ro") {
		t.Errorf("OCI must not mount /repo with HostMediatedRemote=false: %s", strings.Join(ociArgSlice, " "))
	}
}
