package dispatch

import (
	"errors"
	"os"
	"strings"
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

	if !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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
	if result.ok() {
		t.Fatal("want failed on terminal failure")
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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
		ForgeDescriptor:        backend.Descriptor{RelayCapable: true, OutboxRelayCapable: true},
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("79", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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

// An explicit BOX_FORGE_AND_ISSUE_ACCESS=read-write pushes in-box and never
// consults an outbox.
func TestRun_NoOutboxDirForGithubReadWrite(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Descriptor{RelayCapable: true, OutboxRelayCapable: true},
		BoxForgeAndIssueAccess: "read-write",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("80", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].OutboxDir; got != "" {
		t.Errorf("Box.OutboxDir: got %q, want empty for github read-write", got)
	}
}

// Issue #3906: a butler (ReadOnlyBox kind) Dispatch must get the outbox-relay
// treatment even under a raw read-write BoxForgeAndIssueAccess, since
// needsOutbox and buildBoxEnv's BOX_WRITE_ENABLED gate must never desync —
// this pins the OutboxRelayCapable side of that shared fail-closed check.
func TestRun_PopulatesBoxOutboxDir_ButlerReadWrite(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		Kind:                   "butler",
		ForgeDescriptor:        backend.Descriptor{RelayCapable: true, OutboxRelayCapable: true},
		BoxForgeAndIssueAccess: "read-write",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("82", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	outboxDir := fr.RunCalls[0].OutboxDir
	if outboxDir == "" {
		t.Fatal("Box.OutboxDir: got empty, want the per-issue outbox dir for a butler Dispatch")
	}
	info, err := os.Stat(outboxDir)
	if err != nil || !info.IsDir() {
		t.Errorf("Box.OutboxDir %q: want an existing directory, stat err=%v", outboxDir, err)
	}
}

// Forgejo's Strategy carries OutboxRelayCapable: true (issue #2927), so it
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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

// git relays a bundle but has no pull requests (OutboxRelayCapable false), so
// the outbox mount must follow RelayCapable, not the PR bit (issue #6898).
func TestRun_PopulatesBoxOutboxDir_GitReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Git,
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("83", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].OutboxDir == "" {
		t.Fatal("Box.OutboxDir: got empty, want the per-issue outbox dir for git read-only")
	}
}

// The outbox dir is gated on the backend's capability, not just the access
// mode. No registered backend leaves
// both RelayCapable and HostMediatedRemote false today, so the
// descriptor here is a hypothetical backend shape, not a pin on any real
// backend's behavior.
func TestRun_NoOutboxDirForRelayIncapableReadOnly(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		ForgeDescriptor:        backend.Descriptor{},
		BoxForgeAndIssueAccess: "read-only",
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("81", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].OutboxDir; got != "" {
		t.Errorf("Box.OutboxDir: got %q, want empty for a relay-incapable read-only forge", got)
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
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
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if result := d.Fix(1, ""); !result.ok() {
		t.Fatalf("Fix: want succeeded, got %+v", result)
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
	runDir := fr.RunCalls[0].DriverCacheDir
	fixDir := fr.RunCalls[1].DriverCacheDir
	if runDir == "" || fixDir != runDir {
		t.Errorf("Box.DriverCacheDir: run=%q fix=%q, want equal and non-empty", runDir, fixDir)
	}
	if got := fr.RunCalls[1].Env["RECORD_ID"]; got == "" || got != d.recordID {
		t.Errorf("fix Box.Env[RECORD_ID]: got %q, want the Dispatch's record ID %q", got, d.recordID)
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
	if got := box.Env["RECORD_ID"]; got == "" || got != d.recordID {
		t.Errorf("Box.Env[RECORD_ID]: got %q, want the Dispatch's record ID %q", got, d.recordID)
	}
}

// The Box keys its prompt_hashes op to the Dispatch's Record (issue #4786), so
// the Record ID reaches the Box env. Run is covered here; Fix and
// ResolveConflict are asserted in their own tests above.
func TestRun_ForwardsRecordIDIntoBoxEnv(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("42", "My issue")
	d.Run()

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].Env["RECORD_ID"]; got == "" || got != d.recordID {
		t.Errorf("Box.Env[RECORD_ID]: got %q, want the Dispatch's record ID %q", got, d.recordID)
	}
}

// The mirror is seeded fresh before every Pass, not once per launcher
// process: a base-branch commit landed between Passes must be what the next
// Box clones, so each Pass (Run, Fix and ResolveConflict) calls the hook and
// mounts the dir it returns.
func TestDispatch_SeedMirrorBeforeEachPass(t *testing.T) {
	dir := tempLogDir(t)
	mirror := t.TempDir()
	var calls int

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		SeedMirror: func() (string, func(), error) { calls++; return mirror, func() {}, nil },
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("81", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if result := d.Fix(1, ""); !result.ok() {
		t.Fatalf("Fix: want succeeded, got %+v", result)
	}

	if err := d.ResolveConflict("https://github.com/owner/repo/pull/1"); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}

	if calls != 3 {
		t.Errorf("SeedMirror calls: got %d, want 3 (one per Pass)", calls)
	}
	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
	for i, box := range fr.RunCalls {
		if box.MirrorDir != mirror {
			t.Errorf("RunCalls[%d].MirrorDir: got %q, want %q", i, box.MirrorDir, mirror)
		}
	}
}

// A per-Pass mirror (github, forgejo) must outlive its Box and be gone right
// after: release runs once per Pass, after the Box exits, so parallel Passes
// never remove a mirror another Box still mounts.
func TestDispatch_SeedMirrorReleasedAfterBoxExits(t *testing.T) {
	dir := tempLogDir(t)
	var seeds, releases int
	var releasedAtBox []int

	fr := runner.NewFake()
	fr.RunFunc = func(runner.Box) error {
		releasedAtBox = append(releasedAtBox, releases)
		return nil
	}
	f, err := NewFactory(Config{
		SeedMirror: func() (string, func(), error) {
			seeds++
			return t.TempDir(), func() { releases++ }, nil
		},
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("84", "T")
	if result := d.Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if releases != 1 {
		t.Errorf("releases after Run: got %d, want 1", releases)
	}
	if result := d.Fix(1, ""); !result.ok() {
		t.Fatalf("Fix: want succeeded, got %+v", result)
	}
	if seeds != 2 || releases != 2 {
		t.Errorf("after two Passes: seeds=%d releases=%d, want 2 and 2", seeds, releases)
	}
	for i, n := range releasedAtBox {
		if n != i {
			t.Errorf("Box %d ran with %d releases already done, want %d (release only after the Box exits)", i, n, i)
		}
	}
}

func TestRun_NoMirrorDirWithoutSeedHook(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	if result := f.New("82", "T").Run(); !result.ok() {
		t.Fatalf("Run: want succeeded, got %+v", result)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].MirrorDir; got != "" {
		t.Errorf("Box.MirrorDir: got %q, want empty so no /repo mount", got)
	}
}

// A Pass that cannot seed its mirror must not start a Box against a stale or
// missing one.
func TestRun_SeedMirrorErrorFailsPassBeforeBox(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{
		SeedMirror: func() (string, func(), error) { return "", nil, errors.New("push refused") },
	}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	result := f.New("83", "T").Run()
	if result.ok() {
		t.Fatalf("Run: want failure when the mirror seed fails, got %+v", result)
	}
	if note := result.result.FailureNote(); !strings.Contains(note, "seed mirror: push refused") {
		t.Errorf("failure note = %q, want it to name the seed error", note)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box on a failed seed)", len(fr.RunCalls))
	}
}
