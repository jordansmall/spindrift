package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/passmachine"
)

// Compare the native claude Strategy with the claude Connector under the
// launcher's interpreter and under the compiler (issues #6798, #6849). Not part
// of the gate; run on demand from cmd/launcher, the Connector rows skip while
// the wasm is unset:
//
//	SPINDRIFT_DRIVER_CONNECTOR_WASM=/path/claude.wasm \
//	  go test -run '^$' -bench 'Connector' -benchmem ./internal/driver/claude/
//
// A whole-log read of the dogfood corpus takes tens of seconds on the
// interpreter, so add -benchtime 1x for a smoke run, and select one family with
// -bench: Decode, Heartbeat, WholeLog, DecodeSize, FixedCalls, ShadowReplay.

// chunkSize is shorter than most corpus lines, so nearly every line arrives
// split across writes, as from a pipe.
const chunkSize = 97

// connectorEngines is an ordered slice, not a map, so sub-benchmark order is
// stable.
var connectorEngines = []struct {
	name string
	load func(context.Context, []byte) (*connectorhost.Connector, error)
}{
	{"interpreter", connectorhost.Load},
	{"compiler", func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
		return connectorhost.LoadCompiled(ctx, wasm, "")
	}},
}

// benchStrategies runs run against the native Strategy and against the
// Connector loaded under each engine. The Connector rows skip when the wasm is
// unset.
func benchStrategies(b *testing.B, run func(b *testing.B, s driverkit.Strategy)) {
	b.Run("native", func(b *testing.B) { run(b, claude.Strategy) })
	for _, e := range connectorEngines {
		b.Run(e.name, func(b *testing.B) {
			wasm, err := os.ReadFile(connectorWasm(b))
			if err != nil {
				b.Fatal(err)
			}
			c, err := e.load(context.Background(), wasm)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = c.Close(context.Background()) })
			// Cleanups run last-in first-out, so this one sees the Connector
			// before Close. A failed call kills it and every later line degrades
			// to unparsed, which is fast: a dead run would read as a speedup.
			b.Cleanup(func() {
				if err := c.Err(); err != nil {
					b.Errorf("Connector died during the run: %v", err)
				}
			})
			s, err := c.DriverStrategy()
			if err != nil {
				b.Fatal(err)
			}
			run(b, s)
		})
	}
}

// benchCorpora are the testdata directories the per-line rows run over: the
// #6798 corpus and the dogfood-sized one (issue #6849). The name is part of
// each row's path, so the #6798 rows stay selectable on their own.
var benchCorpora = []struct{ name, dir string }{
	{"6798", "corpus"},
	{"dogfood", "dogfood"},
}

// passLogPaths returns every pass log under testdata/<dir>, failing tb when
// there are none.
func passLogPaths(tb testing.TB, dir string) []string {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*", "pass.jsonl"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("%s pass logs: %v (found %d)", dir, err, len(paths))
	}
	return paths
}

// corpusLogs returns every pass log under testdata/<dir>, concatenated, and its
// non-empty lines.
func corpusLogs(b *testing.B, dir string) ([]byte, []string) {
	b.Helper()
	paths := passLogPaths(b, dir)
	var log []byte
	var lines []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			b.Fatal(err)
		}
		log = append(log, data...)
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) > 0 {
				lines = append(lines, string(l))
			}
		}
	}
	return log, lines
}

// BenchmarkConnectorDecode is the per-line decode cost: a fresh decoder over
// every corpus line.
func BenchmarkConnectorDecode(b *testing.B) {
	for _, c := range benchCorpora {
		b.Run("corpus="+c.name, func(b *testing.B) {
			log, lines := corpusLogs(b, c.dir)
			benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
				b.SetBytes(int64(len(log)))
				for b.Loop() {
					decode := s.NewDecoder()
					for _, line := range lines {
						decode(line)
					}
				}
				reportPerLine(b, len(lines))
			})
		})
	}
}

// BenchmarkConnectorHeartbeat is the live path: the corpus written to the
// Driver's heartbeat writer in fixed-size chunks, so line framing and the
// heartbeat's own rendering sit on top of the decode.
func BenchmarkConnectorHeartbeat(b *testing.B) {
	for _, c := range benchCorpora {
		b.Run("corpus="+c.name, func(b *testing.B) {
			log, lines := corpusLogs(b, c.dir)
			benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
				d := driverkit.New(s)
				opts := driverkit.RenderOptions{OnModel: func(string, string) {}}
				b.SetBytes(int64(len(log)))
				for b.Loop() {
					w := d.NewHeartbeatWriter(io.Discard, "1", io.Discard, opts)
					for rest := log; len(rest) > 0; {
						n := min(chunkSize, len(rest))
						if _, err := w.Write(rest[:n]); err != nil {
							b.Fatal(err)
						}
						rest = rest[n:]
					}
				}
				reportPerLine(b, len(lines))
			})
		})
	}
}

func reportPerLine(b *testing.B, lines int) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*lines), "ns/line")
}

// largestDogfoodLog is the biggest dogfood Pass log by size: the worst case for
// a whole-log read and for the shadow replay.
func largestDogfoodLog(b *testing.B) (path string, size int64) {
	b.Helper()
	for _, p := range passLogPaths(b, "dogfood") {
		fi, err := os.Stat(p)
		if err != nil {
			b.Fatal(err)
		}
		if fi.Size() > size {
			path, size = p, fi.Size()
		}
	}
	return path, size
}

// requireSameAsNative fails b when got, a Connector's answer, differs from
// native's. It compares the JSON encoding, as the shadow does: a Connector's
// empty slice is nil natively.
func requireSameAsNative(b *testing.B, what string, got, native any) {
	b.Helper()
	g, gerr := json.Marshal(got)
	n, nerr := json.Marshal(native)
	if gerr != nil || nerr != nil {
		b.Fatalf("%s: encode: %v, %v", what, gerr, nerr)
	}
	if !bytes.Equal(g, n) {
		b.Fatalf("%s differs from native:\n got: %.300s\n native: %.300s", what, g, n)
	}
}

// wholeLogReads is every whole-log read core makes of a finished Pass's log,
// plus the console's interactive transcript render. claude declares no
// ResolveExit, so that row makes no Connector call.
var wholeLogReads = []struct {
	name string
	read func(d driverkit.Driver, path string) (any, error)
}{
	{"ResolveExit", func(d driverkit.Driver, path string) (any, error) { return d.ResolveExit(path, 0) }},
	{"ResultText", func(d driverkit.Driver, path string) (any, error) { return d.ResultText(path) }},
	{"RenderTranscript", func(d driverkit.Driver, path string) (any, error) {
		return d.RenderTranscript(path, driverkit.RenderOptions{TopLevelRole: driverkit.ImplementorRole})
	}},
	{"ExtractUsageAfter", func(d driverkit.Driver, path string) (any, error) {
		r, after, err := d.ExtractUsageAfter(path, driverkit.SessionBaselines{})
		return []any{r, after}, err
	}},
	{"ClassifyTransient", func(d driverkit.Driver, path string) (any, error) { return d.ClassifyTransient(path) }},
	{"ReadPasses", func(d driverkit.Driver, path string) (any, error) {
		var lines []driverkit.PassLine
		tallies, err := d.ReadPasses(path, passmachine.DispositionsPath, driverkit.SessionBaselines{}, func(l driverkit.PassLine) {
			lines = append(lines, l)
		})
		return []any{tallies, lines}, err
	}},
	{"SessionBaselinesAfter", func(d driverkit.Driver, path string) (any, error) {
		return d.SessionBaselinesAfter(path, driverkit.SessionBaselines{})
	}},
}

// BenchmarkConnectorWholeLog is each whole-log read on the largest dogfood
// Pass. Every read opens its own decoder, so each pays a full decode of the log
// through the Connector.
func BenchmarkConnectorWholeLog(b *testing.B) {
	path, size := largestDogfoodLog(b)
	native := driverkit.New(claude.Strategy)
	for _, r := range wholeLogReads {
		b.Run(r.name, func(b *testing.B) {
			want, err := r.read(native, path)
			if err != nil {
				b.Fatalf("native: %v", err)
			}
			benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
				d := driverkit.New(s)
				// Before the loop, which starts the clock on its first call.
				got, err := r.read(d, path)
				if err != nil {
					b.Fatal(err)
				}
				requireSameAsNative(b, r.name, got, want)
				if r.name != "ResolveExit" { // reads nothing, so no throughput
					b.SetBytes(size)
				}
				for b.Loop() {
					if _, err := r.read(d, path); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// sizedLine returns a valid claude tool_result line of about n bytes, the shape
// that dominates the dogfood corpus.
func sizedLine(n int) string {
	line := func(pad int) string {
		rng := rand.New(rand.NewPCG(uint64(n), 1))
		return dogfoodMarshal(map[string]any{
			"type": "user", "uuid": dogfoodID(rng, ""), "session_id": "s", "parent_tool_use_id": nil,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "toolu_1", "content": dogfoodPad(rng, pad),
			}}},
		})
	}
	// Padding is sized before escaping and JSON overhead, so correct once off the
	// first pass's measured excess.
	return line(max(0, n-(len(line(n))-n)))
}

// BenchmarkConnectorDecodeSize is one driver.decode Call on a single line of
// growing size. Its small rows are the Call's fixed cost; ns/byte across the
// large rows is its per-byte cost. The two largest sizes are the dogfood tail.
func BenchmarkConnectorDecodeSize(b *testing.B) {
	for _, n := range []int{256, 1 << 10, 8 << 10, 64 << 10, 256 << 10, 850 << 10} {
		line := sizedLine(n)
		want := claude.Strategy.NewDecoder()(line)
		b.Run(fmt.Sprintf("size=%dB", len(line)), func(b *testing.B) {
			benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
				decode := s.NewDecoder()
				requireSameAsNative(b, "decode", decode(line), want)
				b.SetBytes(int64(len(line)))
				for b.Loop() {
					decode(line)
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(line)), "ns/byte")
			})
		})
	}
}

// BenchmarkConnectorFixedCalls is the size-free Connector calls core makes per
// Pass besides decode: NewDecoder (a driver.newDecoder Call per whole-log read,
// plus a driver.freeDecoder once the decoder is collected), SessionFlags at Box
// start, and ResultEvent at the end.
func BenchmarkConnectorFixedCalls(b *testing.B) {
	home := b.TempDir()
	calls := []struct {
		name string
		call func(s driverkit.Strategy) error
	}{
		{"NewDecoder", func(s driverkit.Strategy) error { s.NewDecoder(); return nil }},
		{"SessionFlags", func(s driverkit.Strategy) error {
			s.SessionFlags("initial", "jordansmall/spindrift", "6849", home)
			return nil
		}},
		{"ResultEvent", func(s driverkit.Strategy) error {
			_, err := s.ResultEvent("SPINDRIFT_OUTCOME issue=6849 landing=none status=reject")
			return err
		}},
	}
	for _, c := range calls {
		b.Run(c.name, func(b *testing.B) {
			benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
				for b.Loop() {
					if err := c.call(s); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// shadowBudget copies driver-exec's unexported bound on the whole shadow replay
// (cmd/launcher/driver-exec/main.go); keep the two in step.
const shadowBudget = 2 * time.Minute

// BenchmarkConnectorShadowReplay is shadow.Run, the launcher's interpreter
// replay, over the largest dogfood Pass. It runs without the budget's deadline
// so the wall time stays measurable past it: budget-frac above 1 means the
// replay would be cut off in production.
func BenchmarkConnectorShadowReplay(b *testing.B) {
	wasm, err := os.ReadFile(connectorWasm(b))
	if err != nil {
		b.Fatal(err)
	}
	path, size := largestDogfoodLog(b)
	b.SetBytes(size)
	var compared int
	for b.Loop() {
		var summary *hostop.ShadowSummary
		shadow.Run(context.Background(), claude.Strategy, wasm, path, 0, func(op hostop.SpindriftOp) {
			if op.Divergence != nil {
				b.Errorf("divergence at line %d (%s)", op.Divergence.LineNo, op.Divergence.Check)
			}
			if op.ShadowSummary != nil {
				summary = op.ShadowSummary
			}
		})
		if summary == nil || !summary.Loaded || summary.Error != "" || summary.Diverged != 0 {
			b.Fatalf("summary = %+v, want a loaded, error-free, divergence-free replay", summary)
		}
		compared = summary.Compared
	}
	wall := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(wall.Seconds(), "s/replay")
	b.ReportMetric(float64(wall)/float64(shadowBudget), "budget-frac")
	b.ReportMetric(float64(compared), "lines")
}

// BenchmarkConnectorLoad is the one-shot startup cost of each engine.
func BenchmarkConnectorLoad(b *testing.B) {
	wasm, err := os.ReadFile(connectorWasm(b))
	if err != nil {
		b.Fatal(err)
	}
	for _, e := range connectorEngines {
		b.Run(e.name, func(b *testing.B) {
			for b.Loop() {
				c, err := e.load(context.Background(), wasm)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := c.DriverStrategy(); err != nil {
					b.Fatal(err)
				}
				_ = c.Close(context.Background())
			}
		})
	}
}
