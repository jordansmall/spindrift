//go:build integration

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/boxclone"
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

// seamRepo returns the work dir box will clone into, which does not exist yet,
// and seeds the origin behind it: a bare repo with HEAD on main at one base
// commit. runBoxSeam routes the clone URL to that origin; box cuts the agent
// branch itself.
func seamRepo(t *testing.T) string {
	t.Helper()
	return seamRepoAt(t, filepath.Join(t.TempDir(), "work"))
}

func seamRepoAt(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	origin := seamOriginPath(dir)
	seamGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", origin)
	scratch := t.TempDir()
	seamGit(t, scratch, "init", "-q", "-b", "main")
	seamGit(t, scratch, "remote", "add", "origin", origin)
	seamGit(t, scratch, "commit", "-q", "--allow-empty", "-m", "base")
	seamGit(t, scratch, "push", "-q", "origin", "main")
	return dir
}

// seamOriginPath is the bare origin seamRepoAt seeded behind a work dir.
func seamOriginPath(workDir string) string { return workDir + ".origin.git" }

// seamScratch clones the origin behind workDir into a scratch work tree, for a
// test to stage origin state in before box runs.
func seamScratch(t *testing.T, workDir string) string {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "scratch")
	seamGit(t, filepath.Dir(scratch), "clone", "-q", seamOriginPath(workDir), scratch)
	return scratch
}

// seamOriginCommit lands one commit of files on the origin's main.
func seamOriginCommit(t *testing.T, workDir, msg string, files map[string]string) {
	t.Helper()
	scratch := seamScratch(t, workDir)
	for path, body := range files {
		full := filepath.Join(scratch, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seamGit(t, scratch, "add", "-A")
	seamGit(t, scratch, "commit", "-q", "-m", msg)
	seamGit(t, scratch, "push", "-q", "origin", "main")
}

// seamCloneRoute points the clone box makes at the origin behind workDir:
// CODE_FORGE=local clones from REPO_MOUNT_DIR, every other forge from a URL
// the HOME gitconfig rewrites to the origin, which keeps the test off the
// global gitconfig and covers the clone's later fetch and the Box's pushes.
// It fills the env vars box reads the URL from.
func seamCloneRoute(t *testing.T, workDir, home string, env map[string]string) {
	t.Helper()
	origin := seamOriginPath(workDir)
	if env["CODE_FORGE"] == "local" {
		env["REPO_MOUNT_DIR"] = origin
		return
	}
	if env["CODE_FORGE"] == "git" && env["CODE_FORGE_REMOTE_URL"] == "" {
		env["CODE_FORGE_REMOTE_URL"] = "https://git.example/o/r.git"
	}
	url, err := boxclone.CloneURL(boxclone.Config{
		CodeForge: env["CODE_FORGE"], RepoSlug: env["REPO_SLUG"], RemoteURL: env["CODE_FORGE_REMOTE_URL"],
		ForgejoBaseURL: env["FORGEJO_BASE_URL"], ForgejoToken: env["FORGEJO_TOKEN"],
	})
	if err != nil {
		return // box rejects the config itself
	}
	f, err := os.OpenFile(filepath.Join(home, ".gitconfig"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[url %q]\n\tinsteadOf = %s\n", origin, url); err != nil {
		t.Fatal(err)
	}
}

// seamCommitWork is the Driver side effect that leaves one commit on the
// agent branch, so a relay Box has something to bundle out at settle.
func seamCommitWork(dir string) string {
	return "cd '" + dir + "' && echo x >f0 && git add f0 && git commit -q -m 'work 0'"
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
		"--forbidden-markers-registry=" + seamtest.Path(t, "forbidden-markers-registry.json"),
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
		"--driver-session-cache-dir=", "--branch-prefix=agent/issue-",
		"--driver-bash-timeout-ms=", "--driver-bash-timeout-env=",
		"--dev-shell-name=", "--dev-shell-probe-timeout=",
		// A nonexistent temp path: the backstop reads no verdict, and an
		// ambient /tmp/run-state.json cannot leak in (issue #4413).
		"--run-state-file=" + filepath.Join(t.TempDir(), "run-state.json"),
	}
}

// seamBaseEnv is the env every seam run launches box with, minus its
// dispatch-kind axes. An ambient Box env must not leak into the knobs the
// seam tests pin, so every other Box var is blanked.
func seamBaseEnv(t *testing.T) map[string]string {
	t.Helper()
	// Nonexistent sources: the layout skips them, leaving the Driver skills dir
	// as baked.
	absent := filepath.Join(t.TempDir(), "absent")
	env := map[string]string{
		"HARNESS_SKILLS_DIR":     filepath.Join(absent, "harness"),
		"OPERATOR_SKILLS_DIR":    filepath.Join(absent, "operator"),
		"HARNESS_HOME_AGENT_DIR": filepath.Join(absent, "home-agent"),
		"TMPDIR":                 t.TempDir(),
		"BASE_BRANCH":            "main",
		"RUN_NONCE":              seamNonce,
		"BOX_SIGNAL_CARRIER":     "log",
		"BOX_WRITE_ENABLED":      "1",
		"MAX_REBASE_ATTEMPTS":    "1",
		"TRANSIENT_BACKOFF_SECS": "0",
		"HOLD_JITTER_SECS":       "0",
		// What checkEnvGuards requires of every dispatch.
		"GH_TOKEN":       "tok",
		"REPO_SLUG":      seamSlug,
		"GIT_USER_NAME":  "spindrift-agent",
		"GIT_USER_EMAIL": "agent@example.com",
	}
	mergeEnv(env, seamCellEnv())
	// box's own toolchain knobs: an ambient PREFETCH would run its hook.
	for _, name := range []string{"PREFETCH", "DEV_SHELL_NAME", "DEV_SHELL_PROBE_TIMEOUT"} {
		env[name] = ""
	}
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
	// homeArgs are flags computed from the run's HOME, appended after
	// extraArgs, for cases that point the layout at paths under HOME.
	homeArgs func(home string) []string
	// driverRuns script the Driver: the first is the first Driver run box
	// performs, the rest are its corrective resumes.
	driverRuns []seamtest.DriverRun
	// snapshot has the orchestrator fake keep each pass's prompt and session
	// file contents, which box deletes once the pass returns.
	snapshot bool
	// nix, when set, puts the nix fake on PATH with this config (Record is
	// filled in); the probe argv it saw lands in seamRun.nixRec.
	nix *seamtest.NixConfig
	// fakes are extra tool fakes put on PATH, each configured through env.
	fakes []string
}

type seamRun struct {
	res       seamtest.Result
	driverRec [][]string
	orchRec   [][]string
	nixRec    [][]string
	outboxDir string
	handoff   string
	sessionID string
	home      string
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
	// The clone runs `gh auth setup-git`; a case's own gh config, in c.env,
	// wins over this answer-nothing default.
	fakeNames := append([]string{"claude", "orchestrator"}, c.fakes...)
	if !slices.Contains(c.fakes, "gh") {
		fakeNames = append(fakeNames, "gh")
	}
	mergeEnv(env, seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: filepath.Join(tmp, "gh.rec")}))
	mergeEnv(env, c.env,
		seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{Record: driverRec, Runs: c.driverRuns}),
		seamtest.WriteFakeConfig(t, "orchestrator", orchCfg))
	seamCloneRoute(t, c.repo, home, env)
	nixRec := filepath.Join(tmp, "nix.rec")
	if c.nix != nil {
		nixCfg := *c.nix
		nixCfg.Record = nixRec
		mergeEnv(env, seamtest.WriteFakeConfig(t, "nix", nixCfg))
		fakeNames = append(fakeNames, "nix")
	}

	args := c.boxArgs(t, outbox)
	if c.homeArgs != nil {
		args = append(args, c.homeArgs(home)...)
	}
	res := seamtest.Run(t, seamtest.Cmd{
		Bin:      box,
		Args:     args,
		Env:      env,
		PathDirs: []string{seamtest.InstallFakes(t, fakeNames...)},
	})
	run := seamRun{
		res:       res,
		driverRec: seamtest.ReadRecord(t, driverRec),
		orchRec:   seamtest.ReadRecord(t, orchRec),
		nixRec:    seamtest.ReadRecord(t, nixRec),
		outboxDir: outbox,
		sessionID: id,
		home:      home,
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
		repo:       seamRepo(t),
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
		repo:       seamRepo(t),
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

// unrecoveredRuns is a first pass and its resume, neither emitting an outcome.
func unrecoveredRuns() []seamtest.DriverRun {
	return []seamtest.DriverRun{{Stdout: seamResult("all finished")}, {Stdout: seamResult("still no marker")}}
}

// unrecoveredOutcome is the backstop's synthetic line after unrecoveredRuns,
// with verdictClause spliced in ahead of the work-preservation clause.
func unrecoveredOutcome(verdictClause string) string {
	return fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=blocked synthetic=true note=driver exited without emitting an outcome; a resume attempt also produced no outcome; %sno work to preserve", seamIssue, seamBranch, verdictClause)
}

func TestBoxSeamUnrecoveredOutcomeIsBackstopped(t *testing.T) {
	r := runBoxSeam(t, seamCase{repo: seamRepo(t), driverRuns: unrecoveredRuns()})
	r.res.WantExit(t, 0)
	r.wantFirstRun(t)

	if res := r.resumes(); len(res) != 1 || len(res[0]) != 4 || !reflect.DeepEqual(res[0][:2], []string{"--resume", r.sessionID}) || res[0][2] != "-p" {
		t.Errorf("resumes = %q; want exactly one `--resume %s -p <nudge>`", res, r.sessionID)
	}
	want := unrecoveredOutcome("")
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("outcome lines = %q; want one synthetic %q", got, want)
	}
}

// The reviewer's last verdict reaches the backstop through --run-state-file.
func TestBoxSeamBackstopReadsRunStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"last_verdict":"BLOCK"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t),
		driverRuns: unrecoveredRuns(),
		// Overrides the flag seamBoxArgs passes: the flag package keeps the last value.
		extraArgs: []string{"--run-state-file=" + path},
	})
	r.res.WantExit(t, 0)
	want := unrecoveredOutcome("reviewer's blocking findings were never cleared; ")
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("outcome lines = %q; want one synthetic %q", got, want)
	}
}

// An advise-only kind never opens a PR and has no branch to push, so box
// neither nudges it for a missing marker nor resumes it: one Driver run, and
// the backstop (when the Driver reports nothing) settles it without recovery.
func TestBoxSeamAdviseOnlyKindsNeverResume(t *testing.T) {
	butler := map[string]string{
		"DISPATCH_KIND": "butler", "DISPATCH_KEYING": "chore", "DISPATCH_ANNOUNCE_VERB": "sweeping",
		"DISPATCH_KEY": "butler-bugs", "ISSUE_NUMBER": "", "ISSUE_TITLE": "", "CHORE_NAME": "bugs",
		"CHORE_HEAD": "deadbeef", "CHORE_DIFF_RANGE": "", "CHORE_SLICE": "agent/entrypoint.sh",
	}
	t.Run("research with no outcome line is backstopped blocked without a resume", func(t *testing.T) {
		r := runBoxSeam(t, seamCase{
			repo:       seamRepo(t),
			env:        seamResearchEnv,
			driverRuns: []seamtest.DriverRun{{Stdout: seamResult("verdict posted")}},
		})
		r.res.WantExit(t, 0)
		if len(r.driverRec) != 1 {
			t.Errorf("driver calls = %q; want the first run only", r.driverRec)
		}
		want := "SPINDRIFT_OUTCOME issue=" + seamIssue + " landing=none status=blocked synthetic=true note=driver exited without emitting an outcome"
		got := outcomeLines(r.res.Stdout)
		if len(got) != 1 || !strings.HasPrefix(got[0], want) {
			t.Errorf("outcome lines = %q; want one starting %q", got, want)
		}
		if strings.Contains(r.res.Stdout, "resume attempt") {
			t.Errorf("the backstop note claims a resume:\n%s", r.res.Stdout)
		}
	})
	t.Run("read-only butler missing PR-intent gets no nudge", func(t *testing.T) {
		line := "SPINDRIFT_OUTCOME issue=butler-bugs landing=none status=ready note=swept"
		r := runBoxSeam(t, seamCase{
			repo:       seamRepo(t),
			relay:      true,
			env:        butler,
			driverRuns: []seamtest.DriverRun{{Stdout: seamResult(line)}},
		})
		r.res.WantExit(t, 0)
		if len(r.driverRec) != 1 {
			t.Errorf("driver calls = %q; want the first run only", r.driverRec)
		}
		if strings.Contains(r.res.Stdout, "PR-intent marker missing") {
			t.Errorf("a PR-intent banner was printed:\n%s", r.res.Stdout)
		}
		if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{line}) {
			t.Errorf("outcome lines = %q; want %q", got, line)
		}
	})
}

func TestBoxSeamPRIntentNudgeTaken(t *testing.T) {
	first := seamOutcomeLine("ready", "done")
	intent := "SPINDRIFT_PR_INTENT " + seamNonce + " " + base64.StdEncoding.EncodeToString([]byte("feat: x\n\nbody"))
	repo := seamRepo(t)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		relay:      true,
		driverRuns: []seamtest.DriverRun{{Sh: seamCommitWork(repo), Stdout: seamResult(first)}, {Stdout: seamResult(intent)}},
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
	repo := seamRepo(t)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		relay:      true,
		driverRuns: []seamtest.DriverRun{{Sh: seamCommitWork(repo), Stdout: seamResult(first)}, {Stdout: seamResult("no marker either")}},
	})
	r.res.WantExit(t, 0)
	if len(r.resumes()) != 1 {
		t.Errorf("resumes = %q; want exactly one", r.resumes())
	}
	if !strings.Contains(r.res.Stdout, "read-only PR-intent nudge exhausted after 1 attempt") {
		t.Errorf("stdout lacks the exhausted-nudge op line:\n%s", r.res.Stdout)
	}
}

// entrypointExports is what the shim's env preamble still exports before it
// execs box, so the golden's env delta is these plus what box itself sets
// (BRANCH, PWD and the Bash-timeout vars) and nothing else. The values are
// hardcoded here, so the Go golden's env section only proves box adds and
// removes nothing; the real bash exports are pinned by
// tests/entrypoint-driver-invocation-golden.bats, which is not a duplicate.
func entrypointExports() map[string]string {
	return map[string]string{"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1"}
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
	var h struct {
		PromptFile   string
		Devshell     bool
		DevshellName string
	}
	if err := json.Unmarshal(raw, &h); err != nil || h.PromptFile == "" {
		t.Fatalf("handoff %s has no PromptFile (%v)", handoff, err)
	}
	want, err := os.ReadFile(h.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "devshell\n%t %s\n", h.Devshell, h.DevshellName)
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
			// A PATH box only prepended to renders as the prepended dirs over
			// $PATH, so the golden does not embed the host's PATH.
			if name == "PATH" && strings.HasSuffix(v, string(os.PathListSeparator)+pre) {
				v = strings.TrimSuffix(v, pre) + "$PATH"
			}
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

// readonlyWorkEnv is the env of a read-only work Box: extra overlays the
// backend- and capability-specific knobs.
func readonlyWorkEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"DISPATCH_KIND": "work", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
		"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7", "BOX_WRITE_ENABLED": "",
	}
	mergeEnv(env, extra)
	return env
}

// TestBoxSeamDriverInvocationGolden pins the first Driver invocation box makes
// to the goldens captured from the bash path in 6ad56073 ("test: pin the bash
// Driver invocation in a golden") before it was deleted; box must keep
// reproducing them byte for byte.
func TestBoxSeamDriverInvocationGolden(t *testing.T) {
	cases := []struct {
		kind string
		env  map[string]string
		// registry arms a Registry route; the goldens without it are the
		// Registry-absent half of the pair.
		registry bool
		// readonly arms a read-only Box with the outbox relay, so box installs
		// the guards and prepends their shim dir to PATH.
		readonly bool
		// fj puts an fj fake on PATH, so a read-only Box shims it as well.
		fj bool
	}{
		{"work", map[string]string{
			"DISPATCH_KIND": "work", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
		}, false, false, false},
		{"work-registry", map[string]string{
			"DISPATCH_KIND": "work", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
		}, true, false, false},
		{"work-readonly", map[string]string{
			"DISPATCH_KIND": "work", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
			"BOX_WRITE_ENABLED": "", "BOX_OUTBOX_RELAY_CAPABLE": "1",
		}, false, true, false},
		{"work-readonly-forgejo", readonlyWorkEnv(map[string]string{
			"BOX_OUTBOX_RELAY_CAPABLE": "1", "CODE_FORGE": "forgejo", "BOX_FORGE_BACKEND": "FORGEJO",
			"FORGEJO_TOKEN": "s3cr3t-forgejo-token", "FORGEJO_BASE_URL": "https://forge.example/",
		}), false, true, true},
		{"work-readonly-local", readonlyWorkEnv(map[string]string{
			"CODE_FORGE": "local", "BOX_HOST_MEDIATED_REMOTE": "1", "BOX_FULLY_LOCAL": "1",
			"GH_TOKEN": "", "REPO_SLUG": "",
		}), false, true, false},
		{"work-readonly-git", readonlyWorkEnv(map[string]string{"CODE_FORGE": "git"}), false, true, false},
		{"research", map[string]string{
			"DISPATCH_KIND": "research", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "researching",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7",
		}, false, false, false},
		// A self-contained run has no clone: the work dir is empty and the
		// token and slug are absent, so the env guards must exempt it.
		{"research-self-contained", map[string]string{
			"DISPATCH_KIND": "research", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "researching",
			"DISPATCH_KEY": "7", "ISSUE_NUMBER": "7", "SELF_CONTAINED": "1", "ISSUE_TRACKER": "local",
			"BOX_TRACKER_AXIS_READ": "LOCAL", "BOX_TRACKER_AXIS_WRITE": "", "BOX_IN_BOX_UNREACHABLE_TRACKER": "1",
			"GH_TOKEN": "", "REPO_SLUG": "",
		}, false, false, false},
		{"butler", map[string]string{
			"DISPATCH_KIND": "butler", "DISPATCH_KEYING": "chore", "DISPATCH_ANNOUNCE_VERB": "sweeping",
			"DISPATCH_KEY": "butler-bugs", "CHORE_NAME": "bugs", "ISSUE_TITLE": "", "CHORE_HEAD": "deadbeef",
			"CHORE_DIFF_RANGE": "cafef00d..deadbeef", "CHORE_SLICE": "cmd/launcher/main.go", "CHORE_MAX_FINDINGS": "5",
		}, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			box := seamtest.Build(t, "./box")
			root := t.TempDir()
			workDir := filepath.Join(root, "work")
			if c.env["SELF_CONTAINED"] != "1" {
				seamRepoAt(t, workDir)
			}
			outbox := filepath.Join(root, "outbox")
			snapshots := filepath.Join(root, "snapshots")
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			registryEnv := map[string]string{}
			if c.registry {
				registryEnv = seamRegistryRoute(t, workDir)
			}

			// The clone runs gh, and a gh on PATH is what gets a read-only shim
			// installed; the fake keeps the run off the host's own gh.
			fakeNames := []string{"claude", "orchestrator", "gh"}
			ghEnv := seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: filepath.Join(root, "gh.rec")})
			if c.fj {
				fakeNames = append(fakeNames, "fj")
				mergeEnv(ghEnv, seamtest.WriteFakeConfig(t, "fj", seamtest.FjConfig{Record: filepath.Join(root, "fj.rec")}))
			}
			fakes := seamtest.InstallFakes(t, fakeNames...)
			base := seamBaseEnv(t)
			mergeEnv(base, ghEnv)
			mergeEnv(base, map[string]string{
				"REPO_SLUG": "owner/repo",
				"HOME":      home,
				"PATH":      fakes + string(os.PathListSeparator) + os.Getenv("PATH"),
			}, c.env, registryEnv, seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{
				Record: filepath.Join(root, "driver.rec"),
				Runs:   []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "golden"))}},
			}), seamtest.WriteFakeConfig(t, "orchestrator", seamtest.OrchestratorConfig{
				Record:   filepath.Join(root, "orch.rec"),
				Snapshot: snapshots,
			}))
			if c.env["SELF_CONTAINED"] != "1" {
				seamCloneRoute(t, workDir, home, base)
			}
			launch := map[string]string{}
			mergeEnv(launch, base, entrypointExports())

			// The Consumer's Bash-timeout knob and the claude Driver's registry
			// entry, as the shim's preambles carry them.
			res := seamtest.Run(t, seamtest.Cmd{
				Bin: box,
				Args: append(seamBoxArgs(t, workDir, outbox, t.TempDir()),
					"--driver-bash-timeout-ms=1800000",
					"--driver-bash-timeout-env=BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS"),
				Env:      launch,
				CleanEnv: true,
				Dir:      root,
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
			if c.readonly {
				assertExecutable(t, filepath.Join(readonlyShimDir(home), "gh"))
			}
			if c.registry {
				assertRegistryBound(t, workDir, home, res.Stdout)
			} else if strings.Contains(got, "npm_config_registry") || strings.Contains(got, "CARGO_REGISTRIES_") {
				t.Errorf("a run without REGISTRY_PROXY_MANIFEST bound the registry proxy:\n%s", got)
			}
		})
	}
}

// seamForwarderPort is bindregistry.ForwarderPort: the address the rendered
// bindings name and the gate probes.
const seamForwarderPort = "27182"

const seamRegistryUpstream = "cargo.mycorp.example"

// seamRegistryRoute arms a Registry route for a box run over workDir and
// returns the env that carries it. The gate wants the proxy's unix socket to
// exist and the Forwarder port to answer; a listener on that port makes the
// probe succeed, so no socat is spawned. The repo's committed cargo and npm
// configs name the upstream host, which is what the bindings rewrite.
func seamRegistryRoute(t *testing.T, workDir string) map[string]string {
	t.Helper()
	// Not under t.TempDir: its long path overruns the 108-byte unix socket
	// limit in the Nix build sandbox.
	sockDir, err := os.MkdirTemp("", "rp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "proxy.sock")
	us, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { us.Close() })
	// The listener is what makes the gate's probe succeed; a port already held
	// elsewhere would let the test pass on someone else's listener, so fail.
	tl, err := net.Listen("tcp", "127.0.0.1:"+seamForwarderPort)
	if err != nil {
		t.Fatalf("Forwarder port %s unavailable: %v", seamForwarderPort, err)
	}
	t.Cleanup(func() { tl.Close() })

	seamOriginCommit(t, workDir, "chore: pin private registries", map[string]string{
		".cargo/config.toml": "[registries.othercorp]\nindex = \"http://" + seamRegistryUpstream + "/other-index/\"\n",
		".npmrc":             "registry=http://" + seamRegistryUpstream + "/\n",
	})

	return map[string]string{
		"REGISTRY_PROXY_MANIFEST": `{"endpoint":"unix://` + sock + `","routes":[{"prefix":"r0","upstreamHost":"` + seamRegistryUpstream +
			`","enforcedPaths":[{"ecosystem":"npm","path":"/"}]}]}`,
	}
}

func seamHeadBlob(t *testing.T, workDir, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", workDir, "show", "HEAD:"+path).Output()
	if err != nil {
		t.Fatalf("git show HEAD:%s: %v", path, err)
	}
	return string(out)
}

// assertRegistryBound checks what the golden's env cannot: the cargo bindings
// land in $HOME/.cargo/config.toml, never the tracked config, and box's exit
// reverts the in-tree npm rewrite so the tree ends pristine.
func assertRegistryBound(t *testing.T, workDir, home, stdout string) {
	t.Helper()
	if strings.Contains(stdout, "bind-registry") {
		t.Errorf("a bind-registry step warned:\n%s", stdout)
	}
	cargoHome := string(readSeamFile(t, filepath.Join(home, ".cargo", "config.toml")))
	for _, want := range []string{
		"[source.spindrift-upstream-othercorp]",
		`registry = "http://` + seamRegistryUpstream + `/other-index/"`,
		`replace-with = "spindrift-registry-proxy-r0-othercorp"`,
		"[registries.spindrift-registry-proxy-r0-othercorp]",
		`index = "sparse+http://127.0.0.1:` + seamForwarderPort + `/r0/other-index/"`,
	} {
		if !strings.Contains(cargoHome, want) {
			t.Errorf("$HOME/.cargo/config.toml lacks %q:\n%s", want, cargoHome)
		}
	}
	for _, path := range []string{".cargo/config.toml", ".npmrc"} {
		if got, want := string(readSeamFile(t, filepath.Join(workDir, path))), seamHeadBlob(t, workDir, path); got != want {
			t.Errorf("%s after box exit = %q; want HEAD's %q", path, got, want)
		}
		out, err := exec.Command("git", "-C", workDir, "ls-files", "-v", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(out), "S") {
			t.Errorf("%s still carries the skip-worktree bit: %s", path, out)
		}
	}
	if out, err := exec.Command("git", "-C", workDir, "status", "--short").Output(); err != nil || len(out) != 0 {
		t.Errorf("git status --short after box exit = %q (%v); want a pristine tree", out, err)
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
		repo: seamRepo(t),
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
		repo:       seamRepo(t),
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
		repo:  seamRepo(t),
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
		repo:       seamRepo(t),
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
			repo:       seamRepo(t),
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

// seamConflictRepo is a work dir whose origin holds a prior run's agent branch
// and an origin/main that advanced past it, both adding the same file
// differently. With seamConflictGh's open PR on the branch, box adopts it and
// its pre-work `git rebase origin/main` stops on the conflict.
func seamConflictRepo(t *testing.T) string {
	t.Helper()
	dir := seamRepo(t)
	scratch := seamScratch(t, dir)
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(scratch, "c"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		seamGit(t, scratch, "add", "c")
	}
	seamGit(t, scratch, "checkout", "-q", "-b", seamBranch)
	write("agent\n")
	seamGit(t, scratch, "commit", "-q", "-m", "agent change")
	seamGit(t, scratch, "push", "-q", "origin", seamBranch)
	seamGit(t, scratch, "checkout", "-q", "main")
	write("main\n")
	seamGit(t, scratch, "commit", "-q", "-m", "main change")
	seamGit(t, scratch, "push", "-q", "origin", "main")
	return dir
}

// seamConflictGh is the env for a gh fake reporting an open PR on seamBranch,
// which is what makes box adopt the prior branch instead of force-resetting it.
func seamConflictGh(t *testing.T) map[string]string {
	t.Helper()
	return seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
		Record: filepath.Join(t.TempDir(), "gh.rec"),
		PRs: []seamtest.GhPR{{
			Number: 9, URL: "https://example.test/o/r/pull/9", HeadRefName: seamBranch, BaseRefName: "main",
		}},
	})
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
	head := seamRevParse(t, seamOriginPath(repo), "refs/heads/"+seamBranch)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		snapshot:   true,
		fakes:      []string{"gh"},
		env:        seamConflictGh(t),
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
		repo:     repo,
		snapshot: true,
		fakes:    []string{"gh"},
		env:      seamConflictGh(t),
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
		fakes:      []string{"gh"},
		env:        mergedEnv(seamConflictGh(t), map[string]string{"CONFLICT_RESOLVE_PR_URL": "https://example.test/o/r/pull/9"}),
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

// homeAgentFixture is a Driver's baked agent-files tree under the seam
// fixtures dir, the same shape the image stages at /agent and /home/agent.
type homeAgentFixture struct{ skills, homeAgent string }

func seamAgentFiles(t *testing.T, driver string) homeAgentFixture {
	t.Helper()
	root := seamtest.Path(t, "agent-files-"+driver)
	return homeAgentFixture{
		skills:    filepath.Join(root, "agent", "skills"),
		homeAgent: filepath.Join(root, "home", "agent"),
	}
}

// layoutEnv hands box the fixture as its harness sources. The fixture is a
// read-only store path, the staging shape bwrap gives box. operatorDir may name
// a directory that does not exist.
func (f homeAgentFixture) layoutEnv(operatorDir string) map[string]string {
	return map[string]string{
		"HARNESS_SKILLS_DIR":     f.skills,
		"OPERATOR_SKILLS_DIR":    operatorDir,
		"HARNESS_HOME_AGENT_DIR": f.homeAgent,
	}
}

// layoutArgs points box's destinations at HOME.
func (f homeAgentFixture) layoutArgs(sessionCacheRel, agentFilesRel string, extra ...string) func(string) []string {
	return func(home string) []string {
		cache, agentFiles := "", ""
		if sessionCacheRel != "" {
			cache = filepath.Join(home, sessionCacheRel)
		}
		if agentFilesRel != "" {
			agentFiles = filepath.Join(home, agentFilesRel)
		}
		return append([]string{
			"--driver-agent-files-dir=" + agentFiles,
			"--driver-session-cache-dir=" + cache,
			"--driver-skills-dir=" + filepath.Join(home, ".claude", "skills"),
		}, extra...)
	}
}

// assertHomeMatchesFixture walks the fixture's home/agent tree and requires
// every entry in home with the same type and owner-writable, and regular
// files' bytes equal unless rewritten names the file (relative path), whose
// bytes are the caller's to check. skip names relative paths left alone.
func assertHomeMatchesFixture(t *testing.T, fixtureHome, home string, rewritten, skip map[string]bool) {
	t.Helper()
	err := filepath.WalkDir(fixtureHome, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fixtureHome, p)
		if rel == "." || skip[rel] {
			return nil
		}
		want, err := d.Info()
		if err != nil {
			return err
		}
		got, err := os.Lstat(filepath.Join(home, rel))
		if err != nil {
			t.Errorf("%s: missing from HOME: %v", rel, err)
			return nil
		}
		if got.Mode().Type() != want.Mode().Type() {
			t.Errorf("%s: HOME type %v; want the fixture's %v", rel, got.Mode().Type(), want.Mode().Type())
			return nil
		}
		if got.Mode().Perm()&0o200 == 0 {
			t.Errorf("%s: mode %v is not owner-writable", rel, got.Mode().Perm())
		}
		if want.Mode().IsRegular() && !rewritten[rel] {
			if got.Mode().Perm()&0o100 != want.Mode().Perm()&0o100 {
				t.Errorf("%s: mode %v; want the fixture's executable bit (%v)", rel, got.Mode().Perm(), want.Mode().Perm())
			}
			if g, w := readSeamFile(t, filepath.Join(home, rel)), readSeamFile(t, p); string(g) != string(w) {
				t.Errorf("%s: content differs from the fixture", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// assertSkillsLaidOut requires every fixture skill under the Driver skills
// dir with the fixture's SKILL.md bytes, except those named in overridden.
func assertSkillsLaidOut(t *testing.T, fixtureSkills, skillsDir string, overridden map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(fixtureSkills)
	if err != nil || len(entries) == 0 {
		t.Fatalf("fixture skills %s: %d entries, %v", fixtureSkills, len(entries), err)
	}
	for _, e := range entries {
		if overridden[e.Name()] {
			continue
		}
		want := readSeamFile(t, filepath.Join(fixtureSkills, e.Name(), "SKILL.md"))
		if got := readSeamFile(t, filepath.Join(skillsDir, e.Name(), "SKILL.md")); string(got) != string(want) {
			t.Errorf("skill %s: SKILL.md differs from the fixture", e.Name())
		}
	}
}

// fenceCount counts the lines that are exactly the YAML fence.
func fenceCount(b []byte) int {
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if l == "---" {
			n++
		}
	}
	return n
}

// frontmatterBytes is b up to and including its second `---` line.
func frontmatterBytes(b []byte) string {
	lines := strings.SplitAfter(string(b), "\n")
	fences := 0
	for i, l := range lines {
		if strings.TrimRight(l, "\n") == "---" {
			if fences++; fences == 2 {
				return strings.Join(lines[:i+1], "")
			}
		}
	}
	return ""
}

func seamHomeCase(t *testing.T, f homeAgentFixture, operatorDir, sessionCacheRel, agentFilesRel string, extraArgs ...string) seamCase {
	t.Helper()
	return seamCase{
		repo:       seamRepo(t),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "home"))}},
		env:        f.layoutEnv(operatorDir),
		homeArgs:   f.layoutArgs(sessionCacheRel, agentFilesRel, extraArgs...),
	}
}

// assertSkillsPreamble requires the assembled prompt to carry the given
// skills-found line. Callers pass a literal so the expectation does not
// re-derive itself from the scan it checks.
func assertSkillsPreamble(t *testing.T, r seamRun, want string) {
	t.Helper()
	if !strings.Contains(r.assembledPrompt(t), want) {
		t.Errorf("assembled prompt lacks %q", want)
	}
}

func TestBoxSeamLaysOutClaudeHome(t *testing.T) {
	f := seamAgentFiles(t, "claude")
	r := runBoxSeam(t, seamHomeCase(t, f, filepath.Join(t.TempDir(), "no-operator"), filepath.Join(".claude", "projects"), ""))
	r.res.WantExit(t, 0)

	// The session cache is the pre-existing live directory; the layout leaves it be.
	assertHomeMatchesFixture(t, f.homeAgent, r.home, nil, map[string]bool{filepath.Join(".claude", "projects"): true})
	if _, err := os.Stat(filepath.Join(r.home, ".claude", "projects", "x", r.sessionID+".jsonl")); err != nil {
		t.Errorf("session cache lost its transcript: %v", err)
	}
	skillsDir := filepath.Join(r.home, ".claude", "skills")
	assertSkillsLaidOut(t, f.skills, skillsDir, nil)
	assertSkillsPreamble(t, r, "Skills available: auto-format, auto-lint, check-hygiene, code-comments.")
}

func TestBoxSeamOperatorSkillOverridesHarnessSkill(t *testing.T) {
	f := seamAgentFiles(t, "claude")
	operator := t.TempDir()
	for name, body := range map[string]string{"auto-format": "operator's own\n", "operator-only": "mine\n"} {
		if err := os.MkdirAll(filepath.Join(operator, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operator, name, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := runBoxSeam(t, seamHomeCase(t, f, operator, filepath.Join(".claude", "projects"), ""))
	r.res.WantExit(t, 0)

	skillsDir := filepath.Join(r.home, ".claude", "skills")
	if got := readSeamFile(t, filepath.Join(skillsDir, "auto-format", "SKILL.md")); string(got) != "operator's own\n" {
		t.Errorf("auto-format SKILL.md = %q; want the operator's override", got)
	}
	if got := readSeamFile(t, filepath.Join(skillsDir, "operator-only", "SKILL.md")); string(got) != "mine\n" {
		t.Errorf("operator-only SKILL.md = %q", got)
	}
	assertSkillsLaidOut(t, f.skills, skillsDir, map[string]bool{"auto-format": true})
	assertSkillsPreamble(t, r, "Skills available: auto-format, auto-lint, check-hygiene, code-comments, operator-only.")
}

func TestBoxSeamLaysOutOpencodeHome(t *testing.T) {
	f := seamAgentFiles(t, "opencode")
	agentsRel := filepath.Join(".config", "opencode", "agents")
	r := runBoxSeam(t, seamHomeCase(t, f, filepath.Join(t.TempDir(), "no-operator"), "", agentsRel,
		"--driver=opencode",
		"--agents-prompt-files="+`{"scout":"scout-prompt.md","reviewer":"review-prompt.md","filer":"filer-prompt.md","worker":"worker-prompt.md"}`))
	r.res.WantExit(t, 0)

	agents := filepath.Join(r.home, agentsRel)
	fixtureAgents := filepath.Join(f.homeAgent, agentsRel)
	// box rewrites the bodies of the agent files the prompt map names and
	// drops reviewer.md; review-axis.md is outside the map and stays as baked.
	rewritten := map[string]bool{}
	for _, name := range []string{"scout", "worker"} {
		rewritten[filepath.Join(agentsRel, name+".md")] = true
	}
	skip := map[string]bool{filepath.Join(agentsRel, "reviewer.md"): true}
	assertHomeMatchesFixture(t, f.homeAgent, r.home, rewritten, skip)

	if _, err := os.Stat(filepath.Join(agents, "reviewer.md")); !os.IsNotExist(err) {
		t.Errorf("reviewer.md still in HOME (stat err %v); box drops it", err)
	}
	for _, name := range []string{"scout", "worker"} {
		want := readSeamFile(t, filepath.Join(fixtureAgents, name+".md"))
		got := readSeamFile(t, filepath.Join(agents, name+".md"))
		if gf, wf := frontmatterBytes(got), frontmatterBytes(want); wf == "" || gf != wf {
			t.Errorf("%s.md frontmatter = %q; want the fixture's %q", name, gf, wf)
		}
		if n := fenceCount(got); n != 2 {
			t.Errorf("%s.md has %d `---` fence lines; want 2", name, n)
		}
		if string(got) == string(want) {
			t.Errorf("%s.md body was not rewritten", name)
		}
	}
	assertSkillsLaidOut(t, f.skills, filepath.Join(r.home, ".claude", "skills"), nil)
}

// TestBoxSeamDevshellPair runs box over a work Target with a flake.nix and the
// nix fake, and pins the devshell pair the golden renders for the Driver
// invocation: the probe's outcome decides it, never the flake alone.
func TestBoxSeamDevshellPair(t *testing.T) {
	probe := []string{"develop", ".#ci", "--command", "true"}
	for _, c := range []struct {
		name      string
		devShells []string
		wantPair  string
		wantLine  string
	}{
		{"present", []string{"ci"}, "true ci", "==> devShell found — lifecycle will run inside nix develop"},
		{"absent", nil, "false default", "==> no devShell in flake (or nix develop failed) — using baked toolchain"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := seamRepo(t)
			seamOriginCommit(t, repo, "chore: add flake", map[string]string{"flake.nix": "{}"})
			r := runBoxSeam(t, seamCase{
				repo:       repo,
				snapshot:   true,
				nix:        &seamtest.NixConfig{DevShells: c.devShells},
				env:        map[string]string{"DEV_SHELL_NAME": "ci", "DEV_SHELL_PROBE_TIMEOUT": "60"},
				driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "devshell"))}},
			})
			r.res.WantExit(t, 0)
			if want := [][]string{probe}; !reflect.DeepEqual(r.nixRec, want) {
				t.Errorf("nix calls = %q; want %q", r.nixRec, want)
			}
			for _, line := range []string{"==> flake.nix found in cloned repo; probing for devShell", c.wantLine} {
				if !strings.Contains(r.res.Stdout, line) {
					t.Errorf("stdout lacks %q:\n%s", line, r.res.Stdout)
				}
			}
			got := normaliseDriverInvocation(t, seamtest.ReadSnapshot(t, r.snapshots, 1), nil, r.outboxDir, filepath.Dir(r.outboxDir))
			if want := "\ndevshell\n" + c.wantPair + "\nprompt\n"; !strings.Contains(got, want) {
				t.Errorf("golden lacks the devshell pair %q:\n%s", c.wantPair, got)
			}
		})
	}
}

// TestBoxSeamLockfileScanWarnsAtSettle pins issue #3199's settle scan: a
// tracked lockfile still naming the Forwarder URL warns whether the Driver
// pass exited clean or not, and a clean repo stays quiet. The scan only needs
// the manifest to parse, so the endpoint need not exist.
func TestBoxSeamLockfileScanWarnsAtSettle(t *testing.T) {
	const warning = "==> WARNING: cargo lockfile Cargo.lock still names the registry proxy Forwarder URL 127.0.0.1:" +
		seamForwarderPort + " — this will ship in the PR (issue #3199)"
	cases := []struct {
		name      string
		staleLock bool
		exit      int
		want      bool
	}{
		{"stale lockfile, clean exit", true, 0, true},
		{"stale lockfile, driver crash", true, 17, true},
		{"clean repo", false, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := seamRepo(t)
			if c.staleLock {
				lock := "[[package]]\nname = \"example\"\nsource = \"registry+http://127.0.0.1:" + seamForwarderPort + "/r0/index/\"\n"
				seamOriginCommit(t, repo, "chore: pin registry", map[string]string{"Cargo.lock": lock})
			}
			r := runBoxSeam(t, seamCase{
				repo: repo,
				env: map[string]string{
					"REGISTRY_PROXY_MANIFEST": `{"endpoint":"unix://` + filepath.Join(t.TempDir(), "proxy.sock") + `","routes":[]}`,
				},
				driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "scan")), Exit: c.exit}},
			})
			r.res.WantExit(t, c.exit)
			if got := strings.Contains(r.res.Stdout, warning); got != c.want {
				t.Errorf("stdout carries the lockfile warning = %v; want %v:\n%s", got, c.want, r.res.Stdout)
			}
			if !c.want && strings.Contains(r.res.Stdout, "still names the registry proxy Forwarder URL") {
				t.Errorf("clean repo warned about a Forwarder URL:\n%s", r.res.Stdout)
			}
		})
	}
}

// TestBoxSeamReadonlyGuardsAndForgejoCLI runs box over each access mode and
// backend, asserting on the files its guard and Forgejo CLI steps leave behind
// and on the PATH the Driver is handed.
func TestBoxSeamReadonlyGuardsAndForgejoCLI(t *testing.T) {
	const token = "s3cr3t-forgejo-token"
	cases := []struct {
		name  string
		relay bool // read-only with the outbox relay (BOX_WRITE_ENABLED off, relay capable)
		env   map[string]string
		fj    bool // an fj fake is on PATH
		// shims are the shims the run must have installed; none means the shim
		// dir must not exist.
		shims []string
		hook  bool
		// fjArgv is the one add-key call fj must have recorded; nil means none.
		fjArgv []string
	}{
		{name: "read-write github", env: map[string]string{}},
		{name: "read-only github with outbox relay", relay: true, shims: []string{"gh"}, hook: true},
		{
			name: "read-only forgejo", relay: true, fj: true,
			env: map[string]string{
				"CODE_FORGE": "forgejo", "BOX_FORGE_BACKEND": "FORGEJO",
				"FORGEJO_TOKEN": token, "FORGEJO_BASE_URL": "https://forge.example/",
			},
			shims: []string{"fj", "gh"}, hook: true,
			fjArgv: []string{"-H", "https://forge.example", "auth", "add-key", "spindrift-agent"},
		},
		{
			name: "read-write forgejo", fj: true,
			env: map[string]string{
				"CODE_FORGE": "forgejo", "BOX_FORGE_BACKEND": "FORGEJO",
				"FORGEJO_TOKEN": token, "GIT_USER_NAME": "bot",
			},
			fjArgv: []string{"-H", "https://codeberg.org", "auth", "add-key", "bot"},
		},
		{
			// Fully local is still read-only: it gets the guards.
			name: "read-only local",
			env: map[string]string{
				"BOX_WRITE_ENABLED": "", "CODE_FORGE": "local",
				"BOX_HOST_MEDIATED_REMOTE": "1", "BOX_FULLY_LOCAL": "1",
			},
			shims: []string{"gh"}, hook: true,
		},
		{
			name:  "read-only git backend",
			env:   map[string]string{"BOX_WRITE_ENABLED": "", "CODE_FORGE": "git"},
			shims: []string{"gh"},
		},
		{name: "read-only self-contained", relay: true, env: map[string]string{"SELF_CONTAINED": "1"}},
		{
			name: "read-only self-contained forgejo", relay: true, fj: true,
			env: map[string]string{
				"SELF_CONTAINED": "1", "CODE_FORGE": "forgejo", "BOX_FORGE_BACKEND": "FORGEJO",
				"FORGEJO_TOKEN": token,
			},
			fjArgv: []string{"-H", "https://codeberg.org", "auth", "add-key", "spindrift-agent"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			work := seamRepo(t)
			if c.env["SELF_CONTAINED"] == "1" {
				// Box skips branch recovery for a self-contained run, yet this work
				// kind still bundles out at settle, so the branch must exist in a
				// work tree the test clones itself, since box clones nothing.
				seamGit(t, filepath.Dir(work), "clone", "-q", seamOriginPath(work), work)
				seamGit(t, work, "checkout", "-q", "-b", seamBranch)
			}
			dir := t.TempDir()
			fjArgvRec, fjStdinRec := filepath.Join(dir, "fj.argv"), filepath.Join(dir, "fj.stdin")
			env := map[string]string{}
			// Ambient values must not stand in for the defaults the cases pin; the guard needs a non-empty GIT_USER_NAME, so the default is passed explicitly.
			mergeEnv(env, map[string]string{"GIT_USER_NAME": "spindrift-agent", "FORGEJO_TOKEN": "", "FORGEJO_BASE_URL": ""}, c.env, seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: filepath.Join(dir, "gh.rec")}))
			fakes := []string{"gh"}
			if c.fj {
				fakes = append(fakes, "fj")
				mergeEnv(env, seamtest.WriteFakeConfig(t, "fj", seamtest.FjConfig{Record: fjArgvRec, StdinRecord: fjStdinRec}))
			}
			r := runBoxSeam(t, seamCase{
				repo: work, relay: c.relay, env: env, fakes: fakes, snapshot: true,
				driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "guards"))}},
			})
			r.res.WantExit(t, 0)

			shimDir := readonlyShimDir(r.home)
			if len(c.shims) == 0 {
				if _, err := os.Stat(shimDir); !os.IsNotExist(err) {
					t.Errorf("shim dir %s exists (%v); want none", shimDir, err)
				}
			}
			for _, name := range c.shims {
				assertExecutable(t, filepath.Join(shimDir, name))
			}

			hooks := filepath.Join(work, ".git", "hooks", "pre-push")
			pushurl, _ := exec.Command("git", "-C", work, "config", "--get", "remote.origin.pushurl").Output()
			if !c.hook {
				if _, err := os.Stat(hooks); !os.IsNotExist(err) {
					t.Errorf("%s exists (%v); want no push hook", hooks, err)
				}
				if len(pushurl) != 0 {
					t.Errorf("remote.origin.pushurl = %q; want unset", pushurl)
				}
			} else {
				assertExecutable(t, hooks)
				assertExecutable(t, filepath.Join(work, ".git", "hooks", "pre-receive"))
				decoy := strings.TrimSpace(string(pushurl))
				if filepath.Base(decoy) != "readonly-push-guard.git" {
					t.Fatalf("remote.origin.pushurl = %q; want the bare decoy", decoy)
				}
				assertExecutable(t, filepath.Join(decoy, "hooks", "pre-receive"))
				assertExecutable(t, filepath.Join(decoy, "hooks", "pre-push"))
			}

			gotFj := seamtest.ReadRecord(t, fjArgvRec)
			if c.fjArgv == nil {
				if len(gotFj) != 0 {
					t.Errorf("fj ran: %v; want no call", gotFj)
				}
			} else {
				if want := [][]string{c.fjArgv}; !reflect.DeepEqual(gotFj, want) {
					t.Errorf("fj argv = %v; want %v", gotFj, want)
				}
				if got, _ := os.ReadFile(fjStdinRec); string(got) != token {
					t.Errorf("fj stdin = %q; want the token", got)
				}
				if strings.Contains(r.res.Stdout+r.res.Stderr, token) {
					t.Error("the token leaked into box's output")
				}
			}

			path := envMap(seamtest.ReadSnapshot(t, r.snapshots, 1).Env)["PATH"]
			if len(c.shims) > 0 {
				if !strings.HasPrefix(path, shimDir+string(os.PathListSeparator)) {
					t.Errorf("Driver PATH = %q; want it to start with %s", path, shimDir)
				}
			} else if strings.Contains(path, shimDir) {
				t.Errorf("Driver PATH = %q names the shim dir; want it untouched", path)
			}

			// Each shim rejects a forbidden command locally; the fake behind it
			// must never see the call.
			for _, tool := range c.shims {
				var stderr bytes.Buffer
				cmd := exec.Command(filepath.Join(shimDir, tool), "pr", "create")
				cmd.Stderr = &stderr
				if err := cmd.Run(); err == nil {
					t.Errorf("`%s pr create` through the shim succeeded; want it rejected", tool)
				}
				if !strings.Contains(stderr.String(), "PR-intent relay") {
					t.Errorf("%s shim rejection = %q; want the PR-intent relay message", tool, stderr.String())
				}
			}
		})
	}
}

// A Box missing something its dispatch needs stops in the env-guards phase,
// naming the variable, before the Driver or orchestrator runs.
func TestBoxSeamEnvGuardNamesTheMissingVariable(t *testing.T) {
	butler := map[string]string{
		"DISPATCH_KIND": "butler", "DISPATCH_KEYING": "chore", "DISPATCH_ANNOUNCE_VERB": "sweeping",
		"DISPATCH_KEY": "butler-bugs", "ISSUE_NUMBER": "", "CHORE_NAME": "bugs",
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"GH_TOKEN", map[string]string{"GH_TOKEN": ""}, "GH_TOKEN is required"},
		{"REPO_SLUG", map[string]string{"REPO_SLUG": ""}, "REPO_SLUG (owner/repo) is required"},
		{"DISPATCH_KEY", map[string]string{"DISPATCH_KEY": ""}, "DISPATCH_KEY is required"},
		{"ISSUE_NUMBER", map[string]string{"ISSUE_NUMBER": ""}, "ISSUE_NUMBER is required"},
		{"GIT_USER_NAME", map[string]string{"GIT_USER_NAME": ""}, "GIT_USER_NAME is required"},
		{"GIT_USER_EMAIL", map[string]string{"GIT_USER_EMAIL": ""}, "GIT_USER_EMAIL is required"},
		{"CHORE_NAME", mergedEnv(butler, map[string]string{"CHORE_NAME": ""}), "CHORE_NAME is required"},
		{"self-contained research on a github tracker", mergedEnv(seamResearchEnv, map[string]string{"SELF_CONTAINED": "1", "REPO_SLUG": ""}), "REPO_SLUG (owner/repo) is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runBoxSeam(t, seamCase{repo: seamRepo(t), env: c.env})
			r.res.WantExit(t, 1)
			if want := "box: env-guards: " + c.want; !strings.Contains(r.res.Stderr, want) {
				t.Errorf("stderr lacks %q:\n%s", want, r.res.Stderr)
			}
			if len(r.driverRec) != 0 || len(r.orchRec) != 0 {
				t.Errorf("driver calls %v, orchestrator calls %v; want none past the guard", r.driverRec, r.orchRec)
			}
		})
	}
}

// Fully-local and self-contained-with-no-reachable-tracker dispatches have no
// forge to resolve GH_TOKEN or REPO_SLUG against, so the guard skips both.
func TestBoxSeamEnvGuardExemptsForgelessDispatches(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"fully local", map[string]string{
			"BOX_FULLY_LOCAL": "1", "CODE_FORGE": "local", "ISSUE_TRACKER": "local",
			"BOX_HOST_MEDIATED_REMOTE": "1", "GH_TOKEN": "", "REPO_SLUG": "",
		}},
		{"self-contained research, unreachable tracker", mergedEnv(seamResearchEnv, map[string]string{
			"SELF_CONTAINED": "1", "BOX_IN_BOX_UNREACHABLE_TRACKER": "1", "ISSUE_TRACKER": "local",
			"GH_TOKEN": "", "REPO_SLUG": "",
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runBoxSeam(t, seamCase{
				repo:       seamRepo(t),
				relay:      true,
				env:        c.env,
				driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "ran"))}},
			})
			r.res.WantExit(t, 0)
			if strings.Contains(r.res.Stderr, "env-guards") {
				t.Errorf("the guard fired:\n%s", r.res.Stderr)
			}
			if len(r.driverRec) != 1 {
				t.Errorf("driver calls = %v; want the first run", r.driverRec)
			}
		})
	}
}

func mergedEnv(srcs ...map[string]string) map[string]string {
	out := map[string]string{}
	mergeEnv(out, srcs...)
	return out
}

func TestBoxSeamPrintsTheWritableStoreNotice(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t),
		env:        map[string]string{"NIX_STORE_WRITABLE": "true"},
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "store"))}},
	})
	r.res.WantExit(t, 0)
	const notice = "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues\n"
	if !strings.HasPrefix(r.res.Stdout, notice) {
		t.Errorf("stdout does not open with the store notice\n%s", r.res.Stdout)
	}
}

// seamPriorBranchRepo is a work dir whose origin holds a prior run's agent
// branch (one commit adding branch.txt unless withWork is off), optionally with
// origin/main advanced past it by a non-conflicting commit.
func seamPriorBranchRepo(t *testing.T, advanceMain, withWork bool) string {
	t.Helper()
	dir := seamRepo(t)
	scratch := seamScratch(t, dir)
	commit := func(file, msg string) {
		if err := os.WriteFile(filepath.Join(scratch, file), []byte(file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		seamGit(t, scratch, "add", file)
		seamGit(t, scratch, "commit", "-q", "-m", msg)
	}
	seamGit(t, scratch, "checkout", "-q", "-b", seamBranch)
	if withWork {
		commit("branch.txt", "prior run work")
	}
	seamGit(t, scratch, "push", "-q", "origin", seamBranch)
	seamGit(t, scratch, "checkout", "-q", "main")
	if advanceMain {
		commit("main_advance.txt", "advance main")
		seamGit(t, scratch, "push", "-q", "origin", "main")
	}
	return dir
}

// seamOriginRef is origin's refs/heads/<seamBranch> sha, or "" when absent.
func seamOriginRef(t *testing.T, work string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", seamOriginPath(work), "rev-parse", "--verify", "--quiet", "refs/heads/"+seamBranch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// seamBundleHeads is `git bundle list-heads` of the outbox bundle as ref -> sha,
// or nil when no bundle was written.
func seamBundleHeads(t *testing.T, work, outbox string) map[string]string {
	t.Helper()
	path := filepath.Join(outbox, seambundle.FileName)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	out, err := exec.Command("git", "-C", work, "bundle", "list-heads", path).Output()
	if err != nil {
		t.Fatalf("git bundle list-heads: %v", err)
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if sha, ref, ok := strings.Cut(line, " "); ok {
			heads[ref] = sha
		}
	}
	return heads
}

func seamGhAskedPRList(t *testing.T, rec string) bool {
	t.Helper()
	for _, argv := range seamtest.ReadRecord(t, rec) {
		if len(argv) >= 2 && argv[0] == "pr" && argv[1] == "list" {
			return true
		}
	}
	return false
}

// Branch recovery through the real box binary against a real origin and the
// gh fake: the prior branch is adopted, force-reset or ignored, and the result
// is read off the pushed ref or the relayed bundle.
func TestBoxSeamBranchRecovery(t *testing.T) {
	forgejo := map[string]string{
		"CODE_FORGE": "forgejo", "BOX_FORGE_BACKEND": "FORGEJO",
		"FORGEJO_TOKEN": "tok", "FORGEJO_BASE_URL": "https://forge.example",
	}
	butler := map[string]string{
		"DISPATCH_KIND": "butler", "DISPATCH_KEYING": "chore", "DISPATCH_ANNOUNCE_VERB": "sweeping",
		"DISPATCH_KEY": "butler-bugs", "ISSUE_NUMBER": "", "ISSUE_TITLE": "", "CHORE_NAME": "bugs",
		"CHORE_HEAD": "deadbeef", "CHORE_DIFF_RANGE": "", "CHORE_SLICE": "agent/entrypoint.sh",
	}
	const (
		unchanged = "unchanged" // origin still holds the prior run's tip
		absent    = "absent"    // origin never gets an agent branch
		rebased   = "rebased"   // origin holds the prior tip rebased onto origin/main
		reset     = "reset"     // origin holds origin/main itself
	)
	cases := []struct {
		name    string
		prior   bool // origin holds a prior agent branch
		advance bool // origin/main moved past it
		openPR  bool // the gh fake reports an open PR on the branch
		noWork  bool // the prior branch is at origin/main with no commits of its own
		relay   bool
		env     map[string]string
		fj      bool

		wantBranch string // the work tree's checked-out branch afterwards
		wantRef    string
		wantBundle string // "" for none, else "rebased": the bundle's branch head
		wantAsked  bool   // gh was asked `pr list`
		wantSay    string
	}{
		{name: "no prior branch", wantBranch: seamBranch, wantRef: absent},
		{
			name: "prior branch with an open PR is rebased and force-pushed", prior: true, advance: true, openPR: true,
			wantBranch: seamBranch, wantRef: rebased, wantAsked: true,
			wantSay: "open PR exists on " + seamBranch + "; skipping force-reset",
		},
		{
			name: "read-only prior branch with an open PR is relayed as a bundle", prior: true, advance: true, openPR: true, relay: true,
			wantBranch: seamBranch, wantRef: unchanged, wantBundle: rebased, wantAsked: true,
			wantSay: "open PR exists on " + seamBranch + "; skipping force-reset",
		},
		{
			name: "read-only adopted branch with nothing ahead of base relays nothing", prior: true, advance: true, openPR: true, relay: true, noWork: true,
			wantBranch: seamBranch, wantRef: unchanged, wantAsked: true,
		},
		{
			name: "stale prior branch with no open PR is force-reset", prior: true, advance: true,
			wantBranch: seamBranch, wantRef: reset, wantAsked: true,
			wantSay: "stale remote branch " + seamBranch + " found (no open PR); force-resetting to main",
		},
		{
			name: "read-only stale prior branch is left alone", prior: true, advance: true, relay: true,
			wantBranch: seamBranch, wantRef: unchanged, wantAsked: true,
			wantSay: "force-resetting to main",
		},
		{
			name: "forgejo starts fresh and never asks gh", prior: true, env: forgejo, fj: true,
			wantBranch: seamBranch, wantRef: unchanged,
			wantSay: "CODE_FORGE=forgejo: starting " + seamBranch + " fresh from origin/main",
		},
		{
			name: "git starts fresh and never asks gh", prior: true, env: map[string]string{"CODE_FORGE": "git"},
			wantBranch: seamBranch, wantRef: unchanged,
			wantSay: "CODE_FORGE=git: starting " + seamBranch + " fresh from origin/main",
		},
		{name: "research never cuts the branch", prior: true, env: seamResearchEnv, wantBranch: "main", wantRef: unchanged},
		{name: "butler never cuts the branch", prior: true, relay: true, env: butler, wantBranch: "main", wantRef: unchanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := seamRepo(t)
			if c.prior {
				repo = seamPriorBranchRepo(t, c.advance, !c.noWork)
			}
			prior := seamOriginRef(t, repo)
			dir := t.TempDir()
			ghRec := filepath.Join(dir, "gh.rec")
			ghCfg := seamtest.GhConfig{Record: ghRec}
			if c.openPR {
				ghCfg.PRs = []seamtest.GhPR{{Number: 9, URL: "https://example.test/o/r/pull/9", HeadRefName: seamBranch, BaseRefName: "main"}}
			}
			env := mergedEnv(c.env, seamtest.WriteFakeConfig(t, "gh", ghCfg))
			fakes := []string{"gh"}
			if c.fj {
				fakes = append(fakes, "fj")
				mergeEnv(env, seamtest.WriteFakeConfig(t, "fj", seamtest.FjConfig{Record: filepath.Join(dir, "fj.rec")}))
			}
			r := runBoxSeam(t, seamCase{
				repo: repo, relay: c.relay, env: env, fakes: fakes,
				driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "recovered"))}},
			})
			r.res.WantExit(t, 0)

			if got := strings.TrimSpace(seamGitOut(t, repo, "symbolic-ref", "--short", "HEAD")); got != c.wantBranch {
				t.Errorf("HEAD = %s; want %s", got, c.wantBranch)
			}
			// Only an adopted branch carries the prior run's work in the tree;
			// every other path starts from origin/main.
			_, err := os.Stat(filepath.Join(repo, "branch.txt"))
			if adopted := c.openPR && c.prior && !c.noWork; (err == nil) != adopted {
				t.Errorf("branch.txt present = %v; want %v", err == nil, adopted)
			}
			if got := seamGhAskedPRList(t, ghRec); got != c.wantAsked {
				t.Errorf("gh asked `pr list` = %v; want %v", got, c.wantAsked)
			}
			if c.wantSay != "" && !strings.Contains(r.res.Stdout, c.wantSay) {
				t.Errorf("stdout lacks %q\n%s", c.wantSay, r.res.Stdout)
			}

			mainTip := seamRevParse(t, repo, "origin/main")
			origin := seamOriginRef(t, repo)
			switch c.wantRef {
			case absent:
				if origin != "" {
					t.Errorf("origin %s = %s; want no agent branch", seamBranch, origin)
				}
			case unchanged:
				if origin != prior {
					t.Errorf("origin %s = %s; want the prior tip %s", seamBranch, origin, prior)
				}
			case reset:
				if origin != mainTip {
					t.Errorf("origin %s = %s; want origin/main %s", seamBranch, origin, mainTip)
				}
			case rebased:
				if local := seamRevParse(t, repo, seamBranch); origin != local {
					t.Errorf("origin %s = %s; want the rebased local branch %s", seamBranch, origin, local)
				}
				if parent := seamRevParse(t, repo, "origin/"+seamBranch+"~1"); parent != mainTip {
					t.Errorf("pushed branch's parent = %s; want the advanced origin/main %s", parent, mainTip)
				}
				if got := seamGitOut(t, repo, "log", "-1", "--format=%s", seamBranch); strings.TrimSpace(got) != "prior run work" {
					t.Errorf("pushed tip = %q; want the prior run's commit", got)
				}
			}

			heads := seamBundleHeads(t, repo, r.outboxDir)
			if c.wantBundle == "" {
				if heads != nil {
					t.Errorf("bundle heads = %v; want no bundle", heads)
				}
				return
			}
			if got, want := heads["refs/heads/"+seamBranch], seamRevParse(t, repo, seamBranch); got != want {
				t.Errorf("bundle head %s = %q; want the rebased local branch %s (heads %v)", seamBranch, got, want, heads)
			}
			if parent := seamRevParse(t, repo, seamBranch+"~1"); parent != mainTip {
				t.Errorf("relayed branch's parent = %s; want the advanced origin/main %s", parent, mainTip)
			}
		})
	}
}

// A failed `gh pr list` is never read as "no PR": box aborts before the Driver
// and leaves the prior branch alone rather than force-resetting a live PR.
func TestBoxSeamBranchRecoveryGhFailureAborts(t *testing.T) {
	repo := seamPriorBranchRepo(t, true, true)
	prior := seamOriginRef(t, repo)
	r := runBoxSeam(t, seamCase{
		repo:  repo,
		fakes: []string{"gh"},
		env: seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{
			Record:  filepath.Join(t.TempDir(), "gh.rec"),
			Replies: []seamtest.GhReply{{Args: []string{"pr", "list"}, Exit: 1}},
		}),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "never"))}},
	})
	r.res.WantExit(t, 1)
	for _, want := range []string{"box: branch-recovery:", "aborting to protect any open PR"} {
		if !strings.Contains(r.res.Stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.res.Stderr)
		}
	}
	if got := seamOriginRef(t, repo); got != prior {
		t.Errorf("origin %s = %s; want the prior tip %s", seamBranch, got, prior)
	}
	if len(r.driverRec) != 0 || len(r.orchRec) != 0 {
		t.Errorf("driver calls %v, orchestrator calls %v; want none past the abort", r.driverRec, r.orchRec)
	}
}

func seamGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// Settle-time bundle-out through the real box binary: whether a bundle lands
// in the outbox, and what outcome line the harness leaves behind, for each
// posture that relays code through it.
func TestBoxSeamSettleBundleOut(t *testing.T) {
	local := map[string]string{"CODE_FORGE": "local", "BOX_HOST_MEDIATED_REMOTE": "1"}
	ready := seamOutcomeLine("ready", "done")
	resolved := seamOutcomeLine("already-resolved", "dup")
	const falseReady = `status=blocked note=.*ready.*no commits exist on agent/issue-7`
	// A run whose Driver commits one file on the branch box cut for it.
	commits := func(repo, out string, exit int) []seamtest.DriverRun {
		return []seamtest.DriverRun{{Sh: seamCommitWork(repo), Stdout: seamResult(out), Exit: exit}}
	}
	cases := []struct {
		name       string
		relay      bool
		env        map[string]string
		runs       func(repo string) []seamtest.DriverRun
		wantExit   int
		wantBundle bool
		noOutbox   bool   // box never creates the outbox dir
		wantLine   string // regexp one SPINDRIFT_OUTCOME line must match; the last one when lastOnly
		lastOnly   bool
	}{
		{
			name: "local with commits", env: local, wantBundle: true,
			runs: func(repo string) []seamtest.DriverRun { return commits(repo, seamOutcomeLine("done", "x"), 0) },
		},
		{
			name: "local with commits and a driver crash keeps the exit code", env: local, wantBundle: true, wantExit: 17,
			runs: func(repo string) []seamtest.DriverRun { return commits(repo, "", 17) },
		},
		{
			name: "local with no commits after a ready claim", env: local, wantLine: falseReady,
			runs: func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Stdout: seamResult(ready)}} },
		},
		{
			name: "read-write github never bundles", noOutbox: true,
			runs: func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Stdout: seamResult(ready)}} },
		},
		{
			name: "read-only github with commits", relay: true, wantBundle: true,
			runs: func(repo string) []seamtest.DriverRun { return commits(repo, ready, 0) },
		},
		{
			name: "read-only github with no commits after a ready claim", relay: true, wantLine: falseReady,
			runs: func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Stdout: seamResult(ready)}} },
		},
		{
			name: "read-only research never bundles", noOutbox: true, relay: true, env: seamResearchEnv,
			runs: func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Stdout: seamResult("verdict")}} },
		},
		{
			name: "read-only research with a driver crash never bundles", noOutbox: true, relay: true, env: seamResearchEnv, wantExit: 17,
			runs: func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Exit: 17}} },
		},
		{
			name: "a stale ADVISE_ONLY=1 does not make a work Box advise-only", relay: true,
			env:      map[string]string{"ADVISE_ONLY": "1"},
			wantLine: falseReady,
			runs:     func(string) []seamtest.DriverRun { return []seamtest.DriverRun{{Stdout: seamResult(ready)}} },
		},
		{
			name: "read-only already-resolved with commits is demoted and bundled", relay: true, wantBundle: true, lastOnly: true,
			wantLine: `landing=agent/issue-7 status=blocked synthetic=true note=.*already-resolved but 1 commits exist on agent/issue-7.*relayed via outbox bundle \(read-only Box\)`,
			runs:     func(repo string) []seamtest.DriverRun { return commits(repo, resolved, 0) },
		},
		{
			name: "local already-resolved with commits is demoted and bundled", env: local, wantBundle: true, lastOnly: true,
			wantLine: `landing=agent/issue-7 status=blocked synthetic=true note=.*already-resolved but 1 commits exist on agent/issue-7.*relayed via outbox bundle \(no writable remote under CODE_FORGE=local\)`,
			runs:     func(repo string) []seamtest.DriverRun { return commits(repo, resolved, 0) },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := seamRepo(t)
			r := runBoxSeam(t, seamCase{
				repo: repo, relay: c.relay, env: c.env, driverRuns: c.runs(repo),
				fakes: []string{"gh"},
			})
			r.res.WantExit(t, c.wantExit)

			heads := seamBundleHeads(t, repo, r.outboxDir)
			switch {
			case c.wantBundle:
				if heads["refs/heads/"+seamBranch] != seamRevParse(t, repo, seamBranch) {
					t.Errorf("bundle heads = %v; want %s at the branch tip", heads, seamBranch)
				}
				bundle := filepath.Join(r.outboxDir, seambundle.FileName)
				if out, err := exec.Command("git", "-C", repo, "bundle", "verify", bundle).CombinedOutput(); err != nil {
					t.Errorf("git bundle verify: %v\n%s", err, out)
				}
			case heads != nil:
				t.Errorf("bundle heads = %v; want no bundle", heads)
			}
			if _, err := os.Stat(filepath.Join(r.outboxDir, seambundle.FileName)); c.wantBundle != (err == nil) {
				t.Errorf("bundle present = %v; want %v", err == nil, c.wantBundle)
			}
			if _, err := os.Stat(r.outboxDir); c.noOutbox && err == nil {
				t.Errorf("outbox %s exists; want it never created", r.outboxDir)
			}

			if c.wantLine == "" {
				return
			}
			lines := outcomeLines(r.res.Stdout)
			if c.lastOnly && len(lines) > 0 {
				lines = lines[len(lines)-1:]
			}
			re := regexp.MustCompile(c.wantLine)
			for _, l := range lines {
				if re.MatchString(l) {
					return
				}
			}
			t.Errorf("no outcome line matches %q; lines %q", c.wantLine, lines)
		})
	}
}

// An unrecognised dispatch kind fails closed rather than defaulting to a
// posture, and before it clones or creates anything.
func TestBoxSeamUnrecognizedDispatchKindFails(t *testing.T) {
	repo := seamRepo(t)
	r := runBoxSeam(t, seamCase{
		repo:       repo,
		env:        map[string]string{"DISPATCH_KIND": "bogus-kind"},
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "never"))}},
	})
	r.res.WantExit(t, 1)
	if !strings.Contains(r.res.Stderr, "bogus-kind") {
		t.Errorf("stderr does not name the kind:\n%s", r.res.Stderr)
	}
	if len(r.driverRec) != 0 {
		t.Errorf("driver calls %v; want none", r.driverRec)
	}
	if _, err := os.Stat(r.outboxDir); err == nil {
		t.Errorf("outbox %s exists after a rejected kind", r.outboxDir)
	}
	if _, err := os.Stat(repo); err == nil {
		t.Errorf("work dir %s exists after a rejected kind", repo)
	}
}

// seamGhRecord is a gh fake config of its own, so a test can read back what
// the run asked gh.
func seamGhRecord(t *testing.T) (map[string]string, string) {
	t.Helper()
	rec := filepath.Join(t.TempDir(), "gh.rec")
	return seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: rec}), rec
}

func TestBoxSeamClonesTheTargetOntoTheAgentBranch(t *testing.T) {
	repo := seamRepo(t)
	ghEnv, ghRec := seamGhRecord(t)
	r := runBoxSeam(t, seamCase{
		repo: repo, env: ghEnv, fakes: []string{"gh"},
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "clone"))}},
	})
	r.res.WantExit(t, 0)
	if got := strings.TrimSpace(seamGitOut(t, repo, "symbolic-ref", "--short", "HEAD")); got != seamBranch {
		t.Errorf("HEAD = %s; want the agent branch %s", got, seamBranch)
	}
	for key, want := range map[string]string{"user.name": "spindrift-agent", "user.email": "agent@example.com"} {
		if got := strings.TrimSpace(seamGitOut(t, repo, "config", "--local", key)); got != want {
			t.Errorf("repo-local %s = %q; want %q", key, got, want)
		}
	}
	if got := seamtest.ReadRecord(t, ghRec); len(got) == 0 || !reflect.DeepEqual(got[0], []string{"auth", "setup-git"}) {
		t.Errorf("gh calls = %q; want `auth setup-git` first", got)
	}
}

func TestBoxSeamLocalForgeClonesTheMountWithoutGh(t *testing.T) {
	repo := seamRepo(t)
	ghEnv, ghRec := seamGhRecord(t)
	env := map[string]string{"CODE_FORGE": "local", "BOX_HOST_MEDIATED_REMOTE": "1"}
	mergeEnv(env, ghEnv)
	r := runBoxSeam(t, seamCase{
		repo: repo, env: env, fakes: []string{"gh"},
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "local"))}},
	})
	r.res.WantExit(t, 0)
	if got := strings.TrimSpace(seamGitOut(t, repo, "log", "-1", "--format=%s", "origin/main")); got != "base" {
		t.Errorf("origin/main tip = %q; want the mount's base commit", got)
	}
	for _, argv := range seamtest.ReadRecord(t, ghRec) {
		if len(argv) >= 2 && argv[0] == "auth" && argv[1] == "setup-git" {
			t.Errorf("CODE_FORGE=local ran gh auth setup-git: %q", argv)
		}
	}
}

func TestBoxSeamSelfContainedClonesNothing(t *testing.T) {
	repo := seamRepo(t)
	r := runBoxSeam(t, seamCase{
		repo: repo, relay: true,
		env: mergedEnv(seamResearchEnv, map[string]string{
			"SELF_CONTAINED": "1", "ISSUE_TRACKER": "local", "BOX_TRACKER_AXIS_READ": "LOCAL",
			"BOX_TRACKER_AXIS_WRITE": "", "BOX_IN_BOX_UNREACHABLE_TRACKER": "1", "GH_TOKEN": "", "REPO_SLUG": "",
		}),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "self-contained"))}},
	})
	r.res.WantExit(t, 0)
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("work dir missing after a self-contained run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		t.Errorf("a self-contained run cloned a repo into %s", repo)
	}
}

// The assembled work prompt tells the agent how to publish: rebase before the
// push, retry a rejected push once, report a persistent failure as blocked
// with a comment, and treat a genuine workflow-file change as a hard stop.
func TestBoxSeamPromptCarriesPushDiscipline(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(seamOutcomeLine("done", "prompt"))}},
	})
	r.res.WantExit(t, 0)
	prompt := r.assembledPrompt(t)
	for _, want := range []string{
		"git rebase", "git fetch", // rebase onto base before pushing
		"rejected", "retry", // one retry on a rejected push
		"status=blocked", "gh issue comment", // a persistent push failure
		".github/workflows", "workflow", // workflow changes are a hard stop
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("assembled prompt lacks %q", want)
		}
	}
}
