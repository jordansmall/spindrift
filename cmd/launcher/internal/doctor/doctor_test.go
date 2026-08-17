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
	}
}

// TestRun_AllThreeProbesSucceed verifies the three registry-driven probes
// (issue tracker, code forge, recoverable count) each print their own
// success line, in order, when they all pass.
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

// TestRun_CodeForgeProbeFailure_ReportsMissingLineAndSkipsRecoverableCheck
// verifies that once the issue-tracker probe succeeds but the code-forge
// probe fails, RunChecksFailFast/ReportResults surface a "MISSING:
// code-forge: ..." line for the failing row (a new line the old hand-rolled
// Run never printed on a built-in probe failure, since it returned before
// any output) and the recoverable-issues probe never runs — no
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

	out := buf.String()
	if !strings.Contains(out, "MISSING: code-forge: code forge connectivity check failed") {
		t.Errorf("want a MISSING line for the failing code-forge row, got:\n%s", out)
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
			Probe: func() error { return wantErr },
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
			Probe: func() error { return errors.New("advisory check failed") },
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
