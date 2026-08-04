package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/usage"
)

// opencodeDriver is the host-side strategy for the opencode Driver: a thin
// adapter onto the driver/opencode subpackage, which owns the opencode CLI's
// NDJSON transcript shape and transient-error taxonomy. It cannot import
// this package (that would cycle back to here). opencode.Classify returns
// driverkit.Classification directly, and this package's Classification is a
// true alias of driverkit.Classification, so the vocabulary is shared by
// construction and ClassifyTransient just returns opencode.Classify's
// result.
type opencodeDriver struct{}

func (opencodeDriver) Name() string { return "opencode" }

func (opencodeDriver) ClassifyTransient(logPath string) (Classification, error) {
	return opencode.Classify(logPath)
}

// NewHeartbeatWriter wraps raw with opencode's own heartbeat writer.
// opencode's transcript carries no role attribution, so opts.TopLevelRole is
// ignored (issue #2092).
func (opencodeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return opencode.New(raw, issue, out)
}

func (opencodeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return opencode.ExtractUsage(logPath)
}

// RenderTranscript renders the opencode transcript at logPath. opencode's
// transcript carries no role attribution, so opts.TopLevelRole is ignored
// (issue #2092).
func (opencodeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return opencode.RenderTranscript(logPath)
}

// ResolveExit derives the exit code entirely from the log, ignoring the
// process's own exitCode: the opencode CLI exits 0 even on a mid-run error,
// so its own exit code is never trustworthy (issue #2263).
func (opencodeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return opencode.SynthesizeExit(logPath)
}

func init() {
	register(opencodeDriver{})
}
