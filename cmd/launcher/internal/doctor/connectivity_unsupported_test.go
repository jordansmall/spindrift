package doctor

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// unsupportedProbeTracker is a Fake whose Probe names a capability its
// Connector does not declare.
type unsupportedProbeTracker struct{ *forge.Fake }

func (unsupportedProbeTracker) Probe() (string, error) {
	return "", fmt.Errorf("the github connector does not declare forge.probe: %w", ErrUnsupported)
}

// A connectivity row whose Connector lacks the capability reports as
// unsupported by name and reason, and does not fail Run.
func TestRun_ConnectivityProbeUnsupportedDoesNotFail(t *testing.T) {
	f := forge.NewFake()
	out, err := runIssueTypeDoctor(t, unsupportedProbeTracker{f}, f)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	want := "unsupported: issue-tracker (the github connector does not declare forge.probe)"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

// unsupportedProtectionForge is a Fake whose branch-protection read names a
// capability its Connector does not declare.
type unsupportedProtectionForge struct{ *forge.Fake }

func (unsupportedProtectionForge) BranchProtected(string) (bool, error) {
	return false, fmt.Errorf("the github connector cannot read branch protection: %w", ErrUnsupported)
}

// A branch-protection row whose Connector lacks the capability reports as
// unsupported, not as a degraded or failed check, and never blocks.
func TestBranchProtectionCheck_UnsupportedIsNotFailed(t *testing.T) {
	f := forge.NewFake()
	caps := forge.Capabilities{BranchProtectionForge: unsupportedProtectionForge{f}}
	res := RunChecks([]Check{BranchProtectionCheck(caps, "auto", "main")})[0]
	if !errors.Is(res.Err, ErrUnsupported) || errors.Is(res.Err, ErrDegraded) {
		t.Fatalf("Err = %v, want ErrUnsupported alone", res.Err)
	}
	if Blocking(res) {
		t.Error("an unsupported branch-protection probe blocks the run")
	}
	var buf bytes.Buffer
	NewReporter(&buf, true).Results([]Result{res})
	want := "unsupported: branch-protection (the github connector cannot read branch protection)"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, buf.String())
	}
}

// unsupportedLabelsTracker is a Fake whose label list names a capability its
// Connector does not declare.
type unsupportedLabelsTracker struct{ *forge.Fake }

func (unsupportedLabelsTracker) ListLabels() ([]string, error) {
	return nil, fmt.Errorf("the github connector cannot list labels: %w", ErrUnsupported)
}

// A label check whose Connector lacks the capability reports as unsupported
// and does not fail Run.
func TestRun_LabelListUnsupportedDoesNotFail(t *testing.T) {
	f := forge.NewFake()
	out, err := runIssueTypeDoctor(t, unsupportedLabelsTracker{f}, f)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out)
	}
	want := "unsupported: labels (the github connector cannot list labels)"
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}
