package forge_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

var errNativeDepsFailed = errors.New("native dependency lookup failed")

// fakeHarness adapts forge.Fake to forgetest.Harness — the Fake needs no
// scripted backend, so SeedIssue and FailNativeDeps talk to it directly.
type fakeHarness struct {
	f *forge.Fake
}

func newFakeHarness() *fakeHarness {
	f := forge.NewFake(testLabels)
	f.VerdictLabels = forge.ResearchVerdictLabels()
	return &fakeHarness{f: f}
}

func (h *fakeHarness) Tracker() forge.IssueTracker { return h.f }
func (h *fakeHarness) SeedIssue(iss forge.Issue)   { h.f.SetIssue(iss) }
func (h *fakeHarness) FailNativeDeps(num string) {
	if h.f.NativeDepsErr == nil {
		h.f.NativeDepsErr = map[string]error{}
	}
	h.f.NativeDepsErr[num] = errNativeDepsFailed
}
func (h *fakeHarness) SeedNativeDeps(num string, ids []string) {
	if h.f.NativeDeps == nil {
		h.f.NativeDeps = map[string][]string{}
	}
	h.f.NativeDeps[num] = ids
}
func (h *fakeHarness) IsolatesNativeFailure() {}

func TestFake_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newFakeHarness())
}
