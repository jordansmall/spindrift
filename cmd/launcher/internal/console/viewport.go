package console

// Viewport owns one pane's scroll/cursor geometry: offset, cursor, and the
// content-row height budget it renders into (issue #1540). Cursorless panes
// never call MoveCursor.
type Viewport struct {
	offset, cursor, height int
	// total caches the row count from the most recent Window, Scroll, or
	// MoveCursor call, because SetHeight takes no total of its own and its
	// clamp-on-shrink needs one. A caller that wants windowing without that
	// clamp (backlog/queue's non-page-capped pgup/pgdown, issue #1060) sets
	// height directly in a struct literal instead.
	total int
}

// Window is the visible-slice bounds [Start, End) into a total row count at a
// Viewport's current offset, plus how many rows sit above and below it. It is
// geometry only, so callers format the "… N more below" and "(X-Y of N)"
// affordances themselves (issue #1540).
type Window struct{ Start, End, Above, Below int }

// Shown returns how many rows Window renders as content, and the "N more
// below" count to print in place of the one row held back to fit that
// affordance line inside the same budget (0 when nothing is truncated).
func (w Window) Shown() (shown, moreBelow int) {
	shown = w.End - w.Start
	if w.Below > 0 && shown > 0 {
		shown--
		moreBelow = w.Below + 1
	}
	return shown, moreBelow
}

// SetHeight sets v's content-row budget, which the caller has already
// stripped of the pane's own header and footer chrome. 0 means unbounded.
// It also clamps offset back against the last seen total, so a shrink leaves
// the last page full instead of mostly blank (issue #829).
func (v *Viewport) SetHeight(h int) {
	if h < 0 {
		h = 0
	}
	v.height = h
	if v.height <= 0 {
		return
	}
	maxOffset := v.total - 1
	if pageMax := v.total - v.height; pageMax < maxOffset {
		maxOffset = pageMax
	}
	if maxOffset < 0 {
		maxOffset = 0
	}
	switch {
	case v.offset < 0:
		v.offset = 0
	case v.offset > maxOffset:
		v.offset = maxOffset
	}
}

// MoveCursor adds delta to v's cursor, clamped into [0, total-1], then moves
// offset just far enough to keep the cursor on screen (issue #1036).
func (v *Viewport) MoveCursor(delta, total int) {
	v.total = total
	v.cursor = clampIndex(v.cursor+delta, total)
	for v.cursor < v.offset {
		v.offset--
	}
	for v.offset < total-1 {
		if v.cursor < v.offset+windowedCount(total-v.offset, v.height) {
			break
		}
		v.offset++
	}
}

// windowedCount returns how many of remaining rows a budget-row window shows:
// remaining when it all fits, otherwise one less than budget, because a row
// is held back for the trailing "N more below" line (issue #1061).
func windowedCount(remaining, budget int) int {
	if budget < 0 {
		budget = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining <= budget {
		return remaining
	}
	n := budget - 1
	if n < 0 {
		n = 0
	}
	return n
}

// Scroll adds delta to v's offset, clamped into [0, total-1], ignoring the
// height and the cursor.
func (v *Viewport) Scroll(delta, total int) {
	v.total = total
	v.offset = clampIndex(v.offset+delta, total)
}

// Window returns the visible-slice bounds into total rows at v's current
// offset. Height 0 means unbounded, so every row from offset shows.
func (v *Viewport) Window(total int) Window {
	v.total = total
	offset := clampIndex(v.offset, total)
	if v.height <= 0 {
		return Window{Start: offset, End: total, Above: offset, Below: 0}
	}
	end := offset + v.height
	if end > total {
		end = total
	}
	return Window{Start: offset, End: end, Above: offset, Below: total - end}
}

func clampIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}
