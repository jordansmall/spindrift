// Command github is the GitHub tracker and forge as a Connector (ADR 0073):
// REST reads, GraphQL queries for the check rollup and repository identity,
// and the forge's branch primitives, sent through the host's http_request Host
// call, which attaches the credential. The native github adapter stays
// authoritative; nothing routes a production write through the Connector yet.
// The Connector returns raw fields only, and core makes every policy decision
// (trust, transitions, dependency parsing, which conclusions fail).
package main

import (
	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

func init() {
	// Comments stays nil: REST reports no minimized state, so core keeps
	// reading comments from the native (GraphQL) adapter.
	tracker.Register(tracker.Reader{
		ListIssues: listIssues,
		Issue:      issue,
		NativeDeps: nativeDeps,
	})
	forge.Register(forge.Reader{
		PRsForBranch: prsForBranch,
		PR:           pr,
		Checks:       checks,
		JobLog:       jobLog,
	})
	forge.RegisterBranches(forge.Branches{
		Remote:       remote,
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
