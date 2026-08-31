package forge

import (
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// TestResolveCapabilities_CodeForgeShapes drives ResolveCapabilities against
// the fake CodeForge shapes in fake_shapes.go, asserting the optional-seam
// fields come back nil/non-nil to match each shape's real-adapter analogue.
func TestResolveCapabilities_CodeForgeShapes(t *testing.T) {
	it := NewFake()

	t.Run("push-only", func(t *testing.T) {
		cf := NewFake().AsPushOnly()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.PRForge != nil {
			t.Errorf("PRForge = %v, want nil for push-only forge", c.PRForge)
		}
		if c.BundleRelay != nil {
			t.Errorf("BundleRelay = %v, want nil for push-only forge", c.BundleRelay)
		}
	})

	t.Run("local", func(t *testing.T) {
		cf := NewFake().AsLocal()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.BundleRelay == nil {
			t.Error("BundleRelay = nil, want non-nil for local forge")
		}
		if c.LandingRef == nil {
			t.Error("LandingRef = nil, want non-nil for local forge")
		}
		if c.LandingRepair == nil {
			t.Error("LandingRepair = nil, want non-nil for local forge")
		}
		if c.LandingContainmentQuery == nil {
			t.Error("LandingContainmentQuery = nil, want non-nil for local forge")
		}
		if c.PRForge != nil {
			t.Errorf("PRForge = %v, want nil for local forge", c.PRForge)
		}
	})

	t.Run("github-read-only", func(t *testing.T) {
		cf := NewFake().AsGithubReadOnly()
		// AsNoLandingRecorder wraps the tracker behind the IssueTracker
		// interface, so the wrapper's method set stops at IssueTracker's own
		// methods and does not promote *Fake's embedded BranchProtectionForge
		// -- unlike a bare NewFake(), which does and would let a
		// c.BranchProtectionForge, _ = it.(BranchProtectionForge) bug pass
		// this assertion too.
		it := NewFake().AsNoLandingRecorder()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.PRForge == nil {
			t.Error("PRForge = nil, want non-nil for github-read-only forge")
		}
		if c.BundleRelay == nil {
			t.Error("BundleRelay = nil, want non-nil for github-read-only forge")
		}
		if c.DraftPRCreator == nil {
			t.Error("DraftPRCreator = nil, want non-nil for github-read-only forge")
		}
		if c.BundleCommitSubjects == nil {
			t.Error("BundleCommitSubjects = nil, want non-nil for github-read-only forge")
		}
		if c.BranchProtectionForge == nil {
			t.Error("BranchProtectionForge = nil, want non-nil for github-read-only forge")
		}
		if c.LandingRef != nil {
			t.Errorf("LandingRef = %v, want nil for github-read-only forge", c.LandingRef)
		}
	})
}

// TestResolveCapabilities_IssueTrackerShapes mirrors the CodeForge-shapes
// test above for the IssueTracker-side optional seams.
func TestResolveCapabilities_IssueTrackerShapes(t *testing.T) {
	cf := NewFake().AsPushOnly()

	t.Run("no-landing-recorder", func(t *testing.T) {
		it := NewFake().AsNoLandingRecorder()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.LandingRecorder != nil {
			t.Errorf("LandingRecorder = %v, want nil", c.LandingRecorder)
		}
		if c.LandingPassRecorder != nil {
			t.Errorf("LandingPassRecorder = %v, want nil for no-landing-recorder (github-shaped) tracker", c.LandingPassRecorder)
		}
		if c.IssueCloser != nil {
			t.Errorf("IssueCloser = %v, want nil", c.IssueCloser)
		}
		if c.GithubTracker == nil {
			t.Error("GithubTracker = nil, want non-nil for no-landing-recorder (github-shaped) tracker")
		}
	})

	t.Run("local-shaped", func(t *testing.T) {
		it := NewFake().AsLocalShaped()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.LandingRecorder == nil {
			t.Error("LandingRecorder = nil, want non-nil for local-shaped tracker")
		}
		if c.LandingPassRecorder == nil {
			t.Error("LandingPassRecorder = nil, want non-nil for local-shaped tracker")
		}
		if c.IssueCloser == nil {
			t.Error("IssueCloser = nil, want non-nil for local-shaped tracker")
		}
		if c.MergeCloser != nil {
			t.Errorf("MergeCloser = %v, want nil for local-shaped tracker", c.MergeCloser)
		}
	})

	t.Run("forgejo-shaped", func(t *testing.T) {
		it := NewFake().AsForgejoShaped()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.MergeCloser == nil {
			t.Error("MergeCloser = nil, want non-nil for forgejo-shaped tracker")
		}
		if c.LandingRecorder != nil {
			t.Errorf("LandingRecorder = %v, want nil for forgejo-shaped tracker", c.LandingRecorder)
		}
		if c.IssueCloser != nil {
			t.Errorf("IssueCloser = %v, want nil for forgejo-shaped tracker", c.IssueCloser)
		}
	})

	t.Run("issue-filer", func(t *testing.T) {
		it := NewFake().AsIssueFiler()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.HostPostedIssueFiler == nil {
			t.Error("HostPostedIssueFiler = nil, want non-nil for issue-filer tracker")
		}
	})
}

// TestResolveCapabilities_BareTrackerSurfaces asserts the five
// IssueTracker-side seams a bare *IssueTrackerFake implements directly
// (BlocksOf, Comment, FlagAbandoned, PriorClaimState, StateLabels) resolve
// against `it`, not `cf` — cf is AsPushOnly(), a shape with none of these
// five methods, so a resolution line that read cf.(X) instead of it.(X)
// would leave the field nil and fail here.
func TestResolveCapabilities_BareTrackerSurfaces(t *testing.T) {
	it := NewFake()
	cf := NewFake().AsPushOnly()
	c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})

	if c.BlockersLister == nil {
		t.Error("BlockersLister = nil, want non-nil (resolved from it, not cf)")
	}
	if c.HostPostedCommenter == nil {
		t.Error("HostPostedCommenter = nil, want non-nil (resolved from it, not cf)")
	}
	if c.AbandonedFlagger == nil {
		t.Error("AbandonedFlagger = nil, want non-nil (resolved from it, not cf)")
	}
	if c.PriorClaimStateReader == nil {
		t.Error("PriorClaimStateReader = nil, want non-nil (resolved from it, not cf)")
	}
	if c.LabeledTracker == nil {
		t.Error("LabeledTracker = nil, want non-nil (resolved from it, not cf)")
	}
}

// TestResolveCapabilities_SeamListerAndFullyPaginated covers the two
// IssueTracker-side seams no bare Fake shape implements (SeamLister,
// FullyPaginated), each paired against cf = AsPushOnly(), which implements
// neither — so a resolution line that read cf.(X) instead of it.(X) would
// leave the field nil and fail here.
func TestResolveCapabilities_SeamListerAndFullyPaginated(t *testing.T) {
	cf := NewFake().AsPushOnly()

	t.Run("seam-listed", func(t *testing.T) {
		it := NewFake().AsSeamListed()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.SeamLister == nil {
			t.Error("SeamLister = nil, want non-nil (resolved from it, not cf)")
		}
	})

	t.Run("fully-paginated", func(t *testing.T) {
		it := NewFake().AsFullyPaginated()
		c := ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
		if c.FullyPaginated == nil {
			t.Error("FullyPaginated = nil, want non-nil (resolved from it, not cf)")
		}
	})
}

// TestResolveCapabilities_DescriptorPassthrough asserts ForgeDescriptor and
// TrackerDescriptor on the returned Capabilities equal exactly the two
// descriptors passed in — a plain passthrough, independently selected per
// role, mirroring main.go's resolveCapabilitySignals pattern of looking up
// two separate backend.Descriptor rows rather than one merged one.
func TestResolveCapabilities_DescriptorPassthrough(t *testing.T) {
	forgeDesc := backend.Descriptor{Name: "github", ValidAsCodeForge: true}
	trackerDesc := backend.Descriptor{Name: "jira", ValidAsTracker: true}

	c := ResolveCapabilities(NewFake().AsPushOnly(), NewFake(), forgeDesc, trackerDesc)

	if c.ForgeDescriptor != forgeDesc {
		t.Errorf("ForgeDescriptor = %+v, want %+v", c.ForgeDescriptor, forgeDesc)
	}
	if c.TrackerDescriptor != trackerDesc {
		t.Errorf("TrackerDescriptor = %+v, want %+v", c.TrackerDescriptor, trackerDesc)
	}
}
