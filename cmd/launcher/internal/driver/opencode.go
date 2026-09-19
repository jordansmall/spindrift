package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/usage"
)

// opencodeDriver adapts the driver/opencode subpackage, which owns the opencode
// CLI's NDJSON transcript shape and transient-error taxonomy. That subpackage
// cannot import this one without a cycle, so it returns a
// driverkit.Classification, which this package's Classification aliases.
type opencodeDriver struct{}

func (opencodeDriver) Name() string { return "opencode" }

func (opencodeDriver) ClassifyTransient(logPath string) (Classification, error) {
	return opencode.Classify(logPath)
}

// NewHeartbeatWriter ignores opts.TopLevelRole: opencode's transcript carries
// no role attribution (issue #2092).
func (opencodeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return opencode.New(raw, issue, out)
}

func (opencodeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return opencode.ExtractUsage(logPath)
}

// RenderTranscript ignores opts.TopLevelRole: opencode's transcript carries no
// role attribution (issue #2092).
func (opencodeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return opencode.RenderTranscript(logPath)
}

// ResolveExit ignores exitCode and derives the exit from the log: the opencode
// CLI exits 0 even on a mid-run error (issue #2263).
func (opencodeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return opencode.SynthesizeExit(logPath)
}

func init() {
	register(opencodeDriver{})
}
