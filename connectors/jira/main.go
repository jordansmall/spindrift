// Command jira is the Jira tracker read path as a Connector (ADR 0073): REST
// reads sent through the host's http_request Host call, which attaches the
// credential. The native jira adapter stays authoritative; every write stays
// in core. The Connector returns raw fields only (state, close time, the
// entity-property markers, link keys), and core makes every policy decision.
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
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
