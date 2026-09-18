package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// claudeDriver adapts the driver/claude subpackage. That subpackage cannot
// import this one without a cycle, so it returns driverkit.Classification,
// which this package aliases.
type claudeDriver struct{}

func (claudeDriver) Name() string { return "claude" }

func (claudeDriver) ClassifyTransient(logPath string) (Classification, error) {
	return claude.Classify(logPath)
}

func (claudeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return claude.NewWithTopLevelRole(raw, issue, out, opts.TopLevelRole)
}

func (claudeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return claude.ExtractUsage(logPath)
}

func (claudeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return claude.RenderTranscriptWithRole(logPath, opts.TopLevelRole)
}

// ResolveExit returns the caller's exitCode unchanged: claude's stream-json
// type:"result" event already carries a trustworthy is_error/subtype pair.
func (claudeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return exitCode, nil
}

func init() {
	register(claudeDriver{})
}
