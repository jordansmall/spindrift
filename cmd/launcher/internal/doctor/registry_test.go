package doctor

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunChecks_RunsEveryCheckEvenAfterAFailure(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "first-fails",
			Tier: Required,
			Probe: func() error {
				ran = append(ran, "first-fails")
				return errors.New("boom")
			},
		},
		{
			Name: "second-still-runs",
			Tier: Required,
			Probe: func() error {
				ran = append(ran, "second-still-runs")
				return nil
			},
		},
	}

	results := RunChecks(checks)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if len(ran) != 2 {
		t.Fatalf("ran = %v, want both checks to have run", ran)
	}
	if ran[0] != "first-fails" || ran[1] != "second-still-runs" {
		t.Fatalf("ran = %v, want [first-fails second-still-runs]", ran)
	}
}

func TestRunChecksFailFast_StopsAfterFirstRequiredFailure(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "first-fails",
			Tier: Required,
			Probe: func() error {
				ran = append(ran, "first-fails")
				return errors.New("boom")
			},
		},
		{
			Name: "second-never-runs",
			Tier: Required,
			Probe: func() error {
				ran = append(ran, "second-never-runs")
				return nil
			},
		},
	}

	results := RunChecksFailFast(checks)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if len(ran) != 1 || ran[0] != "first-fails" {
		t.Fatalf("ran = %v, want only [first-fails] to have run", ran)
	}
}

func TestRunChecksFailFast_AdvisoryFailureDoesNotStopIteration(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "advisory-fails",
			Tier: Advisory,
			Probe: func() error {
				ran = append(ran, "advisory-fails")
				return errors.New("advisory boom")
			},
		},
		{
			Name: "still-runs",
			Tier: Required,
			Probe: func() error {
				ran = append(ran, "still-runs")
				return nil
			},
		},
	}

	results := RunChecksFailFast(checks)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if len(ran) != 2 || ran[0] != "advisory-fails" || ran[1] != "still-runs" {
		t.Fatalf("ran = %v, want [advisory-fails still-runs]", ran)
	}
}

func TestRunChecksFailFast_ReturnsOnlyResultsForChecksThatRan(t *testing.T) {
	checks := []Check{
		{Name: "ok1", Tier: Required, Probe: func() error { return nil }},
		{Name: "req-fails", Tier: Required, Probe: func() error { return errors.New("boom") }},
		{Name: "never-runs", Tier: Required, Probe: func() error { return nil }},
	}

	results := RunChecksFailFast(checks)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (only checks that ran)", len(results))
	}
	if results[0].Check.Name != "ok1" || results[1].Check.Name != "req-fails" {
		t.Fatalf("results = %+v, want [ok1 req-fails]", results)
	}
}

func TestFirstRequiredError_SurfacesRequiredFailure(t *testing.T) {
	wantErr := errors.New("required check failed")
	results := []Result{
		{Check: Check{Name: "req", Tier: Required}, Err: wantErr},
	}

	got := FirstRequiredError(results)

	if !errors.Is(got, wantErr) {
		t.Fatalf("FirstRequiredError() = %v, want %v", got, wantErr)
	}
}

func TestFirstRequiredError_IgnoresAdvisoryFailure(t *testing.T) {
	advisoryErr := errors.New("advisory check failed")
	results := []Result{
		{Check: Check{Name: "adv", Tier: Advisory}, Err: advisoryErr},
	}

	got := FirstRequiredError(results)

	if got != nil {
		t.Fatalf("FirstRequiredError() = %v, want nil (advisory failures must not surface)", got)
	}
}

func TestFirstRequiredError_PicksFirstRequiredFailureInOrder(t *testing.T) {
	firstErr := errors.New("first required failure")
	secondErr := errors.New("second required failure")
	results := []Result{
		{Check: Check{Name: "adv", Tier: Advisory}, Err: errors.New("advisory failure")},
		{Check: Check{Name: "req1", Tier: Required}, Err: firstErr},
		{Check: Check{Name: "req2", Tier: Required}, Err: secondErr},
	}

	got := FirstRequiredError(results)

	if !errors.Is(got, firstErr) {
		t.Fatalf("FirstRequiredError() = %v, want %v (the first required failure in slice order)", got, firstErr)
	}
}

func TestFirstRequiredError_ReturnsNilWhenAllPass(t *testing.T) {
	results := []Result{
		{Check: Check{Name: "req", Tier: Required}, Err: nil},
		{Check: Check{Name: "adv", Tier: Advisory}, Err: nil},
	}

	got := FirstRequiredError(results)

	if got != nil {
		t.Fatalf("FirstRequiredError() = %v, want nil", got)
	}
}

func TestFirstRequiredError_ReturnsErrorVerbatimUnwrapped(t *testing.T) {
	wantErr := errors.New("exact sentinel")
	results := []Result{
		{Check: Check{Name: "req", Tier: Required}, Err: wantErr},
	}

	got := FirstRequiredError(results)

	// Must be the exact same error value, not wrapped/reformatted, so
	// existing callers' exact error-message assertions keep working.
	if got != wantErr {
		t.Fatalf("FirstRequiredError() = %v, want exact same error value %v", got, wantErr)
	}
}

func TestRunChecks_NilProbeReturnsErrorInsteadOfPanicking(t *testing.T) {
	checks := []Check{
		{Name: "no-probe", Tier: Required, Probe: nil},
	}

	results := RunChecks(checks)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Err == nil {
		t.Fatalf("results[0].Err = nil, want a non-nil error for a nil Probe")
	}
	if !strings.Contains(results[0].Err.Error(), "no-probe") {
		t.Fatalf("results[0].Err = %v, want it to mention the check name %q", results[0].Err, "no-probe")
	}
}

func TestReportResults_PrintsOkForSuccess(t *testing.T) {
	results := []Result{
		{Check: Check{Name: "git-user-name"}, Err: nil},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "ok: git-user-name\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

func TestReportResults_UsesSuccessMsgWhenSet(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:       "github-repo",
				SuccessMsg: func() string { return "github-repo: jordansmall/spindrift" },
			},
			Err: nil,
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "ok: github-repo: jordansmall/spindrift\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

func TestReportResults_PrintsNameErrAndRemedyForFailure(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: "set GIT_USER_NAME, or configure git user.name on the host",
			},
			Err: errors.New("GIT_USER_NAME is unset"),
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n" +
		"  remedy: set GIT_USER_NAME, or configure git user.name on the host\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

func TestReportResults_SkipsRemedyLineWhenIdenticalToErrText(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: "GIT_USER_NAME is unset",
			},
			Err: errors.New("GIT_USER_NAME is unset"),
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q (no duplicate remedy line)", buf.String(), want)
	}
}
