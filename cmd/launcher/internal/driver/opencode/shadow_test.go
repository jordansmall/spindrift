package opencode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/shadowrecord"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func corpusLogs(t *testing.T) []string {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no corpus log: %v", err)
	}
	return logs
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// shadowRun replays logPath with opencode.Strategy as the native side and splits
// what Run emitted into divergences and the one closing summary.
func shadowRun(t *testing.T, wasm []byte, logPath string) ([]hostop.ShadowDivergence, hostop.ShadowSummary) {
	t.Helper()
	var ops []hostop.SpindriftOp
	shadow.Run(context.Background(), opencode.Strategy, connectorhost.Load, wasm, logPath, 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
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

// driver.wasm is not opencode, so what it decodes differs from the native
// opencode Strategy's: the deliberately divergent Connector. The recorded
// native side must be what the native Strategy decodes.
func TestDivergentConnectorIsReportedWithLineAndBothResults(t *testing.T) {
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "driver.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range corpusLogs(t) {
		t.Run(filepath.Base(filepath.Dir(log)), func(t *testing.T) {
			lines := readLines(t, log)
			divs, sum := shadowRun(t, wasm, log)
			if sum.Driver != "opencode" || !sum.Loaded || sum.Error != "" || sum.Compared != len(lines) {
				t.Fatalf("summary = %+v, want a clean opencode load comparing %d lines", sum, len(lines))
			}
			if sum.Diverged == 0 || sum.Recorded != len(divs) || sum.Diverged < sum.Recorded {
				t.Fatalf("Diverged = %d, Recorded = %d, emitted %d", sum.Diverged, sum.Recorded, len(divs))
			}
			if sum.NativeNsPerLine <= 0 || sum.ShadowNsPerLine <= 0 {
				t.Errorf("ns per line = %d native, %d shadow, want both populated", sum.NativeNsPerLine, sum.ShadowNsPerLine)
			}
			dec := opencode.Strategy.NewDecoder()
			native := make([]json.RawMessage, len(lines))
			for i, l := range lines {
				native[i] = shadowrecord.EncodeJSON(dec(l))
			}
			checks := map[string]bool{}
			for _, d := range divs {
				checks[d.Check] = true
				if d.Driver != "opencode" {
					t.Errorf("divergence driver = %q, want opencode", d.Driver)
				}
				if len(d.Native) == 0 || len(d.Shadow) == 0 || bytes.Equal(d.Native, d.Shadow) {
					t.Errorf("%s: native=%s shadow=%s, want two differing results", d.Check, d.Native, d.Shadow)
				}
				// resolveExit and resultEvent compare whole-log results, so
				// only events divergences carry a log line.
				if d.Check != "events" {
					continue
				}
				if d.LineNo < 1 || d.LineNo > len(lines) || d.Line != lines[d.LineNo-1] {
					t.Fatalf("divergence at line %d carries %q, want that log line", d.LineNo, d.Line)
				}
				if !bytes.Equal(d.Native, native[d.LineNo-1]) {
					t.Errorf("line %d: recorded native %s, native Strategy decodes %s", d.LineNo, d.Native, native[d.LineNo-1])
				}
			}
			// A log whose events divergences overflow the cap records no
			// resolveExit or resultEvent one, though both are counted.
			for _, c := range []string{"events", "resolveExit", "resultEvent"} {
				if !checks[c] && (c == "events" || sum.Diverged <= shadowrecord.MaxRecorded) {
					t.Errorf("no %s divergence; got checks %v", c, checks)
				}
			}
		})
	}
}

// The compiled opencode Connector must replay every corpus log without a single
// divergence from the native Strategy.
func TestRealConnectorShadowsCleanly(t *testing.T) {
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s is unset; it names the compiled opencode Connector", connectorWasmEnv)
	}
	if name := os.Getenv(connectorNameEnv); name != "" && name != "opencode" {
		t.Skipf("%s=%s: the wasm is not the opencode Connector", connectorNameEnv, name)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range corpusLogs(t) {
		t.Run(filepath.Base(filepath.Dir(log)), func(t *testing.T) {
			divs, sum := shadowRun(t, wasm, log)
			if sum.Driver != "opencode" || !sum.Loaded || sum.Error != "" || sum.Compared != len(readLines(t, log)) {
				t.Fatalf("summary = %+v, want a clean opencode load comparing every line", sum)
			}
			if len(divs) != 0 || sum.Diverged != 0 {
				t.Errorf("Diverged = %d, records %+v, want none", sum.Diverged, divs)
			}
		})
	}
}
