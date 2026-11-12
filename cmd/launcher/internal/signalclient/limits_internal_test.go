package signalclient

import (
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
)

// A capped snapshot must still fit the client's reply limit after worst-case
// JSON escaping (one byte to a six-byte \u escape) plus envelope slack.
func TestMaxReplyBytesFitsACappedIssueSnapshot(t *testing.T) {
	if 6*signalwire.MaxIssueReplyBytes+16*1024 > maxReplyBytes {
		t.Fatalf("a worst-case escaped %d-byte snapshot overruns the client's %d-byte reply limit", signalwire.MaxIssueReplyBytes, maxReplyBytes)
	}
}
