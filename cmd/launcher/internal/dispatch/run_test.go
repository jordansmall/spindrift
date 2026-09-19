package dispatch

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/runner"
)

func TestRun_CallsRunnerWithCorrectBox(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	dir := tempLogDir(t)

	fr := runner.NewFake()
	cfg := Config{BoxEnvVars: "GH_TOKEN"}
	f, err := NewFactory(cfg, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("42", "My issue")
	result := d.Run()

	if !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	box := fr.RunCalls[0]
	if box.Issue != "42" {
		t.Errorf("Box.Issue: got %q, want %q", box.Issue, "42")
	}
	if box.Name != "agent-issue-42" {
		t.Errorf("Box.Name: got %q, want %q", box.Name, "agent-issue-42")
	}
	if box.Env["ISSUE_NUMBER"] != "42" {
		t.Errorf("Box.Env[ISSUE_NUMBER]: got %q, want %q", box.Env["ISSUE_NUMBER"], "42")
	}
	if box.Env["GH_TOKEN"] != "secret" {
		t.Errorf("Box.Env[GH_TOKEN]: got %q, want %q", box.Env["GH_TOKEN"], "secret")
	}
}

// Each Dispatch carries its own per-run nonce (issue #1937), so two
// Dispatch values from one Factory must not share one.
func TestRun_ForwardsRunNonceIntoBoxEnv(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d1 := f.New("42", "My issue")
	d1.Run()
	d2 := f.New("43", "Another issue")
	d2.Run()

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	nonce1 := fr.RunCalls[0].Env["RUN_NONCE"]
	nonce2 := fr.RunCalls[1].Env["RUN_NONCE"]
	if nonce1 == "" {
		t.Error("Box.Env[RUN_NONCE]: got empty, want a minted nonce")
	}
	if nonce1 == nonce2 {
		t.Errorf("two Dispatch values got the same nonce: %q", nonce1)
	}
}

func TestRun_TerminalFailurePropagates(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	fr.RunErr = &runner.RunError{ExitCode: 2}

	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("1", "broken")
	result := d.Run()
	if result.Success {
		t.Fatal("want Success=false on terminal failure")
	}
	if len(fr.RunCalls) != 1 {
		t.Errorf("RunCalls: got %d, want 1 (no retry on terminal)", len(fr.RunCalls))
	}
}

func TestRun_PopulatesBoxDriverCacheDir(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	cfg := Config{DriverSessionCacheDir: "/home/agent/.claude/projects"}
	f, err := NewFactory(cfg, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].DriverCacheDir; got == "" {
		t.Error("Box.DriverCacheDir: got empty, want the per-issue cache dir")
	}
}

// Under CODE_FORGE=local (ADR 0033) the runner's candidateMount only
// produces the writable /outbox mount when the source directory already
// exists, so runOnce must create the directory, not merely name it.
func TestRun_PopulatesBoxOutboxDir(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true},
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("77", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	outboxDir := fr.RunCalls[0].OutboxDir
	if outboxDir == "" {
		t.Fatal("Box.OutboxDir: got empty, want the per-issue outbox dir")
	}
	info, err := os.Stat(outboxDir)
	if err != nil || !info.IsDir() {
		t.Errorf("Box.OutboxDir %q: want an existing directory, stat err=%v", outboxDir, err)
	}
}

// Creating .spindrift/outbox/<num> on every dispatch would litter the
// github/git-flow majority with a directory nothing ever mounts.
func TestRun_NoOutboxDirForNonLocalCodeForge(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("78", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].OutboxDir; got != "" {
		t.Errorf("Box.OutboxDir: got %q, want empty when CodeForge != local", got)
	}
}

// Under BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918) the github Box
// writes seam.bundle to the outbox instead of pushing, so the launcher's
// BundleRelay needs a real mounted directory to find it in.
func TestRun_PopulatesBoxOutboxDir_GithubReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Descriptor{OutboxRelayCapable: true},
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("79", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	outboxDir := fr.RunCalls[0].OutboxDir
	if outboxDir == "" {
		t.Fatal("Box.OutboxDir: got empty, want the per-issue outbox dir")
	}
	info, err := os.Stat(outboxDir)
	if err != nil || !info.IsDir() {
		t.Errorf("Box.OutboxDir %q: want an existing directory, stat err=%v", outboxDir, err)
	}
}

// The default BOX_FORGE_AND_ISSUE_ACCESS=read-write pushes in-box and never
// consults an outbox.
func TestRun_NoOutboxDirForGithubReadWrite(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Descriptor{OutboxRelayCapable: true},
		BoxForgeAndIssueAccess: "read-write",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("80", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].OutboxDir; got != "" {
		t.Errorf("Box.OutboxDir: got %q, want empty for github read-write", got)
	}
}

// Forgejo's backendRow carries OutboxRelayCapable: true (issue #2927), so it
// gets github's outbox-relay treatment (issue #1918). The descriptor comes
// from the real backend.Forgejo registry row rather than a hand-built
// stand-in, so this proves the actual row provisions an outbox and not just
// that the generic plumbing honors an arbitrary true.
func TestRun_PopulatesBoxOutboxDir_ForgejoReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Forgejo,
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("81", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	outboxDir := fr.RunCalls[0].OutboxDir
	if outboxDir == "" {
		t.Fatal("Box.OutboxDir: got empty, want the per-issue outbox dir")
	}
	info, err := os.Stat(outboxDir)
	if err != nil || !info.IsDir() {
		t.Errorf("Box.OutboxDir %q: want an existing directory, stat err=%v", outboxDir, err)
	}
}

// The outbox dir is gated on the backend's capability, not just the access
// mode. No backendRow valid under read-only (github, local, forgejo) leaves
// both OutboxRelayCapable and HostMediatedRemote false today, so the
// descriptor here is a hypothetical backend shape, not a pin on any real
// backend's behavior.
func TestRun_NoOutboxDirForOutboxIncapableReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Descriptor{OutboxRelayCapable: false},
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("81", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].OutboxDir; got != "" {
		t.Errorf("Box.OutboxDir: got %q, want empty for OutboxRelayCapable=false read-only", got)
	}
}

// A Driver declaring no session-cache dir has no in-box target to mount one
// over, so the Factory skips creating it entirely (issue #448).
func TestNewFactory_NoDriverSessionCacheDir_NoCacheCreated(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].DriverCacheDir; got != "" {
		t.Errorf("Box.DriverCacheDir: got %q, want empty when Driver declares no session-cache dir", got)
	}
}

// The fix Box must mount back the initial run's session data, so Fix has to
// reuse the exact cache directory Run used for the same issue.
func TestFix_PopulatesBoxDriverCacheDirWithSameKeyAsRun(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	cfg := Config{DriverSessionCacheDir: "/home/agent/.claude/projects"}
	f, err := NewFactory(cfg, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}
	if result := d.Fix(1, ""); !result.Success {
		t.Fatalf("Fix: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	runDir := fr.RunCalls[0].DriverCacheDir
	fixDir := fr.RunCalls[1].DriverCacheDir
	if runDir == "" || fixDir != runDir {
		t.Errorf("Box.DriverCacheDir: run=%q fix=%q, want equal and non-empty", runDir, fixDir)
	}
}

// ResolveConflict's box never runs the main agent prompt, so there is no
// session to resume and no cache to mount.
func TestResolveConflict_DoesNotMountDriverCache(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("55", "T")
	if err := d.ResolveConflict("https://github.com/owner/repo/pull/1"); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	box := fr.RunCalls[0]
	if box.DriverCacheDir != "" {
		t.Errorf("Box.DriverCacheDir: got %q, want empty", box.DriverCacheDir)
	}
	if box.Env["CONFLICT_RESOLVE_PR_URL"] != "https://github.com/owner/repo/pull/1" {
		t.Errorf("Box.Env[CONFLICT_RESOLVE_PR_URL]: got %q", box.Env["CONFLICT_RESOLVE_PR_URL"])
	}
}
