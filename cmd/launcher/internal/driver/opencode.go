package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/usage"
)

// opencodeDriver runs the shared driverkit readers over the driver/opencode
// subpackage's decoder, patterns, exit resolution, result encoder and session
// flags.
type opencodeDriver struct{}

func (opencodeDriver) Name() string { return "opencode" }

func (opencodeDriver) ClassifyTransient(logPath string) (Classification, error) {
	return driverkit.Classify(logPath, opencode.NewDecoder, opencode.TransientPatterns, opencode.TerminalPatterns, driverkit.ClassifyOptions{})
}

// NewHeartbeatWriter ignores opts.TopLevelRole (opencode's heartbeat carries no
// role attribution, issue #2092) but forwards opts.OnModel, reporting the model
// with no role.
func (opencodeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return driverkit.NewHeartbeatWriter(raw, issue, out, opencode.NewDecoder, "").OnModel(opts.OnModel)
}

// opencodeUsageByModel indirects so tests can simulate an I/O error without a
// real filesystem race against the usage aggregation scan.
var opencodeUsageByModel = func(p string) ([]usage.ModelUsage, error) {
	return driverkit.UsageByModel(p, opencode.NewDecoder)
}

// ExtractUsage sums the step_finish tallies in the log. The decoder attributes
// no subagent roles, so SummedByAgent is the one usage.MainLoopAgent row; its
// APICalls is what `spindrift stats --by role` reads for an opencode pass.
func (opencodeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return driverkit.ExtractUsage(logPath, opencode.NewDecoder, driverkit.PerCallTotals, opencodeUsageByModel,
		func(p string) ([]usage.AgentUsage, error) {
			return driverkit.UsageByAgent(p, opencode.NewDecoder, driverkit.PerCallTotals)
		})
}

// RenderTranscript attributes every event without a role to opts.TopLevelRole
// (opencode's log has no sub-agent attribution); a task tool's result carries
// the subagent it ran as a "[subagent] " tag instead.
func (opencodeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return driverkit.RenderTranscript(logPath, opencode.NewDecoder, opts.TopLevelRole)
}

// ResolveExit keeps a non-zero exitCode and surfaces an unrecovered mid-run
// error event the opencode CLI hides behind exit 0 (issues #2263, #5391).
func (opencodeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return opencode.ResolveExit(logPath, exitCode)
}

// ResultText is the last assistant message only, as claude's result is: an
// outcome-looking line in mid-pass prose must not end the run (issue #5354).
func (opencodeDriver) ResultText(logPath string) (string, error) {
	return driverkit.LastMessageText(logPath, opencode.NewDecoder)
}

func (opencodeDriver) ResultEvent(text string) ([]byte, error) {
	return opencode.ResultEvent(text)
}

func (opencodeDriver) ReadPasses(logPath, trackWrite string, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error) {
	return driverkit.ReadPasses(logPath, driverkit.PassOptions{NewDecoder: opencode.NewDecoder, Totals: driverkit.PerCallTotals, WriteTool: opencode.WriteTool}, trackWrite, visit)
}

func (opencodeDriver) SessionFlags(mode, repoSlug, issue, home string) string {
	return opencode.SessionFlags(mode, repoSlug, issue, home)
}

func init() {
	register(opencodeDriver{})
}
