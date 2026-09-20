package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestStatusWriter_WriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	want := Status{
		Kinds: []Kind{KindDispatch, KindResearch},
		State: StateWorking,
		Slots: []SlotStatus{
			{Slot: 0, Busy: true, Kind: KindDispatch, Revision: "abc123", Issues: []string{"42"}},
			{Slot: 1, Busy: false},
		},
		Checks: []KindCheck{
			{Kind: KindDispatch},
			{Kind: KindResearch, NextCheck: "2026-09-20T12:05:00Z"},
		},
	}
	if err := w.Write(want); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("ReadStatus: Status is nil")
	}
	got := *report.Status
	if got.State != want.State {
		t.Errorf("State = %q, want %q", got.State, want.State)
	}
	if len(got.Kinds) != 2 || got.Kinds[0] != KindDispatch || got.Kinds[1] != KindResearch {
		t.Errorf("Kinds = %v, want %v", got.Kinds, want.Kinds)
	}
	if len(got.Slots) != 2 || got.Slots[0].Revision != "abc123" || got.Slots[0].Issues[0] != "42" {
		t.Errorf("Slots = %+v, want %+v", got.Slots, want.Slots)
	}
	if len(got.Checks) != 2 || got.Checks[1].NextCheck != "2026-09-20T12:05:00Z" {
		t.Errorf("Checks = %+v, want %+v", got.Checks, want.Checks)
	}
}

// TestStatus_StateMarshalsAsPlainString pins State's wire format: it is a
// named string type (not an int enum), so a plain literal is what any
// consumer parsing the status file by hand should expect.
func TestStatus_StateMarshalsAsPlainString(t *testing.T) {
	data, err := json.Marshal(Status{State: StateWorking})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"state":"working"`) {
		t.Errorf("marshalled = %s, want it to contain %q", data, `"state":"working"`)
	}
}

func TestStatusWriter_StampsPidHostStartedTime(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	// The caller leaves Pid/Host/Started/Time zero; Write must stamp them.
	if err := w.Write(Status{State: StateAsleep}); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	got := *report.Status
	if got.Pid != os.Getpid() {
		t.Errorf("Pid = %d, want %d", got.Pid, os.Getpid())
	}
	if got.Host == "" {
		t.Errorf("Host is empty, want stamped hostname")
	}
	wantTime := fixed.Format(time.RFC3339)
	if got.Started != wantTime {
		t.Errorf("Started = %q, want %q", got.Started, wantTime)
	}
	if got.Time != wantTime {
		t.Errorf("Time = %q, want %q", got.Time, wantTime)
	}
}

func TestStatusWriter_WriteTwiceLeavesNoTempLitter(t *testing.T) {
	dir := t.TempDir()
	w := NewStatusWriter(dir, time.Now)

	if err := w.Write(Status{State: StateWaiting}); err != nil {
		t.Fatalf("first Write: unexpected error: %v", err)
	}
	if err := w.Write(Status{State: StateWorking}); err != nil {
		t.Fatalf("second Write: unexpected error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("dir has %d entries after two Writes, want 1: %v", len(entries), names)
	}
	if entries[0].Name() != statusFileName {
		t.Errorf("leftover file %q, want %q", entries[0].Name(), statusFileName)
	}
}

func TestReadStatus_NoLockNoStatus(t *testing.T) {
	dir := t.TempDir()

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.LockHeld {
		t.Errorf("LockHeld = true, want false")
	}
	if report.Status != nil {
		t.Errorf("Status = %+v, want nil", report.Status)
	}
	if report.Stale {
		t.Errorf("Stale = true, want false")
	}
	if report.Live {
		t.Errorf("Live = true, want false")
	}
}

func TestReadStatus_StatusPresentLockNotHeld(t *testing.T) {
	dir := t.TempDir()
	w := NewStatusWriter(dir, time.Now)
	if err := w.Write(Status{State: StateWaiting}); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("Status is nil, want non-nil")
	}
	if !report.Stale {
		t.Errorf("Stale = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false")
	}
}

func TestReadStatus_LockHeldByUsAndStatusNamesOurPid(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	w := NewStatusWriter(dir, time.Now)
	if err := w.Write(Status{State: StateWorking}); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if !report.Live {
		t.Errorf("Live = false, want true")
	}
	if report.Stale {
		t.Errorf("Stale = true, want false")
	}
	want := fmt.Sprintf("pid=%d", os.Getpid())
	if !strings.Contains(report.Holder, want) {
		t.Errorf("Holder = %q, want containing %q", report.Holder, want)
	}
}

func TestReadStatus_LockHeldByUsButStatusNamesOtherPid(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	// Simulate the lock's current holder having published nothing yet,
	// with a dead predecessor's status file left behind under a
	// different pid. Write the file directly — StatusWriter.Write always
	// stamps our own real pid, which is not what this test needs.
	statusPath := filepath.Join(dir, statusFileName)
	data, err := json.Marshal(Status{Pid: os.Getpid() + 1, State: StateWorking})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(statusPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false")
	}
	if !report.Stale {
		t.Errorf("Stale = false, want true")
	}
}

func TestReadStatus_ProbeDoesNotCreateLockFile(t *testing.T) {
	dir := t.TempDir()

	if _, err := ReadStatus(dir); err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}

	lockPath := filepath.Join(dir, checkoutLockFileName)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file exists after probe (err=%v), want still absent", err)
	}
}

func TestReadStatus_GarbageStatusFileIsError(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, statusFileName)
	if err := os.WriteFile(statusPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := ReadStatus(dir); err == nil {
		t.Fatalf("ReadStatus: want error for garbage status file, got nil")
	}
}

// TestReadStatus_LockHeldFalseUnderConcurrentSharedProbe pins the review
// finding against the test this replaces: that one held LOCK_EX (via
// AcquireCheckoutLock), so both ReadStatus calls hit the same EWOULDBLOCK
// branch a regressed LOCK_EX probe would too, and never actually exercised
// probeCheckoutLock's LOCK_SH choice. This instead takes LOCK_SH directly
// on the lock file — standing in for a second, concurrent probe's own
// window — and asserts ReadStatus's probe (a compatible LOCK_SH) still
// succeeds and reports the lock free.
func TestReadStatus_LockHeldFalseUnderConcurrentSharedProbe(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, checkoutLockFileName)

	probeFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock file for simulated concurrent probe: %v", err)
	}
	defer probeFile.Close()
	if err := syscall.Flock(int(probeFile.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatalf("take LOCK_SH for simulated concurrent probe: %v", err)
	}
	defer syscall.Flock(int(probeFile.Fd()), syscall.LOCK_UN)

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.LockHeld {
		t.Fatalf("LockHeld = true while only a concurrent shared probe holds the lock, want false")
	}
}

// TestStatusWriter_PublishSerializesSnapshotAndWrite guards against the
// issue #3545 review finding: Publish must hold w.mu across snap() itself,
// not just the write, or two concurrent publishers can sample in one order
// and land their writes in the other, leaving the file describing neither
// caller's actual state.
func TestStatusWriter_PublishSerializesSnapshotAndWrite(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	var inA atomic.Bool
	var overlap atomic.Bool
	aEntered := make(chan struct{})
	releaseA := make(chan struct{})
	bSnapRan := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = w.Publish(func() Status {
			inA.Store(true)
			close(aEntered)
			<-releaseA
			inA.Store(false)
			return Status{State: StateWorking}
		})
	}()

	<-aEntered
	go func() {
		defer wg.Done()
		_ = w.Publish(func() Status {
			if inA.Load() {
				overlap.Store(true)
			}
			close(bSnapRan)
			return Status{State: StateWaiting}
		})
	}()

	// B's snap must block on w.mu until A releases — give it a short,
	// bounded window to prove it never ran early. Under the pre-fix shape
	// (snapshot taken outside the lock) B's snap runs immediately and this
	// select fires on bSnapRan instead of the timeout.
	select {
	case <-bSnapRan:
		t.Fatalf("B's snap ran while A's Publish was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseA)
	wg.Wait()

	if overlap.Load() {
		t.Errorf("B's snap observed A's snap still in progress")
	}
}

// TestParseHolderPid pins the review finding against parseHolderPid: an
// unreadable holder line — no pid= prefix, or a non-numeric pid= value —
// must parse to 0, which can never equal a real Status.Pid, so a caller
// reads it as not-live rather than a false live.
func TestParseHolderPid(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		want     int
	}{
		{
			name:     "real line as writeHolderIdentity formats it",
			identity: "pid=4242 host=box1 kind=dispatch started=2026-09-20T12:00:00Z",
			want:     4242,
		},
		{
			name:     "no pid= prefix at all",
			identity: "host=box1 kind=dispatch started=2026-09-20T12:00:00Z",
			want:     0,
		},
		{
			name:     "non-numeric pid value",
			identity: "pid=abc host=box1 kind=dispatch",
			want:     0,
		},
		{
			name:     "empty string",
			identity: "",
			want:     0,
		},
		{
			name:     "pid= with nothing after it",
			identity: "pid=",
			want:     0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseHolderPid(tc.identity); got != tc.want {
				t.Errorf("parseHolderPid(%q) = %d, want %d", tc.identity, got, tc.want)
			}
		})
	}
}

// TestReadStatus_UnreadableHolderLineReadsAsNotLive pins the same intent
// through the real ReadStatus seam (review finding against parseHolderPid): a
// held lock whose identity line the parser cannot read must never be
// mistaken for this reader's own live daemon — it must come back
// Live:false, Stale:true, exactly like a recognizably different pid would.
func TestReadStatus_UnreadableHolderLineReadsAsNotLive(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	// Garble the identity line in place while the lock is still held.
	// flock is advisory over the open file description, not the path, so
	// a plain write to the same inode does not contend with the held
	// lock — only a competing flock call would.
	lockPath := filepath.Join(dir, checkoutLockFileName)
	if err := os.WriteFile(lockPath, []byte("garbage, no pid field here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	statusPath := filepath.Join(dir, statusFileName)
	data, err := json.Marshal(Status{Pid: os.Getpid(), State: StateWorking})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(statusPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false: an unreadable holder line must never read as live")
	}
	if !report.Stale {
		t.Errorf("Stale = false, want true")
	}
}

// TestReadStatus_EmptyStatusAndUnparseableHolderNotLive pins the review
// finding at ReadStatus: a bare "{}" status file (zero-valued Pid) beside a
// lock whose identity line has no parseable pid= must not read live just
// because both sides zero-value to 0. Before the holderPid > 0 guard,
// status.Pid == holderPid was 0 == 0 → true.
func TestReadStatus_EmptyStatusAndUnparseableHolderNotLive(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	// Garble the identity line in place while the lock is still held, as
	// TestReadStatus_UnreadableHolderLineReadsAsNotLive does — flock is
	// advisory over the open file description, not the path.
	lockPath := filepath.Join(dir, checkoutLockFileName)
	if err := os.WriteFile(lockPath, []byte("garbage, no pid field here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	statusPath := filepath.Join(dir, statusFileName)
	if err := os.WriteFile(statusPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false: a zero-valued status pid must not match an unparseable holder pid")
	}
}

// TestReadStatus_MatchingPidDifferentHostNotLive pins the review finding
// that Live correlated on pid alone: a status file naming this process's
// real pid but a different host than the lock's own identity line must not
// read live — on a shared checkout a coincidental pid match must not read
// a dead remote daemon's file as live.
func TestReadStatus_MatchingPidDifferentHostNotLive(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	statusPath := filepath.Join(dir, statusFileName)
	data, err := json.Marshal(Status{Pid: os.Getpid(), Host: "a-different-host", State: StateWorking})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(statusPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false: a matching pid on a different host must not read as live")
	}
}

// TestReadStatus_GarbageStatusUnderHeldLockKeepsLockHeld pins the review
// finding that the file parse ran before the lock probe: a genuinely held
// lock beside a garbage status file must still surface in the returned
// report (LockHeld true, Holder populated) alongside the parse error,
// rather than the zero-valued StatusReport{} the pre-fix ordering returned.
func TestReadStatus_GarbageStatusUnderHeldLockKeepsLockHeld(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindDispatch})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	statusPath := filepath.Join(dir, statusFileName)
	if err := os.WriteFile(statusPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	report, err := ReadStatus(dir)
	if err == nil {
		t.Fatalf("ReadStatus: want error for garbage status file, got nil")
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true: the lock probe truth must survive the status file's parse error")
	}
	if report.Holder == "" {
		t.Errorf("Holder = %q, want the held lock's identity line", report.Holder)
	}
}
