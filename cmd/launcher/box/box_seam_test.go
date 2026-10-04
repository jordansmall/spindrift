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

const seamPrompt = "implement the thing"

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
	// driverRuns script the Driver: the first is the first Driver run box
	// performs, the rest are its corrective resumes.
	driverRuns []seamtest.DriverRun
}

type seamRun struct {
	res       seamtest.Result
	driverRec [][]string
	orchRec   [][]string
	outboxDir string
	handoff   string
	sessionID string
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
	write := func(name, content string) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prompt := write("prompt.md", seamPrompt+"\n")
	handoff := write("handoff.json", fmt.Sprintf(`{"Driver":"claude","SessionMode":"initial","PromptFile":%q,"ReviewPromptFile":"/review.md","Other":1}`, prompt))
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
	write(filepath.Join("home", ".claude", "projects", "x", id+".jsonl"), "")

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
	mergeEnv(env,
		seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{Record: driverRec, Runs: c.driverRuns}),
		seamtest.WriteFakeConfig(t, "orchestrator", seamtest.OrchestratorConfig{Record: orchRec}))

	res := seamtest.Run(t, seamtest.Cmd{
		Bin: box,
		Args: []string{
			"--handoff-file", handoff,
			"--work-dir", c.repo,
			"--outbox-dir", outbox,
		},
		Env:      env,
		PathDirs: []string{seamtest.InstallFakes(t, "claude", "orchestrator")},
	})
	return seamRun{
		res:       res,
		driverRec: seamtest.ReadRecord(t, driverRec),
		orchRec:   seamtest.ReadRecord(t, orchRec),
		outboxDir: outbox,
		handoff:   handoff,
		sessionID: id,
	}
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
// orchestrator was given and checks the shared original still names a review
// prompt.
func (r seamRun) assertResumeHandoffStripsReview(t *testing.T) {
	t.Helper()
	if len(r.orchRec) != 2 {
		t.Fatalf("orchestrator runs = %d; want the first run and one resume", len(r.orchRec))
	}
	handoffArg := func(argv []string) string {
		for i, a := range argv {
			if a == "--handoff-file" {
				return argv[i+1]
			}
		}
		return ""
	}
	if got := handoffArg(r.orchRec[0]); got != r.handoff {
		t.Errorf("first run handoff = %q; want the shared %q as-is", got, r.handoff)
	}
	path := handoffArg(r.orchRec[1])
	if path == "" || path == r.handoff {
		t.Fatalf("resume handoff = %q; want a copy distinct from %q", path, r.handoff)
	}
	var got map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["ReviewPromptFile"] != "" || got["Driver"] != "claude" || got["Other"] != float64(1) {
		t.Errorf("resume handoff = %v; want ReviewPromptFile cleared and the rest kept", got)
	}
	orig, _ := os.ReadFile(r.handoff)
	if !strings.Contains(string(orig), `"/review.md"`) {
		t.Errorf("shared handoff was rewritten: %s", orig)
	}
}

// wantFirstRun checks the Driver's first invocation: the session pinned, then
// the assembled prompt.
func (r seamRun) wantFirstRun(t *testing.T) {
	t.Helper()
	if got, want := r.firstRun(t), []string{"--session-id", r.sessionID, "-p", seamPrompt}; !reflect.DeepEqual(got, want) {
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
			"DISPATCH_KEY": "butler-bugs", "CHORE_NAME": "bugs",
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
			write := func(name, content string) string {
				p := filepath.Join(root, name)
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			prompt := write("prompt.md", seamPrompt+"\n")
			handoff := write("handoff.json", fmt.Sprintf(`{"Driver":"claude","SessionMode":"initial","PromptFile":%q}`, prompt))

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
				Args:     []string{"--handoff-file", handoff, "--work-dir", workDir, "--outbox-dir", outbox},
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
