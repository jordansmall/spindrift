package connectortracker

// The in-package helpers the external signal-read tests share: that package
// imports signalsocket, whose closure reaches back to connectortracker.
var (
	StubGuest     = stubGuest
	MustTracker   = mustTracker
	TestLifecycle = testLifecycle
	TestTarget    = testTarget
)
