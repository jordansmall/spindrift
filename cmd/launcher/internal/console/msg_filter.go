package console

// Msg types for the label-filter input mode.

// FilterEditStartMsg is the tea layer's signal that the operator pressed
// "/" — arms filter-input mode, saving the current Filter so Esc can revert
// to it (issue #784).
type FilterEditStartMsg struct{}

func (FilterEditStartMsg) isConsoleMsg() {}

// FilterEditConfirmMsg is the tea layer's signal that the operator pressed
// Enter while editing the filter — leaves the already-live-narrowed Filter
// as-is and exits editing mode.
type FilterEditConfirmMsg struct{}

func (FilterEditConfirmMsg) isConsoleMsg() {}

// FilterEditCancelMsg is the tea layer's signal that the operator pressed
// Esc while editing the filter — restores the Filter active before editing
// started and exits editing mode.
type FilterEditCancelMsg struct{}

func (FilterEditCancelMsg) isConsoleMsg() {}
