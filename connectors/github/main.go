// Command github is the GitHub tracker and forge as a Connector (ADR 0073),
// answering github's reads and writes for every Consumer over REST and
// GraphQL, sent through the host's http_request Host call, which attaches the
// credential. GitHub has no statuses, so a transition always answers unmapped
// and core swaps labels. The Connector returns raw fields only, and core makes
// every policy decision (trust, transitions, dependency parsing, which
// conclusions fail).
package main

import (
	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

func init() {
	tracker.Register(tracker.Reader{
		ListIssues: listIssues,
		Issue:      issue,
		// Comments rides GraphQL, the one API that reports minimized state.
		Comments:   commentsOf,
		NativeDeps: nativeDeps,
		Blocks:     blocks,
		Search:     search,
		// LabeledPage serves the finding dedup scan, which reads closed issues
		// too, one REST page per call.
		LabeledPage: labeledPage,
		// Probe and Count serve the tracker's doctor and daemon-demand reads.
		// RemovedLabels serves recover's prior-claim read; core maps the names.
		Probe:         trackerProbe,
		Count:         count,
		CountInterval: countInterval,
		RemovedLabels: removedLabels,
		// Routes: a label route joins the labels= filter and fileIssue stamps it.
		Routes:       true,
		MatchesRoute: matchesRoute,
	})
	tracker.RegisterWrites(tracker.Writer{
		Transition:  trackerTransition,
		EditLabels:  editLabels,
		Comment:     issueComment,
		FileIssue:   fileIssue,
		CloseIssue:  closeIssue,
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
		BranchExists: branchExists,
		DeleteBranch: deleteBranch,
		Probe:        probe,
	})
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
		CIRun:            ciRun,
		TokenGrant:       tokenGrant,
	})
}

// A reactor's exports are all it does; main is never called.
func main() {}
