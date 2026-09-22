package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestReporter_Success_PrintsOkRow(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, true).Success("git-user-name")

	want := "ok: git-user-name\n"
	if buf.String() != want {
		t.Fatalf("Success() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Success_FormatsArgs(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, true).Success("github-repo: %s", "jordansmall/spindrift")

	want := "ok: github-repo: jordansmall/spindrift\n"
	if buf.String() != want {
		t.Fatalf("Success() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Finding_RequiredTierPrintsMissing(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, true).Finding(Required, "git-user-name: %s", "GIT_USER_NAME is unset")

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n"
	if buf.String() != want {
		t.Fatalf("Finding() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Finding_AdvisoryTierPrintsAdvisory(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, true).Finding(Advisory, "bwrap-cgroup-delegation: %s", "cgroup delegation is not configured")

	want := "advisory: bwrap-cgroup-delegation: cgroup delegation is not configured\n"
	if buf.String() != want {
		t.Fatalf("Finding() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Passthrough_PrintsVerbatimWithNoTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, true).Passthrough("create label %q? [y/N] ", "ready-for-agent")

	want := `create label "ready-for-agent"? [y/N] `
	if buf.String() != want {
		t.Fatalf("Passthrough() wrote %q, want %q", buf.String(), want)
	}
}

// TestReporter_Results_PrintsOkForSuccess and the finding+remedy pairing
// below are the ones test-first per the task; registry_test.go's renamed
// TestReporter_Results_* suite (formerly TestReportResults_*) pins the rest
// of Results' behavior in detail, so this file keeps only the two shapes
// that motivated the Reporter type: a success row, and a finding welded to
// its remedy line.
func TestReporter_Results_PrintsFindingAndRemedyForFailure(t *testing.T) {
	const remedy = "set GIT_USER_NAME, or configure git user.name on the host"
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: remedy,
			},
			Err: errors.New("GIT_USER_NAME is unset"),
		},
	}

	var buf bytes.Buffer
	NewReporter(&buf, true).Results(results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n" +
		"  remedy: " + remedy + "\n"
	if buf.String() != want {
		t.Fatalf("Results() wrote %q, want %q", buf.String(), want)
	}
}

// The quiet/verbose split (issue #3777): a quiet Reporter drops ok: and
// advisory: rows, keeps MISSING: rows and their remedy, and Passthrough
// always writes regardless.

func TestReporter_Quiet_SuppressesSuccessRow(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, false).Success("git-user-name")

	if buf.String() != "" {
		t.Fatalf("Success() wrote %q in quiet mode, want empty", buf.String())
	}
}

func TestReporter_Quiet_SuppressesAdvisoryFinding(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, false).Finding(Advisory, "bwrap-cgroup-delegation: %s", "cgroup delegation is not configured")

	if buf.String() != "" {
		t.Fatalf("Finding(Advisory) wrote %q in quiet mode, want empty", buf.String())
	}
}

func TestReporter_Quiet_KeepsRequiredFinding(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, false).Finding(Required, "git-user-name: %s", "GIT_USER_NAME is unset")

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n"
	if buf.String() != want {
		t.Fatalf("Finding(Required) wrote %q in quiet mode, want %q", buf.String(), want)
	}
}

func TestReporter_Quiet_KeepsPassthroughVerbatim(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf, false).Passthrough("create label %q? [y/N] ", "ready-for-agent")

	want := `create label "ready-for-agent"? [y/N] `
	if buf.String() != want {
		t.Fatalf("Passthrough() wrote %q in quiet mode, want %q", buf.String(), want)
	}
}

func TestReporter_Quiet_Results_KeepsRequiredFindingAndItsRemedy(t *testing.T) {
	const remedy = "set GIT_USER_NAME, or configure git user.name on the host"
	results := []Result{
		{
			Check: Check{
				Name:   "git-user-name",
				Remedy: remedy,
			},
			Err: errors.New("GIT_USER_NAME is unset"),
		},
	}

	var buf bytes.Buffer
	NewReporter(&buf, false).Results(results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n" +
		"  remedy: " + remedy + "\n"
	if buf.String() != want {
		t.Fatalf("Results() wrote %q in quiet mode, want %q", buf.String(), want)
	}
}

func TestReporter_Quiet_Results_DropsSuccessRow(t *testing.T) {
	results := []Result{
		{Check: Check{Name: "git-user-name"}},
	}

	var buf bytes.Buffer
	NewReporter(&buf, false).Results(results)

	if buf.String() != "" {
		t.Fatalf("Results() wrote %q in quiet mode for a success, want empty", buf.String())
	}
}

// A Required-tier failure wrapping ErrDegraded demotes to Advisory at print
// (registry.go's Blocking / Reporter.Results), so it must drop out of a
// quiet run's output along with its remedy — never an orphan "  remedy:"
// line with no row above it.
func TestReporter_Quiet_Results_DropsDegradedRequiredFindingAndItsRemedy(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "branch-protection",
				Tier:   Required,
				Remedy: "grant the token repo-admin scope",
			},
			Err: fmt.Errorf("permission denied reading protection: %w", ErrDegraded),
		},
	}

	var buf bytes.Buffer
	NewReporter(&buf, false).Results(results)

	if buf.String() != "" {
		t.Fatalf("Results() wrote %q in quiet mode for a degraded Required failure, want empty (no orphan remedy line)", buf.String())
	}
}

func TestReporter_Verbose_Results_PrintsDegradedRequiredFindingAsAdvisory(t *testing.T) {
	results := []Result{
		{
			Check: Check{
				Name:   "branch-protection",
				Tier:   Required,
				Remedy: "grant the token repo-admin scope",
			},
			Err: fmt.Errorf("permission denied reading protection: %w", ErrDegraded),
		},
	}

	var buf bytes.Buffer
	NewReporter(&buf, true).Results(results)

	want := "advisory: branch-protection: permission denied reading protection\n" +
		"  remedy: grant the token repo-admin scope\n"
	if buf.String() != want {
		t.Fatalf("Results() wrote %q, want %q", buf.String(), want)
	}
}
