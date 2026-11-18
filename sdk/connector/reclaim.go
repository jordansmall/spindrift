package connector

const (
	// reclaimBytes and reclaimCalls bound how much a guest may allocate between
	// collections. Either alone fails: bytes-only lets small lines pile up
	// per-call garbage, calls-only lets large batches reach the host's memory
	// cap first. reclaimBytes stays well under the host's memoryCapBytes/9,
	// the ~9 bytes of peak guest memory it budgets per request byte.
	reclaimBytes = 4 << 20
	reclaimCalls = 256
)

// A Go wasip1 reactor never schedules the GC's background mark workers while
// its exports return without blocking, so a cycle the runtime starts inside a
// connector_call never completes and debug.SetMemoryLimit does not help
// (measured). An explicit runtime.GC blocks its caller and lets them run.
var sinceReclaim struct{ bytes, calls int }

// addReclaimBytes counts n delivered bytes toward the next reclaim.
func addReclaimBytes(n int) { sinceReclaim.bytes += n }

// reclaimDue records one connector_call carrying a request of n bytes and
// reports whether the caller should collect now, resetting the counters when
// it should.
func reclaimDue(n int) bool {
	sinceReclaim.bytes += n
	sinceReclaim.calls++
	if sinceReclaim.bytes < reclaimBytes && sinceReclaim.calls < reclaimCalls {
		return false
	}
	sinceReclaim = struct{ bytes, calls int }{}
	return true
}
