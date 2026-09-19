package console

// FilterEditStartMsg reports that the operator pressed "/". Handling it saves
// the current Filter so Esc can restore it (issue #784).
type FilterEditStartMsg struct{}

func (FilterEditStartMsg) isConsoleMsg() {}

// FilterEditConfirmMsg reports that the operator pressed Enter. The Filter has
// already narrowed live, so confirming only exits editing mode.
type FilterEditConfirmMsg struct{}

func (FilterEditConfirmMsg) isConsoleMsg() {}

// FilterEditCancelMsg reports that the operator pressed Esc, restoring the
// Filter saved when editing started and exiting editing mode.
type FilterEditCancelMsg struct{}

func (FilterEditCancelMsg) isConsoleMsg() {}
