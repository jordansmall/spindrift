package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRunChecks_RunsEveryCheckEvenAfterAFailure(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "first-fails",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "first-fails")
				return nil, errors.New("boom")
			},
		},
		{
			Name: "second-still-runs",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "second-still-runs")
				return nil, nil
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
			Probe: func() (any, error) {
				ran = append(ran, "first-fails")
				return nil, errors.New("boom")
			},
		},
		{
			Name: "second-never-runs",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "second-never-runs")
				return nil, nil
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
			Probe: func() (any, error) {
				ran = append(ran, "advisory-fails")
				return nil, errors.New("advisory boom")
			},
		},
		{
			Name: "still-runs",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "still-runs")
				return nil, nil
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
		{Name: "ok1", Tier: Required, Probe: func() (any, error) { return nil, nil }},
		{Name: "req-fails", Tier: Required, Probe: func() (any, error) { return nil, errors.New("boom") }},
		{Name: "never-runs", Tier: Required, Probe: func() (any, error) { return nil, nil }},
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
				SuccessMsg: func(output any) string { return "github-repo: jordansmall/spindrift" },
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

// TestReportResults_SuccessMsgReceivesProbeOutput verifies SuccessMsg is
// handed the Result's Output (the value Probe returned alongside a nil
// error) as its own parameter, rather than reading a value some outer
// closure captured on the side.
func TestReportResults_SuccessMsgReceivesProbeOutput(t *testing.T) {
	check := Check{
		Name: "github-repo",
		Probe: func() (any, error) {
			return "some-value", nil
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("ok: %v", output)
		},
	}

	results := RunChecks([]Check{check})

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "ok: ok: some-value\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

const gitUserNameErrText = "GIT_USER_NAME is unset"

// gitUserNameRemedy is a Remedy that says something its paired probe error
// does not, so remedySuffix keeps it rather than suppressing it as a repeat.
const gitUserNameRemedy = "set GIT_USER_NAME, or configure git user.name on the host"

func TestReportResults_PrintsNameErrAndRemedyForFailure(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: gitUserNameRemedy,
			},
			Err: errors.New(gitUserNameErrText),
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n" +
		"  remedy: " + gitUserNameRemedy + "\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

func TestFirstRequiredError_IgnoresDegradedRequiredFailureButSurfacesLaterGenuineOne(t *testing.T) {
	degradedErr := fmt.Errorf("permission denied reading protection: %w", ErrDegraded)
	genuineErr := errors.New("branch is unprotected")
	results := []Result{
		{Check: Check{Name: "branch-protection", Tier: Required}, Err: degradedErr},
		{Check: Check{Name: "other-required", Tier: Required}, Err: genuineErr},
	}

	got := FirstRequiredError(results)

	if !errors.Is(got, genuineErr) {
		t.Fatalf("FirstRequiredError() = %v, want %v (the later genuine required failure)", got, genuineErr)
	}
}

func TestFirstRequiredError_ReturnsNilWhenOnlyDegradedRequiredFailure(t *testing.T) {
	degradedErr := fmt.Errorf("permission denied reading protection: %w", ErrDegraded)
	results := []Result{
		{Check: Check{Name: "branch-protection", Tier: Required}, Err: degradedErr},
	}

	got := FirstRequiredError(results)

	if got != nil {
		t.Fatalf("FirstRequiredError() = %v, want nil (a degraded required failure must not block)", got)
	}
}

func TestRunChecksFailFast_DegradedRequiredFailureDoesNotStopIteration(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "branch-protection",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "branch-protection")
				return nil, fmt.Errorf("permission denied: %w", ErrDegraded)
			},
		},
		{
			Name: "still-runs",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "still-runs")
				return nil, nil
			},
		},
	}

	results := RunChecksFailFast(checks)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if len(ran) != 2 || ran[0] != "branch-protection" || ran[1] != "still-runs" {
		t.Fatalf("ran = %v, want [branch-protection still-runs]", ran)
	}
}

func TestRunChecksFailFast_NonDegradedRequiredFailureStillStopsIteration(t *testing.T) {
	var ran []string
	checks := []Check{
		{
			Name: "genuine-required-fails",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "genuine-required-fails")
				return nil, errors.New("boom")
			},
		},
		{
			Name: "never-runs",
			Tier: Required,
			Probe: func() (any, error) {
				ran = append(ran, "never-runs")
				return nil, nil
			},
		},
	}

	results := RunChecksFailFast(checks)

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if len(ran) != 1 || ran[0] != "genuine-required-fails" {
		t.Fatalf("ran = %v, want only [genuine-required-fails] to have run", ran)
	}
}

func TestReportResults_PrintsAdvisoryForDegradedFailure(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "branch-protection",
				Tier:   Required,
				Remedy: "protect main: block direct pushes and require CI status checks",
			},
			Err: fmt.Errorf("branch protection probe for %q failed: %w: %w", "main", errors.New("permission denied"), ErrDegraded),
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "advisory: branch-protection: branch protection probe for \"main\" failed: permission denied\n" +
		"  remedy: protect main: block direct pushes and require CI status checks\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), want)
	}
}

func TestReportResults_SkipsRemedyLineWhenIdenticalToErrText(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: gitUserNameErrText,
			},
			Err: errors.New(gitUserNameErrText),
		},
	}

	var buf bytes.Buffer
	ReportResults(&buf, results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n"
	if buf.String() != want {
		t.Fatalf("ReportResults() wrote %q, want %q (no duplicate remedy line)", buf.String(), want)
	}
}

// TestRunRequiredFailFast_AppendsRemedyToErrorText is the seam test proving
// RunRequiredFailFast routes its first blocking Result through WithRemedy;
// WithRemedy's own behaviour (nil, empty/identical Remedy, unwrap) is pinned
// directly by the TestWithRemedy_* suite below, not re-proven here. It
// asserts the *RemedyError type, not just the rendered text, so a
// hand-formatted remedy line that bypassed WithRemedy still fails.
func TestRunRequiredFailFast_AppendsRemedyToErrorText(t *testing.T) {
	probeErr := errors.New(gitUserNameErrText)
	checks := []Check{{
		Name:   "git-user-name",
		Tier:   Required,
		Remedy: gitUserNameRemedy,
		Probe:  func() (any, error) { return nil, probeErr },
	}}

	got := RunRequiredFailFast(checks)

	if got == nil {
		t.Fatalf("RunRequiredFailFast() = nil, want a non-nil error")
	}
	var re *RemedyError
	if !errors.As(got, &re) {
		t.Fatalf("RunRequiredFailFast() = %v, want a *RemedyError from WithRemedy", got)
	}
	if !strings.Contains(got.Error(), probeErr.Error()) {
		t.Fatalf("RunRequiredFailFast() = %q, want it to contain the probe error %q", got.Error(), probeErr.Error())
	}
	if !strings.Contains(got.Error(), gitUserNameRemedy) {
		t.Fatalf("RunRequiredFailFast() = %q, want it to contain the Remedy text", got.Error())
	}
}

func TestRunRequiredFailFast_ReturnsLiteralNilWhenAllPass(t *testing.T) {
	checks := []Check{
		{Name: "ok1", Tier: Required, Probe: func() (any, error) { return nil, nil }},
		{Name: "ok2", Tier: Advisory, Probe: func() (any, error) { return nil, errors.New("advisory failure") }},
	}

	got := RunRequiredFailFast(checks)

	if got != nil {
		t.Fatalf("RunRequiredFailFast() = %v, want literal nil", got)
	}
}

func TestRunRequiredFailFast_DegradedRequiredFailureStaysNonBlocking(t *testing.T) {
	degradedErr := fmt.Errorf("permission denied reading protection: %w", ErrDegraded)
	checks := []Check{
		{
			Name:   "branch-protection",
			Tier:   Required,
			Remedy: "protect main",
			Probe:  func() (any, error) { return nil, degradedErr },
		},
	}

	got := RunRequiredFailFast(checks)

	if got != nil {
		t.Fatalf("RunRequiredFailFast() = %v, want nil (a degraded required failure must not block)", got)
	}
}

func TestWithRemedy_NilErrReturnsLiteralNil(t *testing.T) {
	if got := WithRemedy(Result{Check: Check{Name: "ok-row", Remedy: "do a thing"}}); got != nil {
		t.Fatalf("WithRemedy() = %v, want nil", got)
	}
}

func TestWithRemedy_EmptyRemedyReturnsErrVerbatim(t *testing.T) {
	wantErr := errors.New("REPO_SLUG is unset")

	got := WithRemedy(Result{Check: Check{Name: "repo-slug"}, Err: wantErr})

	if got != wantErr {
		t.Fatalf("WithRemedy() = %v, want exact same error value %v", got, wantErr)
	}
}

// TestWithRemedy_RemedyIdenticalToErrTextReturnsErrVerbatim pins the
// identity, not just the text: a Remedy that only repeats its own error adds
// nothing, so WithRemedy hands back the untouched error rather than a
// *RemedyError whose Error() happens to render the same string.
func TestWithRemedy_RemedyIdenticalToErrTextReturnsErrVerbatim(t *testing.T) {
	wantErr := errors.New(gitUserNameErrText)

	got := WithRemedy(Result{
		Check: Check{Name: "git-user-name", Remedy: gitUserNameErrText},
		Err:   wantErr,
	})

	if got != wantErr {
		t.Fatalf("WithRemedy() = %v, want exact same error value %v", got, wantErr)
	}
}

func TestWithRemedy_AppendsRemedyAndUnwraps(t *testing.T) {
	probeErr := errors.New(gitUserNameErrText)
	check := Check{Name: "git-user-name", Remedy: gitUserNameRemedy}

	got := WithRemedy(Result{Check: check, Err: probeErr})

	want := gitUserNameErrText + "\nremedy: " + gitUserNameRemedy
	if got.Error() != want {
		t.Errorf("WithRemedy().Error() = %q, want %q", got.Error(), want)
	}
	if !errors.Is(got, probeErr) {
		t.Errorf("errors.Is(WithRemedy(), probeErr) = false, want true")
	}
	var re *RemedyError
	if !errors.As(got, &re) || re.Check.Name != check.Name || re.Check.Remedy != check.Remedy {
		t.Errorf("errors.As(WithRemedy(), *RemedyError) did not recover the Check, got %+v", got)
	}
}

// TestReportResults_TierAndDegradedDriveRowPrefix walks the Tier-by-degraded
// matrix: a Check's own Tier picks its row prefix, and an Err wrapping
// ErrDegraded demotes any row to "advisory:" with the sentinel's own text
// trimmed off the printed message.
func TestReportResults_TierAndDegradedDriveRowPrefix(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check Check
		err   error
		want  string
	}{
		{
			name:  "required tier bare error renders MISSING",
			check: Check{Name: "git-user-name", Tier: Required},
			err:   errors.New(gitUserNameErrText),
			want:  "MISSING: git-user-name: GIT_USER_NAME is unset\n",
		},
		{
			name:  "advisory tier bare error renders advisory, not MISSING",
			check: Check{Name: "bwrap-cgroup-delegation", Tier: Advisory},
			err:   errors.New("cgroup delegation is not configured"),
			want:  "advisory: bwrap-cgroup-delegation: cgroup delegation is not configured\n",
		},
		{
			name:  "required tier degraded error renders advisory with the sentinel trimmed",
			check: Check{Name: "branch-protection", Tier: Required},
			err:   fmt.Errorf("permission denied reading protection: %w", ErrDegraded),
			want:  "advisory: branch-protection: permission denied reading protection\n",
		},
		{
			name:  "advisory tier degraded error renders advisory with the sentinel trimmed",
			check: Check{Name: "registry-proxy-transport", Tier: Advisory},
			err:   fmt.Errorf("probing the transport failed: %w", ErrDegraded),
			want:  "advisory: registry-proxy-transport: probing the transport failed\n",
		},
		{
			// The trim keys on errors.Is, not on the message text, so an
			// error that merely reads like the sentinel keeps its full text.
			name:  "advisory tier bare error whose own text ends in the sentinel wording is not trimmed",
			check: Check{Name: "bwrap-network-isolation", Tier: Advisory},
			err:   errors.New("network isolation probe failed: check degraded: probe could not determine result"),
			want:  "advisory: bwrap-network-isolation: network isolation probe failed: check degraded: probe could not determine result\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			ReportResults(&buf, []Result{{Check: tc.check, Err: tc.err}})

			if buf.String() != tc.want {
				t.Fatalf("ReportResults() wrote %q, want %q", buf.String(), tc.want)
			}
		})
	}
}
