package shadow

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/shadowrecord"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// passLog writes a Pass log of n claude stream-json lines and returns its path.
// It is generated rather than read from the claude corpus, which the core
// go-test derivation does not carry.
func passLog(t *testing.T, n int) string {
	t.Helper()
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5-5"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`,
	}
	var b strings.Builder
	for i := range n {
		b.WriteString(lines[i%len(lines)] + "\n")
	}
	path := filepath.Join(t.TempDir(), "pass.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("\n"))
}

// replay runs Run and splits what it emitted into divergences and the summary,
// failing unless the summary came exactly once, last.
func replay(t *testing.T, native driverkit.Strategy, wasm []byte, logPath string, exitCode int) ([]hostop.ShadowDivergence, hostop.ShadowSummary) {
	t.Helper()
	var ops []hostop.SpindriftOp
	Run(context.Background(), native, wasm, logPath, exitCode, func(op hostop.SpindriftOp) { ops = append(ops, op) })
	return split(t, ops)
}

func split(t *testing.T, ops []hostop.SpindriftOp) ([]hostop.ShadowDivergence, hostop.ShadowSummary) {
	t.Helper()
	if len(ops) == 0 || ops[len(ops)-1].Op != hostop.OpShadowSummary || ops[len(ops)-1].ShadowSummary == nil {
		t.Fatalf("last op is not a shadow_summary: %+v", ops)
	}
	var divs []hostop.ShadowDivergence
	for _, op := range ops[:len(ops)-1] {
		if op.Op != hostop.OpShadowDivergence || op.Divergence == nil {
			t.Fatalf("op before the summary is not a shadow_divergence: %+v", op)
		}
		divs = append(divs, *op.Divergence)
	}
	return divs, *ops[len(ops)-1].ShadowSummary
}

// driver.wasm is not claude, so every claude line it decodes differs: it is the
// deliberately divergent Connector.
func TestDivergentConnectorIsReportedWithLineAndBothResults(t *testing.T) {
	log := passLog(t, 20)
	divs, sum := replay(t, nativeStrategy(), fixture(t, "driver.wasm"), log, 0)

	if !sum.Loaded || sum.Error != "" {
		t.Fatalf("summary = %+v, want a clean load", sum)
	}
	if sum.Compared != lineCount(t, log) {
		t.Errorf("Compared = %d, want %d", sum.Compared, lineCount(t, log))
	}
	if sum.Diverged == 0 || sum.Recorded != len(divs) || sum.Diverged < sum.Recorded {
		t.Errorf("Diverged = %d, Recorded = %d, emitted %d", sum.Diverged, sum.Recorded, len(divs))
	}
	if sum.NativeNsPerLine <= 0 || sum.ShadowNsPerLine <= 0 {
		t.Errorf("ns per line = %d native, %d shadow, want both populated", sum.NativeNsPerLine, sum.ShadowNsPerLine)
	}

	var first *hostop.ShadowDivergence
	checks := map[string]bool{}
	for i, d := range divs {
		checks[d.Check] = true
		if d.Driver != "native" {
			t.Errorf("divergence driver = %q, want native", d.Driver)
		}
		if d.Check == "events" && first == nil {
			first = &divs[i]
		}
	}
	for _, c := range []string{"events", "resolveExit", "resultEvent"} {
		if !checks[c] {
			t.Errorf("no %s divergence; got checks %v", c, checks)
		}
	}
	if first == nil || first.LineNo == 0 || first.Line == "" {
		t.Fatalf("first events divergence = %v, want a line number and its text", first != nil)
	}
	if len(first.Native) == 0 || len(first.Shadow) == 0 || bytes.Equal(first.Native, first.Shadow) {
		t.Errorf("divergence results native=%s shadow=%s, want two differing results", first.Native, first.Shadow)
	}
}

func TestDivergencesAreCappedButAllCounted(t *testing.T) {
	log := passLog(t, shadowrecord.MaxRecorded+50)
	divs, sum := replay(t, nativeStrategy(), fixture(t, "driver.wasm"), log, 0)
	if len(divs) != shadowrecord.MaxRecorded || sum.Recorded != shadowrecord.MaxRecorded {
		t.Errorf("emitted %d, Recorded = %d, want %d", len(divs), sum.Recorded, shadowrecord.MaxRecorded)
	}
	if sum.Diverged <= shadowrecord.MaxRecorded {
		t.Errorf("Diverged = %d, want more than the cap %d", sum.Diverged, shadowrecord.MaxRecorded)
	}
}

func TestUnloadableConnectorYieldsOnlyASummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		wasm func(t *testing.T) []byte
	}{
		{"garbage", func(*testing.T) []byte { return []byte("not wasm") }},
		{"empty", func(*testing.T) []byte { return nil }},
		// Loads and handshakes, but advertises no "driver" capability.
		{"not a driver", func(t *testing.T) []byte { return fixture(t, "current.wasm") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			divs, sum := replay(t, nativeStrategy(), tc.wasm(t), passLog(t, 20), 0)
			if len(divs) != 0 || sum.Loaded || sum.Error == "" || sum.Compared != 0 {
				t.Errorf("divs = %v, summary = %+v, want Loaded false with an Error and nothing compared", divs, sum)
			}
		})
	}
}

// A Connector that fails closed mid-run is reported as a summary with what was
// compared so far; the failing line is not a divergence. The fixtures have no
// Driver whose decode traps, so the failure is injected through the seam
// Run gives compare.
func TestConnectorFailingMidRunStopsTheReplay(t *testing.T) {
	log := passLog(t, 20)
	var failure error
	shadow := nativeStrategy()
	shadow.NewDecoder = func() driverkit.Decoder {
		inner, n := nativeStrategy().NewDecoder(), 0
		return func(line string) []driverkit.Event {
			if n++; n == 4 {
				failure = errors.New("connector trapped")
				return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
			}
			return inner(line)
		}
	}
	var ops []hostop.SpindriftOp
	r := &run{native: nativeStrategy(), Recorder: shadowrecord.New("native", func(op hostop.SpindriftOp) { ops = append(ops, op) })}
	r.Sum.Loaded = true
	r.compare(context.Background(), shadow, func() error { return failure }, log, 0)
	r.finish()

	divs, sum := split(t, ops)
	if len(divs) != 0 {
		t.Errorf("divergences = %v, want none: the failing line and the end checks are not compared", divs)
	}
	if !sum.Loaded || sum.Error != "connector trapped" || sum.Compared != 3 {
		t.Errorf("summary = %+v, want Loaded, Error set and 3 lines compared", sum)
	}
}

// A shadow Connector whose every decode is slow, though inside the host's
// per-call budget, must not stretch the replay past the overall deadline: the
// run ends as a summary error naming it.
func TestDeadlineEndsTheReplayWithASummaryError(t *testing.T) {
	log := passLog(t, 1000)
	shadow := nativeStrategy()
	shadow.NewDecoder = func() driverkit.Decoder {
		inner := nativeStrategy().NewDecoder()
		return func(line string) []driverkit.Event {
			time.Sleep(20 * time.Millisecond)
			return inner(line)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var ops []hostop.SpindriftOp
	r := &run{native: nativeStrategy(), Recorder: shadowrecord.New("native", func(op hostop.SpindriftOp) { ops = append(ops, op) })}
	r.Sum.Loaded = true
	start := time.Now()
	r.compare(ctx, shadow, func() error { return nil }, log, 0)
	r.finish()

	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("replay took %v, want it cut off near the 150ms deadline", d)
	}
	divs, sum := split(t, ops)
	if len(divs) != 0 || !sum.Loaded || sum.Compared == 0 || sum.Compared >= 1000 ||
		!strings.Contains(sum.Error, "deadline exceeded after") {
		t.Errorf("summary = %+v, divergences = %v, want a deadline Error after some but not all lines", sum, divs)
	}
}

// A real Connector aborted mid-replay, here by its context ending as soon as the
// first divergence is emitted, reports the deadline, not the trap the abort
// surfaces as, and the replay returns without waiting out a call budget.
func TestRealConnectorAbortedMidRunReportsTheDeadline(t *testing.T) {
	const lines = 3000
	log := passLog(t, lines)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ops []hostop.SpindriftOp
	start := time.Now()
	// driver.wasm diverges on every claude line, so the first divergence lands mid-run.
	Run(ctx, nativeStrategy(), fixture(t, "driver.wasm"), log, 0, func(op hostop.SpindriftOp) {
		ops = append(ops, op)
		cancel()
	})
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("Run took %v after the abort, want well inside the 5s call budget", d)
	}
	_, sum := split(t, ops)
	if !sum.Loaded || sum.Compared == 0 || sum.Compared >= lines ||
		!strings.Contains(sum.Error, "deadline exceeded after") || strings.Contains(sum.Error, "trap") {
		t.Errorf("summary = %+v, want Loaded and a deadline Error with only part of the log compared", sum)
	}
}

// The native result reader failing leaves both sides encoding an empty string,
// which would agree; it must be an error instead of a clean run.
func TestNativeReadResultErrorIsASummaryError(t *testing.T) {
	log := passLog(t, 6)
	native := nativeStrategy()
	native.NewDecoder = func() driverkit.Decoder {
		inner, n := nativeStrategy().NewDecoder(), 0
		return func(line string) []driverkit.Event {
			if n++; n == 6 {
				// The scan has the file open; ReadResult then finds a directory.
				_ = os.Remove(log)
				_ = os.Mkdir(log, 0o700)
			}
			return inner(line)
		}
	}
	var ops []hostop.SpindriftOp
	r := &run{native: native, Recorder: shadowrecord.New("", func(op hostop.SpindriftOp) { ops = append(ops, op) })}
	r.Sum.Loaded = true
	r.compare(context.Background(), nativeStrategy(), func() error { return nil }, log, 0)
	r.finish()

	_, sum := split(t, ops)
	if !strings.Contains(sum.Error, "read result") {
		t.Errorf("summary = %+v, want the read failure in Error", sum)
	}
}

func TestPanicBecomesSummaryError(t *testing.T) {
	native := nativeStrategy()
	native.NewDecoder = func() driverkit.Decoder { panic("boom") }
	_, sum := replay(t, native, fixture(t, "driver.wasm"), passLog(t, 20), 0)
	if !sum.Loaded || !strings.Contains(sum.Error, "panic: boom") {
		t.Errorf("summary = %+v, want the panic in Error", sum)
	}
}

// A Connector's clock is a fixed epoch, so only whether a reset time was
// resolved can be compared, not its value.
func TestEventsJSONComparesResetAtPresenceOnly(t *testing.T) {
	at := func(year int) *time.Time { v := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); return &v }
	ev := func(r *time.Time) []driverkit.Event {
		return []driverkit.Event{{Kind: driverkit.KindError, ResetAt: r}}
	}
	if !bytes.Equal(eventsJSON(ev(at(2026))), eventsJSON(ev(at(1970)))) {
		t.Error("two different reset times compared unequal")
	}
	if bytes.Equal(eventsJSON(ev(at(2026))), eventsJSON(ev(nil))) {
		t.Error("a resolved and an unresolved reset time compared equal")
	}
}

// nativeStrategy is a stand-in for a native Driver: a library package's tests
// may not reach a real one (see bundle_guard_test.go). It decodes every
// non-blank line to one text event and encodes a result as a result event.
func nativeStrategy() driverkit.Strategy {
	return driverkit.Strategy{
		Name: "native",
		NewDecoder: func() driverkit.Decoder {
			return func(line string) []driverkit.Event {
				if strings.TrimSpace(line) == "" {
					return nil
				}
				return []driverkit.Event{{Kind: driverkit.KindText, Text: line}}
			}
		},
		ResultEvent: func(text string) ([]byte, error) { return []byte("native " + text + "\n"), nil },
	}
}
