package ledgertest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// CmdCount counts top-level `git <name>` cmd_name events in the
// GIT_TRACE2_EVENT log at tracePath, the shared helper for tests that assert
// how many times a Sweep/Append/FetchTree run shelled out to a given git
// subcommand (issue #3995).
func CmdCount(t *testing.T, tracePath, name string) int {
	t.Helper()
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace2 log %s: %v", tracePath, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Event string `json:"event"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse trace2 line %q: %v", line, err)
		}
		if ev.Event == "cmd_name" && ev.Name == name {
			count++
		}
	}
	return count
}
