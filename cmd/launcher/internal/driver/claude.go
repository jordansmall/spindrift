package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// claudeDriver is the host-side strategy for the claude Driver: a thin
// adapter onto the driver/claude subpackage, which owns the Anthropic
// transient taxonomy, stream-json heartbeat parsing, and usage-log parsing.
// It cannot import this package (that would cycle back to here). Both
// claude.Classification and this package's Classification are true aliases
// of driverkit.Classification, so the vocabulary is shared by construction
// and ClassifyTransient just returns claude.Classify's result.
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

// ResolveExit trusts the caller's own exitCode unchanged: claude's
// stream-json type:"result" event already carries a trustworthy
// is_error/subtype pair, so the process's own exit code needs no
// replacement.
func (claudeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return exitCode, nil
}

func init() {
	register(claudeDriver{})
}
