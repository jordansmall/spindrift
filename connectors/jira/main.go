// Command jira is the Jira tracker as a Connector (ADR 0073): REST calls sent
// through the host's http_request Host call, which attaches the credential.
// The native jira adapter stays authoritative until the cut-over. The
// Connector returns raw fields only (state, status, close time, the
// entity-property markers, link keys, the doctor and daemon checks' raw data)
// and performs the writes core asks for, one transition, label edit, comment or
// filed issue at a time; core makes every lifecycle decision. The Connector
// only refuses a status mapping in which two states other than failed and
// ambiguous share a status.
package main

import "spindrift.dev/sdk/tracker"

func init() {
	// Comments is set, unlike connectors/github: Jira has no minimized-comment
	// concept, so Minimized=false is the faithful report, not a gap.
	tracker.Register(tracker.Reader{
		IDs:        tracker.ProjectKeyIDs,
		ListIssues: listIssues,
		Issue:      issue,
		Comments:   comments,
		NativeDeps: nativeDeps,
		Blocks:     blocks,

		Probe:         probe,
		Statuses:      statuses,
		CreateMeta:    createMeta,
		Count:         count,
		CountInterval: countInterval,
	})
	// CreateLabel is left unset: Jira labels are free text created on first
	// use, so core's create call is answered as a no-op.
	tracker.RegisterWrites(tracker.Writer{
		Transition: transition,
		EditLabels: editLabels,
		ListLabels: listLabels,
		Comment:    comment,
		FileIssue:  fileIssue,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
