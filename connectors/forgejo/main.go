// Command forgejo is the Forgejo forge as a Connector (ADR 0073): the branch
// primitives and the PR and commit-status reads over REST, sent through the
// host's http_request Host call, which attaches the credential. The native
// forgejo adapter stays authoritative; nothing routes a production call
// through the Connector yet. The Connector returns raw fields only, and core
// makes every policy decision.
package main

import "spindrift.dev/sdk/forge"

func init() {
	// JobLog is left unset: a Forgejo Actions job's log is a web route, not
	// part of the REST API the Route table covers.
	forge.Register(forge.Reader{
		PRsForBranch: prsForBranch,
		PR:           pr,
		Checks:       checks,
	})
	forge.RegisterBranches(forge.Branches{
		Remote:       remote,
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
