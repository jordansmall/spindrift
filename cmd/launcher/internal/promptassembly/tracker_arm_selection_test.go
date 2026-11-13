package promptassembly

import (
	"strings"
	"testing"
)

// trackerArmRegistry carries the tracker-axis rows the work prompt wires
// (issue-read, IF BLOCKED comment, PR-body ticket reference) with the real
// gate and var names and one marker body per backend folder, so each case
// reads which arm a tracker selected from the markers alone.
func trackerArmRegistry(t *testing.T) (Registry, Env) {
	t.Helper()
	return syntheticRegistry(t,
		[]FragmentRow{
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_LINKS_READABLE", Fragment: "issue-read.md", Var: "READ_STEP"},
			{Gate: "ISSUE_TRACKER_LINKS_UNREADABLE", Fragment: "issue-read-unreadable.md", Var: "READ_STEP"},
			{Axis: "tracker", Gate: "ISSUE_TRACKER_READWRITE", Fragment: "issue-blocked-comment.md", Var: "BLOCKED_RW_STEP"},
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_RELAYED", Fragment: "issue-blocked-comment-relay.md", Var: "BLOCKED_RELAY_STEP"},
			{Gate: "ISSUE_TRACKER_UNWRITABLE_READWRITE", Fragment: "issue-blocked-comment-unwritable.md", Var: "BLOCKED_UNWRITABLE_STEP"},
			{Gate: "PR_BODY_CLOSES", Fragment: "pr-body-closes.md", Var: "PR_CLOSES_STEP"},
			{Axis: "tracker", Gate: "PR_BODY_ISSUE_REF", Fragment: "pr-body-issue-ref.md", Var: "PR_REF_STEP"},
			{Axis: "tracker", Gate: "PR_BODY_NO_ISSUE_REF", Fragment: "pr-body-no-issue-ref.md", Var: "PR_NOREF_STEP"},
		},
		map[string]string{
			"issue-prompt.md": "read:${READ_STEP}|rw:${BLOCKED_RW_STEP}|relay:${BLOCKED_RELAY_STEP}|" +
				"unwritable:${BLOCKED_UNWRITABLE_STEP}|pr:${PR_CLOSES_STEP}${PR_REF_STEP}${PR_NOREF_STEP}|\n",

			"fragments/issue-read.md":                         "SHARED-READ",
			"fragments/local/issue-read.md":                   "LOCAL-READ",
			"fragments/issue-read-unreadable.md":              "UNREADABLE",
			"fragments/github/issue-blocked-comment.md":       "GH-BLOCKED",
			"fragments/forgejo/issue-blocked-comment.md":      "FJ-BLOCKED",
			"fragments/issue-blocked-comment-relay.md":        "SHARED-RELAY",
			"fragments/github/issue-blocked-comment-relay.md": "GH-RELAY",
			"fragments/local/issue-blocked-comment-relay.md":  "LOCAL-RELAY",
			"fragments/issue-blocked-comment-unwritable.md":   "UNWRITABLE",
			"fragments/pr-body-closes.md":                     "CLOSES",
			"fragments/local/pr-body-issue-ref.md":            "LOCAL-REF",
			"fragments/local/pr-body-no-issue-ref.md":         "LOCAL-NOREF",
		})
}

// Issue #2352, #2533, #5647, #6344, #6894: the tracker and carrier pick each step's arm.
// local reads its own folder and relays its comment; forgejo takes the shared
// issue-read step and its own comment folder; jira reads its own (empty)
// folder and takes the shared files, except where it has no in-Box client
// (host-posted comment). Under the
// log carrier no tracker but local can read a linked issue (unreadable arm).
// The PR-body ref pair keys on LocalIssueReference, and only local carries
// files for it, so github and jira always close the issue.
func TestAssembleTrackerArmSelectsBackendFolder(t *testing.T) {
	forgejo := func(e *Env) {
		e.IssueTracker, e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = "forgejo", "FORGEJO", "FORGEJO", "FORGEJO"
	}
	local := func(e *Env) {
		e.IssueTracker, e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = "local", "LOCAL", "", "GH"
	}
	jira := func(e *Env) {
		e.IssueTracker, e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = "jira", "JIRA", "GITHUB", "GH"
	}
	readOnly := func(e *Env) { e.BoxWriteEnabled = false }
	localRef := func(e *Env) { e.LocalIssueReference = true }
	logCarrier := func(e *Env) { e.SignalCarrier = "log" }
	with := func(fs ...func(*Env)) func(*Env) {
		return func(e *Env) {
			for _, f := range fs {
				f(e)
			}
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Env)
		want   string
	}{
		{"github read-write", with(), "read:SHARED-READ|rw:GH-BLOCKED|relay:|unwritable:|pr:CLOSES|"},
		{"github read-only relays through the github folder", readOnly, "read:SHARED-READ|rw:|relay:GH-RELAY|unwritable:|pr:CLOSES|"},
		{"github ignores LocalIssueReference", localRef, "read:SHARED-READ|rw:GH-BLOCKED|relay:|unwritable:|pr:CLOSES|"},
		{"forgejo read-write", forgejo, "read:SHARED-READ|rw:FJ-BLOCKED|relay:|unwritable:|pr:CLOSES|"},
		{"forgejo read-only falls back to the shared relay", with(forgejo, readOnly), "read:SHARED-READ|rw:|relay:SHARED-RELAY|unwritable:|pr:CLOSES|"},
		{"local relays even on a write-capable box", local, "read:LOCAL-READ|rw:|relay:LOCAL-RELAY|unwritable:|pr:LOCAL-NOREF|"},
		{"local with LocalIssueReference", with(local, localRef), "read:LOCAL-READ|rw:|relay:LOCAL-RELAY|unwritable:|pr:LOCAL-REF|"},
		{"jira read-write", jira, "read:SHARED-READ|rw:|relay:|unwritable:UNWRITABLE|pr:CLOSES|"},
		{"jira read-only falls back to the shared relay", with(jira, readOnly), "read:SHARED-READ|rw:|relay:SHARED-RELAY|unwritable:|pr:CLOSES|"},
		{"jira ignores LocalIssueReference", with(jira, localRef), "read:SHARED-READ|rw:|relay:|unwritable:UNWRITABLE|pr:CLOSES|"},
		{"github without a socket cannot read linked issues", logCarrier, "read:UNREADABLE|rw:GH-BLOCKED|relay:|unwritable:|pr:CLOSES|"},
		{"jira without a socket cannot read linked issues", with(jira, logCarrier), "read:UNREADABLE|rw:|relay:|unwritable:UNWRITABLE|pr:CLOSES|"},
		{"local reads its own folder without a socket", with(local, logCarrier), "read:LOCAL-READ|rw:|relay:LOCAL-RELAY|unwritable:|pr:LOCAL-NOREF|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := trackerArmRegistry(t)
			env.SignalCarrier = "socket"
			tc.mutate(&env)

			result, err := Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if got := strings.Join(strings.Fields(result.Prompt), ""); got != tc.want {
				t.Errorf("Prompt = %q, want %q", got, tc.want)
			}
		})
	}
}
