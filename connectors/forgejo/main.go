// Command forgejo is the Forgejo Code Forge and issue tracker as a Connector
// (ADR 0073, #7122): branch primitives, PR and commit-status reads, PR writes,
// and the tracker's reads, probe, count, scan, search and writes, over REST
// through the host's http_request Host call, which attaches the credential.
// Forgejo has no statuses, so a transition always answers unmapped and core
// swaps labels. The Connector returns raw fields only; core makes every
// policy decision.
package main

import (
	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

func init() {
	forge.Register(forge.Reader{
		PRsForBranch: prsForBranch,
		PR:           pr,
		Checks:       checks,
		JobLog:       jobLog,
	})
	forge.RegisterBranches(forge.Branches{
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
	})
	// CIRun and TokenGrant are left unset: the Connector declares only what it
	// implements.
	forge.RegisterPRs(forge.PRs{
		Open:             open,
		Behind:           behind,
		Files:            prFiles,
		SetDraft:         setDraft,
		Comment:          comment,
		Merge:            merge,
		AutoMergeAllowed: autoMergeAllowed,
		EnqueueAutoMerge: enqueueAutoMerge,
		BranchProtected:  branchProtected,
		Probe:            probe,
	})
	tracker.Register(tracker.Reader{
		ListIssues:    listIssues,
		Issue:         issue,
		Comments:      comments,
		NativeDeps:    nativeDeps,
		Blocks:        blocks,
		Probe:         trackerProbe,
		Count:         count,
		CountInterval: countInterval,
		LabeledPage:   labeledPage,
		Search:        search,
		// Routes: a label route joins the labels filter and fileIssue stamps it.
		Routes:       true,
		MatchesRoute: matchesRoute,
	})
	tracker.RegisterWrites(tracker.Writer{
		Transition:  transition,
		EditLabels:  editLabels,
		Comment:     issueComment,
		FileIssue:   fileIssue,
		CloseIssue:  closeIssue,
		ListLabels:  listLabels,
		CreateLabel: createLabel,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
