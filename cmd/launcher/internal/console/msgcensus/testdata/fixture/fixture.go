package fixture

type AMsg struct{}

func (AMsg) isConsoleMsg() {}

type BMsg struct{}

func (*BMsg) isConsoleMsg() {}

type NotAMsg struct{}
