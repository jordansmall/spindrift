package pi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/pi"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/shadowrecord"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

// shadowCase writes what a shadowed Pass leaves behind for one stream-filter
// fixture: the raw stdout copy and the Pass log, which is the native filter's
// actual output over it.
func shadowCase(t *testing.T, name string) (raw, log string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "streamfilter", name, "stdout.raw"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	raw, log = filepath.Join(dir, "stdout.raw"), filepath.Join(dir, "pass.log")
	if err := os.WriteFile(raw, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return raw, log
}

func shadowRun(t *testing.T, wasm []byte, log, raw string) ([]hostop.ShadowDivergence, hostop.ShadowSummary) {
	t.Helper()
	var ops []hostop.SpindriftOp
	shadow.Run(context.Background(), pi.Strategy, connectorhost.Load, wasm, log, raw, 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
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

// driver.wasm is not pi, so what it decodes and what its filter writes differ
// from the native pi Strategy's: decode and filter divergences come out of one
// replay, and the recorded native side is what the native Strategy produced.
func TestDivergentConnectorIsReportedForDecodeAndFilter(t *testing.T) {
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "driver.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	raw, log := shadowCase(t, "held-deltas-flushed-before-next-assistant-message")
	lines := strings.Split(strings.TrimRight(string(mustRead(t, log)), "\n"), "\n")
	divs, sum := shadowRun(t, wasm, log, raw)

	if sum.Driver != "pi" || !sum.Loaded || sum.Error != "" || sum.Compared != len(lines) {
		t.Fatalf("summary = %+v, want a clean pi load comparing %d lines", sum, len(lines))
	}
	if !sum.FilterRan || !sum.FilterDiverged || sum.FilterError != "" || sum.FilterBytes != int64(len(mustRead(t, raw))) {
		t.Errorf("summary = %+v, want the filter shadow ran and diverged over the raw copy", sum)
	}
	dec := pi.NewDecoder()
	var events, filter int
	for _, d := range divs {
		switch d.Check {
		case "events":
			events++
			if d.LineNo < 1 || d.LineNo > len(lines) || d.Line != lines[d.LineNo-1] {
				t.Fatalf("divergence at line %d carries %q, want that log line", d.LineNo, d.Line)
			}
			if want := shadowrecord.EncodeJSON(dec(d.Line)); !bytes.Equal(d.Native, want) {
				t.Errorf("line %d: recorded native %s, native Strategy decodes %s", d.LineNo, d.Native, want)
			}
		case "streamFilter":
			filter++
			var nativeRegion, shadowRegion string
			if err := json.Unmarshal(d.Native, &nativeRegion); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(d.Shadow, &shadowRegion); err != nil {
				t.Fatal(err)
			}
			if d.LineNo < 1 || nativeRegion == "" || shadowRegion == "" || nativeRegion == shadowRegion {
				t.Errorf("streamFilter divergence = %+v, want a line and two differing regions", d)
			}
		}
	}
	if events == 0 || filter != 1 {
		t.Errorf("events = %d, streamFilter = %d divergences, want some and exactly one", events, filter)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The compiled pi Connector's filter must write what the native filter wrote,
// on every raw stdout the filter fixtures hold.
func TestRealConnectorFilterShadowsCleanly(t *testing.T) {
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		t.Skipf("%s is unset; it names the compiled pi Connector", connectorWasmEnv)
	}
	if name := os.Getenv(connectorNameEnv); name != "" && name != "pi" {
		t.Skipf("%s=%s: the wasm is not the pi Connector", connectorNameEnv, name)
	}
	wasm := mustRead(t, path)
	cases, err := filepath.Glob(filepath.Join("testdata", "streamfilter", "*", "stdout.raw"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no stream filter fixtures: %v", err)
	}
	for _, c := range cases {
		name := filepath.Base(filepath.Dir(c))
		t.Run(name, func(t *testing.T) {
			raw, log := shadowCase(t, name)
			divs, sum := shadowRun(t, wasm, log, raw)
			if !sum.FilterRan || sum.FilterDiverged || sum.FilterError != "" {
				t.Errorf("summary = %+v, want a clean filter shadow", sum)
			}
			for _, d := range divs {
				if d.Check == "streamFilter" {
					t.Errorf("streamFilter divergence: %+v", d)
				}
			}
		})
	}
}
