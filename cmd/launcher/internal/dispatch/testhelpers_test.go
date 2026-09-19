package dispatch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	driverclaude "spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/usage"
)

func tempLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(HostLogDirFor(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

var boxErr = errors.New("exit 1")

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// nonceLine appends d's per-run nonce (issue #1939) to line and a trailing
// newline, the way a genuine Box echoes RUN_NONCE on its outcome line. line
// must have no trailing newline.
func nonceLine(d *Dispatch, line string) []byte {
	return []byte(line + " nonce=" + d.nonce + "\n")
}

// writeOutcomeOnFinalCall makes fr return errs[i] for the i-th Run call (the
// last element repeats once the sequence runs out, like runner.Fake's RunErrs)
// and writes line only on a call whose error is nil. Since issue #2075 a
// printed outcome settles a non-zero exit immediately, so a retry fixture that
// echoed the outcome on every call would stop testing a verdict-less attempt.
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
// overrides the default Terminal/TaskFailed classification. ExtractUsage calls
// the real claude log parsing, so dispatch's UsageReport tests run genuine
// claude-format stream-json fixtures through the Driver seam.
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

func (d fakeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return raw
}

func (d fakeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return driverclaude.ExtractUsage(logPath)
}

func (d fakeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return driverclaude.RenderTranscriptWithRole(logPath, opts.TopLevelRole)
}

// ResolveExit trusts the passed exitCode unchanged, matching claudeDriver
// (issue #2263), since fakeDriver replaces the claude strategy in these tests.
func (d fakeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return exitCode, nil
}
