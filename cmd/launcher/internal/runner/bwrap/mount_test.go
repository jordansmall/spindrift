package bwrap

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// The prompt-dir and skills-dir gates live only in BuildMountSpecs, never in
// an adapter file. The oci half lives in the oci package.
func TestAdapterRendersOnly_NoDuplicatedMountDecisions(t *testing.T) {
	runnertest.AssertNoMountDecisionMarkers(t, "bwrap.go")
}

// The same mount config reaches both backends by construction: both render the
// same BuildMountSpecs list. The oci half lives in the oci package.
func TestMountSpecs_RenderedByBwrap(t *testing.T) {
	fx := runnertest.NewMountFixture(t)
	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams: runner.MountParams{
			PromptDir:             fx.PromptDir,
			SkillsDir:             fx.SkillsDir,
			DriverSessionCacheDir: runnertest.DriverSessionCacheTarget,
		},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: fx.CacheDir, Sockets: []runner.SocketMount{{Source: fx.ProxySocket, Target: runner.RegistryProxySocketTarget}}}

	fx.AssertRendered(t, "bwrap", strings.Join(bwrap.buildArgs("/tmp/fake-etc", box, bwrap.snapshotDirFor(box)), " "), " ")
}

// ADR 0033, issue #1697: the Accumulation-repo and outbox mounts must reach
// the bwrap backend the same way the other mounts do.
func TestLocalCodeForgeMounts_RenderedByBwrap(t *testing.T) {
	repoDir := t.TempDir()
	outboxDir := t.TempDir()

	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   runner.MountParams{HostMediatedRemote: true, AccumulationRepoDir: repoDir},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box, bwrap.snapshotDirFor(box)), " ")

	if !strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") {
		t.Errorf("bwrap missing read-only /repo mount in args: %s", bwrapArgs)
	}
	if !strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap missing writable /outbox mount in args: %s", bwrapArgs)
	}
}

// Issue #3957: the writable /outbox mount reaches the bwrap backend whenever
// Box.OutboxDir is set, whatever MountParams says, with no /repo mount since
// HostMediatedRemote is unset here.
func TestOutboxMount_RenderedByBwrap(t *testing.T) {
	outboxDir := t.TempDir()

	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box, bwrap.snapshotDirFor(box)), " ")

	if !strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap missing writable /outbox mount in args: %s", bwrapArgs)
	}
	if strings.Contains(bwrapArgs, "/repo") {
		t.Errorf("unexpected /repo mount for HostMediatedRemote=false: bwrap=%s", bwrapArgs)
	}
}

// The Accumulation-repo dir is present on purpose, so this pins that the
// adapter does not leak the /repo mount through its own render path when
// HostMediatedRemote is false.
func TestLocalCodeForgeMounts_AbsentOnNonLocalBwrap(t *testing.T) {
	repoDir := t.TempDir()

	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   runner.MountParams{AccumulationRepoDir: repoDir},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}}

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box, bwrap.snapshotDirFor(box)), " ")

	if strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") {
		t.Errorf("bwrap must not mount /repo with HostMediatedRemote=false: %s", bwrapArgs)
	}
}
