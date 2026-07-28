package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/usage"
)

// opencodeDriver is the host-side strategy for the opencode Driver: a thin
// adapter onto the driver/opencode subpackage, which owns the opencode CLI's
// NDJSON transcript shape and transient-error taxonomy. It cannot import
// this package (that would cycle back to here), so ClassifyTransient
// converts opencode's local Class/Reason values onto this package's shared
// vocabulary.
type opencodeDriver struct{}

func (opencodeDriver) Name() string { return "opencode" }

func (opencodeDriver) ClassifyTransient(logPath string) (Classification, error) {
	c, err := opencode.Classify(logPath)
	if err != nil {
		return Classification{}, err
	}
	return Classification{
		Class:   Class(c.Class),
		Reason:  Reason(c.Reason),
		ResetAt: c.ResetAt,
	}, nil
}

// NewHeartbeatWriter wraps raw with opencode's own heartbeat writer.
// opencode's transcript carries no role attribution, so topLevelRole is
// ignored (issue #2092).
func (opencodeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, topLevelRole string) io.Writer {
	return opencode.New(raw, issue, out)
}

func (opencodeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return opencode.ExtractUsage(logPath)
}

// RenderTranscript renders the opencode transcript at logPath. opencode's
// transcript carries no role attribution, so topLevelRole is ignored (issue
// #2092).
func (opencodeDriver) RenderTranscript(logPath, topLevelRole string) (string, error) {
	return opencode.RenderTranscript(logPath)
}

// SynthesizeExit implements the optional ExitSynthesizer interface (see
// exitsynth.go): the opencode CLI exits 0 even on a mid-run error, so the
// launcher derives a trustworthy exit code from the log instead of trusting
// the process's own exit code.
func (opencodeDriver) SynthesizeExit(logPath string) (int, error) {
	return opencode.SynthesizeExit(logPath)
}

func init() {
	register(opencodeDriver{})
}
