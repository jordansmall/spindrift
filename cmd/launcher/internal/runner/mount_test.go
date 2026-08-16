package runner

import (
	"os"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/agentpaths"
)

// TestBuildMountSpecs_PromptDirMounted verifies that a valid PromptDir
// produces a MountSpec targeting agentpaths.PromptsDir, read-only, with the
// SPINDRIFT_PROMPT_DIR operator message — computed once, independent of
// backend. Asserts against the generated constant, not a hardcoded
// "/agent/prompts" literal — but both sides of that comparison read the
// same agentpaths.PromptsDir, so a rename in lib/agent-paths.nix can't make
// this assertion fail by itself; only `agent-paths-gen` (which regenerates
// agentpaths.PromptsDir from lib/agent-paths.nix) catches that drift. What
// this test does guard is mount.go's own wiring: that buildMountSpecs
// actually targets the generated constant, not a stray literal that could
// silently diverge from it (issue #2531).
func TestBuildMountSpecs_PromptDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{PromptDir: dir}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == agentpaths.PromptsDir {
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
	specs, err := buildMountSpecs(MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"}, Box{DriverCacheDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

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
	specs, err := buildMountSpecs(MountParams{}, Box{DriverCacheDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Source == dir {
			t.Errorf("unexpected driver-cache spec when DriverSessionCacheDir is undeclared: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_SkillsDirMounted verifies that a runtime SkillsDir
// override produces a read-only MountSpec at the fixed operatorSkillsDir
// target with the SPINDRIFT_SKILLS_DIR operator message — computed once,
// independent of backend.
func TestBuildMountSpecs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{SkillsDir: dir}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/operator-skills" {
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
	want := "==> SPINDRIFT_SKILLS_DIR set; mounting " + dir + " over /operator-skills\n"
	if found.Message != want {
		t.Errorf("Message = %q, want %q", found.Message, want)
	}
}

// TestBuildMountSpecs_SkillsDirUnset_NoMount verifies that omitting SkillsDir
// produces no skills spec.
func TestBuildMountSpecs_SkillsDirUnset_NoMount(t *testing.T) {
	specs, err := buildMountSpecs(MountParams{}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/operator-skills" {
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
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: true, AccumulationRepoDir: dir}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

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
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{OutboxDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

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
// HostMediatedRemote is false and BoxForgeAndIssueAccess isn't "read-only" —
// the two mounts are local-only (ADR 0033), regardless of OutboxRelayCapable.
func TestBuildMountSpecs_NonLocalCodeForge_NoAccumulationOrOutboxMount(t *testing.T) {
	repoDir, outboxDir := t.TempDir(), t.TempDir()
	for _, outboxRelayCapable := range []bool{true, false} {
		specs, err := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: outboxRelayCapable, AccumulationRepoDir: repoDir}, Box{OutboxDir: outboxDir})
		if err != nil {
			t.Fatalf("buildMountSpecs: %v", err)
		}
		for _, s := range specs {
			if s.Target == "/repo" || s.Target == "/outbox" {
				t.Errorf("OutboxRelayCapable=%v: unexpected spec %+v", outboxRelayCapable, s)
			}
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount
// verifies that an unset/nonexistent AccumulationRepoDir yields no /repo
// spec even under HostMediatedRemote — both local mounts stay gated on
// candidateMount, not just the HostMediatedRemote check.
func TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount(t *testing.T) {
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec when AccumulationRepoDir is unset: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount verifies that an
// unset Box.OutboxDir yields no /outbox spec even under HostMediatedRemote.
func TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount(t *testing.T) {
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec when OutboxDir is unset: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_GithubReadOnly_OutboxMountedWritable verifies that
// CODE_FORGE=github plus BoxForgeAndIssueAccess="read-only" plus a present
// Box.OutboxDir produces a writable /outbox mount, exactly like
// CODE_FORGE=local (issue #1918): the Box writes seam.bundle there instead
// of pushing, since its token can't push under read-only. It gets no /repo
// mount, though -- unlike local, github clones over the network in-box, not
// from a locally mounted Accumulation repo.
func TestBuildMountSpecs_GithubReadOnly_OutboxMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/outbox" {
			found = &specs[i]
		}
		if specs[i].Target == "/repo" {
			t.Errorf("unexpected /repo spec for CODE_FORGE=github: %+v", specs[i])
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

// TestBuildMountSpecs_GithubReadWrite_NoOutboxMount verifies that
// CODE_FORGE=github under the default read-write access produces no /outbox
// mount even with a present Box.OutboxDir -- read-write pushes in-box and
// never consults an outbox.
func TestBuildMountSpecs_GithubReadWrite_NoOutboxMount(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-write"}, Box{OutboxDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for CODE_FORGE=github read-write: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_ForgejoReadOnly_NoOutboxMount pins a pre-existing
// asymmetry (issue #2267): CODE_FORGE=forgejo does NOT get the outbox-relay
// treatment under BoxForgeAndIssueAccess="read-only", unlike github, even
// though forgejo also has its own read-only CodeForge constructor
// (NewReadOnlyForgejoCodeForge). This mirrors github's own OutboxRelayCapable
// field being false for forgejo in the backendRow registry
// (cmd/launcher/backend.go) -- confirmed by running this exact scenario
// (via the string-based CodeForge=="github" check that predates #2267)
// against the pre-migration code, where it also passed, proving this is a
// behavior pin and not new behavior.
func TestBuildMountSpecs_ForgejoReadOnly_NoOutboxMount(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: false, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for CODE_FORGE=forgejo read-only: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_IssuesDirMounted verifies that ISSUE_TRACKER=local plus
// a present LocalIssuesDir produce a read-only MountSpec targeting the
// top-level /issues path (issue #1691, ADR 0032) — computed once, independent
// of backend. Silent: unlike the operator-triggered overrides above, this
// mount is the tracker's normal read path, not a diagnostic override.
func TestBuildMountSpecs_IssuesDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{HostMediatedIssueTracker: true, LocalIssuesDir: dir}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/issues" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected an /issues spec in %+v", specs)
	}
	if found.Source != dir {
		t.Errorf("Source = %q, want %q", found.Source, dir)
	}
	if !found.ReadOnly {
		t.Errorf("issues-dir mount must be read-only")
	}
	if found.Message != "" {
		t.Errorf("issues-dir mount must be silent; got Message = %q", found.Message)
	}
}

// TestBuildMountSpecs_IssuesDirNonLocalTracker_NoMount verifies that a
// non-local ISSUE_TRACKER never mounts /issues, even when LocalIssuesDir
// resolves to a real directory — the mount is local-only (ADR 0032).
func TestBuildMountSpecs_IssuesDirNonLocalTracker_NoMount(t *testing.T) {
	dir := t.TempDir()
	specs, err := buildMountSpecs(MountParams{HostMediatedIssueTracker: false, LocalIssuesDir: dir}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/issues" {
			t.Errorf("unexpected /issues spec for a non-local tracker: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_IssuesDirMissing_NoMount verifies that ISSUE_TRACKER=local
// with an absent LocalIssuesDir yields no mount rather than an error — a
// misconfigured or not-yet-created issues dir fails gracefully (ADR 0032).
func TestBuildMountSpecs_IssuesDirMissing_NoMount(t *testing.T) {
	specs, err := buildMountSpecs(MountParams{HostMediatedIssueTracker: true, LocalIssuesDir: "/nonexistent/does-not-exist"}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/issues" {
			t.Errorf("unexpected /issues spec for a missing dir: %+v", specs)
		}
	}
}

// TestCandidateFileMount_RegularFile verifies candidateFileMount mounts a
// source that stats as a regular file, unlike candidateMount which requires
// a directory.
func TestCandidateFileMount_RegularFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/snapshot.md"
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	spec, ok := candidateFileMount(path, "/issue-snapshot.md", true)
	if !ok {
		t.Fatalf("candidateFileMount(%q): got ok=false, want true", path)
	}
	if spec.Source != path || spec.Target != "/issue-snapshot.md" || !spec.ReadOnly {
		t.Errorf("candidateFileMount: got %+v", spec)
	}
}

// TestCandidateFileMount_RejectsDirectory verifies candidateFileMount
// refuses a source that stats as a directory -- the inverse of
// candidateMount's own directory-only requirement.
func TestCandidateFileMount_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()

	if _, ok := candidateFileMount(dir, "/issue-snapshot.md", true); ok {
		t.Errorf("candidateFileMount(%q): got ok=true for a directory, want false", dir)
	}
}

// TestCandidateFileMount_MissingSource verifies candidateFileMount yields no
// mount for a source that does not exist.
func TestCandidateFileMount_MissingSource(t *testing.T) {
	if _, ok := candidateFileMount("/nonexistent/does-not-exist.md", "/issue-snapshot.md", true); ok {
		t.Error("candidateFileMount: got ok=true for a missing source, want false")
	}
}

// TestCandidateFileMount_EmptySourceOrTarget verifies candidateFileMount
// requires both source and target set, mirroring candidateMount.
func TestCandidateFileMount_EmptySourceOrTarget(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/snapshot.md"
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, ok := candidateFileMount("", "/issue-snapshot.md", true); ok {
		t.Error("candidateFileMount: got ok=true for an empty source, want false")
	}
	if _, ok := candidateFileMount(path, "", true); ok {
		t.Error("candidateFileMount: got ok=true for an empty target, want false")
	}
}

// TestBuildMountSpecs_IssueSnapshotMounted verifies buildMountSpecs mounts
// box.IssueSnapshotPath read-only at /issue-snapshot.md when it points at a
// real file, silently (no operator Message) like the /issues mount.
func TestBuildMountSpecs_IssueSnapshotMounted(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/issue-42.md"
	if err := os.WriteFile(path, []byte("frozen text"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	specs, err := buildMountSpecs(MountParams{}, Box{IssueSnapshotPath: path})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/issue-snapshot.md" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected an /issue-snapshot.md spec in %+v", specs)
	}
	if found.Source != path {
		t.Errorf("Source = %q, want %q", found.Source, path)
	}
	if !found.ReadOnly {
		t.Error("issue-snapshot mount must be read-only")
	}
	if found.Message != "" {
		t.Errorf("issue-snapshot mount must be silent; got Message = %q", found.Message)
	}
}

// TestBuildMountSpecs_IssueSnapshotPathEmpty_NoMount verifies buildMountSpecs
// produces no /issue-snapshot.md mount when box.IssueSnapshotPath is empty.
func TestBuildMountSpecs_IssueSnapshotPathEmpty_NoMount(t *testing.T) {
	specs, err := buildMountSpecs(MountParams{}, Box{})
	if err != nil {
		t.Fatalf("buildMountSpecs: %v", err)
	}

	for _, s := range specs {
		if s.Target == "/issue-snapshot.md" {
			t.Errorf("unexpected /issue-snapshot.md spec for an empty IssueSnapshotPath: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_IssueSnapshotStatFails_Error verifies buildMountSpecs
// returns a descriptive error, rather than silently dropping the mount, when
// box.IssueSnapshotPath is non-empty but does not stat -- e.g. the frozen
// issue-read snapshot Box.Run's writeIssueSnapshot step should have written
// was removed or made unreadable in the window before this mount is
// computed. Since #2547 the snapshot is the box's sole source of issue text
// for implement/review/fix passes, so a silently-empty mount here would
// otherwise leave the box starting with no issue text and only an opaque
// "no such file" from `cat /issue-snapshot.md` inside the box, with no
// diagnostic in the launcher's own output pointing at why. An empty
// IssueSnapshotPath (research dispatches, pre-#2547 Box construction) must
// stay silent -- see TestBuildMountSpecs_IssueSnapshotPathEmpty_NoMount.
func TestBuildMountSpecs_IssueSnapshotStatFails_Error(t *testing.T) {
	path := "/nonexistent/does-not-exist/issue-42.md"

	specs, err := buildMountSpecs(MountParams{}, Box{IssueSnapshotPath: path})
	if err == nil {
		t.Fatalf("buildMountSpecs: got nil error and specs %+v, want a descriptive error", specs)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("buildMountSpecs error = %q, want it to mention the snapshot path %q", err.Error(), path)
	}
}

// TestBuildMountSpecs_IssueSnapshotPathIsDirectory_Error verifies
// buildMountSpecs returns a descriptive error, rather than silently dropping
// the mount, when box.IssueSnapshotPath stats fine but is a directory, not a
// regular file. Before this fix candidateFileMount's fail-open contract (a
// silently-omitted mount whenever the source isn't a plain file) applied
// here too, the exact hole issue #2547's "sole source of issue text" design
// depends on closing.
func TestBuildMountSpecs_IssueSnapshotPathIsDirectory_Error(t *testing.T) {
	dir := t.TempDir()

	specs, err := buildMountSpecs(MountParams{}, Box{IssueSnapshotPath: dir})
	if err == nil {
		t.Fatalf("buildMountSpecs: got nil error and specs %+v, want a descriptive error", specs)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("buildMountSpecs error = %q, want it to mention the snapshot path %q", err.Error(), dir)
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
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	bwrap := &bwrapAdapter{
		agentFiles:            t.TempDir(),
		agentEnv:              "/fake/env",
		bakedPrefetch:         "echo ok",
		promptDir:             promptDir,
		skillsDir:             skillsDir,
		driverSessionCacheDir: "/home/agent/.claude/projects",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir}

	ociArgSlice, err := oci.buildRunArgs(box)
	if err != nil {
		t.Fatalf("oci.buildRunArgs: %v", err)
	}
	ociArgs := strings.Join(ociArgSlice, " ")
	bwrapArgSlice, err := bwrap.buildArgs("/tmp/fake-etc", box)
	if err != nil {
		t.Fatalf("bwrap.buildArgs: %v", err)
	}
	bwrapArgs := strings.Join(bwrapArgSlice, " ")

	for _, mount := range []struct{ source, target string }{
		{promptDir, "/agent/prompts"},
		{skillsDir, "/operator-skills"},
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
		hostMediatedRemote:  true,
		accumulationRepoDir: repoDir,
	}
	bwrap := &bwrapAdapter{
		agentFiles:          t.TempDir(),
		agentEnv:            "/fake/env",
		bakedPrefetch:       "echo ok",
		hostMediatedRemote:  true,
		accumulationRepoDir: repoDir,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice, err := oci.buildRunArgs(box)
	if err != nil {
		t.Fatalf("oci.buildRunArgs: %v", err)
	}
	ociArgs := strings.Join(ociArgSlice, " ")
	bwrapArgSlice, err := bwrap.buildArgs("/tmp/fake-etc", box)
	if err != nil {
		t.Fatalf("bwrap.buildArgs: %v", err)
	}
	bwrapArgs := strings.Join(bwrapArgSlice, " ")

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

// TestGithubReadOnlyOutboxMount_RenderedIdenticallyAcrossBackends verifies
// the writable /outbox mount reaches both backends under CODE_FORGE=github
// plus BoxForgeAndIssueAccess="read-only" (issue #1918), the same way it
// does for CODE_FORGE=local — but with no /repo mount, since github clones
// over the network in-box rather than from a locally mounted Accumulation
// repo.
func TestGithubReadOnlyOutboxMount_RenderedIdenticallyAcrossBackends(t *testing.T) {
	outboxDir := t.TempDir()

	oci := &ociAdapter{
		cli:                    "podman",
		image:                  "spindrift:test",
		outboxRelayCapable:     true,
		boxForgeAndIssueAccess: "read-only",
	}
	bwrap := &bwrapAdapter{
		agentFiles:             t.TempDir(),
		agentEnv:               "/fake/env",
		bakedPrefetch:          "echo ok",
		outboxRelayCapable:     true,
		boxForgeAndIssueAccess: "read-only",
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice, err := oci.buildRunArgs(box)
	if err != nil {
		t.Fatalf("oci.buildRunArgs: %v", err)
	}
	ociArgs := strings.Join(ociArgSlice, " ")
	bwrapArgSlice, err := bwrap.buildArgs("/tmp/fake-etc", box)
	if err != nil {
		t.Fatalf("bwrap.buildArgs: %v", err)
	}
	bwrapArgs := strings.Join(bwrapArgSlice, " ")

	if !slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI missing writable /outbox mount in args: %s", ociArgs)
	}
	if !strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap missing writable /outbox mount in args: %s", bwrapArgs)
	}
	if strings.Contains(ociArgs, "/repo") || strings.Contains(bwrapArgs, "/repo") {
		t.Errorf("unexpected /repo mount for CODE_FORGE=github: oci=%s bwrap=%s", ociArgs, bwrapArgs)
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

	ociArgSlice, err := oci.buildRunArgs(box)
	if err != nil {
		t.Fatalf("oci.buildRunArgs: %v", err)
	}
	bwrapArgSlice, err := bwrap.buildArgs("/tmp/fake-etc", box)
	if err != nil {
		t.Fatalf("bwrap.buildArgs: %v", err)
	}
	bwrapArgs := strings.Join(bwrapArgSlice, " ")

	if slices.Contains(ociArgSlice, repoDir+":/repo:ro") || slices.Contains(ociArgSlice, outboxDir+":/outbox") {
		t.Errorf("OCI must not mount /repo or /outbox with CodeForge unset: %s", strings.Join(ociArgSlice, " "))
	}
	if strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") || strings.Contains(bwrapArgs, "--bind "+outboxDir+" /outbox") {
		t.Errorf("bwrap must not mount /repo or /outbox with CodeForge unset: %s", bwrapArgs)
	}
}
