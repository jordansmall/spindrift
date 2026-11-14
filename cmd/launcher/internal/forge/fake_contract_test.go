package forge_test

import (
	"errors"
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
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
func (h *fakeHarness) SeedComments(num string, comments []forge.Comment) {
	if h.f.CommentsFor == nil {
		h.f.CommentsFor = map[string][]forge.Comment{}
	}
	h.f.CommentsFor[num] = comments
}
func (h *fakeHarness) IsolatesNativeFailure() {}
func (h *fakeHarness) IsPriorityCapable()     {}

func TestFake_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newFakeHarness())
}

// readOnlyHarness wraps the Fake behind a tracker whose writes fail the test,
// the shape of a Connector-backed tracker: RunTrackerReadContract must seed
// dispatch state through SeedState, never TransitionState.
type readOnlyHarness struct {
	*fakeHarness
	t *testing.T
}

type readOnlyTracker struct {
	*forge.Fake
	t *testing.T
}

func (r readOnlyTracker) TransitionState(num string, from, to forge.DispatchState) error {
	r.t.Errorf("read contract wrote: TransitionState(%s, %v, %v)", num, from, to)
	return nil
}

func (r readOnlyTracker) CompleteVerdict(num string, v forge.Verdict) error {
	r.t.Errorf("read contract wrote: CompleteVerdict(%s)", num)
	return nil
}

func (r readOnlyTracker) Comment(num, body string) error {
	r.t.Errorf("read contract wrote: Comment(%s)", num)
	return nil
}

func (h readOnlyHarness) Tracker() forge.IssueTracker {
	return readOnlyTracker{Fake: h.f, t: h.t}
}

func (h readOnlyHarness) SeedState(num string, state forge.DispatchState) {
	iss, err := h.f.Issue(num)
	if err != nil {
		h.t.Fatalf("SeedState(%s): %v", num, err)
	}
	iss.Labels = append(iss.Labels, testLabels.Label(state))
	h.f.SetIssue(iss)
}

func TestFake_TrackerReadContract_NoWrites(t *testing.T) {
	forgetest.RunTrackerReadContract(t, readOnlyHarness{fakeHarness: newFakeHarness(), t: t})
}

// TestFake_TrackerContract_RemoteEngine runs the tracker reads, work and
// verdict contracts with the Fake's Connector in a child connector-host.
func TestFake_TrackerContract_RemoteEngine(t *testing.T) {
	forge.UseRemoteSeam(t, func() *exec.Cmd { return fixturebuild.ConnectorHostCmd(t) })
	forgetest.RunTrackerContract(t, newFakeHarness())
}
