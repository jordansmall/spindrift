package console

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"spindrift.dev/launcher/internal/forge"
)

// Pins issue #3018: apply is the tea layer's one seam onto updateLayout, so it
// must leave t.layout non-nil and equal to resolveLayout of the resulting t.m
// across a sequence of messages, not just the first.
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

// Pins issue #3018: a bare teaModel{m: m} literal is the shape every other test
// in this package constructs, so currentLayout must fall back to resolveLayout
// instead of dereferencing the nil cache.
func TestTeaModelCurrentLayout_ZeroValueCache_ResolvesFresh(t *testing.T) {
	m := NewModel()
	m.Width, m.Height = 80, 24
	tm := teaModel{m: m}

	if got, want := tm.currentLayout(), resolveLayout(m); got != want {
		t.Errorf("currentLayout() on a zero-value-cache teaModel = %+v, want resolveLayout(m) = %+v", got, want)
	}
}

// Pins issue #3018: the cache is an optimization, never a second source of
// truth, so currentLayout must never disagree with a fresh resolveLayout once
// apply has cached one.
func TestTeaModelCurrentLayout_AfterApply_MatchesResolveLayout(t *testing.T) {
	tm := teaModel{m: NewModel()}
	tm = tm.apply(SizeChangedMsg{Width: 100, Height: 30})

	if got, want := tm.currentLayout(), resolveLayout(tm.m); got != want {
		t.Errorf("currentLayout() after apply = %+v, want resolveLayout(t.m) = %+v", got, want)
	}
}

// Pins issue #3018 gotcha 3: refreshPickDecorations, syncStale and
// resolvePendingG return a Model directly without going through updateLayout,
// so withModel must invalidate the cache rather than carry a stale layout
// forward.
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

// Pins issue #3018 gotcha 5: dispatchKey clears Toast before dispatch (ModeList
// only), shrinking the header, so the layout a keymap Action reads must reflect
// the cleared header. The key "z" has no ModeList binding on purpose, which
// isolates the pre-dispatch clear's effect on the cache from any Action.
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

// Pins issue #3018 slice 3 (gotcha 5): clearing Toast shrinks the header by one
// line, which grows ListContentBudget and so sectionPageSize, meaning
// keymap_list.go's pgdown/ctrl+f Action must read t.currentLayout() at dispatch
// time and scroll by the post-clear page size.
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

// Pins issue #3018 slice 3: keymap_sidebar.go's "h"/"left" binding reads
// t.currentLayout().sidebarArrangement rather than re-resolving, and fires
// FocusListMsg only for the docked arrangement. A zoomed sidebar, whether it
// falls back to the floating modal box or to fullscreen, leaves Focus alone.
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
