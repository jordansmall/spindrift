package dispatch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	driverclaude "spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

// tempLogDir creates a temp dir with a .spindrift/logs subdirectory.
func tempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(HostLogDirFor(dir), 0o755); err != nil {
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

// nonceLine appends d's per-run nonce (issue #1939) to line and a trailing
// newline, mirroring how a genuine Box echoes RUN_NONCE on its outcome line.
// line should have no trailing newline.
func nonceLine(d *Dispatch, line string) []byte {
	return []byte(line + " nonce=" + d.nonce + "\n")
}

// writeOutcomeOnFinalCall configures fr to return errs[i] for the i-th Run
// call (mirroring runner.Fake's own RunErrs semantics: the last element is
// reused once the sequence is exhausted), writing line to that call's log
// only when errs[i] is nil. Before issue #2075, dispatchWithRetry never
// scanned a non-zero exit's log for an outcome, so a hold/backoff retry
// fixture could get away with a single fr.WriteToOutput echoing the eventual
// outcome onto every call, including the earlier, still-failing ones. Now
// that a genuine printed outcome settles a non-zero exit immediately, that
// shortcut would falsify the scenario under test -- an earlier attempt dying
// with no verdict at all -- so those fixtures use this helper instead to
// keep the outcome line on the genuinely successful call only.
func writeOutcomeOnFinalCall(fr *runner.Fake, errs []error, line []byte) {
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		i := calls
		if i >= len(errs) {
			i = len(errs) - 1
		}
		calls++
		err := errs[i]
		if err == nil {
			box.Output.Write(line) //nolint:errcheck
		}
		return err
	}
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

func (d fakeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, topLevelRole string) io.Writer {
	return raw
}

func (d fakeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return driverclaude.ExtractUsage(logPath)
}

func (d fakeDriver) RenderTranscript(logPath, topLevelRole string) (string, error) {
	return driverclaude.RenderTranscriptWithRole(logPath, topLevelRole)
}
