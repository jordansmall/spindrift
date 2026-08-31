package console

import "testing"

// TestResolveLayout_NoSidebarNoDetail_Plain verifies a Model with neither a
// Sidebar nor a DetailModal open resolves to BranchPlain — the fallback
// branch none of the sidebar/detail special cases claim.
func TestResolveLayout_NoSidebarNoDetail_Plain(t *testing.T) {
	m := Model{Width: 100, Height: 40}
	l := ResolveLayout(m)
	if l.Branch != BranchPlain {
		t.Errorf("Branch = %v, want BranchPlain", l.Branch)
	}
	if l.SidebarBranch != BranchPlain {
		t.Errorf("SidebarBranch = %v, want the zero value (BranchPlain) when m.Sidebar is nil", l.SidebarBranch)
	}
	if l.ListWidth != 0 {
		t.Errorf("ListWidth = %d, want the zero value when Branch != BranchSidebarDocked", l.ListWidth)
	}
}

// TestResolveLayout_SidebarDocked verifies a wide-enough, non-zoomed
// Sidebar resolves to BranchSidebarDocked on both Branch and SidebarBranch,
// with SidebarWidth/SidebarHeight matching the same computeSidebarWidth/
// bodyBudget math View and Update already key off of.
func TestResolveLayout_SidebarDocked(t *testing.T) {
	m := Model{Width: 200, Height: 40, Sidebar: &SidebarState{Number: "1"}}
	if !sidebarFits(m) {
		t.Fatalf("test setup: want sidebar to fit docked at width %d", m.Width)
	}

	l := ResolveLayout(m)
	if l.Branch != BranchSidebarDocked {
		t.Errorf("Branch = %v, want BranchSidebarDocked", l.Branch)
	}
	if l.SidebarBranch != BranchSidebarDocked {
		t.Errorf("SidebarBranch = %v, want BranchSidebarDocked", l.SidebarBranch)
	}
	if want := computeSidebarWidth(m.Width); l.SidebarWidth != want {
		t.Errorf("SidebarWidth = %d, want %d (computeSidebarWidth(m.Width))", l.SidebarWidth, want)
	}
	if want := bodyBudget(m) - sidebarDockedFooterLines; l.SidebarHeight != want {
		t.Errorf("SidebarHeight = %d, want %d (bodyBudget(m) - sidebarDockedFooterLines)", l.SidebarHeight, want)
	}
	if want := m.Width - l.SidebarWidth - dockedBorderCols; l.ListWidth != want {
		t.Errorf("ListWidth = %d, want %d (m.Width - l.SidebarWidth - dockedBorderCols)", l.ListWidth, want)
	}
}

// TestResolveLayout_SidebarModal verifies a Sidebar too narrow to dock, but
// wide/tall enough for the floating modal box, resolves to
// BranchSidebarModal with SidebarHeight matching sidebarModalScrollBudget.
func TestResolveLayout_SidebarModal(t *testing.T) {
	m := Model{Width: 60, Height: 24, Sidebar: &SidebarState{Number: "1"}}
	if sidebarFits(m) {
		t.Fatalf("test setup: want sidebar too narrow to dock at width %d", m.Width)
	}
	if !sidebarModalFits(m) {
		t.Fatalf("test setup: want the floating modal box to fit at %dx%d", m.Width, m.Height)
	}

	l := ResolveLayout(m)
	if l.Branch != BranchSidebarModal {
		t.Errorf("Branch = %v, want BranchSidebarModal", l.Branch)
	}
	if l.SidebarBranch != BranchSidebarModal {
		t.Errorf("SidebarBranch = %v, want BranchSidebarModal", l.SidebarBranch)
	}
	if want := sidebarModalScrollBudget(m); l.SidebarHeight != want {
		t.Errorf("SidebarHeight = %d, want %d (sidebarModalScrollBudget(m))", l.SidebarHeight, want)
	}
	wantWidth, wantHeight := sidebarModalBoxSize(m.Width, m.Height)
	wantX, wantY := sidebarModalBoxOrigin(m.Width, m.Height, wantWidth, wantHeight)
	wantBox := BoxGeometry{X: wantX, Y: wantY, Width: wantWidth, Height: wantHeight}
	if l.SidebarModalBox == (BoxGeometry{}) {
		t.Fatalf("SidebarModalBox is the zero value, want %+v", wantBox)
	}
	if l.SidebarModalBox != wantBox {
		t.Errorf("SidebarModalBox = %+v, want %+v (sidebarModalBoxSize/sidebarModalBoxOrigin)", l.SidebarModalBox, wantBox)
	}
}

// TestResolveLayout_SidebarFullscreen verifies a Sidebar too small even for
// the floating modal box falls back to BranchSidebarFullscreen, with
// SidebarHeight matching the whole-terminal headerFooterLines/
// trailingNewlineRow budget renderSidebarFullscreen itself uses.
func TestResolveLayout_SidebarFullscreen(t *testing.T) {
	m := Model{Width: 30, Height: 24, Sidebar: &SidebarState{Number: "1"}}
	if sidebarModalFits(m) {
		t.Fatalf("test setup: want the floating modal box too small at %dx%d", m.Width, m.Height)
	}

	l := ResolveLayout(m)
	if l.Branch != BranchSidebarFullscreen {
		t.Errorf("Branch = %v, want BranchSidebarFullscreen", l.Branch)
	}
	if l.SidebarBranch != BranchSidebarFullscreen {
		t.Errorf("SidebarBranch = %v, want BranchSidebarFullscreen", l.SidebarBranch)
	}
	if want := m.Height - headerFooterLines - trailingNewlineRow; l.SidebarHeight != want {
		t.Errorf("SidebarHeight = %d, want %d (m.Height - headerFooterLines - trailingNewlineRow)", l.SidebarHeight, want)
	}
}

// TestResolveLayout_SidebarZoomed_ForcesOffDocked verifies SidebarZoom
// steers a Sidebar off the docked branch even on a terminal wide enough to
// dock it — landing on Modal or Fullscreen per whether the floating box
// itself still fits, mirroring View's own sidebarModal condition.
func TestResolveLayout_SidebarZoomed_ForcesOffDocked(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          Branch
	}{
		{"wide terminal zooms to the floating modal box", 200, 40, BranchSidebarModal},
		{"tiny terminal zooms straight to fullscreen", 30, 24, BranchSidebarFullscreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{Width: c.width, Height: c.height, Sidebar: &SidebarState{Number: "1"}, SidebarZoom: true}

			l := ResolveLayout(m)
			if l.Branch != c.want {
				t.Errorf("Branch = %v, want %v", l.Branch, c.want)
			}
			if l.SidebarBranch != c.want {
				t.Errorf("SidebarBranch = %v, want %v", l.SidebarBranch, c.want)
			}
		})
	}
}

// TestResolveLayout_DetailFullscreen_OverridesBranch_ButNotSidebarBranch
// pins the slice's key subtlety: a too-small-to-float DetailModal forces
// Branch to BranchDetailFullscreen even with a simultaneously dockable
// Sidebar, but SidebarBranch must still read BranchSidebarDocked — Update's
// sidebar-viewport clamp needs the sidebar's own answer regardless of
// whether the detail modal is currently pre-empting the render.
func TestResolveLayout_DetailFullscreen_OverridesBranch_ButNotSidebarBranch(t *testing.T) {
	m := Model{
		Width:       200,
		Height:      8,
		Sidebar:     &SidebarState{Number: "1"},
		DetailModal: &DetailModalState{Number: "42", Title: "fix the thing"},
	}
	if !sidebarFits(m) {
		t.Fatalf("test setup: want the sidebar to fit docked at width %d", m.Width)
	}
	if detailModalFits(m) {
		t.Fatalf("test setup: want the detail modal too small to float at height %d", m.Height)
	}

	l := ResolveLayout(m)
	if l.Branch != BranchDetailFullscreen {
		t.Errorf("Branch = %v, want BranchDetailFullscreen", l.Branch)
	}
	if l.SidebarBranch != BranchSidebarDocked {
		t.Errorf("SidebarBranch = %v, want BranchSidebarDocked even though Branch is BranchDetailFullscreen — the sidebar's own arrangement must not be masked by the detail override", l.SidebarBranch)
	}
}

// TestResolveLayout_DetailModalFields_MirrorHelpers pins the "no
// re-derivation" invariant for the detail-modal-derived fields: each must
// equal calling the existing helper directly against the same Model, across
// both the floating-box and fullscreen-fallback cases.
func TestResolveLayout_DetailModalFields_MirrorHelpers(t *testing.T) {
	sizes := []struct{ width, height int }{
		{100, 40}, // floating box fits
		{200, 60}, // floating box fits, larger
		{20, 8},   // too small to float
	}
	for _, sz := range sizes {
		m := Model{Width: sz.width, Height: sz.height, DetailModal: &DetailModalState{Number: "1", Title: "t"}}

		l := ResolveLayout(m)
		if want := detailModalFits(m); l.DetailModalFits != want {
			t.Errorf("%dx%d: DetailModalFits = %v, want %v", sz.width, sz.height, l.DetailModalFits, want)
		}
		if want := detailModalWrapWidth(m); l.DetailWrapWidth != want {
			t.Errorf("%dx%d: DetailWrapWidth = %d, want %d", sz.width, sz.height, l.DetailWrapWidth, want)
		}
		if want := detailModalScrollBudget(m); l.DetailScrollBudget != want {
			t.Errorf("%dx%d: DetailScrollBudget = %d, want %d", sz.width, sz.height, l.DetailScrollBudget, want)
		}
		if l.DetailModalFits {
			wantWidth, wantHeight := detailModalBoxSize(m.Width, m.Height)
			wantX, wantY := detailModalBoxOrigin(m.Width, m.Height, wantWidth, wantHeight)
			wantBox := BoxGeometry{X: wantX, Y: wantY, Width: wantWidth, Height: wantHeight}
			if l.DetailModalBox == (BoxGeometry{}) {
				t.Fatalf("%dx%d: DetailModalBox is the zero value, want %+v", sz.width, sz.height, wantBox)
			}
			if l.DetailModalBox != wantBox {
				t.Errorf("%dx%d: DetailModalBox = %+v, want %+v (detailModalBoxSize/detailModalBoxOrigin)", sz.width, sz.height, l.DetailModalBox, wantBox)
			}
		} else if l.DetailModalBox != (BoxGeometry{}) {
			t.Errorf("%dx%d: DetailModalBox = %+v, want the zero value when the modal doesn't fit", sz.width, sz.height, l.DetailModalBox)
		}
	}
}

// TestResolveLayout_ListContentBudget pins ListContentBudget's own formula
// against l.Budget directly — l.Budget minus listFooterLines (clamped at 0)
// in ModeList, l.Budget unchanged otherwise — rather than against the
// listContentBudget(m) helper, so the assertion still holds once that
// mirror-helper is inlined away.
func TestResolveLayout_ListContentBudget(t *testing.T) {
	cases := []Model{
		{Width: 100, Height: 40, Mode: ModeList},
		{Width: 100, Height: 40, Mode: ModeHelp},
	}
	for _, m := range cases {
		l := ResolveLayout(m)
		want := l.Budget
		if m.Mode == ModeList {
			want -= listFooterLines
			if want < 0 {
				want = 0
			}
		}
		if l.ListContentBudget != want {
			t.Errorf("Mode %v: ListContentBudget = %d, want %d", m.Mode, l.ListContentBudget, want)
		}
	}
}

// TestResolveLayout_CompactAndBudget_MirrorHelpers pins the same
// no-re-derivation invariant for the two fields shared by every branch,
// with and without a docked sidebar in play.
func TestResolveLayout_CompactAndBudget_MirrorHelpers(t *testing.T) {
	cases := []Model{
		{Width: 100, Height: 40},
		{Width: 200, Height: 40, Sidebar: &SidebarState{Number: "1"}},
		{Width: 60, Height: 24, Sidebar: &SidebarState{Number: "1"}},
	}
	for _, m := range cases {
		l := ResolveLayout(m)
		if want := queueNarrowed(m); l.Compact != want {
			t.Errorf("%+v: Compact = %v, want %v", m, l.Compact, want)
		}
		if want := bodyBudget(m); l.Budget != want {
			t.Errorf("%+v: Budget = %d, want %d", m, l.Budget, want)
		}
	}
}
