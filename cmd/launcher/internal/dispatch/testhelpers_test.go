package dispatch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	driverclaude "spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/usage"
)

// tempLogDir creates a temp dir with a logs/ subdirectory.
func tempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// boxErr is a non-nil error that stands in for a non-zero box exit.
var boxErr = errors.New("exit 1")

// writeFile writes content to path, creating parent directories as needed.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// fakeDriver is a test double for driver.Driver. ClassifyFn, when set,
// overrides the default Terminal/TaskFailed classification. ExtractUsage
// delegates to the real claude subpackage's log parsing (not faked) so
// dispatch's UsageReport tests can exercise real claude-format stream-json
// fixtures through the Driver seam.
type fakeDriver struct {
	ClassifyFn func(logPath string) (driver.Classification, error)
}

func (d fakeDriver) Name() string { return "fake" }

func (d fakeDriver) ClassifyTransient(logPath string) (driver.Classification, error) {
	if d.ClassifyFn != nil {
		return d.ClassifyFn(logPath)
	}
	return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
}

func (d fakeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer) io.Writer {
	return raw
}

func (d fakeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return driverclaude.ExtractUsage(logPath)
}
