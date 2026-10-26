package dispatch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/seamtest"
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

// testStrategy is the seamtest Strategy plus what a real Driver always has and
// the canonical grammar leaves out: tool calls tally on the heartbeat under
// their lowercased name, and the top-level role defaults to implement.
var testStrategy = func() driverkit.Strategy {
	s := seamtest.Strategy
	s.Heartbeat = driverkit.HeartbeatTracking{Kind: strings.ToLower}
	s.DefaultTopLevelRole = driverkit.ImplementorRole
	return s
}()

// fakeDriver is a test double for driver.Driver over the seamtest Strategy, so
// dispatch's tests read canonical-event fixtures and never a CLI's grammar.
// ClassifyFn, when set, overrides the default Terminal/TaskFailed
// classification; DriverName overrides the Strategy's Name.
type fakeDriver struct {
	ClassifyFn func(logPath string) (driver.Classification, error)
	// DriverName is the Driver name the stamp records; empty keeps the
	// Strategy's. Dispatch Records ingest the logs these tests stamp through the
	// registered Driver the stamp names, so a test that ingests registers it.
	DriverName string
	// SessionTotals sums KindResult events for the run totals instead of every
	// KindUsage, for tests asserting a session summary's API time, turns or wall
	// time, which per-call totals cannot express.
	SessionTotals bool
}

func (d fakeDriver) base() driverkit.Driver {
	s := testStrategy
	if d.DriverName != "" {
		s.Name = d.DriverName
	}
	if d.SessionTotals {
		s.Totals = driverkit.SessionTotals
	}
	return driverkit.New(s)
}

func (d fakeDriver) Name() string { return d.base().Name() }

func (d fakeDriver) ClassifyTransient(logPath string) (driver.Classification, error) {
	if d.ClassifyFn != nil {
		return d.ClassifyFn(logPath)
	}
	return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
}

func (d fakeDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	return raw
}

func (d fakeDriver) NewStreamFilter(w io.Writer) driverkit.StreamFilter {
	return d.base().NewStreamFilter(w)
}

func (d fakeDriver) ExtractUsage(logPath string) (usage.Report, error) {
	return d.base().ExtractUsage(logPath)
}

func (d fakeDriver) RenderTranscript(logPath string, opts driverkit.RenderOptions) (string, error) {
	return d.base().RenderTranscript(logPath, opts)
}

func (d fakeDriver) ResolveExit(logPath string, exitCode int) (int, error) {
	return d.base().ResolveExit(logPath, exitCode)
}

func (d fakeDriver) ResultText(logPath string) (string, error) {
	return d.base().ResultText(logPath)
}

func (d fakeDriver) ResultEvent(text string) ([]byte, error) {
	return d.base().ResultEvent(text)
}

func (d fakeDriver) ReadPasses(logPath, trackWrite string, visit func(driverkit.PassLine)) ([]driverkit.PassTally, error) {
	return d.base().ReadPasses(logPath, trackWrite, visit)
}

func (d fakeDriver) SessionFlags(mode, repoSlug, issue, home string) string {
	return d.base().SessionFlags(mode, repoSlug, issue, home)
}

// usageLine is one canonical per-call usage event (the seamtest Strategy's
// token tally); model may be empty.
func usageLine(model string, input, output int, costUSD float64) string {
	return eventLine(driverkit.Event{Kind: driverkit.KindUsage, Model: model, Tokens: driverkit.Tokens{Input: input, Output: output, CostUSD: costUSD}})
}

// eventLine is ev as one newline-terminated canonical log line, as the seamtest
// Strategy decodes; callers concatenate lines without adding separators.
func eventLine(ev driverkit.Event) string {
	b, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// ok reports whether d is a Succeeded disposition. Test-only: production code
// reaches the payload through Route.
func (d Disposition) ok() bool { return d.kind == dispositionSucceeded }

// skipped reports whether d is the Skipped disposition. Test-only.
func (d Disposition) skipped() bool { return d.kind == dispositionSkipped }
