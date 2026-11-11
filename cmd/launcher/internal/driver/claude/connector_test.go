package claude_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/hostop"
)

const connectorWasmEnv = "SPINDRIFT_DRIVER_CONNECTOR_WASM"

// connectorWasm returns the compiled claude Connector's path, skipping t when
// it is unset.
func connectorWasm(t testing.TB) string {
	t.Helper()
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		t.Skipf("%s is unset; it names the compiled claude Connector", connectorWasmEnv)
	}
	return wasm
}

// The compiled Connector port of this Driver (connectors/claude) must pass the
// same corpus against the same goldens as the native Strategy.
func TestConnectorStrategyCorpus(t *testing.T) {
	connectorWasm(t) // skip before the goldens check when no Connector is built
	// conformance.Run would rewrite the native Strategy's goldens from the
	// Connector's output.
	if golden.Update() {
		t.Fatalf("refusing to update goldens from the Connector; unset UPDATE_GOLDENS or %s", connectorWasmEnv)
	}
	conformance.Run(t, loadConnectorStrategy(t), filepath.Join("testdata", "corpus"))
}

func loadConnectorStrategy(t *testing.T) driverkit.Strategy {
	t.Helper()
	wasm, err := os.ReadFile(connectorWasm(t))
	if err != nil {
		t.Fatal(err)
	}
	// Compiled: the corpus pushes many lines through one Connector.
	c, err := connectorhost.LoadCompiled(context.Background(), wasm, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The corpus exercises decoding and classification but not the Strategy's
// describe data, so compare each declarative field the Connector reports with
// the native Strategy's.
func TestConnectorDescribe(t *testing.T) {
	conn := loadConnectorStrategy(t)
	native := claude.Strategy

	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"Name", conn.Name, native.Name},
		{"TransientPatterns", conn.TransientPatterns, native.TransientPatterns},
		{"TerminalPatterns", conn.TerminalPatterns, native.TerminalPatterns},
		{"Classify", conn.Classify, native.Classify},
		{"Totals", conn.Totals, native.Totals},
		{"Result", conn.Result, native.Result},
		{"DefaultTopLevelRole", conn.DefaultTopLevelRole, native.DefaultTopLevelRole},
		{"WriteTool", conn.WriteTool, native.WriteTool},
		{"Heartbeat.KindOrder", conn.Heartbeat.KindOrder, native.Heartbeat.KindOrder},
		{"Heartbeat.KindPlurals", conn.Heartbeat.KindPlurals, native.Heartbeat.KindPlurals},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v; want %#v", c.name, c.got, c.want)
		}
	}
}

// The corpus never calls SessionFlags, so pin the Connector's hand-copied
// session-id derivation against the native Strategy here.
func TestConnectorSessionFlags(t *testing.T) {
	conn := loadConnectorStrategy(t)
	home := t.TempDir()

	for _, c := range []struct{ slug, issue string }{
		{"owner/repo", "7"},
		{"owner/repo", "8"},
		{"other/repo", "7"},
		{"jordansmall/spindrift", "6795"},
		{"", ""},
	} {
		want := claude.Strategy.SessionFlags("initial", c.slug, c.issue, home)
		if !strings.HasPrefix(want, "--session-id ") {
			t.Fatalf("native initial = %q", want)
		}
		if got := conn.SessionFlags("initial", c.slug, c.issue, home); got != want {
			t.Errorf("initial %q#%q = %q; want %q", c.slug, c.issue, got, want)
		}
	}

	// A Connector cannot see home, so it never renders --resume; the native
	// Strategy does once the transcript exists. Deliberate divergence.
	id := strings.TrimPrefix(claude.Strategy.SessionFlags("initial", "owner/repo", "7", home), "--session-id ")
	proj := filepath.Join(home, ".claude", "projects", "x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := conn.SessionFlags("resume", "owner/repo", "7", home); got != "" {
		t.Errorf("connector resume = %q; want empty", got)
	}

	for _, mode := range []string{"", "fix", "bogus"} {
		if got := conn.SessionFlags(mode, "owner/repo", "7", home); got != "" {
			t.Errorf("connector mode %q = %q; want empty", mode, got)
		}
	}
}

func TestConnectorResultEvent(t *testing.T) {
	conn := loadConnectorStrategy(t)
	for _, text := range []string{
		"done",
		"",
		"say \"hi\"\\ then\nnewline\ttab",
		"héllo wörld ☃ \U0001F600",
		"<b>a & b</b>",
	} {
		want, wantErr := claude.Strategy.ResultEvent(text)
		got, gotErr := conn.ResultEvent(text)
		if (gotErr == nil) != (wantErr == nil) {
			t.Errorf("ResultEvent(%q) err = %v; native err = %v", text, gotErr, wantErr)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ResultEvent(%q) = %q; want %q", text, got, want)
		}
	}
}

// A shadow run of the real Connector over every corpus Pass log is the clean
// baseline: a divergence here is a bug in the port, and would be noise in a
// live shadow run.
func TestConnectorShadowRunIsClean(t *testing.T) {
	wasm, err := os.ReadFile(connectorWasm(t))
	if err != nil {
		t.Fatal(err)
	}
	logs, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("corpus Pass logs = %v, err = %v", logs, err)
	}
	for _, log := range logs {
		t.Run(filepath.Base(filepath.Dir(log)), func(t *testing.T) {
			var summary *hostop.ShadowSummary
			shadow.Run(context.Background(), claude.Strategy, wasm, log, 0, func(op hostop.SpindriftOp) {
				switch {
				case op.Divergence != nil:
					d := op.Divergence
					t.Errorf("%s divergence at line %d: %s\n native: %s\n shadow: %s", d.Check, d.LineNo, d.Line, d.Native, d.Shadow)
				case op.ShadowSummary != nil:
					summary = op.ShadowSummary
				}
			})
			if summary == nil || !summary.Loaded || summary.Error != "" || summary.Compared == 0 {
				t.Errorf("summary = %+v, want a loaded, error-free run that compared lines", summary)
			}
		})
	}
}
