package doctor

import (
	"bytes"
	"errors"
	"testing"
)

func TestReporter_Success_PrintsOkRow(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf).Success("git-user-name")

	want := "ok: git-user-name\n"
	if buf.String() != want {
		t.Fatalf("Success() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Success_FormatsArgs(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf).Success("github-repo: %s", "jordansmall/spindrift")

	want := "ok: github-repo: jordansmall/spindrift\n"
	if buf.String() != want {
		t.Fatalf("Success() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Finding_RequiredTierPrintsMissing(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf).Finding(Required, "git-user-name: %s", "GIT_USER_NAME is unset")

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n"
	if buf.String() != want {
		t.Fatalf("Finding() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Finding_AdvisoryTierPrintsAdvisory(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf).Finding(Advisory, "bwrap-cgroup-delegation: %s", "cgroup delegation is not configured")

	want := "advisory: bwrap-cgroup-delegation: cgroup delegation is not configured\n"
	if buf.String() != want {
		t.Fatalf("Finding() wrote %q, want %q", buf.String(), want)
	}
}

func TestReporter_Passthrough_PrintsVerbatimWithNoTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	NewReporter(&buf).Passthrough("create label %q? [y/N] ", "ready-for-agent")

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
	NewReporter(&buf).Results(results)

	want := "MISSING: git-user-name: GIT_USER_NAME is unset\n" +
		"  remedy: " + remedy + "\n"
	if buf.String() != want {
		t.Fatalf("Results() wrote %q, want %q", buf.String(), want)
	}
}
