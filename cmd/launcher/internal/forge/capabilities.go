package forge

import "spindrift.dev/launcher/internal/backend"

// Capabilities is "what this backend pairing can do", resolved once: one
// typed handle per optional forge/tracker seam interface (nil = adapter
// doesn't implement it) plus the two backend.Descriptor rows' config-time
// facts (ADR 0013's second amendment: declaration stays optional
// interfaces, resolution is this constructed value, consumers read instead
// of asserting).
type Capabilities struct {
	// one field per optional CodeForge-side interface declared anywhere in
	// this package (capabilities_completeness_test.go scans the directory,
	// not a fixed file list)
	BundleRelay             BundleRelay
	LandingRef              LandingRef
	LandingRepair           LandingRepair
	LandingContainmentQuery LandingContainmentQuery
	PRForge                 PRForge
	DraftPRCreator          DraftPRCreator
	BranchProtectionForge   BranchProtectionForge
	BundleCommitSubjects    BundleCommitSubjects

	// one field per optional IssueTracker-side interface declared anywhere
	// in this package (same directory scan as the CodeForge-side fields
	// above)
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
	SnapshotReader        SnapshotReader

	// ForgeDescriptor/TrackerDescriptor are the config-time half of the
	// same value -- CODE_FORGE's and ISSUE_TRACKER's own backend.Descriptor
	// rows, kept separate since the two knobs select independently.
	ForgeDescriptor   backend.Descriptor
	TrackerDescriptor backend.Descriptor
}

// ResolveCapabilities resolves cf's and it's optional-interface surfaces
// once via type assertion and folds in forgeDesc/trackerDesc's config-time
// facts, so a caller reads a typed handle instead of asserting itself.
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
	c.SnapshotReader, _ = it.(SnapshotReader)

	c.ForgeDescriptor = forgeDesc
	c.TrackerDescriptor = trackerDesc

	return c
}
