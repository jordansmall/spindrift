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

// NewHeartbeatWriter ignores opts.TopLevelRole (opencode's heartbeat carries no
// role attribution, issue #2092) but forwards opts.OnModel, reporting the model
// with no role.
func (opencodeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return opencode.New(raw, issue, out).OnModel(opts.OnModel)
}

func (opencodeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return opencode.ExtractUsage(logPath)
}

// RenderTranscript attributes every text event to opts.TopLevelRole (opencode's
// log has no sub-agent attribution, so there is no other role).
func (opencodeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return opencode.RenderTranscript(logPath, opts.TopLevelRole)
}

// ResolveExit keeps a non-zero exitCode and surfaces a mid-run error event the
// opencode CLI hides behind exit 0 (issue #2263).
func (opencodeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return opencode.ResolveExit(logPath, exitCode)
}

func (opencodeDriver) ResultText(logPath string) (string, error) {
	return opencode.ResultText(logPath)
}

func (opencodeDriver) ResultEvent(text string) ([]byte, error) {
	return opencode.ResultEvent(text)
}

func (opencodeDriver) SessionFlags(mode, repoSlug, issue, home string) string {
	return opencode.SessionFlags(mode, repoSlug, issue, home)
}

func init() {
	register(opencodeDriver{})
}
