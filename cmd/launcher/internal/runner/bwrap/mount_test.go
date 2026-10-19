package bwrap

import (
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// The prompt-dir and skills-dir gates and their operator messages must live
// only in BuildMountSpecs, never duplicated in an adapter file. The
// driver-cache gate has no unique string to pin, since its rationale comment
// legitimately differs per adapter, so this pins the two mounts that carry
// operator messages. The oci half lives in the oci package.
func TestAdapterRendersOnly_NoDuplicatedMountDecisions(t *testing.T) {
	markers := []string{
		"SPINDRIFT_PROMPT_DIR set",
		"SPINDRIFT_SKILLS_DIR set",
	}
	data, err := os.ReadFile("bwrap.go")
	if err != nil {
		t.Fatalf("read bwrap.go: %v", err)
	}
	for _, marker := range markers {
		if strings.Contains(string(data), marker) {
			t.Errorf("bwrap.go contains mount-decision marker %q; operator messages must come from the shared BuildMountSpecs, not be duplicated in the adapter", marker)
		}
	}
}

// The same mount config reaches both backends by construction: both render the
// same BuildMountSpecs list. The oci half lives in the oci package.
func TestMountSpecs_RenderedByBwrap(t *testing.T) {
	promptDir := t.TempDir()
	skillsDir := t.TempDir()
	cacheDir := t.TempDir()
	proxySocket := runnertest.NewTestSocket(t, "registry-proxy.sock")

	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams: runner.MountParams{
			PromptDir:             promptDir,
			SkillsDir:             skillsDir,
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir, Sockets: []runner.SocketMount{{Source: proxySocket, Target: runner.RegistryProxySocketTarget}}}

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	for _, mount := range []struct{ source, target string }{
		{promptDir, "/agent/prompts"},
		{skillsDir, "/operator-skills"},
		{cacheDir, "/home/agent/.claude/projects"},
		{proxySocket, "/registry-proxy.sock"},
	} {
		if !strings.Contains(bwrapArgs, mount.source+" "+mount.target) {
			t.Errorf("bwrap missing mount %s -> %s in args: %s", mount.source, mount.target, bwrapArgs)
		}
	}
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

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

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

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

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

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	if strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") {
		t.Errorf("bwrap must not mount /repo with HostMediatedRemote=false: %s", bwrapArgs)
	}
}
