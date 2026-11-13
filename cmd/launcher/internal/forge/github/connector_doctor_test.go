package github

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// A whole doctor pass for a github config under a configured row reads the
// repository through the Connector alone: no check reaches gh.
func TestDoctorRun_ConfiguredRowRunsNoGh(t *testing.T) {
	labels := testLabels
	labels.Recoverable = "agent-recoverable"
	h, _, calls := routedHarness(t)
	h.backend.labels = []string{labels.Dispatchable, labels.InProgress, labels.Complete, labels.Failed}
	h.SeedProtection("main", 200, 0)
	h.SeedIssue(forge.Issue{Number: "7", Title: "stranded", Labels: []string{labels.Recoverable}})
	h.SeedIssue(forge.Issue{Number: "8", Title: "other", Labels: []string{labels.Dispatchable}})
	c := NewExecClient(connectorRepo, labels)

	var out bytes.Buffer
	err := doctor.Run(c, c, doctor.Config{
		IssueTracker:    "github",
		Label:           labels.Dispatchable,
		InProgressLabel: labels.InProgress,
		FailedLabel:     labels.Failed,
		CompleteLabel:   labels.Complete,
		MergePolicy:     "immediate",
		BaseBranch:      "main",
	}, doctor.NewReporter(&out, true), bufio.NewScanner(strings.NewReader("")), false, nil)

	if err != nil {
		t.Fatalf("doctor.Run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "1 recoverable issue(s)") {
		t.Errorf("report does not count the recoverable issue read through the Connector:\n%s", out.String())
	}
	if n := calls(); n != 0 {
		t.Errorf("a full doctor pass ran gh %d times", n)
	}
}
