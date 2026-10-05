package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/conflictresolve"
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
		RegistryFile: repopath.RegistryJSON(),
		PromptsDir:   repopath.PromptsDir(),
		SkillsDir:    t.TempDir(),
		Passthrough: promptassembly.Passthrough{
			Model: "opus", Effort: "high", Driver: "claude", DriverBin: "claude-bin",
			DriverFlags: "--verbose", Devshell: true, DevshellName: "dev", HeartbeatLog: "/hb",
			ArgvShape: promptassembly.ArgvShape{PromptStyle: "flag", ModelFlag: "--model", Order: []string{"prompt"}},
			Caps:      promptassembly.Caps{MaxSlices: 3, MaxReviewRounds: 4, MaxBudgetTokens: 7, MaxBudgetUSD: 1.5},
		},
	}
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

// finishRebase makes the pass resolve the conflict, as the agent would.
func (f *crFixture) finishRebase() {
	f.passFn = func() {
		if err := os.RemoveAll(filepath.Join(f.in.WorkDir, ".git", "rebase-merge")); err != nil {
			f.t.Fatal(err)
		}
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
	var h promptassembly.Handoff
	raw, _ := json.Marshal(p.handoff)
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	want := f.in.Assembly.Passthrough
	want.Devshell, want.DevshellName = false, ""
	if h.Issue != "42" || h.Devshell || h.DevshellName != "" || h.PromptFile != "" || h.AgentsFile != "" || h.ReviewPromptFile != "" {
		t.Errorf("handoff = %+v", h)
	}
	got := promptassembly.Passthrough{
		Model: h.Model, Effort: h.Effort, Driver: h.Driver, DriverBin: h.DriverBin, DriverFlags: h.DriverFlags,
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
			CodeForge: "github", QueryOpenPR: true, Push: write, OutboxDir: f.in.OutboxDir,
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

func TestConflictResolve_NoPublishWithoutAdoption(t *testing.T) {
	f := newCRFixture(t)
	f.finishRebase()
	f.run()
	if len(f.publishCfgs) != 0 {
		t.Errorf("publish configs = %+v", f.publishCfgs)
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
