package dispatch

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRun_CallsRunnerWithCorrectBox verifies that Run invokes runner.Run with
// a Box containing the expected issue number, name, and env keys.
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

// TestRun_ForwardsRunNonceIntoBoxEnv verifies Run forwards the Dispatch's
// own per-run nonce (issue #1937) as Box.Env["RUN_NONCE"], and that a
// second Dispatch built by the same Factory gets a different nonce.
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

// TestRun_TerminalFailurePropagates verifies that a terminal box failure is
// reported as Result.Success=false without retry.
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

// TestRun_PopulatesBoxDriverCacheDir verifies Run forwards the Dispatch's
// per-issue driver-cache directory onto the dispatched Box.
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

// TestRun_PopulatesBoxOutboxDir verifies Run forwards a fresh, existing
// per-issue outbox directory onto the dispatched Box (CODE_FORGE=local, ADR
// 0033) — the runner's mount code only produces the writable /outbox mount
// when the source directory already exists (candidateMount), so runOnce must
// create it, not merely name it.
func TestRun_PopulatesBoxOutboxDir(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{Capabilities: forge.Capabilities{
		ForgeDescriptor: backend.Descriptor{HostMediatedRemote: true},
	}}, dir, fr, fakeDriver{}, RealClock())
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

// TestRun_NoOutboxDirForNonLocalCodeForge verifies that a Box dispatched
// under any CodeForge other than "local" gets no outbox directory at all —
// creating .spindrift/outbox/<num> on every dispatch would otherwise litter
// the github/git-flow majority with a directory nothing ever mounts.
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

// TestRun_PopulatesBoxOutboxDir_GithubReadOnly verifies Run provisions the
// same writable per-issue outbox for CODE_FORGE=github under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918) as it already does for
// CODE_FORGE=local: the Box writes seam.bundle there instead of pushing, so
// the launcher's github BundleRelay needs a real mounted directory to find
// it in.
func TestRun_PopulatesBoxOutboxDir_GithubReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		Capabilities:           forge.Capabilities{ForgeDescriptor: backend.Descriptor{OutboxRelayCapable: true}},
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

// TestRun_NoOutboxDirForGithubReadWrite verifies a github Box dispatched
// under the default BOX_FORGE_AND_ISSUE_ACCESS=read-write gets no outbox
// directory at all, exactly like today -- read-write pushes in-box and never
// consults an outbox.
func TestRun_NoOutboxDirForGithubReadWrite(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		Capabilities:           forge.Capabilities{ForgeDescriptor: backend.Descriptor{OutboxRelayCapable: true}},
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

// TestRun_PopulatesBoxOutboxDir_ForgejoReadOnly verifies Run provisions the
// same writable per-issue outbox for CODE_FORGE=forgejo under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only as it already does for CODE_FORGE=github
// (TestRun_PopulatesBoxOutboxDir_GithubReadOnly), now that forgejo's
// backendRow carries OutboxRelayCapable: true (issue #2927) -- forgejo gets
// the same outbox-relay treatment as github (issue #1918). It builds
// Config.Capabilities.ForgeDescriptor from the real backend.Forgejo registry
// row rather than a hand-built stand-in, so the test proves the actual
// registry row provisions an outbox end-to-end, not just that the generic
// plumbing honors an arbitrary true.
func TestRun_PopulatesBoxOutboxDir_ForgejoReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		Capabilities:           forge.Capabilities{ForgeDescriptor: backend.Forgejo},
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

// TestRun_NoOutboxDirForOutboxIncapableReadOnly verifies that a Box
// dispatched with a backend descriptor carrying OutboxRelayCapable: false
// gets no outbox directory even under BOX_FORGE_AND_ISSUE_ACCESS=read-only --
// the outbox-relay dir is gated on the backend's capability, not just the
// access mode. No backendRow valid as a CODE_FORGE under read-only (github,
// local, forgejo) leaves both OutboxRelayCapable and HostMediatedRemote
// false today, so this is a hypothetical-backend-shape case rather than a
// pin on any specific backend's real behavior.
func TestRun_NoOutboxDirForOutboxIncapableReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		Capabilities:           forge.Capabilities{ForgeDescriptor: backend.Descriptor{OutboxRelayCapable: false}},
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

// TestNewFactory_NoDriverSessionCacheDir_NoCacheCreated verifies that a
// Driver declaring no session-cache dir (Config.DriverSessionCacheDir
// empty) makes the Factory skip creating a per-issue cache directory
// entirely -- Box.DriverCacheDir stays empty on every dispatched box, since
// there is no in-box target to mount it over (issue #448).
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

// TestFix_PopulatesBoxDriverCacheDirWithSameKeyAsRun verifies Fix forwards
// the same per-issue cache directory Run used for the same issue -- the
// whole point being that the fix Box mounts back the initial run's session
// data.
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

// TestRun_WritesAndMountsIssueSnapshot verifies that a Config with
// IssueSnapshot set and Kind empty/"work" writes a snapshot file to disk
// before/as part of the box launch, and the launched runner.Box carries
// IssueSnapshotPath pointing at it (issue #2547).
func TestRun_WritesAndMountsIssueSnapshot(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	cfg := Config{
		IssueSnapshot: func(number string) (string, error) {
			return "frozen text for #" + number, nil
		},
	}
	f, err := NewFactory(cfg, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("42", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	path := fr.RunCalls[0].IssueSnapshotPath
	if path == "" {
		t.Fatal("Box.IssueSnapshotPath: got empty, want the snapshot file path")
	}
	if path != SnapshotPathFor(dir, "42") {
		t.Errorf("Box.IssueSnapshotPath: got %q, want %q", path, SnapshotPathFor(dir, "42"))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != "frozen text for #42" {
		t.Errorf("snapshot file content: got %q, want %q", string(got), "frozen text for #42")
	}
}

// TestRun_ResearchKind_NoIssueSnapshot verifies a research-kind Dispatch
// never gets a snapshot -- Scout and research flows are unchanged (issue
// #2547's acceptance criteria).
func TestRun_ResearchKind_NoIssueSnapshot(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	resolveCalled := false
	cfg := Config{
		Kind: "research",
		IssueSnapshot: func(number string) (string, error) {
			resolveCalled = true
			return "should never be written", nil
		},
	}
	f, err := NewFactory(cfg, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("42", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if resolveCalled {
		t.Error("cfg.IssueSnapshot was called for a research dispatch, want it never called")
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].IssueSnapshotPath; got != "" {
		t.Errorf("Box.IssueSnapshotPath: got %q, want empty for a research dispatch", got)
	}
	if _, statErr := os.Stat(SnapshotPathFor(dir, "42")); statErr == nil {
		t.Error("snapshot file was written for a research dispatch, want none")
	}
}

// TestRun_NilIssueSnapshot_NoOp verifies IssueSnapshot: nil (the zero-value
// Config every pre-#2547 test already constructs) is a no-op: no snapshot
// file, no IssueSnapshotPath on the launched Box.
func TestRun_NilIssueSnapshot_NoOp(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("42", "T")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].IssueSnapshotPath; got != "" {
		t.Errorf("Box.IssueSnapshotPath: got %q, want empty when IssueSnapshot is nil", got)
	}
}

// TestResolveConflict_DoesNotMountDriverCache verifies ResolveConflict's box
// does not carry a DriverCacheDir -- it never runs the main agent prompt, so
// there is no session to resume.
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
