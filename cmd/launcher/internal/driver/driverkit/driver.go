package driverkit

import (
	"io"

	"spindrift.dev/launcher/internal/usage"
)

// Driver is the one Driver implementation: it runs the shared readers over a
// Strategy's decoder, patterns and hooks. Build one with New; the registry
// package stores these and a Driver package's tests build their own, so
// neither needs the other.
type Driver struct{ s Strategy }

// New returns the Driver that runs s.
func New(s Strategy) Driver { return Driver{s} }

// usageByModel and usageByAgent are the one seam for tests to simulate an I/O
// error without a real filesystem race against the usage aggregation scan.
var (
	usageByModel = UsageByModel
	usageByAgent = UsageByAgent
)

func (d Driver) Name() string { return d.s.Name }

func (d Driver) ClassifyTransient(logPath string) (Classification, error) {
	return Classify(logPath, d.s.NewDecoder, d.s.TransientPatterns, d.s.TerminalPatterns, d.s.Classify)
}

// NewHeartbeatWriter attributes top-level messages to opts.TopLevelRole, for a
// pass the orchestrator owns as something other than implementation (issue
// #2092); an empty role falls back to the Strategy's DefaultTopLevelRole, and a
// Strategy with none ignores opts.TopLevelRole entirely.
func (d Driver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts RenderOptions) io.Writer {
	role := d.s.DefaultTopLevelRole
	if role != "" && opts.TopLevelRole != "" {
		role = opts.TopLevelRole
	}
	return NewHeartbeatWriter(raw, issue, out, d.s.NewDecoder, role).Track(d.s.Heartbeat).OnModel(opts.OnModel)
}

// StreamFilter sits between a Driver's stdout and its Pass-log tee. Flush
// writes out anything the filter still holds, and is safe to call repeatedly.
type StreamFilter interface {
	io.Writer
	Flush() error
}

type passthrough struct{ io.Writer }

func (passthrough) Flush() error { return nil }

// NewStreamFilter returns the Strategy's filter over w, or one that forwards
// every byte to w when the Strategy declares none.
func (d Driver) NewStreamFilter(w io.Writer) StreamFilter {
	if d.s.NewStreamFilter == nil {
		return passthrough{w}
	}
	return d.s.NewStreamFilter(w)
}

func (d Driver) ExtractUsage(logPath string) (usage.Report, error) {
	byModel := func(p string) ([]usage.ModelUsage, error) {
		return usageByModel(p, d.s.NewDecoder, d.s.Heartbeat.Family)
	}
	byAgent := func(p string) ([]usage.AgentUsage, error) {
		return usageByAgent(p, d.s.NewDecoder, d.s.Totals)
	}
	return ExtractUsage(logPath, d.s.NewDecoder, d.s.Totals, byModel, byAgent)
}

func (d Driver) RenderTranscript(logPath string, opts RenderOptions) (string, error) {
	return RenderTranscript(logPath, d.s.NewDecoder, opts.TopLevelRole)
}

func (d Driver) ResolveExit(logPath string, exitCode int) (int, error) {
	if d.s.ResolveExit == nil {
		return exitCode, nil
	}
	return d.s.ResolveExit(logPath, exitCode)
}

func (d Driver) ResultText(logPath string) (string, error) {
	return d.s.ReadResult(logPath)
}

func (d Driver) ResultEvent(text string) ([]byte, error) {
	return d.s.ResultEvent(text)
}

func (d Driver) ReadPasses(logPath, trackWrite string, visit func(PassLine)) ([]PassTally, error) {
	return ReadPasses(logPath, PassOptions{NewDecoder: d.s.NewDecoder, Totals: d.s.Totals, WriteTool: d.s.WriteTool}, trackWrite, visit)
}

func (d Driver) SessionFlags(mode, repoSlug, issue, home string) string {
	return d.s.SessionFlags(mode, repoSlug, issue, home)
}
