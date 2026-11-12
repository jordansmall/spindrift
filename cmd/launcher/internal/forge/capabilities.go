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
	CIRunReporter           CIRunReporter
	DraftPRCreator          DraftPRCreator
	BranchPusher            BranchPusher
	BranchDeleter           BranchDeleter
	MirrorSeeder            MirrorSeeder
	BranchProtectionForge   BranchProtectionForge
	BundleCommitSubjects    BundleCommitSubjects
	MergeCommitReader       MergeCommitReader

	// One field per optional IssueTracker-side interface. The same directory
	// scan covers these fields.
	BlockersLister        BlockersLister
	HostPostedCommenter   HostPostedCommenter
	HostPostedIssueFiler  HostPostedIssueFiler
	LandingRecorder       LandingRecorder
	LandingPassRecorder   LandingPassRecorder
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
	IssueSearcher         IssueSearcher
	IssueLabeler          IssueLabeler
	DemandCounter         DemandCounter
	IssueTypeChecker      IssueTypeChecker
	StatusMappingChecker  StatusMappingChecker
	// DepsOf and TouchesOf look these two up on the bare IssueTracker instead:
	// their callers (waves, console) hold no Capabilities.
	NativeDepsReader NativeDepsReader
	BodyGrammarer    BodyGrammarer

	// CODE_FORGE and ISSUE_TRACKER select independently, so each knob keeps
	// its own descriptor row.
	ForgeDescriptor   backend.Descriptor
	TrackerDescriptor backend.Descriptor
}

// ResolveCodeForge resolves only the CodeForge-side fields, for callers that
// hold just a CodeForge and no run-wide Capabilities. Tracker fields and
// descriptors stay zero.
func ResolveCodeForge(cf CodeForge) Capabilities {
	var c Capabilities

	c.BundleRelay, _ = AsCodeForge[BundleRelay](cf)
	c.LandingRef, _ = AsCodeForge[LandingRef](cf)
	c.LandingRepair, _ = AsCodeForge[LandingRepair](cf)
	c.LandingContainmentQuery, _ = AsCodeForge[LandingContainmentQuery](cf)
	c.PRForge, _ = AsCodeForge[PRForge](cf)
	c.CIRunReporter, _ = AsCodeForge[CIRunReporter](cf)
	c.DraftPRCreator, _ = AsCodeForge[DraftPRCreator](cf)
	c.BranchPusher, _ = AsCodeForge[BranchPusher](cf)
	c.BranchDeleter, _ = AsCodeForge[BranchDeleter](cf)
	c.MirrorSeeder, _ = AsCodeForge[MirrorSeeder](cf)
	c.BranchProtectionForge, _ = AsCodeForge[BranchProtectionForge](cf)
	c.BundleCommitSubjects, _ = AsCodeForge[BundleCommitSubjects](cf)
	c.MergeCommitReader, _ = AsCodeForge[MergeCommitReader](cf)

	return c
}

// ResolveCapabilities resolves cf (ResolveCodeForge) and it (As) against every
// optional interface once and folds in the two descriptor rows.
func ResolveCapabilities(cf CodeForge, it IssueTracker, forgeDesc, trackerDesc backend.Descriptor) Capabilities {
	c := ResolveCodeForge(cf)

	c.BlockersLister, _ = As[BlockersLister](it)
	c.HostPostedCommenter, _ = As[HostPostedCommenter](it)
	c.HostPostedIssueFiler, _ = As[HostPostedIssueFiler](it)
	c.LandingRecorder, _ = As[LandingRecorder](it)
	c.LandingPassRecorder, _ = As[LandingPassRecorder](it)
	c.IssueCloser, _ = As[IssueCloser](it)
	c.MergeCloser, _ = As[MergeCloser](it)
	c.AbandonedFlagger, _ = As[AbandonedFlagger](it)
	c.SeamLister, _ = As[SeamLister](it)
	c.PriorClaimStateReader, _ = As[PriorClaimStateReader](it)
	c.LabeledTracker, _ = As[LabeledTracker](it)
	c.FullyPaginated, _ = As[FullyPaginated](it)
	c.CommentLister, _ = As[CommentLister](it)
	c.LinkedIssueLister, _ = As[LinkedIssueLister](it)
	c.LabeledBacklogLister, _ = As[LabeledBacklogLister](it)
	c.IssueSearcher, _ = As[IssueSearcher](it)
	c.IssueLabeler, _ = As[IssueLabeler](it)
	c.DemandCounter, _ = As[DemandCounter](it)
	c.IssueTypeChecker, _ = As[IssueTypeChecker](it)
	c.StatusMappingChecker, _ = As[StatusMappingChecker](it)
	c.NativeDepsReader, _ = As[NativeDepsReader](it)
	c.BodyGrammarer, _ = As[BodyGrammarer](it)

	c.ForgeDescriptor = forgeDesc
	c.TrackerDescriptor = trackerDesc

	return c
}

// HostCanOpenPR reports whether the host can push a branch it built itself
// and open a draft PR on it, whatever the Box access mode (issue #4071).
func (c Capabilities) HostCanOpenPR() bool { return c.BranchPusher != nil && c.DraftPRCreator != nil }
