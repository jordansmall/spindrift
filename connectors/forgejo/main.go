// Command forgejo is the Forgejo forge and tracker as a Connector (ADR 0073):
// the branch primitives, the PR and commit-status reads, the PR write
// primitives, the tracker reads (issues, comments, native dependencies), and
// the tracker's probe, open-issue count, labeled scan and text search, and the
// tracker writes (label edits, comments, filing, closing, the label registry)
// over REST, sent through the host's http_request Host call, which
// attaches the credential. The native forgejo adapter stays authoritative
// unless the row sets `primary`, which routes the Code Forge and the issue
// tracker through the Connector with no fallback. Forgejo has no
// statuses, so a transition always answers unmapped and core swaps labels.
// The Connector returns raw fields only, and core makes every policy
// decision.
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
		Remote:       remote,
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
