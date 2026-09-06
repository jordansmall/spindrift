package doctor

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

func defaultDoctorConfig() Config {
	return Config{
		IssueTracker:    "github",
		Label:           "ready-for-agent",
		InProgressLabel: "agent-in-progress",
		FailedLabel:     "agent-failed",
		CompleteLabel:   "agent-complete",
		// MergePolicy "manual" keeps the branch-protection row (issue #2570)
		// Advisory rather than Required for tests that don't care about it —
		// the forge.Fake instances these existing tests build generally
		// don't script SetBranchProtected, so an unset BaseBranch would
		// otherwise report a spurious Required failure unrelated to what
		// each test actually verifies. Tests that DO exercise
		// branch-protection still call defaultDoctorConfig() and override
		// MergePolicy/BaseBranch on the returned Config, rather than
		// building a separate Config{} literal.
		MergePolicy: "manual",
		BaseBranch:  "main",
	}
}

// TestRun_ConnectivityAndRecoverableIssuesProbesSucceed verifies the two
// connectivity rows (issue tracker, code forge) and the recoverable-issues
// repository-state row each print their own success line, in order, when
// they all pass.
func TestRun_ConnectivityAndRecoverableIssuesProbesSucceed(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	wantLines := []string{
		"ok: issue tracker confirmed — owner/repo is reachable",
		"ok: code forge confirmed — owner/repo is reachable",
		"ok: 0 recoverable issue(s) — run `spindrift recover <issue>` to land each",
	}
	lastIdx := -1
	for _, want := range wantLines {
		idx := strings.Index(out, want)
		if idx == -1 {
			t.Fatalf("output missing line %q, got:\n%s", want, out)
		}
		if idx <= lastIdx {
			t.Fatalf("line %q not in expected order, got:\n%s", want, out)
		}
		lastIdx = idx
	}
}

// TestRun_IssueTrackerAuthFailure_CodeForgeProbeDoesNotRun verifies that when
// the issue-tracker probe fails, Run fails fast — the code-forge probe never
// runs (no extra live call, no bogus success line) — and Run returns the
// issue-tracker's wrapped auth-failure error immediately.
func TestRun_IssueTrackerAuthFailure_CodeForgeProbeDoesNotRun(t *testing.T) {
	it := forge.NewFake()
	it.ProbeErr = forge.ErrAuthFailure
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	err := Run(it, cf, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Errorf("want ErrAuthFailure, got %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
	if !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("want error to mention GH_TOKEN, got: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "ok: issue tracker confirmed") {
		t.Errorf("want no issue-tracker success line on failure, got:\n%s", out)
	}
	if strings.Contains(out, "ok: code forge confirmed") {
		t.Errorf("want code forge probe to never run (fail-fast, no short-circuit into an extra live call) once the issue-tracker probe fails, got:\n%s", out)
	}
}

// TestRun_IssueTrackerRateLimit_CodeForgeProbeDoesNotRun verifies that when
// the issue-tracker probe fails with ErrRateLimit, Run fails fast — the
// code-forge probe never runs (no extra live call, no bogus success line) —
// and Run returns the issue-tracker's wrapped rate-limit error immediately.
func TestRun_IssueTrackerRateLimit_CodeForgeProbeDoesNotRun(t *testing.T) {
	it := forge.NewFake()
	it.ProbeErr = forge.ErrRateLimit
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	err := Run(it, cf, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Errorf("want ErrRateLimit, got %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
	if !strings.Contains(err.Error(), "quota window") {
		t.Errorf("want error to mention the quota window, got: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "ok: issue tracker confirmed") {
		t.Errorf("want no issue-tracker success line on failure, got:\n%s", out)
	}
	if strings.Contains(out, "ok: code forge confirmed") {
		t.Errorf("want code forge probe to never run (fail-fast, no short-circuit into an extra live call) once the issue-tracker probe fails, got:\n%s", out)
	}
}

// TestRun_CodeForgeProbeFailure_ReportsMissingLineAndSkipsRecoverableCheck
// verifies that once the issue-tracker probe succeeds but the code-forge
// probe fails, the failing row is never written to w as a "MISSING:
// code-forge: ..." line — only the earlier successful rows are reported via
// ReportResults, and the failure reaches the caller solely through the
// returned error. This matches the pre-refactor Run, which returned before
// any output on a built-in probe failure; writing the failing row to w too
// would double-report it, since the caller (cmdDoctor) also prints the
// returned error. The recoverable-issues probe never runs either — no
// ListIssues(Recoverable) call, and no "ok:" success line for it.
func TestRun_CodeForgeProbeFailure_ReportsMissingLineAndSkipsRecoverableCheck(t *testing.T) {
	it := forge.NewFake()
	it.ProbeRepo = "owner/repo"
	cf := forge.NewFake()
	wantErr := errors.New("boom")
	cf.ProbeErr = wantErr

	var buf bytes.Buffer
	err := Run(it, cf, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "code forge connectivity check failed") || !errors.Is(err, wantErr) {
		t.Errorf("want wrapped code-forge connectivity error, got: %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "MISSING: code-forge") {
		t.Errorf("want no MISSING line for the failing code-forge row (reported only via the returned error), got:\n%s", out)
	}
	if strings.Contains(out, "recoverable issue(s)") {
		t.Errorf("want recoverable-issues probe to never run once code-forge fails, got:\n%s", out)
	}
	if len(it.ListIssuesCalls) != 0 {
		t.Errorf("want ListIssues never called once code-forge probe fails, got calls: %v", it.ListIssuesCalls)
	}
}

// TestRun_ExtraChecks_FailingRequiredRowIsReportedButDoesNotFailRun verifies
// a caller-supplied extraChecks Required row that fails is reported via
// ReportResults (its MISSING line appears in the output) but does NOT fail
// Run overall — extraChecks are informational only.
func TestRun_ExtraChecks_FailingRequiredRowIsReportedButDoesNotFailRun(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	wantErr := errors.New("extra required check failed")
	extra := []Check{
		{
			Name:  "fake required launcher check",
			Tier:  Required,
			Probe: func() (any, error) { return nil, wantErr },
		},
	}

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, extra)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "fake required launcher check") {
		t.Errorf("want output to report the failing extraChecks row, got:\n%s", out)
	}
	if !strings.Contains(out, wantErr.Error()) {
		t.Errorf("want output to include the row's error text, got:\n%s", out)
	}
}

// TestRun_ExtraChecks_FailingAdvisoryRowDoesNotFailRun verifies a
// caller-supplied extraChecks Advisory row that fails does NOT fail Run
// overall — Run proceeds through to the label-tier section and returns nil
// when everything else passes — and that the failure is still surfaced
// (informational) in the output rather than silently swallowed.
func TestRun_ExtraChecks_FailingAdvisoryRowDoesNotFailRun(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	extra := []Check{
		{
			Name:  "fake advisory launcher check",
			Tier:  Advisory,
			Probe: func() (any, error) { return nil, errors.New("advisory check failed") },
		},
	}

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, extra)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "fake advisory launcher check") {
		t.Errorf("want output to report the failing advisory extraChecks row, got:\n%s", out)
	}
}

// TestRun_BranchProtection_RequiredTierUnprotectedFailsRun verifies that
// under a Required merge policy (immediate/auto/empty), an unprotected base
// branch makes Run return a non-nil error naming the branch (AC1 "fails as
// required").
func TestRun_BranchProtection_RequiredTierUnprotectedFailsRun(t *testing.T) {
	for _, mergePolicy := range []string{"immediate", "auto", ""} {
		t.Run(mergePolicy, func(t *testing.T) {
			f := forge.NewFake()
			f.ProbeRepo = "owner/repo"
			f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
			f.SetBranchProtected("main", false)

			cfg := defaultDoctorConfig()
			cfg.MergePolicy = mergePolicy
			cfg.BaseBranch = "main"

			var buf bytes.Buffer
			err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
				t.Errorf("want error to mention base branch and not-protected, got: %v", err)
			}
		})
	}
}

// TestRun_BranchProtection_AdvisoryTierUnprotectedReportsButDoesNotFailRun
// verifies that under mergePolicy "manual" (Advisory tier), an unprotected
// base branch does NOT fail Run, but the row's failure is still visible in
// the output as an advisory line.
func TestRun_BranchProtection_AdvisoryTierUnprotectedReportsButDoesNotFailRun(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetBranchProtected("main", false)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "manual"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "advisory: branch-protection") {
		t.Errorf("want advisory line for branch-protection row, got:\n%s", out)
	}
}

// TestRun_BranchProtection_NotApplicableForForgeWithoutProtectionAPI verifies
// that a forge shape with no branch-protection API (push-only, local)
// reports "not applicable" and never fails Run, regardless of merge policy.
func TestRun_BranchProtection_NotApplicableForForgeWithoutProtectionAPI(t *testing.T) {
	for _, mergePolicy := range []string{"immediate", "manual"} {
		t.Run(mergePolicy, func(t *testing.T) {
			it := forge.NewFake()
			it.ProbeRepo = "owner/repo"
			it.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
			cf := it.AsPushOnly()

			cfg := defaultDoctorConfig()
			cfg.MergePolicy = mergePolicy
			cfg.BaseBranch = "main"

			var buf bytes.Buffer
			err := Run(it, cf, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			out := buf.String()
			const want = "ok: not applicable (code forge has no branch-protection API)\n"
			if !strings.Contains(out, want) {
				t.Errorf("want output to contain %q, got:\n%s", want, out)
			}
		})
	}
}

// TestRun_BranchProtection_ProbeFailureDegradesAndDoesNotFailRun verifies
// AC3: even under a Required merge policy, a probe failure (e.g. missing
// token permission) degrades to non-blocking — Run returns nil — while the
// failure and its Remedy line are still visible in the output.
func TestRun_BranchProtection_ProbeFailureDegradesAndDoesNotFailRun(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	probeErr := errors.New("missing token permission")
	f.SetBranchProtectedErr("main", probeErr)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "auto"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "advisory: branch-protection") {
		t.Errorf("want advisory line for the degraded branch-protection row, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: branch-protection") {
		t.Errorf("want no MISSING line for the degraded branch-protection row, got:\n%s", out)
	}
	if strings.Contains(out, "check degraded") {
		t.Errorf("want ErrDegraded's internal sentinel text trimmed from output, got:\n%s", out)
	}
	if !strings.Contains(out, "remedy:") {
		t.Errorf("want remedy line printed for the degraded branch-protection row, got:\n%s", out)
	}
	if !strings.Contains(out, probeErr.Error()) {
		t.Errorf("want output to include the underlying probe error text, got:\n%s", out)
	}
}

// TestRun_BranchProtection_ProtectedAndRequiredSucceeds verifies that a
// protected base branch under a Required merge policy reports success and
// does not fail Run.
func TestRun_BranchProtection_ProtectedAndRequiredSucceeds(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetBranchProtected("main", true)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `ok: base branch "main" is protected`) {
		t.Errorf("want ok line for branch-protection row, got:\n%s", out)
	}
}

// TestRun_RecoverableIssuesProbeFailure_WrapsErrConnectivity verifies the
// recoverable-issue-count row also wraps ErrConnectivity (issue #2569
// exit-code vocabulary) when its ListIssues call fails, the same treatment
// as the issue-tracker and code-forge probes above.
func TestRun_RecoverableIssuesProbeFailure_WrapsErrConnectivity(t *testing.T) {
	// Recoverable must map to a real label for the probe to call ListIssues
	// at all — see repoStateChecks' recoverable-issues Probe doc comment: an
	// unmapped Recoverable (the zero-value forge.NewFake() default) skips
	// the call entirely to avoid false-matching every open issue.
	f := forge.NewFake(forge.DispatchLabels{Recoverable: "needs-recovery"})
	f.ProbeRepo = "owner/repo"
	wantErr := errors.New("boom")
	f.ListIssuesErr = wantErr

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("want wrapped underlying error, got: %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
}

// TestRun_ListLabelsFailure_WrapsErrConnectivity verifies checkLabels'
// ListLabels failure also wraps ErrConnectivity (issue #2569 exit-code
// vocabulary) — the label-tier probe is a connectivity failure the same way
// the issue-tracker, code-forge, and recoverable-issues probes above are.
func TestRun_ListLabelsFailure_WrapsErrConnectivity(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	wantErr := errors.New("boom")
	f.ListLabelsErr = wantErr

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("want wrapped underlying error, got: %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
}

// TestRun_CreateLabelFailure_WrapsErrConnectivity verifies a CreateLabel
// failure during the interactive label-creation loop also wraps
// ErrConnectivity (issue #2569 exit-code vocabulary) — creating a label is
// itself a code-forge/issue-tracker connectivity operation.
func TestRun_CreateLabelFailure_WrapsErrConnectivity(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three work labels missing
	wantErr := errors.New("boom")
	f.CreateLabelErr = wantErr

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("want wrapped underlying error, got: %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
}

// TestRun_CreateLabelFailure_AdvisoryLabelDoesNotFailRun verifies that a
// CreateLabel failure on a purely advisory-tier label (here, research) does
// NOT make Run return an error — accepting the create-labels prompt must
// never be worse than declining it, which is safe for an advisory-only run
// (issue #2569). Only a failure on a work-tier label is fatal
// (TestRun_CreateLabelFailure_WrapsErrConnectivity above).
func TestRun_CreateLabelFailure_AdvisoryLabelDoesNotFailRun(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	// All four work labels present; every research label missing.
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	wantErr := errors.New("boom")
	f.CreateLabelErr = wantErr

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err != nil {
		t.Fatalf("advisory-label create failure must not fail Run, got: %v", err)
	}
	if !strings.Contains(buf.String(), "advisory: create label") {
		t.Errorf("want an advisory create-failure line, got:\n%s", buf.String())
	}
}

// TestRun_NonInteractive_MissingWorkLabels_WrapsErrRequiredLabelsMissing verifies
// that when work-tier triage labels are missing and Run is non-interactive,
// the returned error wraps ErrRequiredLabelsMissing (issue #2569 exit-code
// vocabulary) and names each missing label in its message — not just "one or
// more" — so a stderr summary can tell an operator exactly which labels to
// create, and that each missing label's stdout row carries the fatal
// "MISSING" prefix, not "advisory".
func TestRun_NonInteractive_MissingWorkLabels_WrapsErrRequiredLabelsMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // agent-in-progress, agent-failed, agent-complete missing

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrRequiredLabelsMissing) {
		t.Errorf("want ErrRequiredLabelsMissing (issue #2569 exit-code vocabulary), got %v", err)
	}
	for _, label := range []string{"agent-in-progress", "agent-failed", "agent-complete"} {
		if !strings.Contains(err.Error(), label) {
			t.Errorf("want error to name missing label %q, got: %v", label, err)
		}
		if !strings.Contains(buf.String(), "MISSING: label \""+label+"\" missing") {
			t.Errorf("want fatal MISSING line for work label %q, got:\n%s", label, buf.String())
		}
	}
}

// TestRun_TTY_Decline_WrapsErrRequiredLabelsMissing verifies that declining the
// interactive create-labels prompt also wraps ErrRequiredLabelsMissing (issue #2569
// exit-code vocabulary) — the same classification as the non-interactive
// missing-labels path above, since both are "required labels missing" exits.
func TestRun_TTY_Decline_WrapsErrRequiredLabelsMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three work labels missing

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("n\n")), true, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrRequiredLabelsMissing) {
		t.Errorf("want ErrRequiredLabelsMissing (issue #2569 exit-code vocabulary), got %v", err)
	}
}

// TestRun_StillMissingAfterCreation_WrapsErrRequiredLabelsMissing verifies that when
// a work-tier label is still missing on the post-creation re-verify, the
// returned error wraps ErrRequiredLabelsMissing (issue #2569 exit-code vocabulary)
// and names the still-missing label.
func TestRun_StillMissingAfterCreation_WrapsErrRequiredLabelsMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	present := []string{"ready-for-agent", "agent-in-progress", "agent-failed"} // agent-complete missing
	f.Labels = present
	f.LabelsSeq = [][]string{
		present,
		present, // re-verify: agent-complete still missing despite CreateLabel "succeeding"
	}

	var buf bytes.Buffer
	err := Run(f, f, defaultDoctorConfig(), &buf, bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrRequiredLabelsMissing) {
		t.Errorf("want ErrRequiredLabelsMissing (issue #2569 exit-code vocabulary), got %v", err)
	}
	if !strings.Contains(err.Error(), "agent-complete") {
		t.Errorf("want error to name still-missing label \"agent-complete\", got: %v", err)
	}
}

// TestRun_BranchProtection_RequiredTierUnprotectedDoesNotSkipLaterRows
// verifies AC3 (issue #2798): a blocking branch-protection failure must not
// short-circuit the rest of Run the way a connectivity-phase failure does.
// The recoverable-issues row, the per-label rows, and the failing row's own
// MISSING/remedy lines must all still reach the output, and the returned
// error must still name the branch-protection failure.
func TestRun_BranchProtection_RequiredTierUnprotectedDoesNotSkipLaterRows(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetBranchProtected("main", false)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("want error to mention base branch and not-protected, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "recoverable issue(s)") {
		t.Errorf("want recoverable-issues row to still run past a blocking branch-protection failure, got:\n%s", out)
	}
	for _, label := range []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"} {
		if !strings.Contains(out, `ok: label "`+label+`" present`) {
			t.Errorf("want per-label row for %q to still run, got:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "MISSING: branch-protection") {
		t.Errorf("want MISSING line for the blocking branch-protection row, got:\n%s", out)
	}
	if !strings.Contains(out, "remedy:") {
		t.Errorf("want remedy line for the blocking branch-protection row, got:\n%s", out)
	}
}

// TestRun_BranchProtection_RequiredTierUnprotected_StillOffersLabelCreation
// verifies AC3: an unprotected base under a Required merge policy must not
// suppress the interactive create-missing-labels offer. The offer is
// printed, CreateLabel is actually called for the missing labels, and Run
// still returns the branch-protection error (not nil, and not a label
// error) — the deferred configuration-phase error wins.
func TestRun_BranchProtection_RequiredTierUnprotected_StillOffersLabelCreation(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // agent-in-progress, agent-failed, agent-complete missing
	f.SetBranchProtected("main", false)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("want the returned error to still be the branch-protection failure, got: %v", err)
	}
	if errors.Is(err, ErrRequiredLabelsMissing) {
		t.Errorf("want branch-protection error to take precedence over any label error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "missing label(s)") {
		t.Errorf("want the create-label offer to still be printed, got:\n%s", out)
	}
	gotNames := make(map[string]bool, len(f.CreateLabelCalls))
	for _, c := range f.CreateLabelCalls {
		gotNames[c.Name] = true
	}
	for _, label := range []string{"agent-in-progress", "agent-failed", "agent-complete"} {
		if !gotNames[label] {
			t.Errorf("want CreateLabel called for missing label %q, got calls: %v", label, f.CreateLabelCalls)
		}
	}
}

// TestRun_BranchProtection_RequiredTierUnprotected_NonInteractivePrecedence
// pins the precedence rule (issue #2798 coordinator decision): when both an
// unprotected required base branch and missing work labels are present in
// non-interactive mode, Run returns the branch-protection error, not
// ErrRequiredLabelsMissing.
func TestRun_BranchProtection_RequiredTierUnprotected_NonInteractivePrecedence(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three work labels missing
	f.SetBranchProtected("main", false)

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("want the branch-protection error to win, got: %v", err)
	}
	if errors.Is(err, ErrRequiredLabelsMissing) {
		t.Errorf("want branch-protection error to take precedence over the label error, got: %v", err)
	}
}

// TestRun_BranchProtection_RequiredTierUnprotected_ListLabelsErrorReportedNotSwallowed
// pins what happens to the loser of the #2798 precedence rule: the deferred
// branch-protection error still wins Run's return value, but the later
// ListLabels failure it masks must still reach the operator via the report
// stream rather than vanish from every stream at once.
func TestRun_BranchProtection_RequiredTierUnprotected_ListLabelsErrorReportedNotSwallowed(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.SetBranchProtected("main", false)
	wantLabelErr := errors.New("label boom")
	f.ListLabelsErr = wantLabelErr

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("want the branch-protection error to win, got: %v", err)
	}
	if errors.Is(err, ErrConnectivity) {
		t.Errorf("want returned error to stay the branch-protection error, not the masked label error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "label boom") {
		t.Errorf("want the masked label-check error reported to w, got:\n%s", out)
	}
}

// TestRun_BranchProtection_RequiredTierUnprotected_CreateLabelErrorReportedNotSwallowed
// is the create-label-side twin of the ListLabels test above: an
// interactive, accepted work-tier CreateLabel failure is also masked by the
// deferred branch-protection error and must still be reported to w.
func TestRun_BranchProtection_RequiredTierUnprotected_CreateLabelErrorReportedNotSwallowed(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three work labels missing
	f.SetBranchProtected("main", false)
	wantCreateErr := errors.New("create boom")
	f.CreateLabelErr = wantCreateErr

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("y\n")), true, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "not protected") {
		t.Errorf("want the branch-protection error to win, got: %v", err)
	}
	if errors.Is(err, ErrConnectivity) {
		t.Errorf("want returned error to stay the branch-protection error, not the masked create-label error, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "create boom") {
		t.Errorf("want the masked create-label error reported to w, got:\n%s", out)
	}
}

// TestRun_RecoverableIssuesRequiredFailure_PrecedesLaterListLabelsError pins
// the precedence rule for the *other* repository-state row: a blocking
// recoverable-issues failure defers the same way a blocking
// branch-protection failure does (both rows share one FirstRequiredError
// call), so it must also win over a later ListLabels failure while a
// passing branch-protection row proves the deferral isn't specific to
// branch-protection.
func TestRun_RecoverableIssuesRequiredFailure_PrecedesLaterListLabelsError(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Recoverable: "needs-recovery"})
	f.ProbeRepo = "owner/repo"
	f.SetBranchProtected("main", true)
	wantRecoverableErr := errors.New("recoverable boom")
	f.ListIssuesErr = wantRecoverableErr
	wantLabelErr := errors.New("label boom")
	f.ListLabelsErr = wantLabelErr

	cfg := defaultDoctorConfig()
	cfg.MergePolicy = "immediate"
	cfg.BaseBranch = "main"

	var buf bytes.Buffer
	err := Run(f, f, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantRecoverableErr) {
		t.Errorf("want the deferred recoverable-issues error to win, got: %v", err)
	}
	if !errors.Is(err, ErrConnectivity) {
		t.Errorf("want ErrConnectivity (issue #2569 exit-code vocabulary), got %v", err)
	}
	if !strings.Contains(err.Error(), "recoverable") {
		t.Errorf("want the returned error to name the recoverable-issues check, got: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "label boom") {
		t.Errorf("want the masked ListLabels error still reported to w, got:\n%s", out)
	}
}
