//go:build !(linux && (amd64 || arm64))

package main

// inPlaceRestartSupported is false off linux amd64/arm64: the exec path needs
// raw rt_sigaction and Pipe2, so mainRun refuses --restart-in-place.
var inPlaceRestartSupported = false

func sigHUPIgnored() (bool, error) { return false, errInPlaceUnsupported }

func ignoreStopSignals() (func(), error) { return nil, errInPlaceUnsupported }

// restoreSIGHUP is a no-op: no exec ever left SIGHUP ignored here.
func restoreSIGHUP(bool) error { return nil }

func handoffDocPipe([]byte) (int, error) { return -1, errInPlaceUnsupported }
