package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// strategyDriver is the one Driver implementation: it runs the shared
// driverkit readers over a driverkit.Strategy's decoder, patterns and hooks.
type strategyDriver struct{ s driverkit.Strategy }

var _ Driver = strategyDriver{}

// usageByModel and usageByAgent are the one seam for tests to simulate an I/O
// error without a real filesystem race against the usage aggregation scan.
//
// The per-model sum runs over DISTINCT message.id (claude-code re-emits a
// multi-block message once per block with identical usage) and ignores the
// result event's own usage header, which covers one call only (issue #2078).
var (
	usageByModel = driverkit.UsageByModel
	usageByAgent = driverkit.UsageByAgent
)

func (d strategyDriver) Name() string { return d.s.Name }

func (d strategyDriver) ClassifyTransient(logPath string) (Classification, error) {
	return driverkit.Classify(logPath, d.s.NewDecoder, d.s.TransientPatterns, d.s.TerminalPatterns, d.s.Classify)
}

// NewHeartbeatWriter attributes top-level messages to opts.TopLevelRole, for a
// pass the orchestrator owns as something other than implementation (issue
// #2092); an empty role falls back to the Strategy's DefaultTopLevelRole, and a
// Strategy with none ignores opts.TopLevelRole entirely.
func (d strategyDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	role := d.s.DefaultTopLevelRole
	if role != "" && opts.TopLevelRole != "" {
		role = opts.TopLevelRole
	}
	return driverkit.NewHeartbeatWriter(raw, issue, out, d.s.NewDecoder, role).Track(d.s.Heartbeat).OnModel(opts.OnModel)
}

func (d strategyDriver) ExtractUsage(logPath string) (usage.Report, error) {
	byModel := func(p string) ([]usage.ModelUsage, error) {
		return usageByModel(p, d.s.NewDecoder, d.s.Heartbeat.Family)
	}
	byAgent := func(p string) ([]usage.AgentUsage, error) {
		return usageByAgent(p, d.s.NewDecoder, d.s.Totals)
	}
	return driverkit.ExtractUsage(logPath, d.s.NewDecoder, d.s.Totals, byModel, byAgent)
}

func (d strategyDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return driverkit.RenderTranscript(logPath, d.s.NewDecoder, opts.TopLevelRole)
}

func (d strategyDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	if d.s.ResolveExit == nil {
		return exitCode, nil
	}
	return d.s.ResolveExit(logPath, exitCode)
}

func (d strategyDriver) ResultText(logPath string) (string, error) {
	return d.s.ReadResult(logPath)
}

func (d strategyDriver) ResultEvent(text string) ([]byte, error) {
	return d.s.ResultEvent(text)
}

func (d strategyDriver) ReadPasses(logPath, trackWrite string, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error) {
	return driverkit.ReadPasses(logPath, driverkit.PassOptions{NewDecoder: d.s.NewDecoder, Totals: d.s.Totals, WriteTool: d.s.WriteTool}, trackWrite, visit)
}

func (d strategyDriver) SessionFlags(mode, repoSlug, issue, home string) string {
	return d.s.SessionFlags(mode, repoSlug, issue, home)
}
