package waves

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
)

// silenceStdout points os.Stdout at /dev/null for the benchmark's duration.
// testutil.CaptureStdout's pipe and draining goroutine would cost enough to
// dominate the measurement. These benchmarks never assert on the text; they
// only need fmt.Print output kept out of go test's own result lines.
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

// staleDrainReportBenchFixture is shared setup so BenchmarkReportStaleDrain
// and BenchmarkReportStaleDrainReleasingMu keep measuring the same dir, queue
// and report instead of drifting apart as two copy-pasted blocks.
func staleDrainReportBenchFixture(b *testing.B) (dir string, queue Queue, report StaleDrainReport) {
	b.Helper()
	dir = tempLogDir(b)
	queue = NewHeadlessQueue(nil, nil, noopPending, dir)
	staleAt := time.Now()
	report = StaleDrainReport{
		StaleAt:   staleAt,
		DrainedAt: staleAt.Add(3 * time.Second),
		HeldBack:  2,
	}
	silenceStdout(b)
	return dir, queue, report
}

// truncateEvery empties path every step iterations with the timer stopped.
// continuous.go opens stale-drain.log O_APPEND, so an unbounded b.N would
// otherwise grow the file without bound and drift the measured append cost.
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

// BenchmarkReportStaleDrain measures ReportStaleDrain's I/O cost alone (stdout
// print, stale-drain.log open/append/write/close), as the baseline for #2775's
// claim that moving this I/O outside mu is worth it. Recorded on the #2775
// fix, with no mutex involved: ~21.6µs/op, 793 B/op, 19 allocs/op, which vary
// by machine and Go version.
func BenchmarkReportStaleDrain(b *testing.B) {
	dir, queue, report := staleDrainReportBenchFixture(b)
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		truncateEvery(b, logPath, 1000, i)
		queue.ReportStaleDrain(report)
	}
}

// BenchmarkReportStaleDrainReleasingMu measures the path #2775 changed, with
// mu held on entry and exit as both call sites in continuous.go hold it, so
// the gap against BenchmarkReportStaleDrain is the added unlock/lock overhead.
// Recorded on the #2775 fix: ~21.1µs/op, 809 B/op, 19 allocs/op, within noise
// of that baseline. Compare the two rather than reading either number alone.
func BenchmarkReportStaleDrainReleasingMu(b *testing.B) {
	dir, queue, report := staleDrainReportBenchFixture(b)
	logPath := filepath.Join(dispatch.HostLogDirFor(dir), staleDrainMarker)
	var mu sync.Mutex

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		truncateEvery(b, logPath, 1000, i)
		mu.Lock()
		reportStaleDrainReleasingMu(&mu, queue, report)
		mu.Unlock()
	}
}
