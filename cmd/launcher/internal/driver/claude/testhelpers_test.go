package claude

import (
	"os"
	"path/filepath"
	"testing"
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
