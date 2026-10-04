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
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	seamGit(t, dir, "init", "-q", "-b", "main")
	seamGit(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	seamGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	seamGit(t, dir, "checkout", "-q", "-b", seamBranch)
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

// seamResumeSessionFile renders the --resume-session-file box is handed by
// running the nix-rendered claude Driver preamble's _driver_session_flags,
// against a HOME that holds the session's transcript.
func seamResumeSessionFile(t *testing.T) (path, id string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	// The preamble uses compgen, which the non-interactive nixpkgs bash omits.
	if exec.Command(bash, "-c", "type compgen").Run() != nil {
		t.Skip("bash has no compgen builtin")
	}
	preamble := seamtest.Path(t, "driver-preamble.sh")
	id = seamSessionID()
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", `source "$1" && _driver_session_flags resume`, "bash", preamble)
	cmd.Env = append(os.Environ(), "HOME="+home, "REPO_SLUG="+seamSlug, "ISSUE_NUMBER="+seamIssue)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("render resume session flags: %v", err)
	}
	if got, want := strings.TrimSpace(string(out)), "--resume "+id; got != want {
		t.Fatalf("rendered session flags = %q; want %q", got, want)
	}
	path = filepath.Join(t.TempDir(), "resume-session")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, id
}

// TestSessionFlagsParity pins the Go Driver's SessionFlags byte for byte to
// the nix-rendered preamble's _driver_session_flags.
func TestSessionFlagsParity(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	if exec.Command(bash, "-c", "type compgen").Run() != nil {
		t.Skip("bash has no compgen builtin")
	}
	preamble := seamtest.Path(t, "driver-preamble.sh")
	d, err := driver.New("claude")
	if err != nil {
		t.Fatal(err)
	}
	absent := t.TempDir()
	present := t.TempDir()
	proj := filepath.Join(present, ".claude", "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, seamSessionID()+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mode, home string }{
		{"initial", absent}, {"resume", present}, {"resume", absent}, {"", present},
	} {
		cmd := exec.Command(bash, "-c", `source "$1" && _driver_session_flags "$2"`, "bash", preamble, c.mode)
		cmd.Env = append(os.Environ(), "HOME="+c.home, "REPO_SLUG="+seamSlug, "ISSUE_NUMBER="+seamIssue)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("render %q: %v", c.mode, err)
		}
		if got := d.SessionFlags(c.mode, seamSlug, seamIssue, c.home); got != string(out) {
			t.Errorf("SessionFlags(%q, home=%s) = %q; preamble rendered %q", c.mode, c.home, got, out)
		}
	}
}

type seamCase struct {
	repo           string
	relay          bool
	outcomeLine    string // the first run's, already printed by the entrypoint
	textLog        string
	streamLog      string
	driverRuns     []seamtest.DriverRun
	orchestratorRC int
}

type seamRun struct {
	res        seamtest.Result
	driverRec  [][]string
	orchRec    [][]string
	outboxDir  string
	handoff    string
	sessionID  string
	sessionArg string
}

func runBoxSeam(t *testing.T, c seamCase) seamRun {
	t.Helper()
	box := seamtest.Build(t, "./box")
	sessionFile, id := seamResumeSessionFile(t)

	tmp := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	handoff := write("handoff.json", `{"Driver":"claude","ReviewPromptFile":"/review.md","Other":1}`)
	outbox := filepath.Join(tmp, "outbox")
	driverRec := filepath.Join(tmp, "driver.rec")
	orchRec := filepath.Join(tmp, "orch.rec")

	env := map[string]string{
		"TMPDIR":                 t.TempDir(),
		"DISPATCH_KIND":          "work",
		"DISPATCH_KEY":           seamIssue,
		"ISSUE_NUMBER":           seamIssue,
		"BRANCH":                 seamBranch,
		"BASE_BRANCH":            "main",
		"RUN_NONCE":              seamNonce,
		"BOX_SIGNAL_CARRIER":     "log",
		"BOX_WRITE_ENABLED":      "1",
		"MAX_REBASE_ATTEMPTS":    "1",
		"TRANSIENT_BACKOFF_SECS": "0",
		"HOLD_JITTER_SECS":       "0",
	}
	// An ambient Box env must not leak into the knobs this test pins.
	for _, name := range promptassembly.BoxEnvVarNames {
		if _, set := env[name]; !set {
			env[name] = ""
		}
	}
	if c.relay {
		env["BOX_WRITE_ENABLED"] = ""
		env["BOX_OUTBOX_RELAY_CAPABLE"] = "1"
	}
	for k, v := range seamtest.WriteFakeConfig(t, "claude", seamtest.DriverConfig{Record: driverRec, Runs: c.driverRuns}) {
		env[k] = v
	}
	for k, v := range seamtest.WriteFakeConfig(t, "orchestrator", seamtest.OrchestratorConfig{Record: orchRec}) {
		env[k] = v
	}

	res := seamtest.Run(t, seamtest.Cmd{
		Bin: box,
		Args: []string{
			"--driver-exit-code", "0",
			"--outcome-line", c.outcomeLine,
			"--stream-log", write("stream.log", c.streamLog),
			"--driver-text-log", write("text.log", c.textLog),
			"--handoff-file", handoff,
			"--resume-session-file", sessionFile,
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

// strippedHandoff reads the handoff the resume's orchestrator was given and
// checks the shared original still names a review prompt.
func (r seamRun) assertResumeHandoffStripsReview(t *testing.T) {
	t.Helper()
	if len(r.orchRec) != 1 {
		t.Fatalf("orchestrator runs = %d; want 1", len(r.orchRec))
	}
	var path string
	for i, a := range r.orchRec[0] {
		if a == "--handoff-file" {
			path = r.orchRec[0][i+1]
		}
	}
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

func TestBoxSeamOutcomePresentRunsNoDriver(t *testing.T) {
	line := seamOutcomeLine("done", "first")
	r := runBoxSeam(t, seamCase{
		repo:        seamRepo(t, 0),
		outcomeLine: line,
		textLog:     line + "\n",
		streamLog:   seamResult(line),
	})
	r.res.WantExit(t, 0)
	if len(r.driverRec) != 0 || len(r.orchRec) != 0 {
		t.Errorf("driver calls %v, orchestrator calls %v; want none", r.driverRec, r.orchRec)
	}
	if got := outcomeLines(r.res.Stdout); len(got) != 0 {
		t.Errorf("box printed outcome lines %q; the entrypoint already printed the first run's", got)
	}
}

func TestBoxSeamMissingOutcomeIsNudgedOnce(t *testing.T) {
	resumed := seamOutcomeLine("done", "after nudge")
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		textLog:    "all finished\n",
		streamLog:  seamResult("all finished"),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult(resumed)}},
	})
	r.res.WantExit(t, 0)

	nudge, err := markergate.RenderNudgePrompt(markergate.NudgeConfig{Marker: markergate.MarkerOutcome, Issue: seamIssue, Landing: seamBranch})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--resume", r.sessionID, "-p", nudge}}
	if !reflect.DeepEqual(r.driverRec, want) {
		t.Errorf("driver calls = %q; want %q", r.driverRec, want)
	}
	r.assertResumeHandoffStripsReview(t)
	if got := outcomeLines(r.res.Stdout); !reflect.DeepEqual(got, []string{resumed}) {
		t.Errorf("outcome lines = %q; want only the resumed pass's %q", got, resumed)
	}
}

func TestBoxSeamUnrecoveredOutcomeIsBackstopped(t *testing.T) {
	r := runBoxSeam(t, seamCase{
		repo:       seamRepo(t, 0),
		textLog:    "all finished\n",
		streamLog:  seamResult("all finished"),
		driverRuns: []seamtest.DriverRun{{Stdout: seamResult("still no marker")}},
	})
	r.res.WantExit(t, 0)

	if len(r.driverRec) != 1 || len(r.driverRec[0]) != 4 || !reflect.DeepEqual(r.driverRec[0][:2], []string{"--resume", r.sessionID}) || r.driverRec[0][2] != "-p" {
		t.Errorf("driver calls = %q; want exactly one `--resume %s -p <nudge>`", r.driverRec, r.sessionID)
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
		repo:        seamRepo(t, 1),
		relay:       true,
		outcomeLine: first,
		textLog:     first + "\n",
		streamLog:   seamResult(first),
		driverRuns:  []seamtest.DriverRun{{Stdout: seamResult(intent)}},
	})
	r.res.WantExit(t, 0)

	nudge, err := markergate.RenderNudgePrompt(markergate.NudgeConfig{Marker: markergate.MarkerPRIntent, Nonce: seamNonce, OriginalOutcomeLine: first})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"--resume", r.sessionID, "-p", nudge}}
	if !reflect.DeepEqual(r.driverRec, want) {
		t.Errorf("driver calls = %q; want %q", r.driverRec, want)
	}
	r.assertResumeHandoffStripsReview(t)
	if got := r.orchRec[0]; got[len(got)-2] != "--manifest-path" || got[len(got)-1] != r.outboxDir+"/manifest.json" {
		t.Errorf("orchestrator argv = %q; want a relay Box to pass --manifest-path last", got)
	}
	if strings.Contains(r.res.Stdout, "nudge exhausted") {
		t.Errorf("stdout reports the nudge exhausted despite a verified marker:\n%s", r.res.Stdout)
	}
	if got := outcomeLines(r.res.Stdout); len(got) != 0 {
		t.Errorf("outcome lines = %q; the first run's line must not be reprinted", got)
	}
	if _, err := os.Stat(filepath.Join(r.outboxDir, seambundle.FileName)); err != nil {
		t.Errorf("relay Box left no bundle in the outbox: %v", err)
	}
}

func TestBoxSeamPRIntentNudgeExhausted(t *testing.T) {
	first := seamOutcomeLine("ready", "done")
	r := runBoxSeam(t, seamCase{
		repo:        seamRepo(t, 1),
		relay:       true,
		outcomeLine: first,
		textLog:     first + "\n",
		streamLog:   seamResult(first),
		driverRuns:  []seamtest.DriverRun{{Stdout: seamResult("no marker either")}},
	})
	r.res.WantExit(t, 0)
	if len(r.driverRec) != 1 {
		t.Errorf("driver calls = %q; want exactly one resume", r.driverRec)
	}
	if !strings.Contains(r.res.Stdout, "read-only PR-intent nudge exhausted after 1 attempt") {
		t.Errorf("stdout lacks the exhausted-nudge op line:\n%s", r.res.Stdout)
	}
}
