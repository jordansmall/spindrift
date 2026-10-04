package main

// Fake-driver-exec loop tests for issue #3246's bounded delta-review gate
// (runDeltaReviewGate, run.go). They reshape
// reviewPassFakeDriverBodyWithLandCommit's fixture (run_test.go): a temp git
// repo, an implement, review(BLOCK), fix, review(APPROVE), land sequence whose
// land pass commits a file the approving round may or may not have named.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/passmanifest"
)

// decodeSpindriftOps decodes every well-formed "spindrift_op" line in stdout,
// in emission order. collectPassUsageOps (run_test.go) decodes pass_usage
// alone; these tests also need pass_start, delta_review_trigger, and verdict.
func decodeSpindriftOps(t *testing.T, stdout string) []claude.SpindriftOp {
	t.Helper()
	var ops []claude.SpindriftOp
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev claude.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SpindriftOp != nil {
			ops = append(ops, *ev.SpindriftOp)
		}
	}
	return ops
}

// teeStreamJSONStep prints text into the fake driver-exec's own log through
// $DRIVER_LOG_PATH, the way a real stream-json Driver does.
func teeStreamJSONStep(text string) string {
	return "printf '%s' '" + text + "' | tee -a \"$DRIVER_LOG_PATH\""
}

// deltaReviewFakeDriverBody scripts the implement, review(BLOCK), fix,
// review(APPROVE), land sequence with the knobs issue #3246's gate tests need.
// Call 5 always commits landed-file.txt so computeLandDelta has a non-zero
// delta, and writes decisionsContent first unless decisionsPath is "". An empty
// deltaReviewStep degrades call 6 to a no-op, the fail-open "no verdict" case.
func deltaReviewFakeDriverBody(callLog, round2NonBlocking, decisionsPath, decisionsContent, deltaReviewStep string) string {
	if deltaReviewStep == "" {
		deltaReviewStep = ":"
	}
	decisionsStep := ":"
	if decisionsPath != "" {
		decisionsStep = "printf '%s' " + fmtQuote(decisionsContent) + " > " + fmtQuote(decisionsPath)
	}
	blockLine := streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:1 -- bug\\n\\n## Non-blocking\\n- none")
	approveLine := streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- " + round2NonBlocking)
	outcomeLine := streamJSONFinalResult("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc")

	return `: > "$DRIVER_LOG_PATH"
n=$(wc -l < "` + callLog + `")
case "$n" in
  2) ` + teeStreamJSONStep(blockLine) + ` ;;
  3) : ;;
  4) ` + teeStreamJSONStep(approveLine) + ` ;;
  5) ` + decisionsStep + ` && printf 'landed content\n' > landed-file.txt && git add landed-file.txt && git commit -m "land" >/dev/null && ` + teeStreamJSONStep(outcomeLine) + ` ;;
  6) ` + deltaReviewStep + ` ;;
esac
exit 0
`
}

// fmtQuote wraps s in double quotes. Its arguments never contain quotes, and a
// plain wrap keeps fmt.Sprintf's format-verb parsing away from the literal "%"
// characters in the surrounding shell text.
func fmtQuote(s string) string {
	return `"` + s + `"`
}

type deltaReviewLoopFixture struct {
	dir              string
	logPath          string
	driverName       string
	callLog          string
	promptFile       string
	reviewPromptFile string
	stateFile        string
	manifestPath     string
	decisionsPath    string
}

func newDeltaReviewLoopFixture(t *testing.T) deltaReviewLoopFixture {
	t.Helper()
	dir := t.TempDir()
	f := deltaReviewLoopFixture{
		dir:              dir,
		logPath:          filepath.Join(dir, "stream.log"),
		driverName:       "claude",
		callLog:          filepath.Join(dir, "calls.log"),
		promptFile:       filepath.Join(dir, "prompt.txt"),
		reviewPromptFile: filepath.Join(dir, "review-prompt.txt"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		manifestPath:     filepath.Join(dir, "pass-manifest.json"),
		decisionsPath:    filepath.Join(dir, "decisions.md"),
	}
	if err := os.WriteFile(f.promptFile, []byte("ORIGINAL PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.reviewPromptFile, []byte("REVIEW PROMPT TEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f deltaReviewLoopFixture) config(maxSlices int) config {
	return config{
		promptFile:       f.promptFile,
		reviewPromptFile: f.reviewPromptFile,
		logPath:          f.logPath,
		driver:           f.driverName,
		stateFile:        f.stateFile,
		manifestPath:     f.manifestPath,
		maxReviewRounds:  3,
		maxSlices:        maxSlices,
	}
}

// finalOutcomeLine is the outcome line the entrypoint's log scan would find in
// the config's logPath once run returns: the last one among the log's
// final-result events.
func (f deltaReviewLoopFixture) finalOutcomeLine(t *testing.T) string {
	t.Helper()
	d, err := driver.New(f.driverName)
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	text, err := d.ResultText(f.logPath)
	if err != nil {
		t.Fatalf("ResultText: %v", err)
	}
	return outcome.ExtractOutcomeLine(outcome.StripResultText(text))
}

const deltaReviewLandOutcomeLine = "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"

func (f deltaReviewLoopFixture) readManifest(t *testing.T) []passmanifest.Entry {
	t.Helper()
	b, err := os.ReadFile(f.manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest []passmanifest.Entry
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v (content: %s)", err, b)
	}
	return manifest
}

func (f deltaReviewLoopFixture) callCount(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	return len(strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
}

// TestRunDeltaReviewGateSkipsWhenDeltaConfinedToFindings pins issue #3246 AC1:
// round 2's APPROVE findings name landed-file.txt, the same file the land pass
// commits, so deltareview.Decide finds nothing beyond the findings and the gate
// declines the extra pass.
func TestRunDeltaReviewGateSkipsWhenDeltaConfinedToFindings(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "landed-file.txt:1 — reviewed, no issue", "", "", ""))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(10), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.callCount(t); got != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (no delta-review pass)", got)
	}

	ops := decodeSpindriftOps(t, stdout.String())
	for _, op := range ops {
		if op.Op == "pass_start" && op.Role == passmachine.KindDeltaReview.String() {
			t.Errorf("stdout carries a delta-review pass_start op, want none: %+v", op)
		}
	}
	var sawSkip bool
	for _, op := range ops {
		if op.Op == "delta_review_trigger" {
			if op.Decision != "skip" {
				t.Errorf("delta_review_trigger op = %+v, want decision %q", op, "skip")
			}
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Error("stdout carries no delta_review_trigger op, want one with decision \"skip\"")
	}

	manifest := f.readManifest(t)
	if len(manifest) != 5 {
		t.Fatalf("manifest entry count = %d, want 5 (no delta-review entry)", len(manifest))
	}
	for _, e := range manifest {
		if e.Kind == passmachine.KindDeltaReview.ManifestKind() {
			t.Errorf("manifest carries a delta-review entry, want none: %+v", e)
		}
	}

	if got := f.finalOutcomeLine(t); got != deltaReviewLandOutcomeLine {
		t.Errorf("log's final outcome = %q, want the land pass's %q", got, deltaReviewLandOutcomeLine)
	}
}

// TestRunDeltaReviewGateFiresAndApproves pins issue #3246 AC2: the land pass
// commits landed-file.txt, which round 2's APPROVE findings never named, so the
// gate fires exactly one delta-review pass. An APPROVE verdict there settles the
// run as it would without the gate, with no corrective outcome line.
func TestRunDeltaReviewGateFiresAndApproves(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	deltaApprove := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none"))
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", deltaApprove))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(10), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.callCount(t); got != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (one delta-review pass)", got)
	}

	ops := decodeSpindriftOps(t, stdout.String())
	var deltaReviewStarts int
	for _, op := range ops {
		if op.Op == "pass_start" && op.Role == passmachine.KindDeltaReview.String() {
			deltaReviewStarts++
		}
	}
	if deltaReviewStarts != 1 {
		t.Errorf("delta-review pass_start op count = %d, want exactly 1 (never a loop)", deltaReviewStarts)
	}
	var sawFire, sawUsage bool
	for _, op := range ops {
		if op.Op == "delta_review_trigger" && op.Decision == "fire" {
			sawFire = true
		}
		if op.Op == "pass_usage" && op.Role == passmachine.KindDeltaReview.String() {
			sawUsage = true
		}
	}
	if !sawFire {
		t.Error("stdout carries no delta_review_trigger op with decision \"fire\"")
	}
	if !sawUsage {
		t.Error("stdout carries no pass_usage op for the delta-review pass")
	}

	if strings.Contains(stdout.String(), "status=blocked") {
		t.Errorf("stdout = %q, want no corrective status=blocked line on an APPROVEd delta review", stdout.String())
	}

	manifest := f.readManifest(t)
	if len(manifest) != 6 {
		t.Fatalf("manifest entry count = %d, want 6", len(manifest))
	}
	last := manifest[5]
	if last.Kind != passmachine.KindDeltaReview.ManifestKind() {
		t.Errorf("manifest[5].Kind = %q, want %q", last.Kind, passmachine.KindDeltaReview.ManifestKind())
	}
	if last.Verdict != "APPROVE" {
		t.Errorf("manifest[5].Verdict = %q, want %q", last.Verdict, "APPROVE")
	}

	// The delta-review pass writes its own log, so the land pass's stream is
	// what the entrypoint still finds in cfg.logPath.
	if got := f.finalOutcomeLine(t); got != deltaReviewLandOutcomeLine {
		t.Errorf("log's final outcome = %q, want the land pass's %q", got, deltaReviewLandOutcomeLine)
	}
}

// TestRunDeltaReviewGateFiresAndBlocks pins issue #3246 AC3: the same fixture as
// TestRunDeltaReviewGateFiresAndApproves, except the delta-review pass returns
// BLOCK. That is terminal, with a corrective status=blocked outcome line
// replacing the land pass's status=ready claim and no further pass after it.
func TestRunDeltaReviewGateFiresAndBlocks(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	deltaBlock := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- landed-file.txt:1 -- undeclared change\\n\\n## Non-blocking\\n- none"))
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", deltaBlock))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(10), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.callCount(t); got != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (one delta-review pass, no fix lap)", got)
	}

	ops := decodeSpindriftOps(t, stdout.String())
	var deltaReviewStartIdx = -1
	var deltaReviewStarts int
	for i, op := range ops {
		if op.Op == "pass_start" && op.Role == passmachine.KindDeltaReview.String() {
			deltaReviewStarts++
			deltaReviewStartIdx = i
		}
	}
	if deltaReviewStarts != 1 {
		t.Fatalf("delta-review pass_start op count = %d, want exactly 1", deltaReviewStarts)
	}
	for _, op := range ops[deltaReviewStartIdx+1:] {
		if op.Op == "pass_start" {
			t.Errorf("a pass_start op follows the delta-review pass_start, want none (no new fix lap): %+v", op)
		}
	}

	manifest := f.readManifest(t)
	if len(manifest) != 6 {
		t.Fatalf("manifest entry count = %d, want 6", len(manifest))
	}
	if got := manifest[5].Verdict; got != "BLOCK" {
		t.Errorf("manifest[5].Verdict = %q, want %q", got, "BLOCK")
	}

	// The corrective line is appended to the land pass's stream log, which the
	// entrypoint reads in place of the ready line; stdout would only duplicate
	// the line the entrypoint prints itself. It copies Issue and Landing
	// verbatim from the land pass's line (call 5).
	if strings.Contains(stdout.String(), "status=blocked") {
		t.Errorf("stdout = %q, want no status=blocked line (the log carries it)", stdout.String())
	}
	correctiveLine := f.finalOutcomeLine(t)
	if correctiveLine == "" || correctiveLine == deltaReviewLandOutcomeLine {
		t.Fatalf("log's final outcome = %q, want a corrective status=blocked line", correctiveLine)
	}
	parsed, err := outcome.Parse(correctiveLine)
	if err != nil {
		t.Fatalf("outcome.Parse(%q): %v", correctiveLine, err)
	}
	if parsed.Status != outcome.StatusBlocked {
		t.Errorf("corrective outcome Status = %q, want %q", parsed.Status, outcome.StatusBlocked)
	}
	if parsed.Issue != "7" {
		t.Errorf("corrective outcome Issue = %q, want the land pass's own %q", parsed.Issue, "7")
	}
	if parsed.Landing != "agent/issue-7" {
		t.Errorf("corrective outcome Landing = %q, want the land pass's own %q", parsed.Landing, "agent/issue-7")
	}
	if parsed.Note == "" || strings.Contains(parsed.Note, "\n") {
		t.Errorf("corrective outcome Note = %q, want a single non-empty line", parsed.Note)
	}
	if !strings.Contains(strings.ToLower(parsed.Note), "delta review") {
		t.Errorf("corrective outcome Note = %q, want it to mention the delta review", parsed.Note)
	}
}

// TestRunDeltaReviewGateFiresOnGateWorkDeclarationDespiteConfinedDelta pins
// issue #3246 AC4 and #3245's declaration contract: the land pass's decisions.md
// declares gate-discovered work while the delta stays confined to what round 2's
// findings named. Only that content separates this test from the skip case
// TestRunDeltaReviewGateSkipsWhenDeltaConfinedToFindings, and it fires the gate.
func TestRunDeltaReviewGateFiresOnGateWorkDeclarationDespiteConfinedDelta(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "landed-file.txt:1 — reviewed, no issue", f.decisionsPath, "Gate-discovered work: inlined a lint nit fix.", ""))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := f.config(10)
	cfg.decisionsPath = f.decisionsPath

	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.callCount(t); got != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (the gate-work declaration fires the gate despite a confined delta)", got)
	}

	ops := decodeSpindriftOps(t, stdout.String())
	var sawDeltaReviewStart, sawFire bool
	for _, op := range ops {
		if op.Op == "pass_start" && op.Role == passmachine.KindDeltaReview.String() {
			sawDeltaReviewStart = true
		}
		if op.Op == "delta_review_trigger" && op.Decision == "fire" {
			sawFire = true
		}
	}
	if !sawDeltaReviewStart {
		t.Error("stdout carries no delta-review pass_start op, want the gate-work declaration to fire it")
	}
	if !sawFire {
		t.Error("stdout carries no delta_review_trigger op with decision \"fire\"")
	}

	manifest := f.readManifest(t)
	if len(manifest) != 6 {
		t.Fatalf("manifest entry count = %d, want 6", len(manifest))
	}
	if manifest[5].Kind != passmachine.KindDeltaReview.ManifestKind() {
		t.Errorf("manifest[5].Kind = %q, want %q", manifest[5].Kind, passmachine.KindDeltaReview.ManifestKind())
	}
}

// TestRunDeltaReviewGateCappedBySlicesSkipsExtraPass pins issue #3246 AC5: the
// delta would otherwise fire, but maxSlices equals the land pass's own pass
// number (5), so passmachine.ExtraPassAllowed reports the cap already spent and
// the run settles as if the gate had never fired.
func TestRunDeltaReviewGateCappedBySlicesSkipsExtraPass(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", ""))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(5), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.callCount(t); got != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 (the cap skips the extra pass before it invokes driver-exec)", got)
	}

	ops := decodeSpindriftOps(t, stdout.String())
	for _, op := range ops {
		if op.Op == "pass_start" && op.Role == passmachine.KindDeltaReview.String() {
			t.Errorf("stdout carries a delta-review pass_start op, want none (capped): %+v", op)
		}
	}
	var sawCapSkip bool
	for _, op := range ops {
		if op.Op == "delta_review_trigger" && op.Decision == "skip" && strings.Contains(op.Reason, "capped") {
			sawCapSkip = true
			if !strings.Contains(op.Reason, "max slices") {
				t.Errorf("delta_review_trigger cap-skip Reason = %q, want it to name the cap (max slices)", op.Reason)
			}
		}
	}
	if !sawCapSkip {
		t.Error("stdout carries no delta_review_trigger op with decision \"skip\" naming the cap")
	}
	if got := f.finalOutcomeLine(t); got != deltaReviewLandOutcomeLine {
		t.Errorf("log's final outcome = %q, want the land pass's %q", got, deltaReviewLandOutcomeLine)
	}

	manifest := f.readManifest(t)
	if len(manifest) != 5 {
		t.Fatalf("manifest entry count = %d, want 5 (no delta-review entry)", len(manifest))
	}
	for _, e := range manifest {
		if e.Kind == passmachine.KindDeltaReview.ManifestKind() {
			t.Errorf("manifest carries a delta-review entry, want none: %+v", e)
		}
	}
}

// TestRunDeltaReviewGateKeepsLandExitCodeWhenReviewerFails: the delta reviewer
// exiting non-zero with no verdict must not replace the land pass's rc, or box
// would skip relaying work the land pass already landed.
func TestRunDeltaReviewGateKeepsLandExitCodeWhenReviewerFails(t *testing.T) {
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", "exit 3"))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	rc, err := run(f.config(10), &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := f.callCount(t); got != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (the delta reviewer ran)", got)
	}
	if rc != 0 {
		t.Errorf("run rc = %d, want the land pass's 0, not the delta reviewer's", rc)
	}
	if got := f.finalOutcomeLine(t); got != deltaReviewLandOutcomeLine {
		t.Errorf("log's final outcome = %q, want the land pass's %q", got, deltaReviewLandOutcomeLine)
	}
}

// TestRunDeltaReviewGateBlockFailsVisibleWhenLogUnwritable: a BLOCK whose
// corrective event cannot be appended must surface as a run error rather than
// leave the land pass's status=ready as the final outcome silently.
func TestRunDeltaReviewGateBlockFailsVisibleWhenLogUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod does not deny writes to root")
	}
	chdirToFreshGitRepo(t)
	f := newDeltaReviewLoopFixture(t)
	deltaBlock := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- landed-file.txt:1 -- undeclared change\\n\\n## Non-blocking\\n- none")) +
		` && chmod 444 "` + f.logPath + `"`
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", deltaBlock))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(10), &stdout); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("run err = %v, want the corrective append's permission error surfaced", err)
	}
}
