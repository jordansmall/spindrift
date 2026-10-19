package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/agentpaths"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Both sides of the Target comparison read agentpaths.PromptsDir, so a rename
// in lib/agent-paths.nix cannot fail this assertion on its own; only
// agent-paths-gen catches that drift. What this pins is that BuildMountSpecs
// targets the generated constant and not a stray literal (issue #2531).
func TestBuildMountSpecs_PromptDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := BuildMountSpecs(MountParams{PromptDir: dir}, Box{})

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

func promptsSpecs(specs []MountSpec) []MountSpec {
	var out []MountSpec
	for _, s := range specs {
		if s.Target == agentpaths.PromptsDir {
			out = append(out, s)
		}
	}
	return out
}

// The prompt is baked into the image, so an unset PromptDir mounts nothing
// and says nothing about SPINDRIFT_PROMPT_DIR.
func TestBuildMountSpecs_PromptDirUnset_NoMount(t *testing.T) {
	specs := BuildMountSpecs(MountParams{}, Box{})

	if got := promptsSpecs(specs); len(got) != 0 {
		t.Errorf("unexpected prompts spec: %+v", got)
	}
	for _, s := range specs {
		if strings.Contains(s.Message, "SPINDRIFT_PROMPT_DIR") {
			t.Errorf("unexpected SPINDRIFT_PROMPT_DIR message: %+v", s)
		}
	}
}

func TestBuildMountSpecs_PromptDirMissing_NoMount(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	specs := BuildMountSpecs(MountParams{PromptDir: missing}, Box{})

	if got := promptsSpecs(specs); len(got) != 0 {
		t.Errorf("unexpected prompts spec for missing dir: %+v", got)
	}
	for _, s := range specs {
		if s.Source == missing || strings.Contains(s.Message, "SPINDRIFT_PROMPT_DIR") {
			t.Errorf("unexpected spec for missing dir: %+v", s)
		}
	}
}

// The override binds the directory, not individual files, so every prompt in
// it reaches the Box through a single mount.
func TestBuildMountSpecs_PromptDirBindsWholeDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"issue-prompt.md", "scout-prompt.md", "review-prompt.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	specs := BuildMountSpecs(MountParams{PromptDir: dir}, Box{})

	got := promptsSpecs(specs)
	if len(got) != 1 {
		t.Fatalf("want exactly one prompts spec, got %+v", got)
	}
	if got[0].Source != dir {
		t.Errorf("Source = %q, want the directory %q", got[0].Source, dir)
	}
}

func TestBuildMountSpecs_DriverCacheDirMountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := BuildMountSpecs(MountParams{DriverSessionCacheDir: "/home/agent/.claude/projects"}, Box{DriverCacheDir: dir})

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
	specs := BuildMountSpecs(MountParams{}, Box{DriverCacheDir: dir})

	for _, s := range specs {
		if s.Source == dir {
			t.Errorf("unexpected driver-cache spec when DriverSessionCacheDir is undeclared: %+v", specs)
		}
	}
}

func TestBuildMountSpecs_SkillsDirMounted(t *testing.T) {
	dir := t.TempDir()
	specs := BuildMountSpecs(MountParams{SkillsDir: dir}, Box{})

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
	specs := BuildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/operator-skills" {
			t.Errorf("unexpected skills-dir spec when SkillsDir is empty: %+v", specs)
		}
	}
}

// candidateMount requires an existing directory, so a SkillsDir naming a
// missing path yields no /operator-skills mount.
func TestBuildMountSpecs_SkillsDirMissing_NoMount(t *testing.T) {
	specs := BuildMountSpecs(MountParams{SkillsDir: filepath.Join(t.TempDir(), "absent")}, Box{})

	for _, s := range specs {
		if s.Target == "/operator-skills" {
			t.Errorf("unexpected skills-dir spec for a nonexistent SkillsDir: %+v", specs)
		}
	}
}

// ADR 0033: the code-in mount is read-only so the operator's Accumulation repo
// stays single-writer.
func TestBuildMountSpecs_LocalCodeForge_AccumulationRepoMountedReadOnly(t *testing.T) {
	dir := t.TempDir()
	specs := BuildMountSpecs(MountParams{HostMediatedRemote: true, AccumulationRepoDir: dir}, Box{})

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

// Issue #3957: dispatch's needsOutbox (internal/dispatch/box.go) is the sole
// decider of Box.OutboxDir; the runner mounts it writable whenever set,
// independent of MountParams.
func TestBuildMountSpecs_OutboxDirSet_MountedWritable(t *testing.T) {
	dir := t.TempDir()
	specs := BuildMountSpecs(MountParams{}, Box{OutboxDir: dir})

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

func TestBuildMountSpecs_OutboxDirEmpty_NoMount(t *testing.T) {
	specs := BuildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/outbox" {
			t.Errorf("unexpected /outbox spec when OutboxDir is unset: %+v", specs)
		}
	}
}

// The /repo mount is local-only (ADR 0033), so it never appears when
// HostMediatedRemote is false, whatever AccumulationRepoDir says.
func TestBuildMountSpecs_NonLocalCodeForge_NoAccumulationRepoMount(t *testing.T) {
	repoDir := t.TempDir()
	specs := BuildMountSpecs(MountParams{HostMediatedRemote: false, AccumulationRepoDir: repoDir}, Box{})
	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec: %+v", s)
		}
	}
}

// The /repo mount stays gated on candidateMount, not just on the
// HostMediatedRemote check.
func TestBuildMountSpecs_LocalCodeForge_AbsentAccumulationRepoDir_NoMount(t *testing.T) {
	specs := BuildMountSpecs(MountParams{HostMediatedRemote: true}, Box{})

	for _, s := range specs {
		if s.Target == "/repo" {
			t.Errorf("unexpected /repo spec when AccumulationRepoDir is unset: %+v", specs)
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
		// "Issue" covers "Issues" and e.g. "IssueDir"; "Tracker" covers the
		// other field name origin/main carried. A re-add named neither is
		// past this heuristic's reach, which is why
		// TestBuildMountSpecs_NeverProducesIssuesMount guards the output too.
		if strings.Contains(name, "Issue") || strings.Contains(name, "Tracker") {
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

	specs := BuildMountSpecs(MountParams{
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
		mountParams: MountParams{
			PromptDir:             promptDir,
			SkillsDir:             skillsDir,
			DriverSessionCacheDir: "/home/agent/.claude/projects",
		},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, DriverCacheDir: cacheDir, Sockets: []SocketMount{{Source: proxySocket, Target: RegistryProxySocketTarget}}}

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
		mountParams:   MountParams{HostMediatedRemote: true, AccumulationRepoDir: repoDir},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

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
	box := Box{Name: "agent-issue-1", Env: map[string]string{}, OutboxDir: outboxDir}

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
		mountParams:   MountParams{AccumulationRepoDir: repoDir},
	}
	box := Box{Name: "agent-issue-1", Env: map[string]string{}}

	bwrapArgs := strings.Join(bwrap.buildArgs("/tmp/fake-etc", box), " ")

	if strings.Contains(bwrapArgs, "--ro-bind "+repoDir+" /repo") {
		t.Errorf("bwrap must not mount /repo with HostMediatedRemote=false: %s", bwrapArgs)
	}
}

// candidateMount rejects a unix socket; candidateSocketMount accepts one.
func TestCandidateSocketMount_RealSocket(t *testing.T) {
	sock := runnertest.NewTestSocket(t, "registry-proxy.sock")

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
	sock := runnertest.NewTestSocket(t, "registry-proxy.sock")
	specs := BuildMountSpecs(MountParams{}, Box{Sockets: []SocketMount{{Source: sock, Target: RegistryProxySocketTarget}}})

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
	specs := BuildMountSpecs(MountParams{}, Box{})

	for _, s := range specs {
		if s.Target == "/registry-proxy.sock" {
			t.Errorf("unexpected /registry-proxy.sock spec when Box.Sockets is unset: %+v", specs)
		}
	}
}

// issue #3723: BuildMountSpecs mounts every entry of Box.Sockets, in order,
// each at its own fixed target — not just a single hardcoded socket.
func TestBuildMountSpecs_MultipleSockets_OneWritableSpecEach(t *testing.T) {
	sockA := runnertest.NewTestSocket(t, "registry-proxy.sock")
	sockB := runnertest.NewTestSocket(t, "signal.sock")
	box := Box{Sockets: []SocketMount{
		{Source: sockA, Target: RegistryProxySocketTarget},
		{Source: sockB, Target: SignalSocketTarget},
	}}

	specs := BuildMountSpecs(MountParams{}, box)

	if len(specs) != 2 {
		t.Fatalf("want 2 specs, got %d: %+v", len(specs), specs)
	}
	if specs[0].Source != sockA || specs[0].Target != RegistryProxySocketTarget {
		t.Errorf("specs[0] = %+v, want Source=%q Target=%q", specs[0], sockA, RegistryProxySocketTarget)
	}
	if specs[1].Source != sockB || specs[1].Target != SignalSocketTarget {
		t.Errorf("specs[1] = %+v, want Source=%q Target=%q", specs[1], sockB, SignalSocketTarget)
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
	sockA := runnertest.NewTestSocket(t, "registry-proxy.sock")
	box := Box{Sockets: []SocketMount{
		{Source: filepath.Join(t.TempDir(), "does-not-exist.sock"), Target: SignalSocketTarget},
		{Source: sockA, Target: RegistryProxySocketTarget},
	}}

	specs := BuildMountSpecs(MountParams{}, box)

	if len(specs) != 1 {
		t.Fatalf("want 1 spec, got %d: %+v", len(specs), specs)
	}
	if specs[0].Source != sockA || specs[0].Target != RegistryProxySocketTarget {
		t.Errorf("specs[0] = %+v, want Source=%q Target=%q", specs[0], sockA, RegistryProxySocketTarget)
	}
}
