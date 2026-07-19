package console

// Viewport owns one pane's scroll/cursor geometry: offset, cursor, and the
// content-row height budget it currently renders into (issue #1540). The
// three scrolling panes — backlog/queue, the drill-in sidebar, and the
// rebuild-output pane — window through one Viewport each rather than
// re-implementing the same offset/cursor/follow/clamp arithmetic. Cursorless
// panes (sidebar, rebuild-output) simply never call MoveCursor. All state is
// unexported; SetHeight, MoveCursor, Scroll, and Window are the only ways in
// or out.
type Viewport struct {
	offset, cursor, height int
	// total caches the row count from the most recent Window/Scroll/
	// MoveCursor call — SetHeight has no total parameter of its own, so a
	// height shrink's clamp-on-shrink reclamps against whichever total was
	// last seen.
	total int
}

// Window is the visible-slice bounds [Start, End) into a total row count at
// a Viewport's current offset, plus how many rows sit above/below it.
// Geometry only — callers keep formatting the "… N more below" / "(X-Y of
// N)" affordances from it (issue #1540).
type Window struct{ Start, End, Above, Below int }

// SetHeight sets v's content-row budget — the row count a pane can actually
// show, already stripped of its own header/footer chrome by the caller. 0
// means unbounded, replacing the nil *int convention. The layout code calls
// this whenever the terminal resizes or the pane's own chrome changes.
// Clamp-on-shrink happens here: offset is immediately pulled back, against
// whichever total was last seen, so the last page still fills the viewport
// (issue #829) instead of rendering mostly blank until the next Scroll or
// MoveCursor.
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

// MoveCursor adds delta to v's cursor, clamped into [0, total-1] (0 when
// total is 0), then advances/rewinds offset just far enough to keep cursor
// on screen — the cursor-follow invariant (issue #1036), used only by
// cursor-owning panes; cursorless panes simply never call this.
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

// windowedCount returns how many of remaining rows a window of budget rows
// actually shows: remaining itself when it all fits, or one less than
// budget (a row held back for a trailing "N more below" affordance line)
// when it doesn't (issue #1061, inherited).
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

// Scroll adds delta to v's offset, clamped into [0, total-1] (0 when total
// is 0) — pgup/pgdown's raw viewport movement, independent of height or any
// cursor.
func (v *Viewport) Scroll(delta, total int) {
	v.total = total
	v.offset = clampIndex(v.offset+delta, total)
}

// Window returns the visible-slice bounds into total rows at v's current
// offset. Height 0 (unbounded) shows every row from offset with nothing
// hidden below.
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

// clampIndex pulls i into [0, n-1], or 0 when n is zero — the single index
// invariant every Viewport method shares.
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
