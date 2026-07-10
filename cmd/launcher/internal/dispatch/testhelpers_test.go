package dispatch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
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
// overrides the default Terminal/TaskFailed classification.
type fakeDriver struct {
	ClassifyFn func(logPath string) (outcome.Classification, error)
}

func (d fakeDriver) Name() string { return "fake" }

func (d fakeDriver) ClassifyTransient(logPath string) (outcome.Classification, error) {
	if d.ClassifyFn != nil {
		return d.ClassifyFn(logPath)
	}
	return outcome.Classification{Class: outcome.Terminal, Reason: outcome.TaskFailed}, nil
}

func (d fakeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer) io.Writer {
	return raw
}
