package settle

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// captureStderr returns everything fn writes to os.Stderr. It reads the pipe
// only after fn returns, so it is safe only for output that stays well under
// the pipe's 64KB buffer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	defer r.Close()
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(captured)
}

// Pins issue #1946: gate.go's blocked path gated the relay on s.pr != nil,
// which is always nil for local's push-only forge, so a read-only
// CODE_FORGE=local Box never got its bundle relayed. local implements no
// DraftPRCreator, so no draft PR is attempted even though the box printed a
// PR-intent line.
func TestSettle_LocalReadOnly_BlockedRelaysBundleWithoutDraftPR(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 1 || fc.RelayBundleCalls[0] != (forge.RelayBundleCall{OutboxDir: "/outbox/1946", Ref: branch}) {
		t.Fatalf("RelayBundleCalls = %+v, want one call with outbox=/outbox/1946 ref=%s", fc.RelayBundleCalls, branch)
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("CreateDraftPR must not be called; local doesn't implement DraftPRCreator, got %+v", fc.CreateDraftPRCalls)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a blocked outcome; labels=%v", iss.Labels)
	}
	var noteCalls []forge.CommentCall
	for _, call := range fc.CommentCalls {
		if call.Body == result.Resolved.Outcome.Note {
			noteCalls = append(noteCalls, call)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
}

// Read-write never consults BundleRelay on a blocked outcome on the push-only
// CODE_FORGE=local forge. The github counterpart is a TestSettle_PRLander row.
func TestSettle_LocalReadWrite_BlockedUnaffectedByHostMediation(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: fc.AgentBranch(issNum), Status: "blocked", Note: "push rejected"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}

	c := baseConfig() // Config.ReadOnly defaults false
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called under read-write, got %+v", fc.RelayBundleCalls)
	}
}

// A relay failure on the push-only CODE_FORGE=local forge still leaves the
// recorded blocked/agent-failed outcome alone; local has no CreateDraftPR to
// skip. The github counterpart is a TestSettle_PRLander row.
func TestSettle_LocalReadOnly_BlockedRelayFailureStaysBlocked(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after a failed relay; labels=%v", iss.Labels)
	}
}

// Issue #2096 under CODE_FORGE=local: an absent bundle logs an informational
// ".." line, not an alarming "??" one, and leaves the recorded
// blocked/agent-failed outcome untouched. The benign log lives at the shared
// relayBlockedWork call site, so this covers the push-only forge shape.
func TestSettle_LocalReadOnly_BlockedRelayAbsentBundleLogsBenign(t *testing.T) {
	const issNum = "1946"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

	d := dispatch.NewFake()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "review never cleared"},
		},
	}

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc, fc.AsLocal())

	stderr := captureStderr(t, func() { s.Settle(d, issNum, 0, result) })

	if strings.Contains(stderr, "could not relay blocked-hand-off bundle") {
		t.Errorf("stderr must not contain the alarming relay-failure phrase, got: %s", stderr)
	}
	if strings.Contains(stderr, "?? #"+issNum) {
		t.Errorf("stderr must not contain a ?? warning line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, ".. #"+issNum+":") {
		t.Errorf("stderr must contain an informational .. line for #%s, got: %s", issNum, stderr)
	}
	if !strings.Contains(stderr, "no blocked-hand-off bundle to relay") {
		t.Errorf("stderr must contain the benign no-bundle-to-relay phrase, got: %s", stderr)
	}

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must still carry agent-failed after an absent-bundle relay; labels=%v", iss.Labels)
	}
}
