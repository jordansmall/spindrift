package driver

import (
	"io"

	"spindrift.dev/launcher/internal/heartbeat"
	"spindrift.dev/launcher/internal/outcome"
)

// claudeDriver is the host-side strategy for the claude Driver: today's
// Anthropic-specific transient markers (outcome.Classify) and stream-json
// heartbeat parsing (heartbeat.New), unchanged from before the Driver seam
// existed.
type claudeDriver struct{}

func (claudeDriver) Name() string { return "claude" }

func (claudeDriver) ClassifyTransient(logPath string) (outcome.Classification, error) {
	return outcome.Classify(logPath)
}

func (claudeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer) io.Writer {
	return heartbeat.New(raw, issue, out)
}

func init() {
	register(claudeDriver{})
}
