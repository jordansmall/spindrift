//go:build integration

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/markergate"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/seambundle"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

func TestMain(m *testing.M) { seamtest.Main(m) }

const (
	seamIssue  = "7"
	seamBranch = "agent/issue-7"
	seamNonce  = "n0nce"
	seamSlug   = "o/r"
)

// seamOutcomeLine is the line a first run reports when it reports one.
func seamOutcomeLine(status, note string) string {
	return fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=%s note=%s", seamIssue, seamBranch, status, note)
}

// seamResult is one claude stream-json result event.
func seamResult(text string) string {
	b, _ := json.Marshal(map[string]string{"type": "result", "result": text})
	return string(b) + "\n"
}

func seamGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// seamRepo returns a clean work tree on the agent branch whose origin/main
// ref sits at the base commit, with extraCommits commits on top of it.
func seamRepo(t *testing.T, extraCommits int) string {
	t.Helper()
	return seamRepoAt(t, t.TempDir(), seamBranch, extraCommits)
}

func seamRepoAt(t *testing.T, dir, branch string, extraCommits int) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	seamGit(t, dir, "init", "-q", "-b", "main")
	seamGit(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	seamGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	seamGit(t, dir, "checkout", "-q", "-b", branch)
	for i := 0; i < extraCommits; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		seamGit(t, dir, "add", "-A")
		seamGit(t, dir, "commit", "-q", "-m", fmt.Sprintf("work %d", i))
	}
	return dir
}

// seamSessionID mirrors the claude Driver preamble's derivation, which the
// rendered preamble recomputes from REPO_SLUG and ISSUE_NUMBER alone.
func seamSessionID() string {
	sum := sha256.Sum256([]byte("spindrift-session:" + seamSlug + ":" + seamIssue))
	h := hex.EncodeToString(sum[:])[:32]
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// seamCellEnv is the Box env a work cell assembles against, the golden suite's
// default cell. Vars box never sees through its flags must come from the
// environment, exactly as in the real Box.
func seamCellEnv() map[string]string {
	return map[string]string{
		"ISSUE_TRACKER":          "github",
		"CODE_FORGE":             "github",
		"BOX_FORGE_BACKEND":      "GH",
		"BOX_TRACKER_AXIS_READ":  "GITHUB",
		"BOX_TRACKER_AXIS_WRITE": "GITHUB",
		"BOX_TRACKER_AXIS_FILER": "GH",
		"ISSUE_TITLE":            "Do the thing",
		"IN_PROGRESS_LABEL":      "agent-in-progress",
		"COMPLETE_LABEL":         "agent-complete",
	}
}

// seamBoxArgs is the flag set entrypoint.sh hands box, over the rendered
// fixtures and the repo's real prompt templates. skillsDir is a Driver skills
// directory (empty is fine).
func seamBoxArgs(t *testing.T, workDir, outboxDir, skillsDir string) []string {
	t.Helper()
	return []string{
		"--work-dir=" + workDir, "--outbox-dir=" + outboxDir,
		"--registry=" + seamtest.Path(t, "fragments-registry.json"),
		"--validate-markers-registry=" + seamtest.Path(t, "prompt-contract-registry.json"),
		"--driver-skills-dir=" + skillsDir,
		"--prompts-dir=" + repopath.PromptsDir(),
		"--agents-prompt-files=", "--driver-agent-files-dir=",
		"--comms-contract-file=" + seamtest.Path(t, "comms-contract.md"),
		"--check-contract-file=" + seamtest.Path(t, "check-contract.md"),
		"--outcome-contract-file=" + seamtest.Path(t, "outcome-contract.md"),
		"--research-outcome-contract-file=" + seamtest.Path(t, "research-outcome-contract.md"),
		"--argv-prompt-style=flag", "--argv-prompt-flag=-p", "--argv-model-flag=--model",
		"--argv-model-omit-empty=0", "--argv-agents-flag=--agents", "--argv-effort-flag=--effort",
		"--argv-order=prompt model agents session driverFlags effort",
		"--model=", "--effort=", "--driver=claude", "--driver-bin=claude", "--driver-flags=",
		"--heartbeat-log=", "--max-budget-tokens=0", "--max-budget-usd=0",
		"--devshell=0", "--devshell-name=default",
		"--prework-rebase-conflict=0", "--publish-rebase=0",
	}
}

// seamBaseEnv is the env every seam run launches box with, minus its
// dispatch-kind axes. An ambient Box env must not leak into the knobs the
// seam tests pin, so every other Box var is blanked.
func seamBaseEnv(t *testing.T) map[string]string {
	t.Helper()
	env := map[string]string{
		"TMPDIR":                 t.TempDir(),
		"BASE_BRANCH":            "main",
		"RUN_NONCE":              seamNonce,
		"BOX_SIGNAL_CARRIER":     "log",
		"BOX_WRITE_ENABLED":      "1",
		"MAX_REBASE_ATTEMPTS":    "1",
		"TRANSIENT_BACKOFF_SECS": "0",
		"HOLD_JITTER_SECS":       "0",
	}
	mergeEnv(env, seamCellEnv())
	for _, name := range promptassembly.BoxEnvVarNames {
		if _, set := env[name]; !set {
			env[name] = ""
		}
	}
	return env
}

func mergeEnv(dst map[string]string, srcs ...map[string]string) {
	for _, src := range srcs {
		for k, v := range src {
			dst[k] = v
		}
	}
}

type seamCase struct {
	repo  string
	relay bool
	// env overlays the run env; skills are the Driver skill directories baked in;
	// agentsPromptFiles is the nix-baked agent -> prompt file map.
	env               map[string]string
	skills            []string
	agentsPromptFiles string
	// promptsDir overrides the repo's templates; extraArgs are appended to
	// the box flags, where a repeated flag overrides the default.
	promptsDir string
	extraArgs  []string
	// driverRuns script the Driver: the first is the first Driver run box
	// performs, the rest are its corrective resumes.
	driverRuns []seamtest.DriverRun
	// snapshot has the orchestrator fake keep each pass's prompt and session
	// file contents, which box deletes once the pass returns.
	snapshot bool
}

type seamRun struct {
	res       seamtest.Result
	driverRec [][]string
	orchRec   [][]string
	outboxDir string
	handoff   string
	sessionID string
	snapshots string // set when the case asked for snapshots
}

// firstRun and resumes split a record at the first Driver run.
func (r seamRun) firstRun(t *testing.T) []string {
	t.Helper()
	if len(r.driverRec) == 0 {
		t.Fatal("the Driver never ran")
	}
	return r.driverRec[0]
}

func (r seamRun) resumes() [][]string {
	if len(r.driverRec) == 0 {
		return nil
	}
	return r.driverRec[1:]
}

func runBoxSeam(t *testing.T, c seamCase) seamRun {
	t.Helper()
	box := seamtest.Build(t, "./box")
	id := seamSessionID()

	tmp := t.TempDir()
	outbox := filepath.Join(tmp, "outbox")
	driverRec := filepath.Join(tmp, "driver.rec")
	orchRec := filepath.Join(tmp, "orch.rec")

	// The claude fake writes no transcript, so a resume needs one in HOME to
	// render --resume.
	home := filepath.Join(tmp, "home")
	proj := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	env := seamBaseEnv(t)
	mergeEnv(env, map[string]string{
		"DISPATCH_KIND":          "work",
		"DISPATCH_KEYING":        "issue",
		"DISPATCH_ANNOUNCE_VERB": "implementing",
		"DISPATCH_KEY":           seamIssue,
		"ISSUE_NUMBER":           seamIssue,
		"BRANCH":                 seamBranch,
		"REPO_SLUG":              seamSlug,
		"HOME":                   home,
	})
	if c.relay {
		env["BOX_WRITE_ENABLED"] = ""
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	orchCfg := seamtest.OrchestratorConfig{Record: orchRec}
	snapshots := ""
	if c.snapshot {
		snapshots = filepath.Join(tmp, "snapshots")
		orchCfg.Snapshot = snapshots
	}
	mergeEnv(env, c.env,
		seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{Record: driverRec, Runs: c.driverRuns}),
		seamtest.WriteFakeConfig(t, "orchestrator", orchCfg))

	res := seamtest.Run(t, seamtest.Cmd{
		Bin:      box,
		Args:     c.boxArgs(t, outbox),
		Env:      env,
		PathDirs: []string{seamtest.InstallFakes(t, "claude", "orchestrator")},
	})
	run := seamRun{
		res:       res,
		driverRec: seamtest.ReadRecord(t, driverRec),
		orchRec:   seamtest.ReadRecord(t, orchRec),
		outboxDir: outbox,
		sessionID: id,
		snapshots: snapshots,
	}
	if len(run.orchRec) > 0 {
		run.handoff = handoffArg(run.orchRec[0])
	}
	return run
}

func handoffArg(argv []string) string {
	for i, a := range argv {
		if a == "--handoff-file" {
			return argv[i+1]
		}
	}
	return ""
}

// readSeamFile reads a file box wrote.
func readSeamFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assembledPrompt is the prompt box assembled, which the first handoff names.
func (r seamRun) assembledPrompt(t *testing.T) string {
	t.Helper()
	if r.handoff == "" {
		t.Fatal("the orchestrator never ran")
	}
	var h struct{ PromptFile string }
	if err := json.Unmarshal(readSeamFile(t, r.handoff), &h); err != nil || h.PromptFile == "" {
		t.Fatalf("handoff %s names no prompt (%v)", r.handoff, err)
	}
	return string(readSeamFile(t, h.PromptFile))
}

// outcomeLines are the SPINDRIFT_OUTCOME lines box printed; the progress
// banners that merely mention the token do not lead with it.
func outcomeLines(stdout string) []string {
	var out []string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "SPINDRIFT_OUTCOME ") {
			out = append(out, l)
		}
	}
	return out
}

// assertResumeHandoffStripsReview reads the handoff the first resume's
// orchestrator was given and checks the shared original, the handoff box wrote,
// still names a review prompt.
func (r seamRun) assertResumeHandoffStripsReview(t *testing.T) {
	t.Helper()
	if len(r.orchRec) != 2 {
		t.Fatalf("orchestrator runs = %d; want the first run and one resume", len(r.orchRec))
	}
	path := handoffArg(r.orchRec[1])
	if path == "" || path == r.handoff {
		t.Fatalf("resume handoff = %q; want a copy distinct from %q", path, r.handoff)
	}
	var got, orig map[string]any
	if err := json.Unmarshal(readSeamFile(t, path), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readSeamFile(t, r.handoff), &orig); err != nil {
		t.Fatal(err)
	}
	if orig["ReviewPromptFile"] == "" || orig["ReviewPromptFile"] == nil {
		t.Errorf("shared handoff names no review prompt: %v", orig)
	}
	if got["ReviewPromptFile"] != "" || got["Driver"] != "claude" || got["Issue"] != seamIssue {
		t.Errorf("resume handoff = %v; want ReviewPromptFile cleared and the rest kept", got)
	}
}

// wantFirstRun checks the Driver's first invocation: the session pinned, then
// the assembled prompt.
func (r seamRun) wantFirstRun(t *testing.T) {
	t.Helper()
	want := []string{"--session-id", r.sessionID, "-p", strings.TrimRight(r.assembledPrompt(t), "\n")}
	if got := r.firstRun(t); !reflect.DeepEqual(got, want) {
		t.Errorf("first Driver run = %q; want %q", got, want)
	}
}

func TestBoxSeamOutcomePresentRunsNoResume(t *testing.T) {
	line := seamOutcomeLine("done", "first")
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(line)}},
	})
	r.res.WantExit(t, 0)
	r.wantFirstRun(t)
	if len(r.driverRec) != 1 || len(r.orchRec) != 1 {
		t.Errorf("driver calls %v, orchestrator calls %v; want only the first run", r.driverRec, r.orchRec)
	}
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{line}) {
		t.Errorf("outcome lines = %q; want the first run's %q printed once", got, line)
	}
}

func TestBoxSeamMissingOutcomeIsNudgedOnce(t *testing.T) {
	resumed := seamOutcomeLine("done", "after nudge")
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult("all finished")}, {Stdout: seamResult(resumed)}},
	})
	r.res.WantExit(t, 0)
	r.wantFirstRun(t)

	nudge, err := markergate.RenderNudgePrompt(markergate.NudgeConfig{Marker: markergate.MarkerOutcome, Issue: seamIssue, Landing: seamBranch})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--resume", r.sessionID, "-p", nudge}}
	if !reflect.DeepEqual(r.resumes(), want) {
		t.Errorf("resumes = %q; want %q", r.resumes(), want)
	}
	r.assertResumeHandoffStripsReview(t)
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{resumed}) {
		t.Errorf("outcome lines = %q; want only the resumed pass's %q", got, resumed)
	}
}

func TestBoxSeamUnrecoveredOutcomeIsBackstopped(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult("all finished")}, {Stdout: seamResult("still no marker")}},
	})
	r.res.WantExit(t, 0)
	r.wantFirstRun(t)

	if res := r.resumes(); len(res) != 1 || len(res[0]) != 4 || !reflect.DeepEqual(res[0][:2], []string{"--resume", r.sessionID}) || res[0][2] != "-p" {
		t.Errorf("resumes = %q; want exactly one `--resume %s -p <nudge>`", res, r.sessionID)
	}
	want := fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=blocked synthetic=true note=driver exited without emitting an outcome; a resume attempt also produced no outcome; no work to preserve", seamIssue, seamBranch)
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("outcome lines = %q; want one synthetic %q", got, want)
	}
}

func TestBoxSeamPRIntentNudgeTaken(t *testing.T) {
	first := seamOutcomeLine("ready", "done")
	intent := "SPINDRIFT_PR_INTENT " + seamNonce + " " + base64.StdEncoding.EncodeToString([]byte("feat: x\n\nbody"))
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 1),
		relay:      true,
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(first)}, {Stdout: seamResult(intent)}},
	})
	r.res.WantExit(t, 0)
	r.wantFirstRun(t)

	nudge, err := markergate.RenderNudgePrompt(markergate.NudgeConfig{Marker: markergate.MarkerPRIntent, Nonce: seamNonce, OriginalOutcomeLine: first})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--resume", r.sessionID, "-p", nudge}}
	if !reflect.DeepEqual(r.resumes(), want) {
		t.Errorf("resumes = %q; want %q", r.resumes(), want)
	}
	r.assertResumeHandoffStripsReview(t)
	manifest := []string{"--manifest-path", r.outboxDir + "/manifest.json"}
	for i, argv := range r.orchRec {
		if got := argv[len(argv)-2:]; !reflect.DeepEqual(got, manifest) {
			t.Errorf("orchestrator run %d argv = %q; want a relay Box to pass %q last", i, argv, manifest)
		}
	}
	if strings.Contains(r.res.Stdout, "nudge exhausted") {
		t.Errorf("stdout reports the nudge exhausted despite a verified marker:\n%s", r.res.Stdout)
	}
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{first}) {
		t.Errorf("outcome lines = %q; want the first run's line printed once", got)
	}
	if _, err := os.Stat(filepath.Join(r.outboxDir, seambundle.FileName)); err != nil {
		t.Errorf("relay Box left no bundle in the outbox: %v", err)
	}
}

func TestBoxSeamPRIntentNudgeExhausted(t *testing.T) {
	first := seamOutcomeLine("ready", "done")
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 1),
		relay:      true,
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(first)}, {Stdout: seamResult("no marker either")}},
	})
	r.res.WantExit(t, 0)
	if len(r.resumes()) != 1 {
		t.Errorf("resumes = %q; want exactly one", r.resumes())
	}
	if !strings.Contains(r.res.Stdout, "read-only PR-intent nudge exhausted after 1 attempt") {
		t.Errorf("stdout lacks the exhausted-nudge op line:\n%s", r.res.Stdout)
	}
}

// entrypointExports is what entrypoint.sh still exports before it execs box,
// so the golden's env delta is these and nothing box adds. Each slice that
// moves a setup step into box drops its entries. The values are hardcoded
// here, so the Go golden's env section only proves box adds and removes
// nothing; the real bash exports are pinned by
// tests/entrypoint-driver-invocation-golden.bats, which is not a duplicate.
func entrypointExports(workDir, branch string) map[string]string {
	return map[string]string{
		"BASH_DEFAULT_TIMEOUT_MS":              "1800000",
		"BASH_MAX_TIMEOUT_MS":                  "1800000",
		"BRANCH":                               branch,
		"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1",
		"PWD":                                  workDir,
	}
}

var (
	goldenMktemp = regexp.MustCompile(`/([^/:\s]+/)*tmp\.[A-Za-z0-9]{10}`)
	goldenStore  = regexp.MustCompile(`/nix/store/[a-z0-9]{32}-`)
)

// goldenValue rewrites run-specific substrings the way
// tests/entrypoint-driver-invocation-golden.bats's _norm_value does, in the
// same order.
func goldenValue(v, outbox, tmpRoot string) string {
	v = strings.ReplaceAll(v, outbox, "<outbox>")
	v = goldenMktemp.ReplaceAllString(v, "<mktemp>")
	v = strings.ReplaceAll(v, tmpRoot, "<tmp>")
	return goldenStore.ReplaceAllString(v, "<store>/")
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

// normaliseDriverInvocation renders an orchestrator snapshot in the golden
// format captured from the bash path in 6ad56073 ("test: pin the bash Driver
// invocation in a golden") before it was deleted. baseEnv is the env box would
// have been launched with had entrypoint.sh exported nothing.
func normaliseDriverInvocation(t *testing.T, s seamtest.Snapshot, baseEnv map[string]string, outbox, tmpRoot string) string {
	t.Helper()
	var b strings.Builder
	var handoff string
	b.WriteString("argv\n")
	for i := 0; i < len(s.Argv); i++ {
		arg := s.Argv[i]
		switch arg {
		case "--handoff-file", "--prompt-file", "--session-file", "--log-path":
			i++
			placeholder := map[string]string{"--handoff-file": "<handoff>", "--prompt-file": "<prompt>", "--session-file": "<session>", "--log-path": "<stream-log>"}[arg]
			if arg == "--handoff-file" {
				handoff = s.Argv[i]
			}
			fmt.Fprintf(&b, "%s\n%s\n", arg, placeholder)
		case "--manifest-path":
			i++
			fmt.Fprintf(&b, "%s\n%s\n", arg, strings.Replace(s.Argv[i], outbox, "<outbox>", 1))
		default:
			fmt.Fprintf(&b, "%s\n", arg)
		}
	}

	b.WriteString("session\n")
	if s.Session != "" {
		b.WriteString(s.Session + "\n")
	}

	raw, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff %q unreadable: %v", handoff, err)
	}
	var h struct{ PromptFile string }
	if err := json.Unmarshal(raw, &h); err != nil || h.PromptFile == "" {
		t.Fatalf("handoff %s has no PromptFile (%v)", handoff, err)
	}
	want, err := os.ReadFile(h.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("prompt\n")
	if strings.TrimRight(s.Prompt, "\n") == strings.TrimRight(string(want), "\n") {
		b.WriteString("<handoff-prompt>\n")
	} else {
		b.WriteString("<differs from handoff PromptFile>\n")
	}

	b.WriteString("env\n")
	post := envMap(s.Env)
	var lines []string
	for name, v := range post {
		if name == "SHLVL" || name == "_" || name == "OLDPWD" {
			continue
		}
		if pre, set := baseEnv[name]; !set || pre != v {
			lines = append(lines, name+"="+strings.ReplaceAll(goldenValue(v, outbox, tmpRoot), "\n", `\n`))
		}
	}
	for name := range baseEnv {
		if _, set := post[name]; !set && name != "SHLVL" && name != "_" && name != "OLDPWD" {
			lines = append(lines, name+"<unset>")
		}
	}
	sort.Strings(lines)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// TestBoxSeamDriverInvocationGolden pins the first Driver invocation box makes
// to the goldens captured from the bash path in 6ad56073 ("test: pin the bash
// Driver invocation in a golden") before it was deleted; box must keep
// reproducing them byte for byte.
func TestBoxSeamDriverInvocationGolden(t *testing.T) {
	cases := []struct {
		kind   string
		branch string
		env    map[string]string
	}{
		{"work", "agent/issue-7", map[string]string{
			"DISPATCH_KIND": "work", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
		}},
		{"research", "agent/issue-7", map[string]string{
			"DISPATCH_KIND": "research", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "researching",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
		}},
		{"butler", "agent/issue-butler-bugs", map[string]string{
			"DISPATCH_KIND": "butler", "DISPATCH_KEYING": "chore", "DISPATCH_ANNOUNCE_VERB": "sweeping",
			"DISPATCH_KEY": "butler-bugs", "CHORE_NAME": "bugs", "ISSUE_TITLE": "", "CHORE_HEAD": "deadbeef",
			"CHORE_DIFF_RANGE": "cafef00d..deadbeef", "CHORE_SLICE": "cmd/launcher/main.go", "CHORE_MAX_FINDINGS": "5",
		}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			box := seamtest.Build(t, "./box")
			root := t.TempDir()
			workDir := filepath.Join(root, "work")
			seamRepoAt(t, workDir, c.branch, 0)
			outbox := filepath.Join(root, "outbox")
			snapshots := filepath.Join(root, "snapshots")
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}

			fakes := seamtest.InstallFakes(t, "claude", "orchestrator")
			base := seamBaseEnv(t)
			mergeEnv(base, c.env, map[string]string{
				"REPO_SLUG": "owner/repo",
				"HOME":      home,
				"PATH":      fakes + string(os.PathListSeparator) + os.Getenv("PATH"),
			}, seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{
				Record: filepath.Join(root, "driver.rec"),
				Runs:   []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "golden"))}},
			}), seamtest.WriteFakeConfig(t, "orchestrator", seamtest.OrchestratorConfig{
				Record:   filepath.Join(root, "orch.rec"),
				Snapshot: snapshots,
			}))
			launch := map[string]string{}
			mergeEnv(launch, base, entrypointExports(workDir, c.branch))

			res := seamtest.Run(t, seamtest.Cmd{
				Bin:      box,
				Args:     seamBoxArgs(t, workDir, outbox, t.TempDir()),
				Env:      launch,
				CleanEnv: true,
				Dir:      workDir,
			})
			res.WantExit(t, 0)

			got := normaliseDriverInvocation(t, seamtest.ReadSnapshot(t, snapshots, 1), base, outbox, root)
			golden, err := os.ReadFile(filepath.Join("testdata", "driver-invocation", c.kind+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(golden) {
				t.Errorf("first Driver invocation differs from %s.golden\n--- golden ---\n%s--- actual ---\n%s--- end ---", c.kind, golden, got)
			}
		})
	}
}

// boxArgs is seamBoxArgs with the case's baked skills and agent prompt map.
func (c seamCase) boxArgs(t *testing.T, outbox string) []string {
	t.Helper()
	skillsDir := t.TempDir()
	for _, name := range c.skills {
		if err := os.MkdirAll(filepath.Join(skillsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillsDir, name, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := seamBoxArgs(t, c.repo, outbox, skillsDir)
	for i, a := range args {
		if strings.HasPrefix(a, "--agents-prompt-files=") {
			args[i] = "--agents-prompt-files=" + c.agentsPromptFiles
		}
	}
	if c.promptsDir != "" {
		for i, a := range args {
			if strings.HasPrefix(a, "--prompts-dir=") {
				args[i] = "--prompts-dir=" + c.promptsDir
			}
		}
	}
	return append(args, c.extraArgs...)
}

// TestBoxSeamAssemblesTheGoldenPrompt runs box over the golden suite's default
// work cell and requires the prompt it hands the orchestrator to equal the
// pinned golden byte for byte: box, not bash, now produces it.
func TestBoxSeamAssemblesTheGoldenPrompt(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo: seamRepo(t, 0),
		env: map[string]string{
			"ISSUE_TITLE":              "Do the thing",
			"BOX_OUTBOX_RELAY_CAPABLE": "1",
			"RUN_NONCE":                "test-run-nonce-0001",
		},
		skills:            []string{"caveman", "tdd", "commit", "code-review", "check-hygiene", "code-comments"},
		agentsPromptFiles: `{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`,
		driverRuns:        []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "golden"))}},
	})
	r.res.WantExit(t, 0)
	want, err := os.ReadFile(filepath.Join(repopath.PromptAssemblyGoldenDir(), "no-roster.prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.assembledPrompt(t); got != string(want) {
		t.Errorf("assembled prompt differs from no-roster.prompt.txt\n--- golden ---\n%s\n--- actual ---\n%s\n--- end ---", want, got)
	}
}

// seamStubPrompts is a prompts dir holding only stub templates, so each
// case's marker set is exactly what its files say and no real fragment
// supplies a marker the stub omits.
func seamStubPrompts(t *testing.T, override map[string]string) string {
	t.Helper()
	files := map[string]string{
		"issue-prompt.md":  "issue stub\n",
		"scout-prompt.md":  "scout stub\n",
		"review-prompt.md": "reviewer stub\n\nVERDICT: APPROVE or BLOCK\n",
		"worker-prompt.md": "worker stub\n",
	}
	for name, body := range override {
		files[name] = body
	}
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var seamResearchEnv = map[string]string{
	"DISPATCH_KIND": "research", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "researching",
}

// The real prompt-contract registry's reject row stops the Box before any
// Driver run: a read-only research prompt without SPINDRIFT_COMMENT would
// lose the verdict.
func TestBoxSeamValidatorRejectStopsBeforeTheDriver(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		relay:      true,
		env:        seamResearchEnv,
		promptsDir: seamStubPrompts(t, map[string]string{"research-prompt.md": "research stub, no verdict-comment marker here\n"}),
	})
	if r.res.ExitCode == 0 {
		t.Fatalf("box exited 0 on a rejected prompt; stdout:\n%s", r.res.Stdout)
	}
	if !strings.Contains(r.res.Stdout, "SPINDRIFT_COMMENT") {
		t.Errorf("stdout does not name the missing marker:\n%s", r.res.Stdout)
	}
	if strings.Contains(r.res.Stderr, "prompt-assembly") {
		t.Errorf("a marker rejection must print bare, got stderr:\n%s", r.res.Stderr)
	}
	if strings.Contains(r.res.Stdout, "prompt-assembly:") {
		t.Errorf("a marker rejection must print bare, got stdout:\n%s", r.res.Stdout)
	}
	if len(r.driverRec) != 0 || len(r.orchRec) != 0 {
		t.Errorf("driver calls %v, orchestrator calls %v; want none after a rejected prompt", r.driverRec, r.orchRec)
	}
}

// The same row accepts a prompt that carries the marker, however the prose
// around it is worded.
func TestBoxSeamValidatorPassesAMarkedResearchPrompt(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:  seamRepo(t, 0),
		relay: true,
		env:   seamResearchEnv,
		promptsDir: seamStubPrompts(t, map[string]string{
			"research-prompt.md": "# WRAP UP\n\nWhen you are all done, drop a comment carrying SPINDRIFT_COMMENT so the launcher hears your call.\n",
		}),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "researched"))}},
	})
	r.res.WantExit(t, 0)
	if len(r.driverRec) != 1 {
		t.Errorf("driver calls = %v; want the first run", r.driverRec)
	}
	if !strings.Contains(r.assembledPrompt(t), "SPINDRIFT_COMMENT") {
		t.Error("the assembled prompt lacks the marker")
	}
}

// A warn row is advisory: a read-only work prompt without SPINDRIFT_PR_INTENT
// reaches the Driver, with the advisory in the Box log.
func TestBoxSeamValidatorWarnsAndProceeds(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		relay:      true,
		promptsDir: seamStubPrompts(t, map[string]string{"issue-prompt.md": "issue stub, no PR-intent marker here\n"}),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "warned"))}},
	})
	r.res.WantExit(t, 0)
	if !strings.Contains(r.res.Stdout, "SPINDRIFT_PR_INTENT") {
		t.Errorf("stdout lacks the advisory:\n%s", r.res.Stdout)
	}
	if len(r.driverRec) != 1 {
		t.Errorf("driver calls = %v; want the first run", r.driverRec)
	}
}

// box's flags are the only source of the Driver settings in the handoff the
// orchestrator receives: the real binary carries each one through.
func TestBoxSeamHandoffCarriesTheFlagSettings(t *testing.T) {
	handoff := func(t *testing.T, extra ...string) promptassembly.Handoff {
		t.Helper()
		r := runBoxSeam(t, seamCase{
			repo:       seamRepo(t, 0),
			extraArgs:  extra,
			driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "handoff"))}},
		})
		r.res.WantExit(t, 0)
		h, err := promptassembly.LoadHandoffFile(r.handoff)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	t.Run("operator knobs", func(t *testing.T) {
		h := handoff(t, "--model=claude-test-model", "--effort=high", "--max-budget-tokens=500000", "--max-budget-usd=4.44")
		if h.Model != "claude-test-model" || h.Effort != "high" {
			t.Errorf("model/effort = %q/%q", h.Model, h.Effort)
		}
		if h.Caps.MaxBudgetTokens != 500000 || h.Caps.MaxBudgetUSD != 4.44 {
			t.Errorf("caps = %+v", h.Caps)
		}
	})

	t.Run("schema defaults and claude's argv shape", func(t *testing.T) {
		h := handoff(t)
		if h.DriverBin != "claude" || h.Driver != "claude" {
			t.Errorf("driver = %q bin %q", h.Driver, h.DriverBin)
		}
		if h.Caps.MaxBudgetTokens != 0 || h.Caps.MaxBudgetUSD != 0 {
			t.Errorf("caps = %+v; want zero budgets by default", h.Caps)
		}
		want := promptassembly.ArgvShape{
			PromptStyle: "flag", PromptFlag: "-p", ModelFlag: "--model", AgentsFlag: "--agents", EffortFlag: "--effort",
			Order: []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
		}
		if !reflect.DeepEqual(h.ArgvShape, want) {
			t.Errorf("ArgvShape = %+v; want %+v", h.ArgvShape, want)
		}
	})
}

// seamConflictRepo is a work tree whose agent branch and origin/main both add
// the same file differently, with the pre-work `git rebase origin/main` already
// stopped on the conflict, the state --prework-rebase-conflict=1 reports.
func seamConflictRepo(t *testing.T) string {
	t.Helper()
	dir := seamRepo(t, 0)
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "c"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		seamGit(t, dir, "add", "c")
	}
	write("agent\n")
	seamGit(t, dir, "commit", "-q", "-m", "agent change")
	seamGit(t, dir, "checkout", "-q", "main")
	write("main\n")
	seamGit(t, dir, "commit", "-q", "-m", "main change")
	seamGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	seamGit(t, dir, "checkout", "-q", seamBranch)
	cmd := exec.Command("git", "-C", dir, "rebase", "origin/main")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if err := cmd.Run(); err == nil {
		t.Fatal("the rebase did not conflict")
	}
	if !seamRebaseInProgress(dir) {
		t.Fatal("the conflicting rebase left no rebase in progress")
	}
	return dir
}

func seamRebaseInProgress(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "rebase-merge"))
	return err == nil
}

func seamRevParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// seamResolveRebase is the Driver side effect that finishes the conflicted
// rebase the way a resolving agent would.
func seamResolveRebase(dir string) string {
	return "cd '" + dir + "' && echo resolved >c && git add c && " +
		"GIT_EDITOR=true GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t git rebase --continue"
}

// wantConflictPass checks the first orchestrator invocation was the sessionless
// conflict-resolve pass and returns its snapshot: the handoff, prompt, session
// and log files named, an empty session, the rendered conflict prompt, and a
// Driver run that was handed no session flag.
func (r seamRun) wantConflictPass(t *testing.T) seamtest.Snapshot {
	t.Helper()
	if len(r.orchRec) == 0 || len(r.driverRec) == 0 {
		t.Fatalf("orchestrator calls %v, Driver calls %v; want the conflict pass to have run", r.orchRec, r.driverRec)
	}
	flags := map[string]bool{}
	for i := 0; i < len(r.orchRec[0]); i += 2 {
		flags[r.orchRec[0][i]] = true
	}
	for _, f := range []string{"--handoff-file", "--prompt-file", "--session-file", "--log-path"} {
		if !flags[f] {
			t.Errorf("conflict pass argv %q lacks %s", r.orchRec[0], f)
		}
	}
	snap := seamtest.ReadSnapshot(t, r.snapshots, 1)
	if snap.Session != "" {
		t.Errorf("conflict pass session file = %q; want it empty (sessionless)", snap.Session)
	}
	// Substituted by hand rather than rendered through
	// conflictresolve.RenderPrompt, so a rendering regression cannot cancel
	// itself out. The fixture bakes no skills, so the preamble/caveman slots
	// are empty.
	tmpl, err := os.ReadFile(filepath.Join(repopath.PromptsDir(), "conflict-resolve-prompt.md"))
	if err != nil {
		t.Fatalf("read conflict-resolve prompt template: %v", err)
	}
	head, _, found := strings.Cut(string(tmpl), "${BRANCH}")
	if !found {
		t.Fatalf("conflict-resolve prompt template has no ${BRANCH} token")
	}
	rest, _, _ := strings.Cut(string(tmpl)[len(head):], "\n")
	wantHead := head + rest + "\n"
	for k, v := range map[string]string{
		"SKILL_PREAMBLE": "", "CAVEMAN_STEP": "",
		"BASE_BRANCH": "main", "BRANCH": seamBranch,
	} {
		wantHead = strings.ReplaceAll(wantHead, "${"+k+"}", v)
	}
	if !strings.HasPrefix(snap.Prompt, wantHead) || strings.Contains(snap.Prompt, "${") || strings.HasSuffix(snap.Prompt, "\n") {
		t.Errorf("conflict pass prompt = %q; want it to open with %q, fully substituted and newline-trimmed", snap.Prompt, wantHead)
	}
	if got, want := r.driverRec[0], []string{"-p", snap.Prompt}; !reflect.DeepEqual(got, want) {
		t.Errorf("conflict pass Driver run = %q; want %q", got, want)
	}
	return snap
}

func TestBoxSeamConflictUnresolvedAborts(t *testing.T) {
	repo := seamConflictRepo(t)
	head := seamRevParse(t, repo, seamBranch)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		snapshot:   true,
		extraArgs:  []string{"--prework-rebase-conflict=1"},
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult("could not resolve")}},
	})
	r.res.WantExit(t, 1)
	for _, line := range []string{
		"==> pre-work rebase conflict detected — invoking conflict-resolve agent",
		"==> pre-work rebase onto origin/main failed — conflict agent could not resolve",
	} {
		if !strings.Contains(r.res.Stdout, line) {
			t.Errorf("stdout lacks %q\n%s", line, r.res.Stdout)
		}
	}
	if len(r.orchRec) != 1 || len(r.driverRec) != 1 {
		t.Fatalf("orchestrator calls %v, Driver calls %v; want one of each (no assembly, no main run)", r.orchRec, r.driverRec)
	}
	r.wantConflictPass(t)
	if seamRebaseInProgress(repo) {
		t.Error("the unresolved rebase was left in progress; want it aborted")
	}
	if got := seamRevParse(t, repo, seamBranch); got != head {
		t.Errorf("%s = %s after the abort; want its original head %s", seamBranch, got, head)
	}
}

func TestBoxSeamConflictResolvedContinuesIntoTheMainRun(t *testing.T) {
	repo := seamConflictRepo(t)
	r := runBoxSeam(t, seamCase{
		repo:      repo,
		snapshot:  true,
		extraArgs: []string{"--prework-rebase-conflict=1"},
		driverRuns: []seamtest.DriverRun{
			{Sh: seamResolveRebase(repo), Stdout: seamResult("resolved")},
			{Stdout: seamResult(seamOutcomeLine("done", "after the conflict pass"))},
		},
	})
	r.res.WantExit(t, 0)
	if !strings.Contains(r.res.Stdout, "==> pre-work rebase conflict resolved by agent") {
		t.Errorf("stdout lacks the resolved line\n%s", r.res.Stdout)
	}
	if len(r.orchRec) != 2 || len(r.driverRec) != 2 {
		t.Fatalf("orchestrator calls %v, Driver calls %v; want the conflict pass and the main run", r.orchRec, r.driverRec)
	}
	conflict := r.wantConflictPass(t)
	main := seamtest.ReadSnapshot(t, r.snapshots, 2)
	if main.Prompt == conflict.Prompt || main.Session == "" {
		t.Errorf("main run was handed the conflict prompt or no session; session %q", main.Session)
	}
	want := []string{"--session-id", r.sessionID, "-p", strings.TrimRight(main.Prompt, "\n")}
	if got := r.driverRec[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("main Driver run = %q; want %q", got, want)
	}
	if seamRebaseInProgress(repo) {
		t.Error("a rebase is still in progress after the resolve")
	}
	if got, want := seamRevParse(t, repo, seamBranch+"~1"), seamRevParse(t, repo, "origin/main"); got != want {
		t.Errorf("%s~1 = %s; want the rebased branch on origin/main %s", seamBranch, got, want)
	}
}

func TestBoxSeamConflictResolveOnlyStopsBeforeTheMainRun(t *testing.T) {
	repo := seamConflictRepo(t)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		snapshot:   true,
		env:        map[string]string{"CONFLICT_RESOLVE_PR_URL": "https://example.test/o/r/pull/9"},
		extraArgs:  []string{"--prework-rebase-conflict=1"},
		driverRuns: []seamtest.DriverRun{{Sh: seamResolveRebase(repo), Stdout: seamResult("resolved")}},
	})
	r.res.WantExit(t, 0)
	if !strings.Contains(r.res.Stdout, "==> CONFLICT_RESOLVE_PR_URL: conflict resolved — exiting without main agent") {
		t.Errorf("stdout lacks the resolve-only line\n%s", r.res.Stdout)
	}
	if len(r.orchRec) != 1 || len(r.driverRec) != 1 {
		t.Fatalf("orchestrator calls %v, Driver calls %v; want only the conflict pass", r.orchRec, r.driverRec)
	}
	r.wantConflictPass(t)
}
