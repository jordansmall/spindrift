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
		// Advisory rather than Required: most forge.Fake instances here don't
		// script SetBranchProtected, so a Required row would report a spurious
		// failure unrelated to what each test verifies. Branch-protection tests
		// override MergePolicy/BaseBranch on the returned Config instead.
		MergePolicy: "manual",
		BaseBranch:  "main",
	}
}

func TestRun_AllThreeProbesSucceed(t *testing.T) {
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

// A failing built-in probe reaches the caller solely through the returned
// error, never as a "MISSING:" row on w — writing the row too would
// double-report it, since cmdDoctor also prints the returned error.
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

// extraChecks are informational only: even a Required-tier row that fails is
// reported without failing Run.
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

// immediate/auto/empty are the merge policies that put the row at Required tier.
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

// mergePolicy "manual" puts the branch-protection row at Advisory tier.
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
	if !strings.Contains(out, "MISSING: branch-protection") {
		t.Errorf("want MISSING line for branch-protection row, got:\n%s", out)
	}
}

// AsPushOnly models a local/push-only forge shape with no protection API.
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

// Even under a Required merge policy, a probe failure (e.g. missing token
// permission) degrades to non-blocking rather than failing Run — an
// unanswerable question is not the same as a negative answer.
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

func TestRun_RecoverableIssuesProbeFailure_WrapsErrConnectivity(t *testing.T) {
	// Recoverable must map to a real label for the probe to call ListIssues
	// at all — see builtinChecks' recoverable-issues Probe doc comment: an
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

// Accepting the create-labels prompt must never be worse than declining it
// (issue #2569): a CreateLabel failure on an advisory-tier label (here,
// research) is not fatal. Only a work-tier label failure is.
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

// The error must name each missing label — not just "one or more" — so a
// stderr summary tells an operator exactly which labels to create.
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
	}
}

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
