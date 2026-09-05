package console

import "testing"

// TestResolveLayout_NoSidebarNoDetail_Plain verifies a Model with neither a
// Sidebar nor a DetailModal open resolves to arrangementPlain — the fallback
// arrangement none of the sidebar/detail special cases claim.
func TestResolveLayout_NoSidebarNoDetail_Plain(t *testing.T) {
	m := Model{Width: 100, Height: 40}
	l := resolveLayout(m)
	if l.arrangement != arrangementPlain {
		t.Errorf("arrangement = %v, want arrangementPlain", l.arrangement)
	}
	if l.sidebarArrangement != arrangementPlain {
		t.Errorf("sidebarArrangement = %v, want the zero value (arrangementPlain) when m.Sidebar is nil", l.sidebarArrangement)
	}
	if l.listWidth != 0 {
		t.Errorf("listWidth = %d, want the zero value when arrangement != arrangementSidebarDocked", l.listWidth)
	}
}

// TestResolveLayout_SidebarDocked verifies a wide-enough, non-zoomed
// Sidebar resolves to arrangementSidebarDocked on both arrangement and
// sidebarArrangement, with sidebarWidth/sidebarHeight matching the same
// computeSidebarWidth/bodyBudget math View and Update already key off of.
func TestResolveLayout_SidebarDocked(t *testing.T) {
	m := Model{Width: 200, Height: 40, Sidebar: &SidebarState{Number: "1"}}
	if !sidebarFits(m) {
		t.Fatalf("test setup: want sidebar to fit docked at width %d", m.Width)
	}

	l := resolveLayout(m)
	if l.arrangement != arrangementSidebarDocked {
		t.Errorf("arrangement = %v, want arrangementSidebarDocked", l.arrangement)
	}
	if l.sidebarArrangement != arrangementSidebarDocked {
		t.Errorf("sidebarArrangement = %v, want arrangementSidebarDocked", l.sidebarArrangement)
	}
	if want := computeSidebarWidth(m.Width); l.sidebarWidth != want {
		t.Errorf("sidebarWidth = %d, want %d (computeSidebarWidth(m.Width))", l.sidebarWidth, want)
	}
	if want := bodyBudget(m) - sidebarDockedFooterLines; l.sidebarHeight != want {
		t.Errorf("sidebarHeight = %d, want %d (bodyBudget(m) - sidebarDockedFooterLines)", l.sidebarHeight, want)
	}
	if want := m.Width - l.sidebarWidth - dockedBorderCols; l.listWidth != want {
		t.Errorf("listWidth = %d, want %d (m.Width - l.sidebarWidth - dockedBorderCols)", l.listWidth, want)
	}
}

// TestResolveLayout_SidebarModal verifies a Sidebar too narrow to dock, but
// wide/tall enough for the floating modal box, resolves to
// arrangementSidebarModal with sidebarHeight matching sidebarModalScrollBudget.
func TestResolveLayout_SidebarModal(t *testing.T) {
	m := Model{Width: 60, Height: 24, Sidebar: &SidebarState{Number: "1"}}
	if sidebarFits(m) {
		t.Fatalf("test setup: want sidebar too narrow to dock at width %d", m.Width)
	}
	if !sidebarModalFits(m) {
		t.Fatalf("test setup: want the floating modal box to fit at %dx%d", m.Width, m.Height)
	}

	l := resolveLayout(m)
	if l.arrangement != arrangementSidebarModal {
		t.Errorf("arrangement = %v, want arrangementSidebarModal", l.arrangement)
	}
	if l.sidebarArrangement != arrangementSidebarModal {
		t.Errorf("sidebarArrangement = %v, want arrangementSidebarModal", l.sidebarArrangement)
	}
	if want := sidebarModalScrollBudget(m); l.sidebarHeight != want {
		t.Errorf("sidebarHeight = %d, want %d (sidebarModalScrollBudget(m))", l.sidebarHeight, want)
	}
	wantWidth, wantHeight := sidebarModalBoxSize(m.Width, m.Height)
	wantX, wantY := sidebarModalBoxOrigin(m.Width, m.Height, wantWidth, wantHeight)
	wantBox := boxGeometry{X: wantX, Y: wantY, Width: wantWidth, Height: wantHeight}
	if l.sidebarModalBox == (boxGeometry{}) {
		t.Fatalf("sidebarModalBox is the zero value, want %+v", wantBox)
	}
	if l.sidebarModalBox != wantBox {
		t.Errorf("sidebarModalBox = %+v, want %+v (sidebarModalBoxSize/sidebarModalBoxOrigin)", l.sidebarModalBox, wantBox)
	}
}

// TestResolveLayout_SidebarFullscreen verifies a Sidebar too small even for
// the floating modal box falls back to arrangementSidebarFullscreen, with
// sidebarHeight matching the whole-terminal headerFooterLines/
// trailingNewlineRow budget renderSidebarFullscreen itself uses.
func TestResolveLayout_SidebarFullscreen(t *testing.T) {
	m := Model{Width: 30, Height: 24, Sidebar: &SidebarState{Number: "1"}}
	if sidebarModalFits(m) {
		t.Fatalf("test setup: want the floating modal box too small at %dx%d", m.Width, m.Height)
	}

	l := resolveLayout(m)
	if l.arrangement != arrangementSidebarFullscreen {
		t.Errorf("arrangement = %v, want arrangementSidebarFullscreen", l.arrangement)
	}
	if l.sidebarArrangement != arrangementSidebarFullscreen {
		t.Errorf("sidebarArrangement = %v, want arrangementSidebarFullscreen", l.sidebarArrangement)
	}
	if want := m.Height - headerFooterLines - trailingNewlineRow; l.sidebarHeight != want {
		t.Errorf("sidebarHeight = %d, want %d (m.Height - headerFooterLines - trailingNewlineRow)", l.sidebarHeight, want)
	}
}

// TestResolveLayout_SidebarZoomed_ForcesOffDocked verifies SidebarZoom
// steers a Sidebar off the docked arrangement even on a terminal wide enough to
// dock it — landing on Modal or Fullscreen per whether the floating box
// itself still fits, mirroring View's own sidebarModal condition.
func TestResolveLayout_SidebarZoomed_ForcesOffDocked(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		want          arrangement
	}{
		{"wide terminal zooms to the floating modal box", 200, 40, arrangementSidebarModal},
		{"tiny terminal zooms straight to fullscreen", 30, 24, arrangementSidebarFullscreen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{Width: c.width, Height: c.height, Sidebar: &SidebarState{Number: "1"}, SidebarZoom: true}

			l := resolveLayout(m)
			if l.arrangement != c.want {
				t.Errorf("arrangement = %v, want %v", l.arrangement, c.want)
			}
			if l.sidebarArrangement != c.want {
				t.Errorf("sidebarArrangement = %v, want %v", l.sidebarArrangement, c.want)
			}
		})
	}
}

// TestResolveLayout_DetailFullscreen_OverridesArrangement_ButNotSidebarArrangement
// pins the slice's key subtlety: a too-small-to-float DetailModal forces
// arrangement to arrangementDetailFullscreen even with a simultaneously
// dockable Sidebar, but sidebarArrangement must still read
// arrangementSidebarDocked — Update's sidebar-viewport clamp needs the
// sidebar's own answer regardless of whether the detail modal is
// currently pre-empting the render.
func TestResolveLayout_DetailFullscreen_OverridesArrangement_ButNotSidebarArrangement(t *testing.T) {
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

	l := resolveLayout(m)
	if l.arrangement != arrangementDetailFullscreen {
		t.Errorf("arrangement = %v, want arrangementDetailFullscreen", l.arrangement)
	}
	if l.sidebarArrangement != arrangementSidebarDocked {
		t.Errorf("sidebarArrangement = %v, want arrangementSidebarDocked even though arrangement is arrangementDetailFullscreen — the sidebar's own arrangement must not be masked by the detail override", l.sidebarArrangement)
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

		l := resolveLayout(m)
		if want := detailModalFits(m); l.detailModalFits != want {
			t.Errorf("%dx%d: detailModalFits = %v, want %v", sz.width, sz.height, l.detailModalFits, want)
		}
		if want := detailModalWrapWidth(m); l.detailWrapWidth != want {
			t.Errorf("%dx%d: detailWrapWidth = %d, want %d", sz.width, sz.height, l.detailWrapWidth, want)
		}
		if want := detailModalScrollBudget(m); l.detailScrollBudget != want {
			t.Errorf("%dx%d: detailScrollBudget = %d, want %d", sz.width, sz.height, l.detailScrollBudget, want)
		}
		if l.detailModalFits {
			wantWidth, wantHeight := detailModalBoxSize(m.Width, m.Height)
			wantX, wantY := detailModalBoxOrigin(m.Width, m.Height, wantWidth, wantHeight)
			wantBox := boxGeometry{X: wantX, Y: wantY, Width: wantWidth, Height: wantHeight}
			if l.detailModalBox == (boxGeometry{}) {
				t.Fatalf("%dx%d: detailModalBox is the zero value, want %+v", sz.width, sz.height, wantBox)
			}
			if l.detailModalBox != wantBox {
				t.Errorf("%dx%d: detailModalBox = %+v, want %+v (detailModalBoxSize/detailModalBoxOrigin)", sz.width, sz.height, l.detailModalBox, wantBox)
			}
		} else if l.detailModalBox != (boxGeometry{}) {
			t.Errorf("%dx%d: detailModalBox = %+v, want the zero value when the modal doesn't fit", sz.width, sz.height, l.detailModalBox)
		}
	}
}

// TestResolveLayout_ListContentBudget pins listContentBudget's own formula
// against l.bodyBudget directly — l.bodyBudget minus listFooterLines
// (clamped at 0) in ModeList, l.bodyBudget unchanged otherwise — rather
// than against the listContentBudget(m) helper, so the assertion still
// holds once that mirror-helper is inlined away.
func TestResolveLayout_ListContentBudget(t *testing.T) {
	cases := []Model{
		{Width: 100, Height: 40, Mode: ModeList},
		{Width: 100, Height: 40, Mode: ModeHelp},
	}
	for _, m := range cases {
		l := resolveLayout(m)
		want := l.bodyBudget
		if m.Mode == ModeList {
			want -= listFooterLines
			if want < 0 {
				want = 0
			}
		}
		if l.listContentBudget != want {
			t.Errorf("Mode %v: listContentBudget = %d, want %d", m.Mode, l.listContentBudget, want)
		}
	}
}

// TestResolveLayout_CompactAndBodyBudget_MirrorHelpers pins the same
// no-re-derivation invariant for the two fields shared by every arrangement,
// with and without a docked sidebar in play.
func TestResolveLayout_CompactAndBodyBudget_MirrorHelpers(t *testing.T) {
	cases := []Model{
		{Width: 100, Height: 40},
		{Width: 200, Height: 40, Sidebar: &SidebarState{Number: "1"}},
		{Width: 60, Height: 24, Sidebar: &SidebarState{Number: "1"}},
	}
	for _, m := range cases {
		l := resolveLayout(m)
		if want := queueNarrowed(m); l.compact != want {
			t.Errorf("%+v: compact = %v, want %v", m, l.compact, want)
		}
		if want := bodyBudget(m); l.bodyBudget != want {
			t.Errorf("%+v: bodyBudget = %d, want %d", m, l.bodyBudget, want)
		}
	}
}
