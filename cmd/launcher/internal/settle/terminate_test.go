package settle

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// assertNoTransitionOrComment fails t if TransitionState, Comment, or
// EnqueueAutoMerge was called, the shared "must not act after termination"
// check. MarkReady is deliberately left out: termination may land during or
// after its round-trip, so a row asserts the landing calls it expects itself.
func assertNoTransitionOrComment(t *testing.T, fc *forge.Fake) {
	t.Helper()
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("Comment must not be called after termination; got %+v", fc.CommentCalls)
	}
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("EnqueueAutoMerge must not be called after termination; got %+v", fc.EnqueueAutoMergeCalls)
	}
}

// assertClaimUntouched checks issue 1 still wears the live run's
// agent-in-progress claim and was never demoted to agent-failed.
func assertClaimUntouched(t *testing.T, fc *forge.Fake) {
	t.Helper()
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-in-progress") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("live run's claim must stand untouched; labels=%v", iss.Labels)
	}
}

// Guards against #1174 regressing: doc-comments here once pinned another file's
// exact line number, which rots the instant that file shifts. Symbol names
// (function, type, const) are stable across edits; line numbers are not.
func TestDocComments_NoHardcodedLineNumbers(t *testing.T) {
	src, err := os.ReadFile("terminate_test.go")
	if err != nil {
		t.Fatalf("ReadFile(terminate_test.go) = %v", err)
	}

	re := regexp.MustCompile(`\b\w+\.go:\d+`)
	var found []string
	for _, line := range strings.Split(string(src), "\n") {
		if _, comment, ok := strings.Cut(line, "//"); ok {
			found = append(found, re.FindAllString(comment, -1)...)
		}
	}
	if found != nil {
		t.Errorf("doc-comments must reference symbols, not line numbers; found %v", found)
	}
}

// Registry is the Settle's own and never changes identity, so a mark written
// through the handle any caller took is the mark the settle checkpoints read
// (#3522): before this, a second caller could install a registry of its own
// and strand the first caller's marks where nothing checks them.
// Internal seam: asserts the checkpoint through prLander.gateToGreen, the narrowest reader of the registry mark.
func TestInternalSeam_Registry_StableHandleIsWhatCheckpointsRead(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	if s.Registry() != s.Registry() {
		t.Fatal("Registry: want the same registry on every call")
	}
	s.Registry().Mark("1")

	got, _ := prLander{s}.gateToGreen("1", 0, testPR, false)

	if !errors.Is(got.err, errAbandoned) {
		t.Errorf("gateToGreen = %v (err %v), want errAbandoned", got.outcome, got.err)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}
