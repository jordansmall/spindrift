package driver

// ExitSynthesizer is implemented by Drivers whose native exit code is
// unreliable — the opencode CLI exits 0 even on a mid-run error, unlike
// claude, whose exit code the launcher already trusts. SynthesizeExit
// derives a trustworthy exit code from the log, replacing the process's own
// exit code for a Driver that implements this optional interface. A Driver
// that doesn't need this (e.g. claude) simply doesn't implement it; callers
// type-assert for it rather than requiring it on the base Driver interface.
type ExitSynthesizer interface {
	SynthesizeExit(logPath string) (int, error)
}
