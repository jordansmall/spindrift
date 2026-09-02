package runner

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/agentpaths"
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/registrymanifest"
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
	specs := buildMountSpecs(MountParams{PromptDir: dir}, Box{})

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
// override produces a read-only MountSpec at the fixed operatorSkillsDir
// target with the SPINDRIFT_SKILLS_DIR operator message — computed once,
// independent of backend.
func TestBuildMountSpecs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{SkillsDir: dir}, Box{})

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
	specs := buildMountSpecs(MountParams{}, Box{})

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
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true, AccumulationRepoDir: dir}, Box{})

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
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{OutboxDir: dir})

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
		specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: outboxRelayCapable, AccumulationRepoDir: repoDir}, Box{OutboxDir: outboxDir})
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
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})

	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec when AccumulationRepoDir is unset: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount verifies that an
// unset Box.OutboxDir yields no /outbox spec even under HostMediatedRemote.
func TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})

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
	specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})

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
	specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-write"}, Box{OutboxDir: dir})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for CODE_FORGE=github read-write: %+v", specs)
		}
	}
}

// TestBuildMountSpecs_ForgejoReadOnly_OutboxMountedWritable verifies that
// CODE_FORGE=forgejo plus BoxForgeAndIssueAccess="read-only" plus a present
// Box.OutboxDir produces a writable /outbox mount, now that forgejo's
// backendRow carries OutboxRelayCapable: true (issue #2927) -- forgejo gets
// the same outbox-relay treatment as github (issue #1918): the Box writes
// seam.bundle there instead of pushing, since its token can't push under
// read-only. It gets no /repo mount, though -- like github, forgejo clones
// over the network in-box, not from a locally mounted Accumulation repo.
// Builds MountParams from backend.Forgejo's real OutboxRelayCapable field
// rather than a hand-built literal, so the test exercises the actual
// registry row.
func TestBuildMountSpecs_ForgejoReadOnly_OutboxMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{HostMediatedRemote: backend.Forgejo.HostMediatedRemote, OutboxRelayCapable: backend.Forgejo.OutboxRelayCapable, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/outbox" {
			found = &specs[i]
		}
		if specs[i].Target == "/repo" {
			t.Errorf("unexpected /repo spec for CODE_FORGE=forgejo: %+v", specs[i])
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

// TestBuildMountSpecs_OutboxIncapableReadOnly_NoOutboxMount verifies that a
// backend with OutboxRelayCapable: false produces no /outbox mount even
// under BoxForgeAndIssueAccess="read-only" with a present Box.OutboxDir --
// the outbox-relay mount is gated on the backend's capability, not just the
// access mode. No backendRow valid as a CODE_FORGE under read-only (github,
// local, forgejo) leaves both OutboxRelayCapable and HostMediatedRemote
// false today, so this is a hypothetical-backend-shape case rather than a
// pin on any specific backend's real behavior.
func TestBuildMountSpecs_OutboxIncapableReadOnly_NoOutboxMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: false, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for OutboxRelayCapable=false read-only: %+v", specs)
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
	specs := buildMountSpecs(MountParams{HostMediatedIssueTracker: true, LocalIssuesDir: dir}, Box{})

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
	specs := buildMountSpecs(MountParams{HostMediatedIssueTracker: false, LocalIssuesDir: dir}, Box{})

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
	specs := buildMountSpecs(MountParams{HostMediatedIssueTracker: true, LocalIssuesDir: "/nonexistent/does-not-exist"}, Box{})

	for _, s := range specs {
		if s.Target == "/issues" {
			t.Errorf("unexpected /issues spec for a missing dir: %+v", specs)
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
	proxySocket := newTestSocket(t, "registry-proxy.sock")

	mp := MountParams{
		PromptDir:             promptDir,
		SkillsDir:             skillsDir,
		DriverSessionCacheDir: "/home/agent/.claude/projects",
	}
	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: mp,
	}
	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   mp,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir, RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(proxySocket)}}

	ociArgs := strings.Join(oci.buildRunArgs(box), " ")
	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	for _, mount := range []struct{ source, target string }{
		{promptDir, "/agent/prompts"},
		{skillsDir, "/operator-skills"},
		{cacheDir, "/home/agent/.claude/projects"},
		{proxySocket, "/registry-proxy.sock"},
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

	mp := MountParams{HostMediatedRemote: true, AccumulationRepoDir: repoDir}
	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: mp,
	}
	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   mp,
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

// TestGithubReadOnlyOutboxMount_RenderedIdenticallyAcrossBackends verifies
// the writable /outbox mount reaches both backends under CODE_FORGE=github
// plus BoxForgeAndIssueAccess="read-only" (issue #1918), the same way it
// does for CODE_FORGE=local — but with no /repo mount, since github clones
// over the network in-box rather than from a locally mounted Accumulation
// repo.
func TestGithubReadOnlyOutboxMount_RenderedIdenticallyAcrossBackends(t *testing.T) {
	outboxDir := t.TempDir()

	mp := MountParams{OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-only"}
	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: mp,
	}
	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   mp,
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

	ociArgSlice := oci.buildRunArgs(box)
	ociArgs := strings.Join(ociArgSlice, " ")
	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

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

	mp := MountParams{AccumulationRepoDir: repoDir}
	oci := &ociAdapter{
		cli:         "podman",
		image:       "spindrift:test",
		mountParams: mp,
	}
	bwrap := &bwrapAdapter{
		agentFiles:    t.TempDir(),
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
		mountParams:   mp,
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

// newTestSocket creates a real unix domain socket file named name and
// returns its path, closing the listener on test cleanup. It deliberately
// does not nest under t.TempDir(): that helper's directory embeds the full
// test (and subtest) name, and under a nix build sandbox the build root
// itself is already a long path -- concatenating the two can exceed
// AF_UNIX's ~108-byte sun_path limit (net.Listen then fails with "bind:
// invalid argument"). A short os.MkdirTemp prefix keeps the whole path well
// under that limit regardless of the test name's length or the sandbox's
// own root path.
func newTestSocket(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen(unix, %s): %v", path, err)
	}
	t.Cleanup(func() { l.Close() })
	return path
}

// TestCandidateSocketMount_RealSocket verifies that a real unix socket file
// on disk produces a MountSpec, unlike candidateMount, which rejects it.
func TestCandidateSocketMount_RealSocket(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")

	spec, ok := candidateSocketMount(sock, "/registry-proxy.sock")
	if !ok {
		t.Fatalf("expected a mount for socket %s", sock)
	}
	if spec.Source != sock {
		t.Errorf("Source = %q, want %q", spec.Source, sock)
	}
	if spec.Target != "/registry-proxy.sock" {
		t.Errorf("Target = %q, want /registry-proxy.sock", spec.Target)
	}
	if spec.ReadOnly {
		t.Errorf("socket mount must be writable, not read-only")
	}
}

// TestCandidateSocketMount_RegularFile_NoMount verifies that a plain regular
// file (not a socket) at the source path yields no mount.
func TestCandidateSocketMount_RegularFile_NoMount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, ok := candidateSocketMount(path, "/registry-proxy.sock"); ok {
		t.Errorf("expected no mount for a regular file at %s", path)
	}
}

// TestCandidateSocketMount_Directory_NoMount verifies that a directory at the
// source path yields no mount.
func TestCandidateSocketMount_Directory_NoMount(t *testing.T) {
	dir := t.TempDir()

	if _, ok := candidateSocketMount(dir, "/registry-proxy.sock"); ok {
		t.Errorf("expected no mount for a directory at %s", dir)
	}
}

// TestCandidateSocketMount_EmptyPath_NoMount verifies that an empty source
// path yields no mount.
func TestCandidateSocketMount_EmptyPath_NoMount(t *testing.T) {
	if _, ok := candidateSocketMount("", "/registry-proxy.sock"); ok {
		t.Errorf("expected no mount for an empty source path")
	}
}

// TestBuildMountSpecs_RegistryProxySocketMounted verifies that a set unix
// RegistryProxy.Endpoint produces a writable MountSpec at the fixed in-box
// target /registry-proxy.sock (ADR 0044) — computed once, independent of
// backend.
func TestBuildMountSpecs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	specs := buildMountSpecs(MountParams{}, Box{RegistryProxy: RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(sock)}})

	var found *MountSpec
	for i := range specs {
		if specs[i].Target == "/registry-proxy.sock" {
			found = &specs[i]
		}
	}
	if found == nil {
		t.Fatalf("expected a /registry-proxy.sock spec in %+v", specs)
	}
	if found.Source != sock {
		t.Errorf("Source = %q, want %q", found.Source, sock)
	}
	if found.ReadOnly {
		t.Errorf("registry-proxy socket mount must be writable, not read-only")
	}
}

// TestBuildMountSpecs_RegistryProxySocketUnset_NoMount verifies that a zero
// RegistryProxy.Endpoint produces no /registry-proxy.sock spec — the
// registry proxy feature is off for this Box.
func TestBuildMountSpecs_RegistryProxySocketUnset_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/registry-proxy.sock" {
			t.Errorf("unexpected /registry-proxy.sock spec when RegistryProxy.Endpoint is unset: %+v", specs)
		}
	}
}
