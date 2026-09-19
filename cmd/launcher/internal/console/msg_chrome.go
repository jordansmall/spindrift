package console

// DogfoodNoticeMsg reports whether a live dogfood pid-file was found at startup,
// meaning a headless loop competes for the same queue. Informational only: the
// console never blocks or gates on it.
type DogfoodNoticeMsg struct {
	Live bool
}

func (DogfoodNoticeMsg) isConsoleMsg() {}

// GPendingMsg reports that "g" armed the "gg" leader window (issue #1628).
type GPendingMsg struct{}

func (GPendingMsg) isConsoleMsg() {}

// GResolvedMsg reports that a pending "gg" chord resolved: a trailing "g"
// completed it, the leader window timed out, or another key cancelled it
// (issue #1628).
type GResolvedMsg struct{}

func (GResolvedMsg) isConsoleMsg() {}

// ToastDismissedMsg reports that a pick-transition toast should clear, fired by
// the next keypress or the generation-pinned auto-dismiss timer, whichever comes
// first (issue #1830).
type ToastDismissedMsg struct{}

func (ToastDismissedMsg) isConsoleMsg() {}

// HelpToggleMsg toggles the help overlay open or closed (issue #784).
type HelpToggleMsg struct{}

func (HelpToggleMsg) isConsoleMsg() {}

// SizeChangedMsg carries the terminal's current width and height, sent on every
// resize including the initial size event (issue #842). Update clamps zero or
// negative values to a safe floor.
type SizeChangedMsg struct {
	Width, Height int
}

func (SizeChangedMsg) isConsoleMsg() {}
