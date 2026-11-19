package promptassembly

import (
	"strings"
	"testing"
)

// trackerArmRegistry carries the tracker-axis rows the work prompt wires
// (issue-read, IF BLOCKED comment relay, PR-body ticket reference) with the real
// gate and var names and one marker body per backend folder, so each case
// reads which arm a tracker selected from the markers alone.
func trackerArmRegistry(t *testing.T) (Registry, Env) {
	t.Helper()
	return syntheticRegistry(t,
		[]FragmentRow{
			{Axis: "tracker", Shared: true, Gate: "ISSUE_TRACKER_LINKS_READABLE", Fragment: "issue-read.md", Var: "READ_STEP"},
			{Gate: "ISSUE_TRACKER_LINKS_UNREADABLE", Fragment: "issue-read-unreadable.md", Var: "READ_STEP"},
			{Axis: "tracker", Shared: true, Fragment: "issue-blocked-comment-relay.md", Var: "BLOCKED_RELAY_STEP"},
			{Gate: "PR_BODY_CLOSES", Fragment: "pr-body-closes.md", Var: "PR_CLOSES_STEP"},
			{Axis: "tracker", Gate: "PR_BODY_ISSUE_REF", Fragment: "pr-body-issue-ref.md", Var: "PR_REF_STEP"},
			{Axis: "tracker", Gate: "PR_BODY_NO_ISSUE_REF", Fragment: "pr-body-no-issue-ref.md", Var: "PR_NOREF_STEP"},
		},
		map[string]string{
			"issue-prompt.md": "read:${READ_STEP}|relay:${BLOCKED_RELAY_STEP}|" +
				"pr:${PR_CLOSES_STEP}${PR_REF_STEP}${PR_NOREF_STEP}|\n",

			"fragments/issue-read.md":                         "SHARED-READ",
			"fragments/local/issue-read.md":                   "LOCAL-READ",
			"fragments/issue-read-unreadable.md":              "UNREADABLE",
			"fragments/issue-blocked-comment-relay.md":        "SHARED-RELAY",
			"fragments/github/issue-blocked-comment-relay.md": "GH-RELAY",
			"fragments/local/issue-blocked-comment-relay.md":  "LOCAL-RELAY",
			"fragments/pr-body-closes.md":                     "CLOSES",
			"fragments/local/pr-body-issue-ref.md":            "LOCAL-REF",
			"fragments/local/pr-body-no-issue-ref.md":         "LOCAL-NOREF",
		})
}

// Issue #2352, #2533, #5647, #6344, #6894: the tracker and carrier pick each
// step's arm. local reads its own folder and relays its comment from it;
// forgejo and jira take the shared issue-read and relay files, github its own
// folder's relay. Under the log carrier no tracker but local can read a linked
// issue (unreadable arm).
// The PR-body ref pair keys on LocalIssueReference, and only local carries
// files for it. PR_BODY_CLOSES needs the forge to share the tracker's issue
// namespace: github on github and forgejo on forgejo close, the rest do not.
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
	forgejoForge := func(e *Env) { e.CodeForge = "forgejo" }
	localRef := func(e *Env) { e.LocalIssueReference = true }
	logCarrier := func(e *Env) { e.SignalCarrier, e.SignalIssueRead = "log", false }
	noSignal := func(e *Env) { e.SignalIssueRead = false }
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
		{"github relays through the github folder", with(), "read:SHARED-READ|relay:GH-RELAY|pr:CLOSES|"},
		{"github ignores LocalIssueReference", localRef, "read:SHARED-READ|relay:GH-RELAY|pr:CLOSES|"},
		{"forgejo falls back to the shared relay", forgejo, "read:SHARED-READ|relay:SHARED-RELAY|pr:|"},
		{"forgejo tracker on a forgejo forge closes", with(forgejo, forgejoForge), "read:SHARED-READ|relay:SHARED-RELAY|pr:CLOSES|"},
		{"local relays from its own folder", local, "read:LOCAL-READ|relay:LOCAL-RELAY|pr:LOCAL-NOREF|"},
		{"local with LocalIssueReference", with(local, localRef), "read:LOCAL-READ|relay:LOCAL-RELAY|pr:LOCAL-REF|"},
		{"jira falls back to the shared relay", jira, "read:SHARED-READ|relay:SHARED-RELAY|pr:|"},
		{"jira ignores LocalIssueReference", with(jira, localRef), "read:SHARED-READ|relay:SHARED-RELAY|pr:|"},
		{"github on the socket without the host read signal cannot read linked issues", noSignal, "read:UNREADABLE|relay:GH-RELAY|pr:CLOSES|"},
		{"github without a socket cannot read linked issues", logCarrier, "read:UNREADABLE|relay:GH-RELAY|pr:CLOSES|"},
		{"jira without a socket cannot read linked issues", with(jira, logCarrier), "read:UNREADABLE|relay:SHARED-RELAY|pr:|"},
		{"local reads its own folder without a socket", with(local, logCarrier), "read:LOCAL-READ|relay:LOCAL-RELAY|pr:LOCAL-NOREF|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, env := trackerArmRegistry(t)
			env.SignalCarrier = "socket"
			env.SignalIssueRead = true
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
