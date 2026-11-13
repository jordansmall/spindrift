package doctor

import (
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
