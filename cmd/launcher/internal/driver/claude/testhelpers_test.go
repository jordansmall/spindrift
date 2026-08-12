package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// WriteLog writes lines to a temp log file and returns its path. Exported so
// external test files in package claude_test can share it.
func WriteLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// ClassifyAt exposes classifyAt to external test files in package
// claude_test, so they can pin the clock instead of leaking the real
// wall-clock time.Now() into a parsed resetsAt fallback (issue #2443).
func ClassifyAt(logPath string, now time.Time) (driverkit.Classification, error) {
	return classifyAt(logPath, now)
}
