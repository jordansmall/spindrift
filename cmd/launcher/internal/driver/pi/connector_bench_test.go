package pi_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

const (
	// driverExecReadMax is the most one read hands the filter. driver-exec sets
	// cmd.Stdout to a non-*os.File writer (driver-exec/run.go, runOnce), so
	// os/exec copies the Driver's private stdout pipe with io.Copy's 32 KiB
	// buffer; a read returns what the pipe holds, up to that.
	driverExecReadMax = 32 * 1024
)

// benchChunkings are how a stream is cut into Writes. A live pi flushes one
// JSONL event per write, so a read usually returns one line ("line"); a
// backed-up pipe returns a full buffer ("32KiB").
var benchChunkings = []struct {
	name  string
	split func(in []byte) [][]byte
}{
	{"line", func(in []byte) [][]byte {
		var out [][]byte
		for len(in) > 0 {
			n := bytes.IndexByte(in, '\n') + 1
			if n == 0 {
				n = len(in)
			}
			n = min(n, driverExecReadMax)
			out, in = append(out, in[:n]), in[n:]
		}
		return out
	}},
	{"32KiB", func(in []byte) [][]byte {
		var out [][]byte
		for len(in) > 0 {
			n := min(len(in), driverExecReadMax)
			out, in = append(out, in[:n]), in[n:]
		}
		return out
	}},
}

// benchCorpora are the streams, one filter (one Pass) each: the pass logs, and
// the raw stdout fixtures that carry the deltas the filter drops.
var benchCorpora = []struct{ name, glob string }{
	{"pass", "testdata/corpus/*/pass.jsonl"},
	{"raw", "testdata/streamfilter/*/stdout.raw"},
}

func readStreams(tb testing.TB, glob string) [][]byte {
	tb.Helper()
	paths, err := filepath.Glob(glob)
	if err != nil || len(paths) == 0 {
		tb.Fatalf("%s: %v (found %d)", glob, err, len(paths))
	}
	var out [][]byte
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

// runFilter feeds chunks to a fresh filter over w, then flushes.
func runFilter(tb testing.TB, s driverkit.Strategy, w io.Writer, chunks [][]byte) {
	tb.Helper()
	f := s.NewStreamFilter(w)
	for _, c := range chunks {
		if _, err := f.Write(c); err != nil {
			tb.Fatal(err)
		}
	}
	if err := f.Flush(); err != nil {
		tb.Fatal(err)
	}
}

// Compare the native pi stream filter with the pi Connector's under the
// launcher's interpreter and under the compiler (issue #6875). Not part of the
// gate; run on demand from cmd/launcher, skipped while the wasm is unset:
//
//	SPINDRIFT_DRIVER_CONNECTOR_WASM=/path/pi.wasm SPINDRIFT_DRIVER_CONNECTOR_NAME=pi \
//	  go test -run '^$' -bench ConnectorStreamFilter ./internal/driver/pi/
//
// The filter sits on the live stdout path, so its cost is per Write, not per
// replayed log.
//
// BenchmarkConnectorStreamFilter reports ns/call, a call being one Write of one
// chunk. The filter's construction and Flush are in the time but not the count,
// so a short stream carries them. Before timing, each Connector row's output
// must equal native's over the same chunks.
func BenchmarkConnectorStreamFilter(b *testing.B) {
	for _, corpus := range benchCorpora {
		streams := readStreams(b, corpus.glob)
		for _, ch := range benchChunkings {
			var cut [][][]byte
			calls := 0
			for _, s := range streams {
				c := ch.split(s)
				cut = append(cut, c)
				calls += len(c)
			}
			run := func(b *testing.B, s driverkit.Strategy) {
				for b.Loop() {
					for _, c := range cut {
						runFilter(b, s, io.Discard, c)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*calls), "ns/call")
			}
			b.Run("corpus="+corpus.name+"/chunks="+ch.name, func(b *testing.B) {
				b.Run("native", func(b *testing.B) { run(b, pi.Strategy) })
				for _, e := range []struct {
					name string
					load func(context.Context, []byte) (*connectorhost.Connector, error)
				}{
					{"interpreter", connectorhost.Load},
					{"compiler", func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
						return connectorhost.LoadCompiled(ctx, wasm, "")
					}},
				} {
					b.Run(e.name, func(b *testing.B) {
						s := loadConnectorStrategy(b, e.load)
						for i, c := range cut {
							var want, got bytes.Buffer
							runFilter(b, pi.Strategy, &want, c)
							runFilter(b, s, &got, c)
							if !bytes.Equal(want.Bytes(), got.Bytes()) {
								b.Fatalf("stream %d: Connector filter output differs from native", i)
							}
						}
						b.ResetTimer()
						run(b, s)
					})
				}
			})
		}
	}
}
