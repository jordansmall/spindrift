// Package readshadow is the host-side sampled shadow for a backend's read
// path. Every Nth call per read operation, the Connector-backed reader answers
// the same question off the caller's goroutine; the two answers are compared at
// canonical types and each divergence is recorded through shadowrecord.
//
// Native stays authoritative: callers always get the native result and error
// untouched, and every write goes straight to the native backend. No Connector
// failure escapes: one that fails to load, is refused by its Route table, fails
// closed, panics, or hangs is logged into the summary and the native path keeps
// running.
package readshadow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/shadowrecord"
)

// queueSize bounds the sampled calls awaiting the Connector; a full queue
// drops the sample (counted as skipped) rather than block a native call.
const queueSize = 64

// Reads is the Connector side, opened lazily on the first sampled call.
type Reads struct {
	Tracker forge.IssueTracker // Connector-backed tracker reads; nil = tracker ops not shadowed
	PR      forge.PRForge      // Connector-backed PR reads; nil = PR ops not shadowed
	Failed  func() error       // the Connector's fatal failure; nil-safe
	Abort   func()             // ends a call in flight
	Close   func()
}

// Config configures a Shadow.
type Config struct {
	Backend string // summary Driver name, e.g. "github"
	Every   int    // sample every Everyth call per operation; below 1 means 1
	// Open loads the Connector. observe must receive every audit entry the
	// Connector records, so 403s and rate limits reach the summary.
	Open func(ctx context.Context, observe func(connectorhost.AuditEntry)) (Reads, error)
	Emit func(hostop.SpindriftOp)
	Warn io.Writer // one line per first 403 or rate limit of each kind; nil discards
}

type job struct {
	op, inputs string
	native     json.RawMessage
	nativeErr  bool
	nativeNs   time.Duration
	// call returns the encoded Connector outcome; ok is false when the
	// Connector has no reader for this operation.
	call func(Reads) (out json.RawMessage, failed, ok bool)
}

// Shadow samples one backend's reads. Build it with New; layer it over the
// native backend with Tracker and CodeForge, and Close it once at run end.
type Shadow struct {
	cfg    Config
	every  int
	jobs   chan job
	ctx    context.Context
	cancel context.CancelFunc
	// workerDone closes when the worker has drained jobs.
	workerDone chan struct{}
	closeOnce  sync.Once

	// mu guards everything below and rec.Sum: native goroutines touch the
	// counters while the worker and the audit callback touch the rest.
	mu        sync.Mutex
	rec       *shadowrecord.Recorder
	calls     map[string]int
	started   bool
	closing   bool
	finished  bool // the summary is out; late worker results are discarded
	stopped   bool // no more sampling: load failure, fail-closed, panic, rate limit
	abandoned bool // close gave up waiting: a Connector that opens now is closed, not kept
	reads     Reads
	nativeNs  time.Duration
	shadowNs  time.Duration
	warned    map[string]bool
}

// New returns a Shadow; nothing is loaded until the first sampled call.
func New(cfg Config) *Shadow {
	if cfg.Warn == nil {
		cfg.Warn = io.Discard
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Shadow{
		cfg: cfg, every: max(cfg.Every, 1),
		jobs: make(chan job, queueSize), ctx: ctx, cancel: cancel,
		workerDone: make(chan struct{}),
		rec:        shadowrecord.New(cfg.Backend, cfg.Emit),
		calls:      map[string]int{}, warned: map[string]bool{},
	}
}

// sampled counts one call of op and reports whether it is this call's turn;
// call k (0-based) is sampled when k%every == 0, so an op's first call always is.
func (s *Shadow) sampled(op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.closing {
		return false
	}
	k := s.calls[op]
	s.calls[op]++
	return k%s.every == 0
}

func (s *Shadow) enqueue(j job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.closing {
		return
	}
	if !s.started {
		s.started = true
		go s.run()
	}
	select {
	case s.jobs <- j:
	default:
		s.rec.Sum.Skipped++
	}
}

// stopLocked ends sampling; the first reason is the one the summary keeps.
func (s *Shadow) stopLocked(reason string) {
	s.stopped = true
	if s.rec.Sum.Error == "" {
		s.rec.Sum.Error = reason
	}
}

func (s *Shadow) stop(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked(reason)
}

func (s *Shadow) run() {
	defer close(s.workerDone)
	opened := false
	for j := range s.jobs {
		s.mu.Lock()
		skip := s.stopped
		s.mu.Unlock()
		if !skip {
			opened = s.process(j, opened)
		}
	}
}

// process handles one job and returns whether the Connector is open. A panic
// anywhere in it is a summary error, never a crash of the run.
func (s *Shadow) process(j job, opened bool) bool {
	defer func() {
		if r := recover(); r != nil {
			s.stop(fmt.Sprintf("panic: %v", r))
		}
	}()
	if !opened {
		reads, err := s.cfg.Open(s.ctx, s.observe)
		s.mu.Lock()
		if err != nil {
			s.stopLocked("open connector: " + err.Error())
		} else if s.abandoned {
			// close saw no reads to release; the Connector is ours to close.
			s.mu.Unlock()
			if reads.Close != nil {
				reads.Close()
			}
			return false
		} else {
			s.reads = reads
			s.rec.Sum.Loaded = true
		}
		s.mu.Unlock()
		if err != nil {
			return false
		}
		opened = true
	}
	start := time.Now()
	out, shadowFailed, ok := j.call(s.reads)
	took := time.Since(start)
	if !ok {
		return opened
	}
	var failure error
	if s.reads.Failed != nil {
		failure = s.reads.Failed()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return opened
	}
	s.rec.Sum.Compared++
	s.nativeNs += j.nativeNs
	s.shadowNs += took
	// Failures are not canonical (the Connector words its errors its own
	// way), so two failing reads agree; exactly one failing is a divergence.
	if !(j.nativeErr && shadowFailed) {
		s.rec.Check(j.op, 0, j.inputs, j.native, out)
	}
	if failure != nil {
		s.stopLocked("connector failed: " + failure.Error())
	}
	return opened
}

// observe tallies the Connector's audit entries. Any rate limit suspends the
// shadow for the rest of the run, since it must not add load once the forge
// pushes back.
func (s *Shadow) observe(e connectorhost.AuditEntry) {
	var kind string
	var suspend bool
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.RateLimit == connectorhost.RateLimitPrimary:
		kind, suspend = "primary rate limit", true
		s.rec.Sum.RateLimited++
	case e.RateLimit == connectorhost.RateLimitSecondary:
		kind, suspend = "secondary rate limit", true
		s.rec.Sum.SecondaryRateLimited++
	case e.Status == 403:
		kind = "403"
		s.rec.Sum.Forbidden++
	default:
		return
	}
	if !s.warned[kind] {
		s.warned[kind] = true
		retry := ""
		if e.RetryAfter != "" {
			retry = " (retry-after " + e.RetryAfter + ")"
		}
		fmt.Fprintf(s.cfg.Warn, "==> shadow %s: %s on %s %s%s%s\n", s.cfg.Backend, kind, e.Method, e.Host, e.Path, retry)
	}
	if suspend {
		s.stopLocked("shadow suspended by a " + kind)
	}
}

// Close stops sampling, waits for queued calls until ctx ends, and emits the
// summary exactly once. When ctx ends first it aborts the Connector, so a
// hung shadow call costs the run only the grace ctx allows.
func (s *Shadow) Close(ctx context.Context) {
	s.closeOnce.Do(func() { s.close(ctx) })
}

func (s *Shadow) close(ctx context.Context) {
	defer s.cancel()
	s.mu.Lock()
	s.closing = true
	started := s.started
	close(s.jobs)
	s.mu.Unlock()

	if started {
		select {
		case <-s.workerDone:
		case <-ctx.Done():
			s.mu.Lock()
			s.abandoned = true
			s.stopLocked(fmt.Sprintf("shadow deadline exceeded after %d calls", s.rec.Sum.Compared))
			reads := s.reads
			s.mu.Unlock()
			s.cancel()
			if reads.Abort != nil {
				reads.Abort()
			}
		}
	}
	s.mu.Lock()
	reads := s.reads
	s.mu.Unlock()
	if reads.Close != nil {
		reads.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	if n := time.Duration(s.rec.Sum.Compared); n > 0 {
		s.rec.Sum.NativeNsPerLine = int64(s.nativeNs / n)
		s.rec.Sum.ShadowNsPerLine = int64(s.shadowNs / n)
	}
	s.rec.Finish()
}

// offer hands a native call's outcome to the worker when it is this op's turn.
// The outcome is encoded here, synchronously, so the caller may mutate what it
// got back without racing the worker.
func offer[T any](s *Shadow, op string, in any, start time.Time, v T, err error, norm func(T) any, has func(Reads) bool, read func(Reads) (T, error)) {
	took := time.Since(start)
	if !s.sampled(op) {
		return
	}
	encode := func(v T, err error) json.RawMessage {
		if err != nil {
			return shadowrecord.EncodeJSON(map[string]string{"error": err.Error()})
		}
		if norm != nil {
			return shadowrecord.EncodeJSON(norm(v))
		}
		return shadowrecord.EncodeJSON(v)
	}
	s.enqueue(job{
		op: op, inputs: string(shadowrecord.EncodeJSON(in)),
		native: encode(v, err), nativeErr: err != nil, nativeNs: took,
		call: func(r Reads) (json.RawMessage, bool, bool) {
			if !has(r) {
				return nil, false, false
			}
			sv, serr := read(r)
			return encode(sv, serr), serr != nil, true
		},
	})
}

// sortedIssues orders by issue number and never yields null: the contract
// pins each adapter's own list order, not one shared order, and an empty list
// may arrive as nil or as [].
func sortedIssues(in []forge.Issue) any {
	out := slices.Clone(in)
	if out == nil {
		out = []forge.Issue{}
	}
	slices.SortStableFunc(out, func(a, b forge.Issue) int {
		na, ea := strconv.Atoi(a.Number)
		nb, eb := strconv.Atoi(b.Number)
		if ea != nil || eb != nil {
			return strings.Compare(a.Number, b.Number)
		}
		return na - nb
	})
	return out
}

// Tracker layers the shadow over native's reads.
func (s *Shadow) Tracker(native forge.IssueTracker) forge.IssueTracker {
	return &trackerLayer{IssueTracker: native, s: s}
}

type trackerLayer struct {
	forge.IssueTracker
	s *Shadow
}

var _ forge.Unwrapper = (*trackerLayer)(nil)

func (t *trackerLayer) Unwrap() forge.IssueTracker { return t.IssueTracker }

func hasTracker(r Reads) bool { return r.Tracker != nil }

func hasPR(r Reads) bool { return r.PR != nil }

func (t *trackerLayer) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	start := time.Now()
	v, err := t.IssueTracker.ListIssues(state)
	offer(t.s, "ListIssues", map[string]string{"state": state.String()}, start, v, err, sortedIssues,
		hasTracker, func(r Reads) ([]forge.Issue, error) { return r.Tracker.ListIssues(state) })
	return v, err
}

func (t *trackerLayer) ListOpenIssues() ([]forge.Issue, error) {
	start := time.Now()
	v, err := t.IssueTracker.ListOpenIssues()
	offer(t.s, "ListOpenIssues", struct{}{}, start, v, err, sortedIssues,
		hasTracker, func(r Reads) ([]forge.Issue, error) { return r.Tracker.ListOpenIssues() })
	return v, err
}

func (t *trackerLayer) Issue(num string) (forge.Issue, error) {
	start := time.Now()
	v, err := t.IssueTracker.Issue(num)
	offer(t.s, "Issue", map[string]string{"num": num}, start, v, err, nil,
		hasTracker, func(r Reads) (forge.Issue, error) { return r.Tracker.Issue(num) })
	return v, err
}

// CodeForge layers the shadow over native's PR reads. A native with no PRForge
// has nothing to shadow and is returned unchanged.
func (s *Shadow) CodeForge(native forge.CodeForge) forge.CodeForge {
	pr, ok := forge.AsCodeForge[forge.PRForge](native)
	if !ok {
		return native
	}
	return &prLayer{CodeForge: native, PRForge: pr, s: s}
}

// prLayer is the native CodeForge with its PRForge replaced by a shadowed one;
// forge.AsCodeForge finds it for PRForge and walks on to native for the rest.
type prLayer struct {
	forge.CodeForge
	// PRForge supplies the unshadowed methods (Mergeable, HeadCommitSHA, ...).
	forge.PRForge
	s *Shadow
}

var (
	_ forge.CodeForgeUnwrapper = (*prLayer)(nil)
	_ forge.PRForge            = (*prLayer)(nil)
)

func (p *prLayer) UnwrapCodeForge() forge.CodeForge { return p.CodeForge }

func (p *prLayer) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	type res struct {
		PR forge.PR
		OK bool
	}
	start := time.Now()
	pr, ok, err := p.PRForge.OpenPRForBranch(branch)
	offer(p.s, "OpenPRForBranch", map[string]string{"branch": branch}, start, res{pr, ok}, err, nil,
		hasPR, func(r Reads) (res, error) {
			pr, ok, err := r.PR.OpenPRForBranch(branch)
			return res{pr, ok}, err
		})
	return pr, ok, err
}

func (p *prLayer) PRForBranch(branch string) (string, bool, error) {
	type res struct {
		URL string
		OK  bool
	}
	start := time.Now()
	url, ok, err := p.PRForge.PRForBranch(branch)
	offer(p.s, "PRForBranch", map[string]string{"branch": branch}, start, res{url, ok}, err, nil,
		hasPR, func(r Reads) (res, error) {
			url, ok, err := r.PR.PRForBranch(branch)
			return res{url, ok}, err
		})
	return url, ok, err
}

func (p *prLayer) PRState(url string) (forge.PRState, error) {
	start := time.Now()
	v, err := p.PRForge.PRState(url)
	offer(p.s, "PRState", map[string]string{"url": url}, start, v, err, nil,
		hasPR, func(r Reads) (forge.PRState, error) { return r.PR.PRState(url) })
	return v, err
}

func (p *prLayer) CheckState(url string) (forge.RollupState, error) {
	start := time.Now()
	v, err := p.PRForge.CheckState(url)
	offer(p.s, "CheckState", map[string]string{"url": url}, start, v, err, nil,
		hasPR, func(r Reads) (forge.RollupState, error) { return r.PR.CheckState(url) })
	return v, err
}

func (p *prLayer) FailureDetail(url string) (string, error) {
	start := time.Now()
	v, err := p.PRForge.FailureDetail(url)
	offer(p.s, "FailureDetail", map[string]string{"url": url}, start, v, err, nil,
		hasPR, func(r Reads) (string, error) { return r.PR.FailureDetail(url) })
	return v, err
}
