package main

// Fake-driver-exec loop tests for issue #3246's bounded delta-review gate
// (runDeltaReviewGate, run.go). These are variations on
// reviewPassFakeDriverBodyWithLandCommit's own fixture (run_test.go): a real
// temp git repo (chdirToFreshGitRepo), an implement -> review(BLOCK) ->
// fix -> review(APPROVE) -> land sequence, with the land pass (call 5)
// committing a file the approving round's own findings may or may not have
// named -- deltaReviewFakeDriverBody generalizes that shape with the knobs
// each test below needs: what the approving round's findings say, whether
// the land pass declares gate-discovered work, and what (if anything) the
// gate's own delta-review pass (call 6) says.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/passmanifest"
)

// decodeSpindriftOps decodes every well-formed "spindrift_op" line in
// stdout, in emission order, the same decode-not-substring-match contract
// collectPassUsageOps (run_test.go) already applies to pass_usage alone --
// generalized here since these tests need to inspect pass_start,
// delta_review_trigger, and verdict ops too, none of which collectPassUsageOps
// itself decodes.
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

// teeStreamJSONStep renders the shell step reviewPassFakeDriverBodyWithLandCommit's
// own case arms already use to hand text back through $DRIVER_LOG_PATH:
// print text into the fake driver-exec's own log, the same way a real
// stream-json Driver would have.
func teeStreamJSONStep(text string) string {
	return "printf '%s' '" + text + "' | tee -a \"$DRIVER_LOG_PATH\""
}

// deltaReviewFakeDriverBody scripts the same implement -> review(BLOCK) ->
// fix -> review(APPROVE) -> land sequence reviewPassFakeDriverBodyWithLandCommit
// (run_test.go) drives, generalized for issue #3246's gate tests: the land
// pass (call 5) always commits landed-file.txt (so computeLandDelta has a
// real, non-zero delta to compare), optionally writes decisionsContent to
// decisionsPath first (decisionsPath == "" skips that step -- mirroring a
// land pass that never declares gate-discovered work), round 2's own APPROVE
// findings (call 4) take round2NonBlocking as their sole Non-blocking
// bullet, and the gate's own delta-review pass, if it fires (call 6), runs
// deltaReviewStep -- "" degrades to a no-op call, the fail-open "no verdict"
// case runDeltaReviewGate already handles.
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
	outcomeLine := streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc")

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

// fmtQuote wraps s in double quotes -- decisionsStep's own content and path
// arguments are always simple (no embedded quotes), so a plain wrap is
// enough; matches reviewPassFakeDriverBodyWithDispositions's own %q-quoted
// path convention (run_test.go) without pulling in fmt.Sprintf's format-verb
// parsing for a value that already contains this function's own literal "%"
// characters.
func fmtQuote(s string) string {
	return `"` + s + `"`
}

// deltaReviewLoopFixture wires up the shared driver-exec/prompt/state
// scaffolding every test below needs -- config differs only in maxSlices and
// decisionsPath, which each test sets itself.
type deltaReviewLoopFixture struct {
	dir              string
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
		logPath:          filepath.Join(f.dir, "stream.log"),
		stateFile:        f.stateFile,
		manifestPath:     f.manifestPath,
		maxReviewRounds:  3,
		maxSlices:        maxSlices,
	}
}

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

// TestRunDeltaReviewGateSkipsWhenDeltaConfinedToFindings verifies issue
// #3246 AC1: round 2's own APPROVE findings name landed-file.txt (the same
// file the land pass's own commit touches), so deltareview.Decide finds
// nothing beyond the findings and the gate declines to spend the extra
// pass -- the run settles exactly as reviewPassFakeDriverBodyWithLandCommit's
// own no-verdict-case sibling (TestRunWithReviewPassLandDeltaNonZero) does
// when nothing fires it.
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
}

// TestRunDeltaReviewGateFiresAndApproves verifies issue #3246 AC2: the land
// pass's own commit touches landed-file.txt, which round 2's own APPROVE
// findings never named ("none"), so the gate fires exactly one delta-review
// pass -- and when that pass's own verdict is APPROVE, the run settles
// exactly as it would have without the gate: no corrective outcome line.
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
}

// TestRunDeltaReviewGateFiresAndBlocks verifies issue #3246 AC3: the same
// fixture as TestRunDeltaReviewGateFiresAndApproves, except the delta-review
// pass itself returns BLOCK -- terminal, with a corrective status=blocked
// outcome line standing in for the land pass's own now-contradicted
// status=ready claim, and no further pass runs after it (no new fix lap).
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

	// The land pass's own outcome line (call 5) is the corrective line's
	// verbatim Issue/Landing source; both lines are present in stdout since
	// the launcher's own last-line-wins scan needs the corrective line last,
	// not the land pass's own line erased.
	var correctiveLine string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "SPINDRIFT_OUTCOME") && strings.Contains(line, "status=blocked") {
			correctiveLine = strings.TrimSpace(line)
		}
	}
	if correctiveLine == "" {
		t.Fatalf("stdout = %q, want a corrective SPINDRIFT_OUTCOME status=blocked line", stdout.String())
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

// TestRunDeltaReviewGateFiresOnGateWorkDeclarationDespiteConfinedDelta
// verifies issue #3246 AC4/#3245's own declaration contract: the land pass's
// own decisions.md declares gate-discovered work even though the delta
// itself stays confined to what round 2's own findings named (same fixture
// TestRunDeltaReviewGateSkipsWhenDeltaConfinedToFindings uses to prove the
// gate stays quiet on a confined delta alone) -- deltareview.Decide checks
// the declaration first and fires unconditionally, so this test's only
// difference from that skip case is the decisions.md content, and the only
// thing distinguishing the two outcomes.
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

// TestRunDeltaReviewGateCappedBySlicesSkipsExtraPass verifies issue #3246
// AC5/"counted by the budget caps": the delta would otherwise fire (round 2's
// own findings never named landed-file.txt, same as
// TestRunDeltaReviewGateFiresAndApproves), but maxSlices == the land pass's
// own pass number (5) makes passmachine.ExtraPassAllowed report the cap
// already spent -- the run settles as if the gate had never fired at all.
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
