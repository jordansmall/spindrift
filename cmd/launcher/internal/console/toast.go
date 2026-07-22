package console

import "fmt"

// pickTransitionToast returns the toast text for a PickState transition
// detected between old and next — "" when nothing transitioned. When more
// than one pick transitions within a single snapshot, the last one in next's
// order wins (iteration order, not arrival time — next carries no
// timestamps to order by).
func pickTransitionToast(old, next []Pick) string {
	prior := make(map[string]PickState, len(old))
	for _, p := range old {
		prior[p.Number] = p.State
	}
	toast := ""
	for _, p := range next {
		was, ok := prior[p.Number]
		if !ok || was == p.State {
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
