package dispatch

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/runner"
)

// nonceHexWidth is newNonce's hex width. nix/checks/prompts.nix parses this
// binding out of this file's source to derive the research-verdict payload
// budget, so its name and single-space spelling are load-bearing there.
const nonceHexWidth = 32

// Pins newNonce's length (16 random bytes, hex-encoded). The research-verdict
// fragments' payload budget (issue #3669) is BASH_MAX_OUTPUT_LENGTH minus the
// whole marker prefix, i.e. minus (len("SPINDRIFT_COMMENT ") + len(nonce) + 1),
// so a silent change to the nonce width would invalidate the budget text
// baked into those fragments without this test catching it.
func TestNewNonce_LengthIsNonceHexWidth(t *testing.T) {
	if got := len(newNonce()); got != nonceHexWidth {
		t.Fatalf("len(newNonce()) = %d, want %d", got, nonceHexWidth)
	}
}

// A nil generation means "use the runner adapter's own startup-baked default",
// matching runner.Box.ClosureGeneration's nil-means-default contract.
func TestFactory_AgentGenerationNilBeforeSet(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	if got := f.AgentGeneration(); got != nil {
		t.Errorf("AgentGeneration before any SetAgentGeneration: want nil, got %+v", got)
	}
}

func TestFactory_SetAgentGenerationThenGet(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	gen := runner.NewAgentGeneration("/nix/store/abc-agent-files")
	f.SetAgentGeneration(&gen)

	got := f.AgentGeneration()
	if got != &gen {
		t.Errorf("AgentGeneration after SetAgentGeneration: want %p, got %p", &gen, got)
	}
}

// Unlike SetHeartbeatOut, SetAgentGeneration carries no before-any-New() panic
// guard: a hot-swap has to be able to land after dispatching already started.
func TestFactory_SetAgentGenerationAfterNewStillApplies(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	f.New("1", "first dispatch")

	gen := runner.NewAgentGeneration("/nix/store/def-agent-files")
	f.SetAgentGeneration(&gen)

	if got := f.AgentGeneration(); got != &gen {
		t.Errorf("AgentGeneration after New() then SetAgentGeneration: want %p, got %p", &gen, got)
	}
}

// Every non-bwrap-hotswap caller depends on this unchanged nil default.
func TestRun_BoxClosureGenerationDefaultNil(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("99", "no generation set")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != nil {
		t.Errorf("Box.ClosureGeneration with no SetAgentGeneration: want nil, got %+v", got)
	}
}

func TestRun_BoxClosureGenerationSnapshottedAtNew(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	gen := runner.NewAgentGeneration("/nix/store/ghi-agent-files")
	f.SetAgentGeneration(&gen)

	d := f.New("100", "generation set before New")
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != &gen {
		t.Errorf("Box.ClosureGeneration: want %p, got %p", &gen, got)
	}
}

// Pins issue #2682: a Dispatch minted before a hot-swap keeps launching Boxes
// with the generation it snapshotted at its own New(), even across a Run() that
// happens after a later SetAgentGeneration, while a Dispatch minted after the
// swap picks up the new generation.
func TestDispatch_KeepsAgentGenerationSnapshotFromNewDespiteLaterSwap(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d1 := f.New("1", "pre-swap")

	gen := runner.NewAgentGeneration("/nix/store/swap-agent-closure")
	f.SetAgentGeneration(&gen)

	if result := d1.Run(); !result.Success {
		t.Fatalf("d1.Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls after d1.Run: want 1, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].ClosureGeneration; got != nil {
		t.Errorf("d1 Box.ClosureGeneration after later swap: want nil (snapshot from before swap), got %+v", got)
	}

	d2 := f.New("2", "post-swap")
	if result := d2.Run(); !result.Success {
		t.Fatalf("d2.Run: want Success=true, got %+v", result)
	}
	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls after d2.Run: want 2, got %d", len(fr.RunCalls))
	}
	if got := fr.RunCalls[1].ClosureGeneration; got != &gen {
		t.Errorf("d2 Box.ClosureGeneration: want %p, got %p", &gen, got)
	}
}

// Issue #3875 (ADR 0056): NewChore keys the Dispatch "butler-"+Name, distinct
// from any tracker issue number New() could be called with, so its log path
// carries the same key.
func TestFactory_NewChore_KeysLogPathButlerPrefixed(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.NewChore(Chore{Name: "lint-sweep", Branch: "butler/lint-sweep"})
	want := logPathFor(dir, "butler-lint-sweep")
	if got := d.logPath(); got != want {
		t.Errorf("NewChore logPath: got %q, want %q", got, want)
	}
}

// Issue #3907: New and NewChore populate the sealed subject sum type
// directly, the seam buildBoxEnv and announce switch over.
func TestFactory_SetsSubject(t *testing.T) {
	dir := tempLogDir(t)
	f, err := NewFactory(Config{}, dir, runner.NewFake(), fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.New("7", "Test issue")
	wantSubj := issueSubject("7", "Test issue")
	if !reflect.DeepEqual(d.subject, wantSubj) {
		t.Errorf("subject: got %+v, want %+v", d.subject, wantSubj)
	}
	if d.number != d.subject.key.String() || d.number != "7" {
		t.Errorf("New: d.number = %q, want %q (== subject.key.String())", d.number, d.subject.key.String())
	}
	if got := d.subject.title; got != "Test issue" {
		t.Errorf("New: subject.title = %q, want %q", got, "Test issue")
	}

	c := Chore{Name: "lint-sweep", Branch: "butler/lint-sweep"}
	cd := f.NewChore(c)
	if !cd.subject.key.IsChore() || !reflect.DeepEqual(cd.subject.chore, c) {
		t.Errorf("NewChore subject: got %+v, want chore %+v", cd.subject, c)
	}
	if want := dispatchkey.Chore("lint-sweep").String(); cd.number != cd.subject.key.String() || cd.number != want {
		t.Errorf("NewChore: d.number = %q, want %q (== subject.key.String())", cd.number, want)
	}
	if got, want := cd.subject.title, "butler: lint-sweep"; got != want {
		t.Errorf("NewChore: subject.title = %q, want %q", got, want)
	}
}

// NewChore's Run() forwards the Chore straight through to buildBoxEnv, on the
// same seam Run() already uses for an issue-keyed Dispatch.
func TestFactory_NewChore_RunForwardsChoreEnv(t *testing.T) {
	dir := tempLogDir(t)

	fr := runner.NewFake()
	f, err := NewFactory(Config{}, dir, fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Cleanup()

	d := f.NewChore(Chore{
		Name:   "lint-sweep",
		Branch: "butler/lint-sweep",
		Scope:  chore.Scope{Head: "deadbeef"},
	})
	if result := d.Run(); !result.Success {
		t.Fatalf("Run: want Success=true, got %+v", result)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: want 1, got %d", len(fr.RunCalls))
	}
	env := fr.RunCalls[0].Env
	if got := env["CHORE_NAME"]; got != "lint-sweep" {
		t.Errorf("CHORE_NAME: got %q, want %q", got, "lint-sweep")
	}
	if got := env["BASE_BRANCH"]; got != "butler/lint-sweep" {
		t.Errorf("BASE_BRANCH: got %q, want %q", got, "butler/lint-sweep")
	}
	if _, ok := env["ISSUE_NUMBER"]; ok {
		t.Error("ISSUE_NUMBER should be absent for a chore Dispatch's Run()")
	}
}

func issueTextFactory(t *testing.T, fr runner.Runner, resolve func(string) (string, error)) *Factory {
	t.Helper()
	cfg := Config{OpenPRForIssue: noOpenPR, IssueTextFor: resolve}
	f, err := NewFactory(cfg, tempLogDir(t), fr, fakeDriver{}, RealClock())
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(f.Cleanup)
	return f
}

// One Factory outlives many dispatches in the Console and the continuous
// loop, so a fresh Dispatch for the same issue number must re-resolve rather
// than serve text cached by an earlier dispatch (issue #4160).
func TestFactory_New_ResolvesIssueTextFreshPerDispatch(t *testing.T) {
	fr := runner.NewFake()
	text := "first body"
	f := issueTextFactory(t, fr, func(string) (string, error) { return text, nil })

	d1 := f.New("7", "t")
	if r := d1.Run(); !r.Success {
		t.Fatalf("first Run: %+v", r)
	}
	d1.Close()
	text = "edited body"
	d2 := f.New("7", "t")
	defer d2.Close()
	if r := d2.Run(); !r.Success {
		t.Fatalf("second Run: %+v", r)
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls = %d, want 2", len(fr.RunCalls))
	}
	if got := fr.RunCalls[0].Env["ISSUE_TEXT"]; got != "first body" {
		t.Errorf("first ISSUE_TEXT = %q, want %q", got, "first body")
	}
	if got := fr.RunCalls[1].Env["ISSUE_TEXT"]; got != "edited body" {
		t.Errorf("second ISSUE_TEXT = %q, want %q", got, "edited body")
	}
}

// Within one Dispatch every Box shares a byte-identical ISSUE_TEXT so the
// prompt's stable prefix keeps hitting the cache (issue #3445).
func TestFactory_New_IssueTextStableAcrossRunAndFix(t *testing.T) {
	fr := runner.NewFake()
	text := "first body"
	resolved := 0
	f := issueTextFactory(t, fr, func(string) (string, error) {
		resolved++
		return text, nil
	})

	d := f.New("7", "t")
	if r := d.Run(); !r.Success {
		t.Fatalf("Run: %+v", r)
	}
	text = "edited body"
	if r := d.Fix(1, ""); !r.Success {
		t.Fatalf("Fix: %+v", r)
	}

	if resolved != 1 {
		t.Errorf("resolver calls = %d, want 1 per Dispatch", resolved)
	}
	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls = %d, want 2", len(fr.RunCalls))
	}
	for i, c := range fr.RunCalls {
		if got := c.Env["ISSUE_TEXT"]; got != "first body" {
			t.Errorf("RunCalls[%d] ISSUE_TEXT = %q, want %q", i, got, "first body")
		}
	}
}
