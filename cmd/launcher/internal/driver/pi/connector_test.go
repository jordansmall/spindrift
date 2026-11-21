package pi_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
	"spindrift.dev/launcher/internal/golden"
)

const (
	connectorWasmEnv = "SPINDRIFT_DRIVER_CONNECTOR_WASM"
	connectorNameEnv = "SPINDRIFT_DRIVER_CONNECTOR_NAME"
)

// loadConnectorStrategy loads the compiled pi Connector named by connectorWasmEnv
// with load and returns its Strategy, skipping when the env does not name one.
func loadConnectorStrategy(tb testing.TB, load func(context.Context, []byte) (*connectorhost.Connector, error)) driverkit.Strategy {
	tb.Helper()
	path := os.Getenv(connectorWasmEnv)
	if path == "" {
		tb.Skipf("%s is unset; it names the compiled pi Connector", connectorWasmEnv)
	}
	if name := os.Getenv(connectorNameEnv); name != "" && name != "pi" {
		tb.Skipf("%s=%s: the wasm is not the pi Connector", connectorNameEnv, name)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	c, err := load(context.Background(), wasm)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = c.Close(context.Background()) })
	// A failed call kills the Connector and every later write fails fast, which
	// would read as a speedup in the benchmark and a pass in a hold-back test.
	tb.Cleanup(func() {
		if err := c.Err(); err != nil {
			tb.Errorf("Connector died during the run: %v", err)
		}
	})
	s, err := c.DriverStrategy()
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

func loadCompiledConnectorStrategy(tb testing.TB) driverkit.Strategy {
	tb.Helper()
	return loadConnectorStrategy(tb, func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
		return connectorhost.LoadCompiled(ctx, wasm, "")
	})
}

// The compiled Connector's stream filter, through the host adapter, must hold
// back before Flush as native does: complete lines forward as they arrive, a
// trailing partial line and every subagent update wait for Flush.
func TestConnectorStreamFilterHoldsUntilFlush(t *testing.T) {
	for _, row := range []struct {
		name string
		s    func(testing.TB) driverkit.Strategy
	}{
		{"native", func(testing.TB) driverkit.Strategy { return pi.Strategy }},
		{"connector", loadCompiledConnectorStrategy},
	} {
		t.Run(row.name, func(t *testing.T) {
			s := row.s(t)
			t.Run("partial-line", func(t *testing.T) {
				var out bytes.Buffer
				f := s.NewStreamFilter(&out)
				for _, chunk := range []string{"not json\n", `{"type":"agent_`, `end"}` + "\n", `{"type":"tru`} {
					if _, err := f.Write([]byte(chunk)); err != nil {
						t.Fatal(err)
					}
				}
				if want := "not json\n{\"type\":\"agent_end\"}\n"; out.String() != want {
					t.Errorf("before Flush = %q, want %q", out.String(), want)
				}
				if err := f.Flush(); err != nil {
					t.Fatal(err)
				}
				if want := "not json\n{\"type\":\"agent_end\"}\n{\"type\":\"tru"; out.String() != want {
					t.Errorf("after Flush = %q, want %q", out.String(), want)
				}
			})
			t.Run("subagent-updates-held", func(t *testing.T) {
				var out bytes.Buffer
				f := s.NewStreamFilter(&out)
				other := `{"type":"tool_execution_update","toolCallId":"b1","toolName":"bash","partialResult":{}}`
				latestA := subagentUpdateFor("a", "a-2")
				latestB := subagentUpdateFor("b", "b-3")
				writeLines(t, f,
					subagentUpdateFor("a", "a-1"),
					subagentUpdateFor("b", "b-1"),
					other,
					latestA,
					subagentUpdateFor("b", "b-2"),
					latestB,
				)
				if got := out.String(); got != other+"\n" {
					t.Fatalf("before Flush = %q, want only the non-subagent update", got)
				}
				if err := f.Flush(); err != nil {
					t.Fatal(err)
				}
				if want := other + "\n" + latestA + "\n" + latestB + "\n"; out.String() != want {
					t.Errorf("after Flush =\n%s\nwant\n%s", out.String(), want)
				}
			})
		})
	}
}

// Sizes are set against connectorhost's maxRequest (8 MiB), the most one
// resolveExit call carries: the host sends the Connector only the newest lines
// that fit.
const (
	// overRequest exceeds maxRequest, so the window drops the oldest filler.
	overRequest = 9 << 20
	// midLine is between native logscan's 4 MiB line buffer and the host's own
	// oversize skip (~8 MiB): the host forwards such a line, so the Connector must
	// skip it as native does.
	midLine = 4<<20 + 1<<10
	// fillerLine is a filler line small enough for the window to keep or drop whole.
	fillerLine = 1 << 20
)

// Exit resolution through the compiled Connector port (connectors/pi) must
// agree with the native Strategy on every corpus log, including logs larger than
// the host's per-request limit and a line native skips but the host forwards.
func TestConnectorResolveExitMatchesNative(t *testing.T) {
	s := loadCompiledConnectorStrategy(t)
	if golden.Update() {
		t.Fatalf("refusing to run against the Connector with UPDATE_GOLDENS set; unset it or %s", connectorWasmEnv)
	}
	conn, native := driverkit.New(s), driverkit.New(pi.Strategy)

	logs, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("no corpus log: %v", err)
	}
	// Not JSON, so neutral to every resolver: neither an error nor progress.
	line := func(n int) []byte { return append(bytes.Repeat([]byte("x"), n-1), '\n') }
	var filler []byte
	for len(filler) < overRequest {
		filler = append(filler, line(fillerLine)...)
	}
	var midError []byte
	midError = append(midError, `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"400: `...)
	midError = append(midError, bytes.Repeat([]byte("x"), midLine)...)
	midError = append(midError, "\",\"timestamp\":1}}\n"...)

	// resolve writes log to a file and resolves it with exit code 0.
	resolve := func(t *testing.T, d driverkit.Driver, log []byte) int {
		t.Helper()
		p := filepath.Join(t.TempDir(), "pass.log")
		if err := os.WriteFile(p, log, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := d.ResolveExit(p, 0)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// The wasm costs seconds per MiB of request, so only the cheap variants run on
	// every log; the ones that send ~8 MiB run on one log of each outcome.
	var quiet, loud []byte
	for _, p := range logs {
		corpus, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			agree := func(variant string, log []byte) (want int) {
				t.Helper()
				want = resolve(t, native, log)
				if got := resolve(t, conn, log); got != want {
					t.Errorf("%s: ResolveExit(0) = %d through the Connector, native %d", variant, got, want)
				}
				return want
			}
			natural := agree("natural", corpus)
			// One line over maxRequest: the host skips it whole and sends the corpus log.
			agree("oversized-prefix", append(line(overRequest), corpus...))
			switch want := natural; {
			case want == 0 && quiet == nil:
				quiet = corpus
			case want != 0 && loud == nil:
				loud = corpus
			}
		})
	}
	if quiet == nil || loud == nil {
		t.Fatalf("corpus lacks a log native resolves to 0 (%v) or non-zero (%v)", quiet != nil, loud != nil)
	}
	// The window drops the oldest filler; a lost corpus log would resolve differently.
	// Agreement is bounded: an unrecovered error older than the host's ~8 MiB
	// window followed by >8 MiB of non-progress output resolves 0 here but 1
	// natively, a limit of the host sending only the tail (connectorhost logWindow).
	t.Run("windowed", func(t *testing.T) {
		log := append(append([]byte{}, filler...), loud...)
		if want, got := resolve(t, native, log), resolve(t, conn, log); got != want {
			t.Errorf("ResolveExit(0) = %d through the Connector, native %d", got, want)
		}
	})
	// The error line is newest, so counting it would flip the quiet log to 1.
	t.Run("windowed-mid-size-error", func(t *testing.T) {
		log := append(append(append([]byte{}, filler[:5*fillerLine]...), quiet...), midError...)
		if want, got := resolve(t, native, log), resolve(t, conn, log); got != want {
			t.Errorf("ResolveExit(0) = %d through the Connector, native %d", got, want)
		}
	})
}
