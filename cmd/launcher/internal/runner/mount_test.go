package runner

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/agentpaths"
	"spindrift.dev/launcher/internal/backend"
)

// Both sides of the Target comparison read agentpaths.PromptsDir, so a rename
// in lib/agent-paths.nix cannot fail this assertion on its own; only
// agent-paths-gen catches that drift. What this pins is that buildMountSpecs
// targets the generated constant and not a stray literal (issue #2531).
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

// A Driver that declares no session-state dir gives the host cache no in-box
// target to mount over, so a present DriverCacheDir yields no spec (issue
// #448).
func TestBuildMountSpecs_DriverSessionCacheDirUndeclared_NoMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{}, Box{DriverCacheDir: dir})

	for _, s := range specs {
		if s.Source == dir {
			t.Errorf("unexpected driver-cache spec when DriverSessionCacheDir is undeclared: %+v", specs)
		}
	}
}

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

func TestBuildMountSpecs_SkillsDirUnset_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/operator-skills" {
			t.Errorf("unexpected skills-dir spec when SkillsDir is empty: %+v", specs)
		}
	}
}

// ADR 0033: the code-in mount is read-only so the operator's Accumulation repo
// stays single-writer.
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

// ADR 0033: the Box emits its branch bundle through a throwaway writable outbox
// because it cannot push to the read-only /repo mount.
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

// Both mounts are local-only (ADR 0033), so neither appears when
// HostMediatedRemote is false and the access mode is not read-only, whatever
// OutboxRelayCapable says.
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

// Both local mounts stay gated on candidateMount, not just on the
// HostMediatedRemote check.
func TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})

	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec when AccumulationRepoDir is unset: %+v", specs)
		}
	}
}

func TestBuildMountSpecs_LocalCodeForge_AbsentOutboxDir_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec when OutboxDir is unset: %+v", specs)
		}
	}
}

// Issue #1918: under read-only the Box's token cannot push, so it writes
// seam.bundle to /outbox exactly as CODE_FORGE=local does. It gets no /repo
// mount, because github clones over the network in-box rather than from a
// locally mounted Accumulation repo.
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

// Read-write pushes in-box and never consults an outbox, so a present
// Box.OutboxDir still produces no mount.
func TestBuildMountSpecs_GithubReadWrite_NoOutboxMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: true, BoxForgeAndIssueAccess: "read-write"}, Box{OutboxDir: dir})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for CODE_FORGE=github read-write: %+v", specs)
		}
	}
}

// Forgejo's backendRow carries OutboxRelayCapable: true (issue #2927), so it
// gets the same read-only outbox relay as github (issue #1918) and no /repo
// mount, since it also clones over the network in-box. MountParams comes from
// backend.Forgejo's real fields rather than a hand-built literal so the test
// exercises the actual registry row.
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

// The outbox-relay mount is gated on the backend's capability, not just the
// access mode. No backendRow valid as a CODE_FORGE under read-only (github,
// local, forgejo) leaves both OutboxRelayCapable and HostMediatedRemote false
// today, so this covers a hypothetical backend shape rather than pinning any
// real backend's behavior.
func TestBuildMountSpecs_OutboxIncapableReadOnly_NoOutboxMount(t *testing.T) {
	dir := t.TempDir()
	specs := buildMountSpecs(MountParams{HostMediatedRemote: false, OutboxRelayCapable: false, BoxForgeAndIssueAccess: "read-only"}, Box{OutboxDir: dir})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec for OutboxRelayCapable=false read-only: %+v", specs)
		}
	}
}

// The discriminating, red-first pin for issue #3471: on origin/main this fails
// on both HostMediatedIssueTracker and LocalIssuesDir; here it passes because
// neither field exists on the struct at all.
func TestMountParams_TakesNoIssuesDirInput(t *testing.T) {
	typ := reflect.TypeOf(MountParams{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		// "Issues" and "Tracker" cover the two field names origin/main carried
		// and catch a re-add named e.g. IssuesSource. Plain "Issue" is not
		// usable here: BoxForgeAndIssueAccess legitimately contains it. A
		// re-add named neither is past this heuristic's reach, which is why
		// TestBuildMountSpecs_NeverProducesIssuesMount guards the output too.
		if strings.Contains(name, "Issues") || strings.Contains(name, "Tracker") {
			t.Errorf("MountParams.%s: field name suggests an issues-dir or tracker-gating mount input; the /issues mount was removed by issue #3471", name)
		}
	}
}

// This guard passes on origin/main too, since a zero-tracker MountParams
// produced no /issues mount there either; TestMountParams_TakesNoIssuesDirInput
// is the discriminating pin. Its job is to keep that absence non-vacuous by
// asserting every other expected mount still comes out of a fully populated
// MountParams and Box.
func TestBuildMountSpecs_NeverProducesIssuesMount(t *testing.T) {
	promptDir := t.TempDir()
	skillsDir := t.TempDir()
	cacheDir := t.TempDir()
	repoDir := t.TempDir()
	outboxDir := t.TempDir()

	specs := buildMountSpecs(MountParams{
		PromptDir:             promptDir,
		SkillsDir:             skillsDir,
		DriverSessionCacheDir: "/home/agent/.claude/projects",
		HostMediatedRemote:    true,
		AccumulationRepoDir:   repoDir,
	}, Box{DriverCacheDir: cacheDir, OutboxDir: outboxDir})

	wantTargets := []string{
		agentpaths.PromptsDir,
		"/operator-skills",
		"/home/agent/.claude/projects",
		"/repo",
		"/outbox",
	}
	for _, want := range wantTargets {
		found := false
		for _, s := range specs {
			if s.Target == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a %s spec in %+v", want, specs)
		}
	}

	for _, s := range specs {
		if s.Target == "/issues" {
			t.Errorf("unexpected /issues spec: %+v", specs)
		}
	}
}

// The prompt-dir and skills-dir gates and their operator messages must live
// only in buildMountSpecs, never duplicated in an adapter file. The
// driver-cache gate has no unique string to pin, since its rationale comment
// legitimately differs per adapter, so this pins the two mounts that carry
// operator messages.
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

// The same mount config reaches both backends by construction: add or remove a
// spec and both adapters follow, because both render the same buildMountSpecs
// list.
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
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir, Sockets: []SocketMount{{Source: proxySocket, Target: RegistryProxySocketTarget}}}

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

// ADR 0033, issue #1697: the Accumulation-repo and outbox mounts must reach
// both backends the same way the other mounts do.
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

// Issue #1918: the writable /outbox mount reaches both backends under
// read-only github the same way it does for local, but with no /repo mount,
// since github clones over the network in-box rather than from a locally
// mounted Accumulation repo.
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

// Both host dirs are present on purpose, so this pins that neither adapter
// leaks the local-only mounts through its own render path when CodeForge is
// not "local".
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

// newTestSocket deliberately avoids t.TempDir(): that helper's directory embeds
// the full test name, and under a nix build sandbox the build root is already
// long, so the two together can exceed AF_UNIX's ~108-byte sun_path limit
// (net.Listen then fails with "bind: invalid argument"). A short os.MkdirTemp
// prefix keeps the path under the limit whatever the test name.
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

// candidateMount rejects a unix socket; candidateSocketMount accepts one.
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

func TestCandidateSocketMount_Directory_NoMount(t *testing.T) {
	dir := t.TempDir()

	if _, ok := candidateSocketMount(dir, "/registry-proxy.sock"); ok {
		t.Errorf("expected no mount for a directory at %s", dir)
	}
}

func TestCandidateSocketMount_EmptyPath_NoMount(t *testing.T) {
	if _, ok := candidateSocketMount("", "/registry-proxy.sock"); ok {
		t.Errorf("expected no mount for an empty source path")
	}
}

// ADR 0044 fixes the in-box target at /registry-proxy.sock.
func TestBuildMountSpecs_RegistryProxySocketMounted(t *testing.T) {
	sock := newTestSocket(t, "registry-proxy.sock")
	specs := buildMountSpecs(MountParams{}, Box{Sockets: []SocketMount{{Source: sock, Target: RegistryProxySocketTarget}}})

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

func TestBuildMountSpecs_RegistryProxySocketUnset_NoMount(t *testing.T) {
	specs := buildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/registry-proxy.sock" {
			t.Errorf("unexpected /registry-proxy.sock spec when Box.Sockets is unset: %+v", specs)
		}
	}
}

// issue #3723: buildMountSpecs mounts every entry of Box.Sockets, in order,
// each at its own fixed target — not just a single hardcoded socket.
func TestBuildMountSpecs_MultipleSockets_OneWritableSpecEach(t *testing.T) {
	sockA := newTestSocket(t, "registry-proxy.sock")
	sockB := newTestSocket(t, "signal.sock")
	box := Box{Sockets: []SocketMount{
		{Source: sockA, Target: RegistryProxySocketTarget},
		{Source: sockB, Target: "/signal.sock"},
	}}

	specs := buildMountSpecs(MountParams{}, box)

	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d: %+v", len(specs), specs)
	}
	if specs[0].Source != sockA || specs[0].Target != RegistryProxySocketTarget {
		t.Errorf("specs[0] = %+v, want Source=%q Target=%q", specs[0], sockA, RegistryProxySocketTarget)
	}
	if specs[1].Source != sockB || specs[1].Target != "/signal.sock" {
		t.Errorf("specs[1] = %+v, want Source=%q Target=%q", specs[1], sockB, "/signal.sock")
	}
	for _, s := range specs {
		if s.ReadOnly {
			t.Errorf("socket mount %+v must be writable, not read-only", s)
		}
	}
}

// A non-existent or non-socket source is dropped, but a good entry
// elsewhere in the list still mounts (issue #3723).
func TestBuildMountSpecs_MultipleSockets_SkipsBadEntryKeepsGoodOne(t *testing.T) {
	sockA := newTestSocket(t, "registry-proxy.sock")
	box := Box{Sockets: []SocketMount{
		{Source: filepath.Join(t.TempDir(), "does-not-exist.sock"), Target: "/signal.sock"},
		{Source: sockA, Target: RegistryProxySocketTarget},
	}}

	specs := buildMountSpecs(MountParams{}, box)

	if len(specs) != 1 {
		t.Fatalf("want 1 spec, got %d: %+v", len(specs), specs)
	}
	if specs[0].Source != sockA || specs[0].Target != RegistryProxySocketTarget {
		t.Errorf("specs[0] = %+v, want Source=%q Target=%q", specs[0], sockA, RegistryProxySocketTarget)
	}
}
