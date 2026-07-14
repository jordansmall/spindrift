package driver

import (
	"io"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/usage"
)

// claudeDriver is the host-side strategy for the claude Driver: a thin
// adapter onto the driver/claude subpackage, which owns the Anthropic
// transient taxonomy, stream-json heartbeat parsing, and usage-log parsing.
// It cannot import this package (that would cycle back to here), so
// ClassifyTransient converts claude's local Class/Reason values onto this
// package's shared vocabulary.
type claudeDriver struct{}

func (claudeDriver) Name() string { return "claude" }

func (claudeDriver) ClassifyTransient(logPath string) (Classification, error) {
	c, err := claude.Classify(logPath)
	if err != nil {
		return Classification{}, err
	}
	return Classification{
		Class:   Class(c.Class),
		Reason:  Reason(c.Reason),
		ResetAt: c.ResetAt,
	}, nil
}

func (claudeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer) io.Writer {
	return claude.New(raw, issue, out)
}

func (claudeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return claude.ExtractUsage(logPath)
}

func (claudeDriver) RenderTranscript(logPath string) (string, error) {
	return claude.RenderTranscript(logPath)
}

func init() {
	register(claudeDriver{})
}
