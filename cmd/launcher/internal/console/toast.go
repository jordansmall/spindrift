package console

import "fmt"

// pickTransitionToast returns the toast text for a PickState transition
// detected between old and next — "" when nothing transitioned. When more
// than one pick transitions within a single snapshot, the last one in next's
// order wins (iteration order, not arrival time — next carries no
// timestamps to order by).
//
// Only the newest row per (Number, kind) is compared, and only when the key
// has the same row count in both snapshots: picks only append (re-pick) or
// are removed (unpick), so a count change means the newest rows are
// different logical rows, not one row transitioning (issue #5392). The
// trade-off: a re-pick whose Queued row the Model never saw (a coalesced
// refresh) gets no "started" toast, like a first-time pick.
func pickTransitionToast(old, next []Pick) string {
	type rows struct {
		count  int
		newest int // index of the newest row
	}
	index := func(picks []Pick) map[pickKey]rows {
		m := make(map[pickKey]rows, len(picks))
		for i, p := range picks {
			k := p.pickKey()
			m[k] = rows{m[k].count + 1, i}
		}
		return m
	}
	prior, current := index(old), index(next)
	toast := ""
	for i, p := range next {
		k := p.pickKey()
		cur := current[k]
		if cur.newest != i {
			continue
		}
		was, ok := prior[k]
		if !ok || was.count != cur.count || old[was.newest].State == p.State {
			continue
		}
		title := SanitizeControlSequences(p.Title)
		switch p.State {
		case PickRunning:
			toast = fmt.Sprintf("#%s started: %s", p.Number, title)
		case PickSettled:
			toast = fmt.Sprintf("#%s settled: %s", p.Number, title)
		case PickFailed:
			toast = fmt.Sprintf("#%s failed: %s", p.Number, title)
		case PickHeld:
			toast = fmt.Sprintf("#%s held: %s", p.Number, title)
		}
	}
	return toast
}
