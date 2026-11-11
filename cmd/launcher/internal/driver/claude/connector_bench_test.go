package claude_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// Compare the native claude Strategy with the claude Connector under the
// launcher's interpreter and under the compiler (issue #6798). Not part of the
// gate; run on demand from cmd/launcher, the Connector rows skip while the wasm
// is unset:
//
//	SPINDRIFT_DRIVER_CONNECTOR_WASM=/path/claude.wasm \
//	  go test -run '^$' -bench 'Connector' -benchmem ./internal/driver/claude/

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
			s, err := c.DriverStrategy()
			if err != nil {
				b.Fatal(err)
			}
			run(b, s)
		})
	}
}

// corpusLogs returns every corpus pass log, concatenated, and its non-empty
// lines.
func corpusLogs(b *testing.B) ([]byte, []string) {
	b.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(paths) == 0 {
		b.Fatalf("corpus pass logs: %v (found %d)", err, len(paths))
	}
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
	_, lines := corpusLogs(b)
	benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
		for b.Loop() {
			decode := s.NewDecoder()
			for _, line := range lines {
				decode(line)
			}
		}
		reportPerLine(b, len(lines))
	})
}

// BenchmarkConnectorHeartbeat is the live path: the corpus written to the
// Driver's heartbeat writer in fixed-size chunks, so line framing and the
// heartbeat's own rendering sit on top of the decode.
func BenchmarkConnectorHeartbeat(b *testing.B) {
	log, lines := corpusLogs(b)
	benchStrategies(b, func(b *testing.B, s driverkit.Strategy) {
		d := driverkit.New(s)
		opts := driverkit.RenderOptions{OnModel: func(string, string) {}}
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
}

func reportPerLine(b *testing.B, lines int) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*lines), "ns/line")
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
