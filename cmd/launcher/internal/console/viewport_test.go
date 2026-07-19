package console

import "testing"

// TestViewport_Window_Unbounded_ZeroValue verifies a freshly zero-valued
// Viewport (height 0, its "unbounded" value, issue #1540) windows every row
// from offset 0 with nothing hidden above or below — the nil *int convention
// it replaces.
func TestViewport_Window_Unbounded_ZeroValue(t *testing.T) {
	var v Viewport
	w := v.Window(5)
	want := Window{Start: 0, End: 5, Above: 0, Below: 0}
	if w != want {
		t.Errorf("Window(5) = %+v, want %+v", w, want)
	}
}

// TestViewport_Window_BoundedHeight_TruncatesAndCountsBelow verifies a
// bounded height slices [offset, offset+height) and reports the remaining
// rows as Below when total overruns it.
func TestViewport_Window_BoundedHeight_TruncatesAndCountsBelow(t *testing.T) {
	var v Viewport
	v.SetHeight(3)
	w := v.Window(10)
	want := Window{Start: 0, End: 3, Above: 0, Below: 7}
	if w != want {
		t.Errorf("Window(10) = %+v, want %+v", w, want)
	}
}

// TestViewport_Scroll_ClampsIntoBounds verifies Scroll adds delta to offset,
// clamped into [0, total-1] — pgup/pgdown's raw movement, independent of
// height or any cursor.
func TestViewport_Scroll_ClampsIntoBounds(t *testing.T) {
	var v Viewport
	v.Scroll(2, 5)
	if got := v.Window(5).Start; got != 2 {
		t.Errorf("offset after Scroll(2, 5) = %d, want 2", got)
	}
	v.Scroll(-100, 5)
	if got := v.Window(5).Start; got != 0 {
		t.Errorf("offset after Scroll(-100, 5) = %d, want 0 (clamped at top)", got)
	}
	v.Scroll(100, 5)
	if got := v.Window(5).Start; got != 4 {
		t.Errorf("offset after Scroll(100, 5) = %d, want 4 (clamped to last row)", got)
	}
}

// TestViewport_MoveCursor_NonPositiveHeight_ClampsOffsetBelowTotal verifies
// MoveCursor's cursor-follow never leaves offset == total: with height 0
// (unbounded — no window ever excludes the cursor), offset simply never
// needs to advance past total-1 (issue #1054, inherited by the Viewport
// extraction).
func TestViewport_MoveCursor_NonPositiveHeight_ClampsOffsetBelowTotal(t *testing.T) {
	var v Viewport
	v.MoveCursor(4, 5)
	if got := v.Window(5).Start; got != 4 {
		t.Errorf("offset after MoveCursor(4, 5) with height 0 = %d, want 4 (total-1)", got)
	}
}

// TestViewport_MoveCursor_PositiveHeight_AdvancesOffsetToKeepCursorVisible
// verifies MoveCursor advances offset just far enough that a bounded window
// still shows the cursor's row (issue #1036).
func TestViewport_MoveCursor_PositiveHeight_AdvancesOffsetToKeepCursorVisible(t *testing.T) {
	var v Viewport
	v.SetHeight(2)
	v.MoveCursor(4, 5)
	if got := v.Window(5).Start; got != 3 {
		t.Errorf("offset after MoveCursor(4, 5) with height 2 = %d, want 3", got)
	}
}

// TestViewport_SetHeight_ClampOnShrink_PullsOffsetBackToLastFullPage
// verifies binding a previously-unbounded (or looser) height immediately
// pulls a now-too-far offset back so the viewport's last page still fills it
// instead of rendering mostly blank (issue #829) — clamp-on-shrink lives
// inside SetHeight itself (issue #1540), using the total from the most
// recent Window/Scroll/MoveCursor call.
func TestViewport_SetHeight_ClampOnShrink_PullsOffsetBackToLastFullPage(t *testing.T) {
	var v Viewport
	v.Scroll(99, 100) // unbounded (height 0): offset can sit anywhere, lands at 99
	v.SetHeight(10)   // now bounded: offset 99 would leave only 1 of 10 rows filled
	if got := v.Window(100).Start; got != 90 {
		t.Errorf("offset after binding height to 10 = %d, want 90 (last page fills the new viewport)", got)
	}
}
