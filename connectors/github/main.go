// Command github is the GitHub tracker's read path as a Connector (ADR 0073):
// REST reads sent through the host's http_request Host call, which attaches
// the credential. The native github adapter stays authoritative; every write
// stays in core. The Connector returns raw fields only, and core makes every
// policy decision (trust, transitions, dependency parsing).
package main

import "spindrift.dev/sdk/tracker"

func init() {
	// Comments stays nil: REST reports no minimized state, so core keeps
	// reading comments from the native (GraphQL) adapter.
	tracker.Register(tracker.Reader{
		ListIssues: listIssues,
		Issue:      issue,
		NativeDeps: nativeDeps,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
