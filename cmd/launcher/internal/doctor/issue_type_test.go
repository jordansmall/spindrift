package doctor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// issueTypeTracker is a Fake that also answers the IssueTypeChecker seam.
type issueTypeTracker struct {
	*forge.Fake
	unfilled []string
	err      error
	calls    int
}

func (t *issueTypeTracker) CheckIssueType() ([]string, error) {
	t.calls++
	return t.unfilled, t.err
}

func runIssueTypeDoctor(t *testing.T, it forge.IssueTracker, f *forge.Fake) (string, error) {
	t.Helper()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	var buf bytes.Buffer
	err := Run(it, f, defaultDoctorConfig(), NewReporter(&buf, true), bufio.NewScanner(strings.NewReader("")), false, nil)
	return buf.String(), err
}

func TestRun_IssueType_UnknownTypeFailsRequired(t *testing.T) {
	f := forge.NewFake()
	it := &issueTypeTracker{Fake: f, err: fmt.Errorf("JIRA_ISSUE_TYPE %q: %w", "Story", forge.ErrUnknownIssueType)}
	out, err := runIssueTypeDoctor(t, it, f)
	if err == nil || !errors.Is(err, forge.ErrUnknownIssueType) {
		t.Fatalf("Run err = %v, want one wrapping ErrUnknownIssueType", err)
	}
	if !strings.Contains(out, "MISSING: "+IssueTypeCheckName) {
		t.Errorf("output lacks a MISSING row for %s:\n%s", IssueTypeCheckName, out)
	}
	if it.calls != 1 {
		t.Errorf("CheckIssueType called %d times, want 1 shared across both rows", it.calls)
	}
}

func TestRun_IssueType_APIErrorIsIndeterminate(t *testing.T) {
	f := forge.NewFake()
	it := &issueTypeTracker{Fake: f, err: errors.New("403 forbidden")}
	out, err := runIssueTypeDoctor(t, it, f)
	if err != nil {
		t.Fatalf("an unreadable createmeta must not fail Run, got %v", err)
	}
	if !strings.Contains(out, "advisory: "+IssueTypeCheckName) {
		t.Errorf("want an advisory row for %s:\n%s", IssueTypeCheckName, out)
	}
}

func TestRun_IssueType_UnfilledFieldsWarnAdvisory(t *testing.T) {
	f := forge.NewFake()
	it := &issueTypeTracker{Fake: f, unfilled: []string{`"Team" (customfield_10010)`}}
	out, err := runIssueTypeDoctor(t, it, f)
	if err != nil {
		t.Fatalf("unfilled fields are advisory, got %v", err)
	}
	if !strings.Contains(out, "advisory: "+IssueTypeFieldsCheckName) || !strings.Contains(out, "customfield_10010") {
		t.Errorf("want an advisory row naming the field:\n%s", out)
	}
}

func TestRun_IssueType_AllGoodReportsOK(t *testing.T) {
	f := forge.NewFake()
	out, err := runIssueTypeDoctor(t, &issueTypeTracker{Fake: f}, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "advisory: "+IssueTypeFieldsCheckName) || strings.Contains(out, "MISSING: "+IssueTypeCheckName) {
		t.Errorf("clean createmeta reported a finding:\n%s", out)
	}
	if !strings.Contains(out, "ok: issue type") {
		t.Errorf("want an ok row for the issue type:\n%s", out)
	}
}

func TestRun_IssueType_AbsentCapabilityShowsNoRow(t *testing.T) {
	f := forge.NewFake()
	out, err := runIssueTypeDoctor(t, f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "issue type") || strings.Contains(out, "issue-type") {
		t.Errorf("tracker without the capability grew an issue-type row:\n%s", out)
	}
}
