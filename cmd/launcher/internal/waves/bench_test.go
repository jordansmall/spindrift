package waves

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
)

// silenceStdout redirects os.Stdout to /dev/null for the duration of a
// benchmark run, restoring it via b.Cleanup. Unlike testutil.CaptureStdout's
// pipe-plus-draining-goroutine approach, writing to /dev/null is a cheap,
// ~constant-cost syscall that won't itself dominate the measurement -- the
// goal here is only to stop fmt.Print output from interleaving with go
// test's own benchmark result line and from being the dominant cost, not to
// capture or assert on the emitted text.
func silenceStdout(b *testing.B) {
	b.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = devNull
	b.Cleanup(func() {
		os.Stdout = orig
		devNull.Close()
	})
}

// staleDrainReportBenchFixture builds the dir/cfg/report
// BenchmarkEmitStaleDrainReport and BenchmarkEmitStaleDrainReportReleasingMu
// both loop against, and silences stdout for the benchmark's duration --
// shared setup so the pair stays in lockstep instead of copy-pasted.
func staleDrainReportBenchFixture(b *testing.B) (dir string, cfg Config, report StaleDrainReport) {
	b.Helper()
	dir = tempLogDir(b)
	cfg = baseConfig()
	staleAt := time.Now()
	report = StaleDrainReport{
		StaleAt:   staleAt,
		DrainedAt: staleAt.Add(3 * time.Second),
		HeldBack:  2,
	}
	silenceStdout(b)
	return dir, cfg, report
}

// truncateEvery truncates path back to empty every step iterations, with
// the truncation itself excluded from the timer -- stale-drain.log is opened
// O_APPEND (continuous.go), so an unbounded b.N would otherwise grow the
// file without bound and drift the measured append cost across the run.
func truncateEvery(b *testing.B, path string, step, i int) {
	if i%step != 0 {
		return
	}
	b.StopTimer()
	if err := os.Truncate(path, 0); err != nil && !os.IsNotExist(err) {
		b.Fatal(err)
	}
	b.StartTimer()
}

// BenchmarkEmitStaleDrainReport measures emitStaleDrainReport's own I/O cost
// (stdout print, stale-drain.log open/append/write/close) in isolation from
// emitStaleDrainReportReleasingMu's locking -- a baseline for #2775's claim
// that moving this I/O outside mu is worth it, and a guard against a future
// change silently making the I/O path itself much more expensive.
// OnStaleDrainReport is left nil so the loop measures the realistic default
// path (no Console-session callback), not a synthetic one.
//
// Recorded on the #2775 fix (I/O-only baseline, no mutex involved):
// ~21.6µs/op, 793 B/op, 19 allocs/op -- ns/op and B/op vary by machine and Go
// version, but this is the cost
// BenchmarkEmitStaleDrainReportReleasingMu's unlock/lock overhead is
// negligible relative to.
func BenchmarkEmitStaleDrainReport(b *testing.B) {
	dir, cfg, report := staleDrainReportBenchFixture(b)
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		truncateEvery(b, logPath, 1000, i)
		emitStaleDrainReport(cfg, dir, report)
	}
}

// BenchmarkEmitStaleDrainReportReleasingMu measures the actual path #2775
// changed: emitStaleDrainReportReleasingMu's unlock-I/O-relock cycle,
// modeled on how both real call sites in continuous.go use it -- mu already
// held on entry, held again on exit. Compared against
// BenchmarkEmitStaleDrainReport's I/O-only baseline, this isolates the
// unlock/lock overhead #2775 added around that same I/O.
//
// Recorded on the #2775 fix: ~21.1µs/op, 809 B/op, 19 allocs/op -- within
// noise of BenchmarkEmitStaleDrainReport's baseline, confirming the
// unlock/lock overhead is negligible relative to the I/O cost. ns/op and
// B/op vary by machine and Go version; that shape of comparison is the
// invariant worth recording.
func BenchmarkEmitStaleDrainReportReleasingMu(b *testing.B) {
	dir, cfg, report := staleDrainReportBenchFixture(b)
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	var mu sync.Mutex

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		truncateEvery(b, logPath, 1000, i)
		mu.Lock()
		emitStaleDrainReportReleasingMu(&mu, cfg, dir, report)
		mu.Unlock()
	}
}
