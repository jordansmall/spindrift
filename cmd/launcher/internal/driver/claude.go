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
	return driverkit.Classify(logPath, claude.NewDecoder, claude.TransientPatterns, claude.TerminalPatterns, claude.ClassifyOptions)
}

// NewHeartbeatWriter attributes top-level messages to opts.TopLevelRole, for a
// pass the orchestrator owns as something other than implementation (issue
// #2092); an empty role keeps the driverkit.ImplementorRole default.
func (claudeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	role := opts.TopLevelRole
	if role == "" {
		role = driverkit.ImplementorRole
	}
	return driverkit.NewHeartbeatWriter(raw, issue, out, claude.NewDecoder, role).Track(claude.HeartbeatTracking).OnModel(opts.OnModel)
}

// claudeUsageByModel and claudeUsageByAgent indirect so tests can simulate an
// I/O error without a real filesystem race against the usage aggregation scan.
//
// The per-model sum runs over DISTINCT message.id (claude-code re-emits a
// multi-block message once per block with identical usage) and ignores the
// result event's own usage header, which covers one call only (issue #2078).
// Every agent row's OutputTokens stays 0 for ExtractUsage to patch from the
// result events.
var claudeUsageByModel = func(p string) ([]usage.ModelUsage, error) {
	return driverkit.UsageByModel(p, claude.NewDecoder, claude.HeartbeatTracking.Family)
}

var claudeUsageByAgent = func(p string) ([]usage.AgentUsage, error) {
	return driverkit.UsageByAgent(p, claude.NewDecoder, driverkit.SessionTotals)
}

func (claudeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return driverkit.ExtractUsage(logPath, claude.NewDecoder, driverkit.SessionTotals, claudeUsageByModel, claudeUsageByAgent)
}

func (claudeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return driverkit.RenderTranscript(logPath, claude.NewDecoder, opts.TopLevelRole)
}

// ResolveExit returns the caller's exitCode unchanged: claude's stream-json
// type:"result" event already carries a trustworthy is_error/subtype pair.
func (claudeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return exitCode, nil
}

func (claudeDriver) ResultText(logPath string) (string, error) {
	return driverkit.ResultText(logPath, claude.NewDecoder)
}

func (claudeDriver) ResultEvent(text string) ([]byte, error) {
	return claude.ResultEvent(text)
}

func (claudeDriver) ReadPasses(logPath, trackWrite string, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error) {
	return driverkit.ReadPasses(logPath, driverkit.PassOptions{NewDecoder: claude.NewDecoder, Totals: driverkit.SessionTotals, WriteTool: claude.WriteTool}, trackWrite, visit)
}

func (claudeDriver) SessionFlags(mode, repoSlug, issue, home string) string {
	return claude.SessionFlags(mode, repoSlug, issue, home)
}

func init() {
	register(claudeDriver{})
}
