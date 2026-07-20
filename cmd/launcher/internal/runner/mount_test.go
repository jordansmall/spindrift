package runner

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestBuildMountSpecs_PromptDirMounted verifies that a valid PromptDir
// produces a MountSpec targeting /agent/prompts, read-only, with the
// SPINDRIFT_PROMPT_DIR operator message — computed once, independent of
// backend.
func TestBuildMountSpecs_PromptDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{PromptDir: dir}, Box{})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/agent/prompts" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a /agent/prompts spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("prompt-dir mount must be read-only")
	}
	want := "==> SPINDRIFT_PROMPT_DIR set; mounting " + dir + " over the baked prompt\n"
	if found.Message != want {
		t.Errorf("Message = %q, want %q", found.Message, want)
	}
}

// TestBuildMountSpecs_DriverCacheDirMountedWritable verifies that a declared
// DriverSessionCacheDir plus a present Box.DriverCacheDir produce a writable
// MountSpec with no operator message — computed once, independent of backend.
func TestBuildMountSpecs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"}, Box{DriverCacheDir: dir})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/home/agent/.claude/projects" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a driver-cache spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if found.ReadOnly {
		t.Errorf("driver-cache mount must be writable, not read-only")
	}
	if found.Message != "" {
		t.Errorf("driver-cache mount must be silent; got Message = %q", found.Message)
	}
}

// TestBuildMountSpecs_DriverSessionCacheDirUndeclared_NoMount verifies that a
// Driver declaring no session-state dir yields no cache spec even when a host
// DriverCacheDir is present — there is no in-box target to mount it over
// (issue #448).
func TestBuildMountSpecs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{}, Box{DriverCacheDir: dir})

	for _, s := range specs {
		if s.Source == dir {
			t.Errorf("unexpected driver-cache spec when DriverSessionCacheDir is undeclared: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_SkillsDirMounted verifies that a runtime SkillsDir
// override plus a declared DriverSkillsDir produce a read-only MountSpec with
// the SPINDRIFT_SKILLS_DIR operator message — computed once, independent of
// backend.
func TestBuildMountSpecs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{SkillsDir: dir, DriverSkillsDir: "/home/agent/.claude/skills"}, Box{})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/home/agent/.claude/skills" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a skills-dir spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("skills-dir mount must be read-only")
	}
	want := "==> SPINDRIFT_SKILLS_DIR set; mounting " + dir + " over /home/agent/.claude/skills\n"
	if found.Message != want {
		t.Errorf("Message = %q, want %q", found.Message, want)
	}
}

// TestBuildMountSpecs_SkillsDirUnset_NoMount verifies that omitting SkillsDir
// produces no skills spec.
func TestBuildMountSpecs_SkillsDirUnset_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{DriverSkillsDir: "/home/agent/.claude/skills"}, Box{})

	for _, s := range specs {
		if s.Target == "/home/agent/.claude/skills" {
			t.Errorf("unexpected skills-dir spec when SkillsDir is empty: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AccumulationRepoMountedReadOnly verifies
// that CODE_FORGE=local plus a present AccumulationRepoDir produces a
// read-only /repo MountSpec (ADR 0033: the code-in mount keeps the operator's
// Accumulation repo single-writer).
func TestBuildMountSpecs_LocalCodeForge_AccumulationRepoMountedReadOnly(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{CodeForge: "local", AccumulationRepoDir: dir}, Box{})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/repo" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a /repo spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("accumulation-repo mount must be read-only")
	}
}

// TestBuildMountSpecs_LocalCodeForge_OutboxMountedWritable verifies that
// CODE_FORGE=local plus a present Box.OutboxDir produces a writable /outbox
// MountSpec (ADR 0033: the Box emits its branch bundle through a throwaway
// writable outbox since it cannot push to the read-only /repo mount).
func TestBuildMountSpecs_LocalCodeForge_OutboxMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{CodeForge: "local"}, Box{OutboxDir: dir})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/outbox" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected an /outbox spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if found.ReadOnly {
		t.Errorf("outbox mount must be writable, not read-only")
	}
}

// TestBuildMountSpecs_NonLocalCodeForge_NoAccumulationOrOutboxMount verifies
// that a present AccumulationRepoDir/OutboxDir produce neither mount when
// CodeForge is not "local" — the two mounts are local-only (ADR 0033).
func TestBuildMountSpecs_NonLocalCodeForge_NoAccumulationOrOutboxMount(t *testing.T) {
	repoDir, outboxDir := t.TempDir(), t.TempDir()
	for _, cf := range []string{"github", "git", ""} {
		specs := buildMountSpecs(MountParams{CodeForge: cf, AccumulationRepoDir: repoDir}, Box{OutboxDir: outboxDir})
		for _, s := range specs {
			if s.Target == "/repo" || s.Target == "/outbox" {
				t.Errorf("CodeForge=%q: unexpected spec %+v", cf, s)
			}
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount
// verifies that an unset/nonexistent AccumulationRepoDir yields no /repo
// spec even under CODE_FORGE=local — both local mounts stay gated on
// candidateMount, not just the CodeForge check.
func TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{CodeForge: "local"}, Box{})

	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec when AccumulationRepoDir is unset: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount verifies that an
// unset Box.OutboxDir yields no /outbox spec even under CODE_FORGE=local.
func TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{CodeForge: "local"}, Box{})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec when OutboxDir is unset: %+v", specs)
		}
	}
}

// TestAdaptersRenderOnly_NoDuplicatedMountDecisions is the issue's grep pin:
// the prompt-dir/skills-dir mount gates and their operator messages must
// live only in buildMountSpecs, not be duplicated in either adapter file.
// The driver-cache gate has no unique string to pin (its rationale comment
// legitimately differs per adapter — OCI has no baked-skills fallback to
// explain, bwrap does), so this pins the two mounts with operator messages.
func TestAdaptersRenderOnly_NoDuplicatedMountDecisions(t *testing.T) {
	markers := []string{
		"SPINDRIFT_PROMPT_DIR set",
		"SPINDRIFT_SKILLS_DIR set",
	}
	for _, path := range []string{"oci.go", "bwrap.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, marker := range markers {
			if strings.Contains(string(data), marker) {
				t.Errorf("%s contains mount-decision marker %q; operator messages must come from the shared buildMountSpecs, not be duplicated in the adapter", path, marker)
			}
		}
	}
}

// TestMountSpecs_RenderedIdenticallyAcrossBackends is the issue's demoable
// criterion: the same mount config reaches both backends by construction.
// Add a spec, both adapters emit it correctly rendered; remove it, both
// drop it — because both render the same buildMountSpecs list.
func TestMountSpecs_RenderedIdenticallyAcrossBackends(t *testing.T) {
	promptDir := t.TempDir()
	skillsDir := t.TempDir()
	cacheDir := t.TempDir()

	oci := &ociAdapter{
		cli:                   "podman",
		image:                 "spindrift:test",
		promptDir:             promptDir,
		skillsDir:             skillsDir,
		driverSkillsDir:       "/home/agent/.claude/skills",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	bwrap := &bwrapAdapter{
		agentFiles:            t.TempDir(),
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		promptDir:             promptDir,
		skillsDir:             skillsDir,
		driverSkillsDir:       "/home/agent/.claude/skills",
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir}

	ociArgs := strings.Join(oci.buildRunArgs(box), " ")
	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	for _, mount := range []struct{ source, target string }{
		{promptDir, "/agent/prompts"},
		{skillsDir, "/home/agent/.claude/skills"},
		{cacheDir, "/home/agent/.claude/projects"},
	} {
		if !strings.Contains(ociArgs, mount.source+":"+mount.target) {
			t.Errorf("OCI missing mount %s -> %s in args: %s", mount.source, mount.target, ociArgs)
		}
		if !strings.Contains(bwrapArgs, mount.source+" "+mount.target) {
			t.Errorf("bwrap missing mount %s -> %s in args: %s", mount.source, mount.target, bwrapArgs)
		}
	}
}

// TestLocalCodeForgeMounts_RenderedIdenticallyAcrossBackends verifies the
// Accumulation-repo (read-only) and outbox (writable) mounts reach both
// backends the same way the other mounts do (ADR 0033, issue #1697): OCI
// renders /repo with :ro and /outbox without it; bwrap renders /repo with
// --ro-bind and /outbox with --bind.
func TestLocalCodeForgeMounts_RenderedIdenticallyAcrossBackends(t *testing.T) {
	repoDir := t.TempDir()
	outboxDir := t.TempDir()

	oci := &ociAdapter{
		cli:                 "podman",
		image:               "spindrift:test",
		codeForge:           "local",
		accumulationRepoDir: repoDir,
	}
	bwrap := &bwrapAdapter{
		agentFiles:          t.TempDir(),
		agentEnv:            "/fake/env",
		bakedPrefetch:       "echo ok",
		codeForge:           "local",
		accumulationRepoDir: repoDir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice := oci.buildRunArgs(box)
	ociArgs := strings.Join(ociArgSlice, " ")
	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	if !slices.Contains(ociArgSlice, repoDir+":/repo:ro") {
		t.Errorf("OCI missing read-only /repo mount in args: %s", ociArgs)
	}
	if !slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI missing writable /outbox mount in args: %s", ociArgs)
	}
	if !strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") {
		t.Errorf("bwrap missing read-only /repo mount in args: %s", bwrapArgs)
	}
	if !strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap missing writable /outbox mount in args: %s", bwrapArgs)
	}
}

// TestLocalCodeForgeMounts_AbsentOnNonLocalBackends verifies that neither
// backend renders the /repo or /outbox mount when CodeForge is not "local",
// even though both host dirs are present — the render layer must not leak
// the local-only mounts through either adapter's own path.
func TestLocalCodeForgeMounts_AbsentOnNonLocalBackends(t *testing.T) {
	repoDir := t.TempDir()
	outboxDir := t.TempDir()

	oci := &ociAdapter{
		cli:                 "podman",
		image:               "spindrift:test",
		accumulationRepoDir: repoDir,
	}
	bwrap := &bwrapAdapter{
		agentFiles:          t.TempDir(),
		agentEnv:            "/fake/env",
		bakedPrefetch:       "echo ok",
		accumulationRepoDir: repoDir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice := oci.buildRunArgs(box)
	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	if slices.Contains(ociArgSlice, repoDir+":/repo:ro") || slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI must not mount /repo or /outbox with CodeForge unset: %s", strings.Join(ociArgSlice, " "))
	}
	if strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") || strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap must not mount /repo or /outbox with CodeForge unset: %s", bwrapArgs)
	}
}
