package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/conflictresolve"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// crFixture is a fixture whose box starts with a failed pre-work rebase, plus
// recorders for the conflict pass and its side effects.
type crFixture struct {
	*fixture
	events []string
	pass   *orchCall
	passRC int
	passFn func() // runs inside the pass, e.g. to finish the rebase
	aborts []string
}

func newCRFixture(t *testing.T) *crFixture {
	t.Helper()
	f := &crFixture{fixture: newFixture(t)}
	f.recovery.Conflict = true
	f.in.Assembly = assemblyInputs{
		SkillsDir: t.TempDir(),
		AssemblyFlags: promptassembly.AssemblyFlags{
			RegistryFile: repopath.RegistryJSON(),
			PromptsDir:   repopath.PromptsDir(),
			Passthrough: promptassembly.Passthrough{
				Model: "opus", Effort: "high", Driver: "claude", DriverBin: "claude-bin",
				DriverFlags: "--verbose", Devshell: true, DevshellName: "dev", HeartbeatLog: "/hb",
				ArgvShape: promptassembly.ArgvShape{PromptStyle: "flag", ModelFlag: "--model", Order: []string{"prompt"}},
				Caps:      promptassembly.Caps{MaxSlices: 3, MaxReviewRounds: 4, MaxBudgetTokens: 7, MaxBudgetUSD: 1.5},
			},
		},
	}
	f.target("flake.nix")
	f.knobs["DEV_SHELL_NAME"], f.knobs["DEV_SHELL_PROBE_TIMEOUT"] = "dev", "300"
	f.knobs["ISSUE_NUMBER"] = "42"
	f.knobs["BASE_BRANCH"] = "main"
	f.knobs["BRANCH"] = "agent/issue-42"
	f.env.BoxWriteEnabled = true
	if err := os.MkdirAll(filepath.Join(f.in.WorkDir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	assemble := f.d.Assemble
	f.d.Assemble = func(in assemblyInputs, env promptassembly.Env, w io.Writer) (string, error) {
		f.events = append(f.events, "assemble")
		return assemble(in, env, w)
	}
	driverRun := f.d.Orchestrate
	f.d.Orchestrate = func(argv []string) int {
		if f.pass != nil || !f.recovery.Conflict {
			f.events = append(f.events, "main-run")
			return driverRun(argv)
		}
		f.events = append(f.events, "pass")
		f.pass = f.readCall(argv)
		if f.passFn != nil {
			f.passFn()
		}
		return f.passRC
	}
	f.d.PublishBranch = func(cfg branchrecovery.Config, _ io.Writer) error {
		f.events = append(f.events, "publish")
		f.publishCfgs = append(f.publishCfgs, cfg)
		return f.publishErr
	}
	f.d.AbortRebase = func(workDir string, _ io.Writer) {
		f.events = append(f.events, "abort")
		f.aborts = append(f.aborts, workDir)
	}
	return f
}

func (f *crFixture) readCall(argv []string) *orchCall {
	f.t.Helper()
	call := &orchCall{argv: map[string]string{}}
	for i := 0; i+1 < len(argv); i += 2 {
		call.argv[argv[i]] = argv[i+1]
	}
	read := func(flag string) string {
		b, err := os.ReadFile(call.argv[flag])
		if err != nil {
			f.t.Fatalf("%s unreadable during the pass: %v", flag, err)
		}
		return string(b)
	}
	call.prompt = read("--prompt-file")
	call.session = read("--session-file")
	if err := json.Unmarshal([]byte(read("--handoff-file")), &call.handoff); err != nil {
		f.t.Fatalf("handoff not JSON: %v", err)
	}
	return call
}

// passHandoff decodes the recorded pass handoff into the typed Handoff.
func (f *crFixture) passHandoff(t *testing.T) promptassembly.Handoff {
	t.Helper()
	raw, err := json.Marshal(f.pass.handoff)
	if err != nil {
		t.Fatal(err)
	}
	var h promptassembly.Handoff
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	return h
}

// finishRebase makes the pass resolve the conflict, as the agent would.
func (f *crFixture) finishRebase() {
	f.passFn = f.completeRebase
}

func (f *crFixture) completeRebase() {
	if err := os.RemoveAll(filepath.Join(f.in.WorkDir, ".git", "rebase-merge")); err != nil {
		f.t.Fatal(err)
	}
}

// answer writes the pass's final message into its stream log, as the Driver
// would, so the Box can read it back as the pass summary.
func (f *crFixture) answer(text string) {
	f.t.Helper()
	d, err := driver.New(f.in.Assembly.Passthrough.Driver)
	if err != nil {
		f.t.Fatal(err)
	}
	ev, err := d.ResultEvent(text)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.pass.argv["--log-path"], ev, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *crFixture) wantPrompt() string {
	f.t.Helper()
	reg, err := promptassembly.LoadRegistryFile(f.in.Assembly.RegistryFile)
	if err != nil {
		f.t.Fatal(err)
	}
	want, err := conflictresolve.RenderPrompt(f.in.Assembly.PromptsDir, f.in.Assembly.SkillsDir, conflictresolve.SubstNames(reg), f.d.Getenv)
	if err != nil {
		f.t.Fatal(err)
	}
	return want
}

func TestConflictResolve_NoConflictRunsNoPass(t *testing.T) {
	f := newCRFixture(t)
	f.recovery.Conflict = false
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got, want := f.events, []string{"assemble", "main-run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if strings.Contains(f.stdout(), "conflict") {
		t.Errorf("stdout narrates a conflict: %q", f.stdout())
	}
}

func TestConflictResolve_MainRunRedecidesTheDevshellOnTheResolvedTree(t *testing.T) {
	f := newCRFixture(t)
	f.nixErr = errors.New("exit status 1") // the conflicted flake.lock fails the probe
	f.knobs["PREFETCH"] = "go mod download"
	f.passFn = func() {
		f.nixErr = nil
		f.completeRebase()
	}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if h := f.passHandoff(t); h.Devshell {
		t.Errorf("pass handoff Devshell = true, want false (probe failed on the conflicted tree)")
	}
	if len(f.nixCalls) != 2 {
		t.Fatalf("nix ran %d times, want 2 (conflicted tree, then resolved tree)", len(f.nixCalls))
	}
	if len(f.prefetched) != 2 {
		t.Errorf("prefetched %d times, want 2 (conflicted tree, then resolved tree)", len(f.prefetched))
	}
	// "dev" is newCRFixture's DEV_SHELL_NAME.
	if p := f.assembledIn[0].Passthrough; !p.Devshell || p.DevshellName != "dev" {
		t.Errorf("main run Devshell, DevshellName = %v, %q; want true, dev", p.Devshell, p.DevshellName)
	}
}

func TestConflictResolve_ResolvedRunsOneSessionlessPassThenTheMainRun(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got, want := f.events, []string{"pass", "assemble", "main-run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	p := f.pass
	if p.prompt != f.wantPrompt() || !strings.Contains(p.prompt, "agent/issue-42") {
		t.Errorf("prompt = %q, want the rendered conflict-resolve prompt", p.prompt)
	}
	if p.session != "" {
		t.Errorf("session file = %q, want empty (sessionless)", p.session)
	}
	if _, ok := p.argv["--manifest-path"]; ok {
		t.Errorf("manifest path passed with no outbox mounted: %v", p.argv)
	}
	h := f.passHandoff(t)
	want := f.in.Assembly.Passthrough
	if h.Issue != "42" || !h.Devshell || h.DevshellName != "dev" || h.PromptFile != "" || h.AgentsFile != "" || h.ReviewPromptFile != "" {
		t.Errorf("handoff = %+v", h)
	}
	got := promptassembly.Passthrough{
		Model: h.Model, Effort: h.Effort, Driver: h.Driver, DriverBin: h.DriverBin, DriverFlags: h.DriverFlags,
		Devshell: h.Devshell, DevshellName: h.DevshellName,
		HeartbeatLog: h.HeartbeatLog, ArgvShape: h.ArgvShape, Caps: h.Caps,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("handoff passthrough = %+v, want %+v", got, want)
	}
	out := f.stdout()
	for _, line := range []string{
		"==> pre-work rebase conflict detected — invoking conflict-resolve agent\n",
		"==> pre-work rebase conflict resolved by agent\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("stdout missing %q", line)
		}
	}
	for _, file := range []string{"--handoff-file", "--prompt-file", "--session-file", "--log-path"} {
		if _, err := os.Stat(p.argv[file]); !os.IsNotExist(err) {
			t.Errorf("%s %s left behind (stat err %v)", file, p.argv[file], err)
		}
	}
}

func TestConflictResolve_ManifestPathWhenOutboxMounted(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.env.HostMediatedRemote = true
	f.run()
	if got, want := f.pass.argv["--manifest-path"], f.in.OutboxDir+"/manifest.json"; got != want {
		t.Errorf("--manifest-path = %q, want %q", got, want)
	}
}

func TestConflictResolve_ForwardsRunStateFileAsStateFile(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.run()
	if f.in.RunStateFile == "" {
		t.Fatal("fixture RunStateFile is empty")
	}
	if f.pass == nil {
		t.Fatal("conflict pass never ran")
	}
	if got := f.pass.argv["--state-file"]; got != f.in.RunStateFile {
		t.Errorf("--state-file = %q, want %q", got, f.in.RunStateFile)
	}
}

func TestConflictResolve_OrchestratorExitCodeIsIgnored(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.passRC = 7
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d, want the main run's", rc)
	}
	if len(f.aborts) != 0 {
		t.Errorf("aborted %v after a resolved rebase", f.aborts)
	}
}

func TestConflictResolve_UnresolvedAbortsAndExitsOneBeforeAssembly(t *testing.T) {
	f := newCRFixture(t)
	if rc := f.run(); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if got, want := f.events, []string{"pass", "abort"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if f.aborts[0] != f.in.WorkDir {
		t.Errorf("abort workDir = %q", f.aborts[0])
	}
	if !strings.Contains(f.stdout(), "==> pre-work rebase onto origin/main failed — conflict agent could not resolve\n") {
		t.Errorf("stdout = %q", f.stdout())
	}
}

func TestConflictResolve_RebaseApplyDirAlsoCountsAsUnresolved(t *testing.T) {
	f := newCRFixture(t)
	if err := os.Rename(filepath.Join(f.in.WorkDir, ".git", "rebase-merge"), filepath.Join(f.in.WorkDir, ".git", "rebase-apply")); err != nil {
		t.Fatal(err)
	}
	if rc := f.run(); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
}

func TestConflictResolve_ResolveOnlyExitsZeroBeforeAssembly(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.knobs["CONFLICT_RESOLVE_PR_URL"] = "https://example/pr/1"
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got, want := f.events, []string{"pass"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !strings.Contains(f.stdout(), "CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent") {
		t.Errorf("stdout = %q", f.stdout())
	}
}

func TestConflictResolve_ResolveOnlyWithCleanRebaseSkipsThePass(t *testing.T) {
	f := newCRFixture(t)
	f.recovery.Conflict = false
	f.knobs["CONFLICT_RESOLVE_PR_URL"] = "https://example/pr/1"
	if rc := f.run(); rc != 0 || len(f.events) != 0 {
		t.Fatalf("rc = %d events = %v", rc, f.events)
	}
}

func TestConflictResolve_PublishGoesThroughBranchRecovery(t *testing.T) {
	for _, write := range []bool{true, false} {
		f := newCRFixture(t)
		f.finishRebase()
		f.recovery.Adopted = true
		f.env.BoxWriteEnabled = write
		f.run()
		want := []branchrecovery.Config{{
			WorkDir: f.in.WorkDir, Branch: "agent/issue-42", BaseBranch: "main",
			CodeForge: "github", Push: write, OutboxDir: f.in.OutboxDir,
		}}
		if !reflect.DeepEqual(f.publishCfgs, want) {
			t.Fatalf("write=%v: publish configs = %+v, want %+v", write, f.publishCfgs, want)
		}
		if got, want := f.events, []string{"pass", "publish", "assemble", "main-run"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("write=%v: events = %v, want %v", write, got, want)
		}
	}
}

func TestConflictResolve_PublishFailureExitsOneBeforeAssembly(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.recovery.Adopted = true
	f.publishErr = io.ErrClosedPipe
	if rc := f.run(); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	if got, want := f.events, []string{"pass", "publish"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestConflictResolve_NoFlakeLeavesTheDevshellOff(t *testing.T) {
	f := newCRFixture(t)
	if err := os.Remove(filepath.Join(f.in.WorkDir, "flake.nix")); err != nil {
		t.Fatal(err)
	}
	f.finishRebase()
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	h := f.passHandoff(t)
	if h.Devshell {
		t.Errorf("handoff = %+v, want the devShell off without a flake.nix", h)
	}
}

func TestConflictResolve_NoPublishWithoutAdoption(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.run()
	if len(f.publishCfgs) != 0 {
		t.Errorf("publish configs = %+v", f.publishCfgs)
	}
}

// checkedTree is a tree whose "check" is a file that must read "ok". The fake
// pass writes it, so a test can show publish runs after the pass and sees the
// working tree the pass left.
func (f *crFixture) checkedTree() (check func() bool, write func(string)) {
	path := filepath.Join(f.in.WorkDir, "check.txt")
	check = func() bool { b, _ := os.ReadFile(path); return string(b) == "ok" }
	write = func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return check, write
}

func TestConflictResolve_PublishesTheTreeThePassLeftAfterRepair(t *testing.T) {
	f := newCRFixture(t)
	f.recovery.Adopted = true
	check, write := f.checkedTree()
	var checkAtPublish bool
	publish := f.d.PublishBranch
	f.d.PublishBranch = func(cfg branchrecovery.Config, w io.Writer) error {
		checkAtPublish = check()
		return publish(cfg, w)
	}
	f.passFn = func() {
		h := f.passHandoff(t)
		if !h.Devshell {
			t.Error("pass handoff leaves the devShell off")
		}
		if !strings.Contains(f.pass.prompt, "run the repo's full check gate") {
			t.Errorf("prompt does not tell the resolver to run the check: %q", f.pass.prompt)
		}
		write("resolved but broken")
		if check() {
			t.Error("fixture check passes on a broken tree")
		}
		write("ok")
		f.completeRebase()
		f.answer("ran check.txt gate: red, repaired, green")
	}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got, want := f.events, []string{"pass", "publish", "assemble", "main-run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !checkAtPublish {
		t.Error("the published head fails the check")
	}
	if !strings.Contains(f.stdout(), "==> conflict-resolve pass summary:\n  | ran check.txt gate: red, repaired, green\n") {
		t.Errorf("stdout = %q", f.stdout())
	}
}

func TestConflictResolve_RedCheckStillPublishesAndTheSummaryNamesIt(t *testing.T) {
	f := newCRFixture(t)
	f.recovery.Adopted = true
	f.passFn = func() {
		f.completeRebase()
		f.answer("flakemodule-equivalence red")
	}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if got, want := f.events, []string{"pass", "publish", "assemble", "main-run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if len(f.aborts) != 0 {
		t.Errorf("aborted %v on a red check", f.aborts)
	}
	if !strings.Contains(f.stdout(), "==> conflict-resolve pass summary:\n  | flakemodule-equivalence red\n") {
		t.Errorf("stdout = %q", f.stdout())
	}
}

func TestConflictResolve_NoResultEventNarratesNoSummary(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if strings.Contains(f.stdout(), "pass summary") {
		t.Errorf("stdout = %q", f.stdout())
	}
}

func TestConflictResolve_SetupFailureIsAPhaseError(t *testing.T) {
	f := newCRFixture(t)
	f.in.Assembly.RegistryFile = filepath.Join(f.dir, "missing.json")
	_, err := run(f.in, f.env, f.d)
	if err == nil || !strings.HasPrefix(err.Error(), "conflict-resolve: ") {
		t.Fatalf("err = %v", err)
	}
	if len(f.events) != 0 {
		t.Errorf("events = %v, want none", f.events)
	}
}

func TestConflictResolve_OpencodeSummaryIsTheFinalMessageOnly(t *testing.T) {
	f := newCRFixture(t)
	f.in.Assembly.Passthrough.Driver = "opencode"
	f.passFn = func() {
		f.completeRebase()
		log := `{"type":"text","part":{"messageID":"msg_1","text":"mid-pass chatter"}}` + "\n" +
			`{"type":"step_finish","part":{"messageID":"msg_1"}}` + "\n" +
			`{"type":"text","part":{"messageID":"msg_2","text":"rebased, check green"}}` + "\n"
		if err := os.WriteFile(f.pass.argv["--log-path"], []byte(log), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if rc := f.run(); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if !strings.Contains(f.stdout(), "==> conflict-resolve pass summary:\n  | rebased, check green\n") {
		t.Errorf("stdout = %q", f.stdout())
	}
	if strings.Contains(f.stdout(), "mid-pass chatter") {
		t.Errorf("summary leaked a non-final message: %q", f.stdout())
	}
}
