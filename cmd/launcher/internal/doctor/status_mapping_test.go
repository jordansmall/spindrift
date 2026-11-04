package doctor

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// statusMappingTracker is a Fake that also answers the StatusMappingChecker seam.
type statusMappingTracker struct {
	*forge.Fake
	missing []string
	err     error
	calls   int
}

func (t *statusMappingTracker) CheckMappedStatuses() ([]string, error) {
	t.calls++
	return t.missing, t.err
}

func TestRun_StatusMapping_MissingStatusFailsRequired(t *testing.T) {
	f := forge.NewFake()
	it := &statusMappingTracker{Fake: f, missing: []string{"Dnoe", "Wip"}}
	out, err := runIssueTypeDoctor(t, it, f)
	if err == nil {
		t.Fatalf("Run err = nil, want a Required failure\n%s", out)
	}
	for _, want := range []string{`"Dnoe"`, `"Wip"`, "JIRA_STATUS_MAPPING"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if !strings.Contains(out, "MISSING: "+StatusMappingCheckName) {
		t.Errorf("output lacks a MISSING row for %s:\n%s", StatusMappingCheckName, out)
	}
}

func TestRun_StatusMapping_AllPresentPasses(t *testing.T) {
	f := forge.NewFake()
	it := &statusMappingTracker{Fake: f}
	out, err := runIssueTypeDoctor(t, it, f)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "every mapped status exists") {
		t.Errorf("want a passing %s row:\n%s", StatusMappingCheckName, out)
	}
}

func TestRun_StatusMapping_APIErrorIsIndeterminate(t *testing.T) {
	f := forge.NewFake()
	it := &statusMappingTracker{Fake: f, err: errors.New("403 forbidden")}
	out, err := runIssueTypeDoctor(t, it, f)
	if err != nil {
		t.Fatalf("an unreadable statuses endpoint must not fail Run, got %v", err)
	}
	if !strings.Contains(out, "advisory: "+StatusMappingCheckName) {
		t.Errorf("want an advisory row for %s:\n%s", StatusMappingCheckName, out)
	}
}

func TestStatusMappingChecks_NoCapabilityNoRows(t *testing.T) {
	if rows := StatusMappingChecks(forge.Capabilities{}); len(rows) != 0 {
		t.Fatalf("got %d rows, want none", len(rows))
	}
}
