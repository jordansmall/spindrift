package console

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/forge"
)

// TestTeaModelApply_CachesLayoutMatchingResolveLayout verifies apply — the
// tea layer's one seam onto updateLayout (issue #3018) — leaves t.layout
// non-nil and equal to resolveLayout of the resulting t.m, across a
// sequence of messages rather than just the first.
func TestTeaModelApply_CachesLayoutMatchingResolveLayout(t *testing.T) {
	tm := teaModel{m: NewModel()}

	for _, msg := range []Msg{
		SizeChangedMsg{Width: 80, Height: 24},
		CursorMoveMsg{Delta: 1},
	} {
		tm = tm.apply(msg)

		if tm.layout == nil {
			t.Fatalf("apply(%T): layout cache = nil, want a resolved layout", msg)
		}
		if want := resolveLayout(tm.m); *tm.layout != want {
			t.Errorf("apply(%T): cached layout = %+v, want resolveLayout(t.m) = %+v", msg, *tm.layout, want)
		}
	}
}

// TestTeaModelCurrentLayout_ZeroValueCache_ResolvesFresh verifies
// currentLayout on a bare teaModel{m: m} literal — the shape every existing
// test in this package already constructs, and apply's zero value must
// stay safe for (issue #3018) — falls back to resolveLayout instead of
// dereferencing a nil cache.
func TestTeaModelCurrentLayout_ZeroValueCache_ResolvesFresh(t *testing.T) {
	m := NewModel()
	m.Width, m.Height = 80, 24
	tm := teaModel{m: m}

	if got, want := tm.currentLayout(), resolveLayout(m); got != want {
		t.Errorf("currentLayout() on a zero-value-cache teaModel = %+v, want resolveLayout(m) = %+v", got, want)
	}
}

// TestTeaModelCurrentLayout_AfterApply_MatchesResolveLayout verifies
// currentLayout never disagrees with a fresh ResolveLayout call once apply
// has cached one — the cache is an optimization, never a second source of
// truth (issue #3018).
func TestTeaModelCurrentLayout_AfterApply_MatchesResolveLayout(t *testing.T) {
	tm := teaModel{m: NewModel()}
	tm = tm.apply(SizeChangedMsg{Width: 100, Height: 30})

	if got, want := tm.currentLayout(), resolveLayout(tm.m); got != want {
		t.Errorf("currentLayout() after apply = %+v, want resolveLayout(t.m) = %+v", got, want)
	}
}

// TestTeaModelWithModel_InvalidatesCache verifies the direct-mutation seam
// (refreshPickDecorations/syncStale/resolvePendingG's own Model return, none
// of which flow through updateLayout) invalidates rather than carries a
// stale cached layout forward — currentLayout must still resolve fresh
// against the newly installed Model (issue #3018 gotcha 3).
func TestTeaModelWithModel_InvalidatesCache(t *testing.T) {
	tm := teaModel{m: NewModel()}
	tm = tm.apply(SizeChangedMsg{Width: 80, Height: 24})

	resized := tm.m
	resized.Width, resized.Height = 40, 10
	tm = tm.withModel(resized)

	if tm.layout != nil {
		t.Fatalf("withModel: layout cache = %+v, want nil (invalidated)", *tm.layout)
	}
	if got, want := tm.currentLayout(), resolveLayout(tm.m); got != want {
		t.Errorf("currentLayout() after withModel = %+v, want resolveLayout(t.m) = %+v", got, want)
	}
}

// TestDispatchKey_PreDispatchToastClear_LayoutReflectsClearedHeader verifies
// gotcha 5: dispatchKey's pre-dispatch Toast clear (ModeList only) shrinks
// the header before whatever layout a keymap Action would go on to read, so
// that layout must reflect the cleared header, not the pre-clear one. A
// deliberately unbound key ("z" has no ModeList binding) isolates the
// pre-dispatch clear's own effect on the cache from a binding's own Action.
func TestDispatchKey_PreDispatchToastClear_LayoutReflectsClearedHeader(t *testing.T) {
	base := NewModel()
	base.Width, base.Height = 80, 24
	base.Toast = "#1818 started: fix the thing"

	tm := teaModel{m: base}
	tm, _ = tm.dispatchKey(ModeList, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})

	if tm.m.Toast != "" {
		t.Fatalf("Toast = %q after dispatchKey, want cleared by the pre-dispatch step", tm.m.Toast)
	}
	stale := resolveLayout(base)
	want := resolveLayout(tm.m)
	got := tm.currentLayout()
	if got != want {
		t.Errorf("currentLayout() = %+v, want resolveLayout(post-clear model) = %+v", got, want)
	}
	if got == stale {
		t.Errorf("currentLayout() = %+v still matches the pre-clear, Toast-inflated-header Layout %+v", got, stale)
	}
}

// TestDispatchKey_ListScroll_UsesPostToastClearLayoutBudget verifies
// keymap_list.go's pgdown/ctrl+f Action (issue #3018 slice 3) reads
// t.currentLayout() at dispatch time, not a value cached before dispatchKey's
// own pre-dispatch Toast clear ran — clearing Toast shrinks the header by
// one line, growing ListContentBudget and therefore sectionPageSize's own
// page size, so the Action must scroll by the post-clear page size, not one
// computed against the still-Toast-inflated header (gotcha 5).
func TestDispatchKey_ListScroll_UsesPostToastClearLayoutBudget(t *testing.T) {
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	issues := make([]forge.Issue, 100)
	for i := range issues {
		issues[i] = forge.Issue{Number: fmt.Sprintf("%d", i), Title: fmt.Sprintf("issue %d", i)}
	}
	m = Update(m, IssuesLoadedMsg{Issues: issues})
	m.Toast = "#1 started: fix the thing"

	withToast := sectionPageSize(m, resolveLayout(m))
	postClear := Update(m, ToastDismissedMsg{})
	withoutToast := sectionPageSize(postClear, resolveLayout(postClear))
	if withoutToast <= withToast {
		t.Fatalf("setup: sectionPageSize without Toast = %d, want larger than with Toast pending (%d) — clearing it must actually shrink the header", withoutToast, withToast)
	}

	tm := teaModel{m: m}
	tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})

	if tm.m.Offset != withoutToast {
		t.Errorf("Offset after pgdown = %d, want %d (the post-Toast-clear page size) — the Action closure must read the layout as dispatchKey's own pre-dispatch clear left it, not one cached beforehand", tm.m.Offset, withoutToast)
	}
}

// TestDispatchKey_SidebarHKey_OnlyFocusesListWhenDocked verifies
// keymap_sidebar.go's "h"/"left" binding (issue #3018 slice 3) reads
// t.currentLayout().sidebarArrangement rather than re-resolving, and still only
// fires FocusListMsg for the docked arrangement — a zoomed sidebar, whether it
// falls back to the floating modal box or fullscreen, must leave Focus
// alone.
func TestDispatchKey_SidebarHKey_OnlyFocusesListWhenDocked(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		zoom          bool
	}{
		{"docked", sidebarMinListWidth + sidebarWidth + dockedBorderCols, 24, false},
		{"zoomed modal", 200, 40, true},
		{"zoomed fullscreen", 30, 24, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{Width: c.width, Height: c.height, Sidebar: &SidebarState{Number: "1"}, Focus: FocusSidebar, SidebarZoom: c.zoom}
			tm := teaModel{m: m}

			wantDocked := tm.currentLayout().sidebarArrangement == arrangementSidebarDocked

			tm, _ = tm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})

			gotFocusList := tm.m.Focus == FocusList
			if gotFocusList != wantDocked {
				t.Errorf("Focus == FocusList = %v after \"h\", want %v (sidebarArrangement docked = %v)", gotFocusList, wantDocked, wantDocked)
			}
		})
	}
}
