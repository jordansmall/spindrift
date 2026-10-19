package oci

import (
	"os"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// The prompt-dir and skills-dir gates and their operator messages must live
// only in BuildMountSpecs, never duplicated in an adapter file. The
// driver-cache gate has no unique string to pin, since its rationale comment
// legitimately differs per adapter, so this pins the two mounts that carry
// operator messages.
func TestAdapterRendersOnly_NoDuplicatedMountDecisions(t *testing.T) {
	markers := []string{
		"SPINDRIFT_PROMPT_DIR set",
		"SPINDRIFT_SKILLS_DIR set",
	}
	data, err := os.ReadFile("oci.go")
	if err != nil {
		t.Fatalf("read oci.go: %v", err)
	}
	for _, marker := range markers {
		if strings.Contains(string(data), marker) {
			t.Errorf("oci.go contains mount-decision marker %q; operator messages must come from the shared BuildMountSpecs, not be duplicated in the adapter", marker)
		}
	}
}

// The same mount config reaches both backends by construction: both render the
// same BuildMountSpecs list. The bwrap half lives in the bwrap package.
func TestMountSpecs_RenderedByOCI(t *testing.T) {
	promptDir := t.TempDir()
	skillsDir := t.TempDir()
	cacheDir := t.TempDir()
	proxySocket := runnertest.NewTestSocket(t, "registry-proxy.sock")

	mp := runner.MountParams{
		PromptDir:             promptDir,
		SkillsDir:             skillsDir,
		DriverSessionCacheDir: "/home/agent/.claude/projects",
	}
	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: mp,
	}
	box := runner.Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir, Sockets: []runner.SocketMount{{Source: proxySocket, Target: runner.RegistryProxySocketTarget}}}

	ociArgs := strings.Join(oci.buildRunArgs(box), " ")

	for _, mount := range []struct{ source, target string }{
		{promptDir, "/agent/prompts"},
		{skillsDir, "/operator-skills"},
		{cacheDir, "/home/agent/.claude/projects"},
		{proxySocket, "/registry-proxy.sock"},
	} {
		if !strings.Contains(ociArgs, mount.source+":"+mount.target) {
			t.Errorf("OCI missing mount %s -> %s in args: %s", mount.source, mount.target, ociArgs)
		}
	}
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
