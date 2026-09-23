package forge

import "spindrift.dev/launcher/internal/backend"

// Capabilities holds one typed handle per optional forge/tracker seam
// interface, resolved once. A nil field means the adapter does not implement
// that interface. Consumers read these fields instead of asserting for
// themselves (ADR 0013's second amendment).
type Capabilities struct {
	// One field per optional CodeForge-side interface declared anywhere in
	// this package. capabilities_completeness_test.go scans the whole
	// directory, not a fixed file list.
	BundleRelay             BundleRelay
	LandingRef              LandingRef
	LandingRepair           LandingRepair
	LandingContainmentQuery LandingContainmentQuery
	PRForge                 PRForge
	DraftPRCreator          DraftPRCreator
	BranchProtectionForge   BranchProtectionForge
	BundleCommitSubjects    BundleCommitSubjects

	// One field per optional IssueTracker-side interface. The same directory
	// scan covers these fields.
	BlockersLister        BlockersLister
	HostPostedCommenter   HostPostedCommenter
	HostPostedIssueFiler  HostPostedIssueFiler
	LandingRecorder       LandingRecorder
	LandingPassRecorder   LandingPassRecorder
	GithubTracker         GithubTracker
	IssueCloser           IssueCloser
	MergeCloser           MergeCloser
	AbandonedFlagger      AbandonedFlagger
	SeamLister            SeamLister
	PriorClaimStateReader PriorClaimStateReader
	LabeledTracker        LabeledTracker
	FullyPaginated        FullyPaginated
	CommentLister         CommentLister
	LinkedIssueLister     LinkedIssueLister
	LabeledBacklogLister  LabeledBacklogLister

	// CODE_FORGE and ISSUE_TRACKER select independently, so each knob keeps
	// its own descriptor row.
	ForgeDescriptor   backend.Descriptor
	TrackerDescriptor backend.Descriptor
}

// ResolveCapabilities type-asserts cf and it against every optional interface
// once and folds in the two descriptor rows.
func ResolveCapabilities(cf CodeForge, it IssueTracker, forgeDesc, trackerDesc backend.Descriptor) Capabilities {
	var c Capabilities

	c.BundleRelay, _ = cf.(BundleRelay)
	c.LandingRef, _ = cf.(LandingRef)
	c.LandingRepair, _ = cf.(LandingRepair)
	c.LandingContainmentQuery, _ = cf.(LandingContainmentQuery)
	c.PRForge, _ = cf.(PRForge)
	c.DraftPRCreator, _ = cf.(DraftPRCreator)
	c.BranchProtectionForge, _ = cf.(BranchProtectionForge)
	c.BundleCommitSubjects, _ = cf.(BundleCommitSubjects)

	c.BlockersLister, _ = it.(BlockersLister)
	c.HostPostedCommenter, _ = it.(HostPostedCommenter)
	c.HostPostedIssueFiler, _ = it.(HostPostedIssueFiler)
	c.LandingRecorder, _ = it.(LandingRecorder)
	c.LandingPassRecorder, _ = it.(LandingPassRecorder)
	c.GithubTracker, _ = it.(GithubTracker)
	c.IssueCloser, _ = it.(IssueCloser)
	c.MergeCloser, _ = it.(MergeCloser)
	c.AbandonedFlagger, _ = it.(AbandonedFlagger)
	c.SeamLister, _ = it.(SeamLister)
	c.PriorClaimStateReader, _ = it.(PriorClaimStateReader)
	c.LabeledTracker, _ = it.(LabeledTracker)
	c.FullyPaginated, _ = it.(FullyPaginated)
	c.CommentLister, _ = it.(CommentLister)
	c.LinkedIssueLister, _ = it.(LinkedIssueLister)
	c.LabeledBacklogLister, _ = it.(LabeledBacklogLister)

	c.ForgeDescriptor = forgeDesc
	c.TrackerDescriptor = trackerDesc

	return c
}
