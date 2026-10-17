package console

import "testing"

// Height 0 is the zero value and means unbounded, replacing the nil *int
// convention (issue #1540).
func TestViewport_Window_Unbounded_ZeroValue(t *testing.T) {
	var v Viewport
	w := v.Window(5)
	want := Window{Start: 0, End: 5, Above: 0, Below: 0}
	if w != want {
		t.Errorf("Window(5) = %+v, want %+v", w, want)
	}
}

func TestViewport_Window_BoundedHeight_TruncatesAndCountsBelow(t *testing.T) {
	var v Viewport
	v.SetHeight(3)
	w := v.Window(10)
	want := Window{Start: 0, End: 3, Above: 0, Below: 7}
	if w != want {
		t.Errorf("Window(10) = %+v, want %+v", w, want)
	}
}

func TestWindow_Shown_NotTruncated_ShowsEveryRowNoAffordance(t *testing.T) {
	w := Window{Start: 0, End: 5, Above: 0, Below: 0}
	shown, moreBelow := w.Shown()
	if shown != 5 || moreBelow != 0 {
		t.Errorf("Shown() = (%d, %d), want (5, 0)", shown, moreBelow)
	}
}

// Shown holds one row back so the "N more below" line fits in the same
// budget, and the remaining count includes that held-back row (issue #1061).
func TestWindow_Shown_Truncated_HoldsBackOneRowForMoreBelow(t *testing.T) {
	w := Window{Start: 0, End: 4, Above: 0, Below: 46}
	shown, moreBelow := w.Shown()
	if shown != 3 || moreBelow != 47 {
		t.Errorf("Shown() = (%d, %d), want (3, 47)", shown, moreBelow)
	}
}

// Scroll is pgup/pgdown's raw movement, clamped into [0, total-1] and
// independent of height or any cursor.
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

// At height <= 0 the window is unbounded, so the cursor is always visible and
// offset never advances: it stays <= cursor, hence below total (issues #1054,
// #5394). Only the pull-back toward a cursor above offset still applies.
func TestViewport_MoveCursor_NonPositiveHeight_ClampsOffsetBelowTotal(t *testing.T) {
	tests := []struct {
		name       string
		v          Viewport
		delta      int
		total      int
		wantCursor int
		wantOffset int
	}{
		{"zero viewport, cursor to last row", Viewport{}, 4, 5, 4, 0},
		{"zero viewport, cursor stays at top", Viewport{}, 0, 5, 0, 0},
		{"zero height, mid-list cursor", Viewport{cursor: 2}, 0, 5, 2, 0},
		{"negative height", Viewport{height: -1}, 3, 5, 3, 0},
		{"zero height, pull-back to cursor above offset", Viewport{cursor: 3, offset: 4}, -2, 5, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			v.MoveCursor(tc.delta, tc.total)
			if v.cursor != tc.wantCursor || v.offset != tc.wantOffset {
				t.Errorf("cursor, offset = %d, %d, want %d, %d", v.cursor, v.offset, tc.wantCursor, tc.wantOffset)
			}
		})
	}
}

// Offset advances just far enough to keep the cursor's row inside a bounded
// window (issue #1036).
func TestViewport_MoveCursor_PositiveHeight_AdvancesOffsetToKeepCursorVisible(t *testing.T) {
	var v Viewport
	v.SetHeight(2)
	v.MoveCursor(4, 5)
	if got := v.Window(5).Start; got != 3 {
		t.Errorf("offset after MoveCursor(4, 5) with height 2 = %d, want 3", got)
	}
}

// Binding a tighter height pulls a now-too-far offset back so the last page
// still fills the viewport instead of rendering mostly blank (issue #829).
// SetHeight itself does the clamping (issue #1540), using the total from the
// most recent Window, Scroll or MoveCursor call.
func TestViewport_SetHeight_ClampOnShrink_PullsOffsetBackToLastFullPage(t *testing.T) {
	var v Viewport
	v.Scroll(99, 100) // height 0 lets the offset park at the very last row
	v.SetHeight(10)   // offset 99 would now leave only 1 of 10 rows filled
	if got := v.Window(100).Start; got != 90 {
		t.Errorf("offset after binding height to 10 = %d, want 90 (last page fills the new viewport)", got)
	}
}
