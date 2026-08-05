package console

// Msg types for miscellaneous chrome: dogfood notice, gg-leader chord, toast, help, and resize.

// DogfoodNoticeMsg reports whether a live dogfood pid-file was found at
// startup — a headless loop competing for the same queue. Informational
// only: the console never blocks or gates on it.
type DogfoodNoticeMsg struct {
	Live bool
}

func (DogfoodNoticeMsg) isConsoleMsg() {}

// GPendingMsg is the tea layer's signal that "g" armed the "gg" leader
// window (issue #1628).
type GPendingMsg struct{}

func (GPendingMsg) isConsoleMsg() {}

// GResolvedMsg is the tea layer's signal that a pending "gg" chord
// resolved — either a trailing "g" completed it, the leader window timed
// out, or any other key cancelled it (issue #1628).
type GResolvedMsg struct{}

func (GResolvedMsg) isConsoleMsg() {}

// ToastDismissedMsg is the tea layer's signal that a pick-transition toast
// (Model.Toast, issue #1830) should clear — fired by the operator's next
// keypress or the generation-pinned auto-dismiss timer, whichever comes
// first.
type ToastDismissedMsg struct{}

func (ToastDismissedMsg) isConsoleMsg() {}

// HelpToggleMsg is the tea layer's signal that the operator pressed "?" —
// opens the help overlay, or closes it if already open (issue #784).
type HelpToggleMsg struct{}

func (HelpToggleMsg) isConsoleMsg() {}

// SizeChangedMsg carries the terminal's current width/height — the tea
// layer's translation of Bubble Tea's WindowSizeMsg, sent on every resize
// including the initial size event (issue #842). Update clamps non-sensical
// values (zero/negative) to a safe floor.
type SizeChangedMsg struct {
	Width, Height int
}

func (SizeChangedMsg) isConsoleMsg() {}
