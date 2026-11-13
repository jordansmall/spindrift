// Command github is the GitHub tracker and forge as a Connector (ADR 0073):
// REST reads, GraphQL queries for the check rollup and repository identity,
// and the forge's branch and PR primitives (open, draft flips, comment, merge,
// auto-merge, branch protection, probe; the flips and auto-merge enqueue are
// GraphQL mutations), sent through the host's http_request Host call, which
// attaches the credential. It also performs the tracker write primitives:
// label edits, comments, issue filing and the label registry. GitHub has no
// statuses, so a transition always answers unmapped and core swaps labels.
// The native github adapter stays authoritative; nothing routes a production
// write through the Connector yet. The Connector returns raw fields only, and
// core makes every policy decision (trust, transitions, dependency parsing,
// which conclusions fail).
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
		// Probe and Count serve the tracker's doctor and daemon-demand reads.
		// RemovedLabels serves recover's prior-claim read; core maps the names.
		Probe:         trackerProbe,
		Count:         count,
		CountInterval: countInterval,
		RemovedLabels: removedLabels,
	})
	tracker.RegisterWrites(tracker.Writer{
		Transition:  trackerTransition,
		EditLabels:  editLabels,
		Comment:     issueComment,
		FileIssue:   fileIssue,
		ListLabels:  listLabels,
		CreateLabel: createLabel,
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
	// CIRun stays nil: nothing needs the CI run URL from the Connector.
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
}

// A reactor's exports are all it does; main is never called.
func main() {}
