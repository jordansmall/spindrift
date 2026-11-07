package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
)

func TestStatusWriter_WriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	want := Status{
		Kinds: []Kind{KindOf(dispatchkind.Work), KindOf(dispatchkind.Research)},
		State: StateWorking,
		Slots: []SlotStatus{
			{Slot: 0, Busy: true, Kind: KindOf(dispatchkind.Work), Revision: "abc123", Issues: []string{"42"}},
			{Slot: 1, Busy: false},
		},
		Checks: []KindCheck{
			{Kind: KindOf(dispatchkind.Work)},
			{Kind: KindOf(dispatchkind.Research), NextCheck: "2026-09-20T12:05:00Z"},
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
	if len(got.Kinds) != 2 || got.Kinds[0] != KindOf(dispatchkind.Work) || got.Kinds[1] != KindOf(dispatchkind.Research) {
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

// TestSlotStatus_ChoreMarshalsUnderChoreKey pins the "chore" key: the box,
// settled, and child_finish events carry the Chore under that same key, and
// docs/reference.md promises an operator can join the two on it.
func TestSlotStatus_ChoreMarshalsUnderChoreKey(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, Kind: KindOf(dispatchkind.Butler), Chore: "bugs"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"chore":"bugs"`) {
		t.Errorf("marshalled = %s, want it to contain %q", data, `"chore":"bugs"`)
	}
}

// TestSlotStatus_PassMarshalsUnderPassKeyAndOmitsWhenEmpty pins the additive
// "pass" key, which must stay absent on a slot with no box record yet.
func TestSlotStatus_PassMarshalsUnderPassKeyAndOmitsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, Pass: "initial"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"pass":"initial"`) {
		t.Errorf("marshalled = %s, want it to contain %q", data, `"pass":"initial"`)
	}
	data, err = json.Marshal(SlotStatus{Busy: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"pass"`) {
		t.Errorf("marshalled = %s, want no pass key when empty", data)
	}
}

// TestSlotStatus_ModelMarshalsUnderModelKeysAndOmitsWhenEmpty pins the
// additive "model" and "model_role" keys, absent until a model record.
func TestSlotStatus_ModelMarshalsUnderModelKeysAndOmitsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, Model: "claude-opus-5-5", ModelRole: "reviewer"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"model":"claude-opus-5-5"`, `"model_role":"reviewer"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marshalled = %s, want it to contain %q", data, want)
		}
	}
	data, err = json.Marshal(SlotStatus{Busy: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"model`) {
		t.Errorf("marshalled = %s, want no model keys when empty", data)
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

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
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
	if report.HolderPidGone {
		t.Errorf("HolderPidGone = true, want false")
	}
	want := fmt.Sprintf("pid=%d", os.Getpid())
	if !strings.Contains(report.Holder, want) {
		t.Errorf("Holder = %q, want containing %q", report.Holder, want)
	}
}

func TestReadStatus_LockHeldByUsButStatusNamesOtherPid(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	// Simulate the lock's current holder having published nothing yet,
	// with a dead predecessor's status file left behind under a
	// different pid. Write the file directly — StatusWriter.Write always
	// stamps our own real pid, which is not what this test needs.
	writeStatusFile(t, dir, Status{Pid: os.Getpid() + 1, State: StateWorking})

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

// TestReadStatus_LockFlockedButPredecessorIdentityLineNotLive pins issue
// #3597: between a new daemon's Flock and its identity truncate the lock
// file still carries a killed predecessor's identity line, and the status
// file is the predecessor's too, so they agree on pid and host. Only asking
// the OS whether that pid exists can tell the file is stale.
func TestReadStatus_LockFlockedButPredecessorIdentityLineNotLive(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname: %v", err)
	}
	dir := flockedWindow(t, host)

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if report.Live {
		t.Errorf("Live = true, want false: the holder line names a dead pid")
	}
	if !report.Stale {
		t.Errorf("Stale = false, want true")
	}
	if !report.HolderPidGone {
		t.Errorf("HolderPidGone = false, want true")
	}
}

// TestReadStatus_LockFlockedRemoteHolderReadsLiveByContent pins the host
// gate on the #3597 pid probe: a pid under a foreign hostname cannot be
// probed from here, so a lock line and status file that agree decide alone.
func TestReadStatus_LockFlockedRemoteHolderReadsLiveByContent(t *testing.T) {
	dir := flockedWindow(t, "other-host")

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if !report.LockHeld {
		t.Errorf("LockHeld = false, want true")
	}
	if !report.Live {
		t.Errorf("Live = false, want true: a remote holder's pid cannot be probed locally")
	}
	if report.Stale {
		t.Errorf("Stale = true, want false")
	}
	if report.HolderPidGone {
		t.Errorf("HolderPidGone = true, want false")
	}
}

// flockedWindow builds the acquire window of issue #3597: the lock is
// flocked but still carries a dead predecessor's identity line, and the
// status file is that predecessor's too, both naming host. It returns the
// checkout dir; the flock is released via t.Cleanup.
func flockedWindow(t *testing.T, host string) string {
	t.Helper()
	dir := t.TempDir()

	// A real, reaped pid: re-exec the test binary with nothing to run. It
	// could in principle be recycled before ReadStatus runs (known gap, see
	// docs/reference.md).
	child := exec.Command(os.Args[0], "-test.run=^$")
	if err := child.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}
	deadPid := child.Process.Pid

	started := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	lockPath := filepath.Join(dir, checkoutLockFileName)
	identity := fmt.Sprintf("pid=%d host=%s kind=dispatch started=%s exe=/dead\n", deadPid, host, started)
	if err := os.WriteFile(lockPath, []byte(identity), 0o644); err != nil {
		t.Fatalf("WriteFile lock: %v", err)
	}
	writeStatusFile(t, dir, Status{Pid: deadPid, Host: host, Started: started, State: StateWorking})

	// Hold the lock without writing an identity: the new daemon's window.
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Flock: %v", err)
	}
	return dir
}

// writeStatusFile writes s directly as dir's status file — StatusWriter
// always stamps our own real pid and host, which these tests must override.
func writeStatusFile(t *testing.T, dir string, s Status) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, statusFileName), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
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

// TestStatusWriter_PublishDropsOlderSeq pins issue #3623's ordering rule: a
// Publish whose seq is older than the last one written must leave the file
// holding the newer snapshot, even though the older one landed second —
// the scenario that used to require serializing sample-and-write under
// w.mu (issue #3545) is now handled by seq order instead, so an
// out-of-order arrival is a no-op rather than a race.
func TestStatusWriter_PublishDropsOlderSeq(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	newer := Status{State: StateWorking}
	older := Status{State: StateWaiting}

	if err := w.Publish(2, newer); err != nil {
		t.Fatalf("Publish(2, newer): unexpected error: %v", err)
	}
	if err := w.Publish(1, older); err != nil {
		t.Fatalf("Publish(1, older): unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("ReadStatus: Status is nil")
	}
	if got := report.Status.State; got != StateWorking {
		t.Errorf("State = %q, want %q (older seq must not overwrite newer)", got, StateWorking)
	}
}

// TestStatusWriter_PublishWritesInOrderSeq is TestStatusWriter_PublishDropsOlderSeq's
// counterpart: a strictly increasing seq sequence must still write every
// snapshot, so the drop rule only ever suppresses a real reordering.
func TestStatusWriter_PublishWritesInOrderSeq(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	w := NewStatusWriter(dir, func() time.Time { return fixed })

	if err := w.Publish(1, Status{State: StateWaiting}); err != nil {
		t.Fatalf("Publish(1, ...): unexpected error: %v", err)
	}
	if err := w.Publish(2, Status{State: StateWorking}); err != nil {
		t.Fatalf("Publish(2, ...): unexpected error: %v", err)
	}

	report, err := ReadStatus(dir)
	if err != nil {
		t.Fatalf("ReadStatus: unexpected error: %v", err)
	}
	if report.Status == nil {
		t.Fatalf("ReadStatus: Status is nil")
	}
	if got := report.Status.State; got != StateWorking {
		t.Errorf("State = %q, want %q", got, StateWorking)
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

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
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

	writeStatusFile(t, dir, Status{Pid: os.Getpid(), State: StateWorking})

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

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
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

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
	if err != nil {
		t.Fatalf("AcquireCheckoutLock: unexpected error: %v", err)
	}
	defer lock.Release()

	writeStatusFile(t, dir, Status{Pid: os.Getpid(), Host: "a-different-host", State: StateWorking})
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
	if report.HolderPidGone {
		t.Errorf("HolderPidGone = true, want false: the host differs, so the status file is not the holder line's predecessor")
	}
}

// TestReadStatus_GarbageStatusUnderHeldLockKeepsLockHeld pins the review
// finding that the file parse ran before the lock probe: a genuinely held
// lock beside a garbage status file must still surface in the returned
// report (LockHeld true, Holder populated) alongside the parse error,
// rather than the zero-valued StatusReport{} the pre-fix ordering returned.
func TestReadStatus_GarbageStatusUnderHeldLockKeepsLockHeld(t *testing.T) {
	dir := t.TempDir()

	lock, err := AcquireCheckoutLock(dir, []Kind{KindOf(dispatchkind.Work)})
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

// TestPoolSnapshotOmitsReadyAtJamWhileBaselinePending pins that a jam whose
// baseline awaits its next probe reports no ready_at_jam rather than a
// count the claim's fold already skewed.
func TestPoolSnapshotOmitsReadyAtJamWhileBaselinePending(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
	work := KindOf(dispatchkind.Work)
	cfg := testConfig(1)
	cfg.Kinds = []Kind{work}
	cfg.ProbeIntervals = map[Kind]time.Duration{work: time.Minute}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)

	now := clk.Now()
	s := p.st.sched
	s, _ = s.Observe(now, demandProbed{Kind: work, Ready: 2})
	s, _ = s.Observe(now, claimed{Kind: work})
	s, _ = s.Observe(now, childDone{Kind: work, Result: childJammed})
	p.st.sched = s

	w := p.snapshot().Checks[0]
	if w.JamUntil == "" || w.ReadyAtJam != nil {
		t.Errorf("work = %+v, want jam_until set and ready_at_jam omitted", w)
	}
}

// TestPoolSnapshotCarriesDemandFieldsPerKind pins which per-kind Demand
// fields a snapshot carries: a probed kind with a count, an exit-driven kind
// with none, and a jam-gated probed kind that also carries its jam.
func TestPoolSnapshotCarriesDemandFieldsPerKind(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
	work, research, butler := KindOf(dispatchkind.Work), KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)
	cfg := testConfig(1)
	cfg.Kinds = []Kind{work, research, butler}
	cfg.ProbeIntervals = map[Kind]time.Duration{work: time.Minute, research: time.Minute}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)

	now := clk.Now()
	s := p.st.sched
	s, _ = s.Observe(now, demandProbed{Kind: work, Ready: 0})
	s, _ = s.Observe(now, demandProbed{Kind: research, Ready: 3})
	s, _ = s.Observe(now, childDone{Kind: research, Result: childJammed})
	p.st.sched = s

	checks := p.snapshot().Checks
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }

	w := checks[0]
	if w.Ready == nil || *w.Ready != 0 || w.ProbedAt != ts(0) || w.NextProbe != ts(time.Minute) {
		t.Errorf("probed work = %+v, want ready 0 (present), probed_at now, next_probe +1m", w)
	}
	if w.JamUntil != "" || w.ReadyAtJam != nil {
		t.Errorf("work carries jam fields %+v with no jam", w)
	}

	r := checks[1]
	if r.Ready == nil || *r.Ready != 3 || r.ReadyAtJam == nil || *r.ReadyAtJam != 3 || r.JamUntil == "" || !r.Jammed {
		t.Errorf("jammed research = %+v, want ready 3, ready_at_jam 3, jam_until set, jammed", r)
	}

	b := checks[2]
	if b.Ready != nil || b.ProbedAt != "" || b.NextProbe != "" || b.ReadyAtJam != nil {
		t.Errorf("exit-driven butler = %+v, want no demand fields", b)
	}

	data, err := json.Marshal(checks[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ready":0`, `"probed_at"`, `"next_probe"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("work JSON = %s, want it to contain %s", data, key)
		}
	}
}

// TestPoolSnapshotCarriesChildReportedNextDue pins the butler's next_due: none
// before a child has reported, the instant (also as nextCheck) after a timed
// report, and on_tip_move with no nextCheck after a tip-only report.
func TestPoolSnapshotCarriesChildReportedNextDue(t *testing.T) {
	clk := &testClock{now: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)}
	butler := KindOf(dispatchkind.Butler)
	cfg := testConfig(1)
	cfg.Kinds = []Kind{butler}
	var buf bytes.Buffer
	p, _ := newPool(context.Background(), cfg, &scriptedRunner{}, newTestEmitter(&buf), clk)
	now := clk.Now()

	if got := p.snapshot().Checks[0]; got.NextDue != "" {
		t.Errorf("before any report next_due = %q, want empty", got.NextDue)
	}

	due := now.Add(time.Hour)
	p.st.sched, _ = p.st.sched.Observe(now, childDone{Kind: butler, Result: childEmpty, NextDue: NextDue{At: due}})
	want := due.UTC().Format(time.RFC3339)
	got := p.snapshot().Checks[0]
	if got.NextDue != want || got.NextCheck != want {
		t.Errorf("timed report = next_due %q nextCheck %q, want both %q", got.NextDue, got.NextCheck, want)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"next_due":"`+want+`"`) {
		t.Errorf("JSON = %s, want next_due %s", data, want)
	}

	p.st.sched, _ = p.st.sched.Observe(now, childDone{Kind: butler, Result: childEmpty, NextDue: NextDue{OnTipMove: true}})
	got = p.snapshot().Checks[0]
	if got.NextDue != "on_tip_move" || got.NextCheck != "" {
		t.Errorf("tip-only report = next_due %q nextCheck %q, want on_tip_move and none", got.NextDue, got.NextCheck)
	}
	if got.NextDueOnTipMove {
		t.Error("tip-only report set next_due_on_tip_move, want it only alongside an instant")
	}

	p.st.sched, _ = p.st.sched.Observe(now, childDone{Kind: butler, Result: childEmpty, NextDue: NextDue{At: due, OnTipMove: true}})
	got = p.snapshot().Checks[0]
	if got.NextDue != want || !got.NextDueOnTipMove {
		t.Errorf("instant plus tip report = next_due %q on_tip_move %v, want %q and true", got.NextDue, got.NextDueOnTipMove, want)
	}
	if data, err = json.Marshal(got); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(data), `"next_due_on_tip_move":true`) {
		t.Errorf("JSON = %s, want next_due_on_tip_move true", data)
	}
}

// TestSlotStatus_CIWaitMarshalsUnderCIWaitKeysAndOmitsWhenEmpty pins the
// additive "ci_wait" and "pr_url" keys, absent until a ci_wait record.
func TestSlotStatus_CIWaitMarshalsUnderCIWaitKeysAndOmitsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, CIWait: true, PRURL: "https://example.test/pr/7"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"ci_wait":true`, `"pr_url":"https://example.test/pr/7"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marshalled = %s, want it to contain %q", data, want)
		}
	}
	data, err = json.Marshal(SlotStatus{Busy: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"ci_wait"`) || strings.Contains(string(data), `"pr_url"`) {
		t.Errorf("marshalled = %s, want no ci_wait keys when empty", data)
	}
}

// TestSlotStatus_CIRunURLMarshalsUnderCIRunURLKeyAndOmitsWhenEmpty pins the
// additive "ci_run_url" key, absent until a ci_wait record carries a run.
func TestSlotStatus_CIRunURLMarshalsUnderCIRunURLKeyAndOmitsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, CIWait: true, PRURL: "https://example.test/pr/7", CIRunURL: "https://example.test/runs/9"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"ci_run_url":"https://example.test/runs/9"`; !strings.Contains(string(data), want) {
		t.Errorf("marshalled = %s, want it to contain %q", data, want)
	}
	data, err = json.Marshal(SlotStatus{Busy: true, CIWait: true, PRURL: "https://example.test/pr/7"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"ci_run_url"`) {
		t.Errorf("marshalled = %s, want no ci_run_url key when empty", data)
	}
}

// TestSlotStatus_ImageBuildSinceMarshalsAndOmitsWhenEmpty pins the additive
// "image_build_since" key, absent unless a Box image build is running.
func TestSlotStatus_ImageBuildSinceMarshalsAndOmitsWhenEmpty(t *testing.T) {
	data, err := json.Marshal(SlotStatus{Busy: true, ImageBuildSince: "2026-10-12T00:00:00Z"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"image_build_since":"2026-10-12T00:00:00Z"`; !strings.Contains(string(data), want) {
		t.Errorf("marshalled = %s, want it to contain %q", data, want)
	}
	data, err = json.Marshal(SlotStatus{Busy: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "image_build") {
		t.Errorf("marshalled = %s, want no image_build key when empty", data)
	}
}

// TestProjectStatus feeds hand-built state and a fixed time to projectStatus:
// no pool, no clock, no lock.
func TestProjectStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }
	win, err := ParseWindow("09:00-17:00 UTC")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	work := KindOf(dispatchkind.Work)
	idle := func(n int) []slotState {
		ss := make([]slotState, n)
		for i := range ss {
			ss[i] = slotState{phase: PhaseIdle, since: now}
		}
		return ss
	}

	t.Run("idle with no Awake window is checking", func(t *testing.T) {
		got := projectStatus(&state{slots: idle(2)}, Config{Kinds: []Kind{work}, RepoURL: "https://example.test/r"}, now)
		if got.State != StateChecking || got.Reason != "" {
			t.Fatalf("state/reason = %q/%q, want checking and none", got.State, got.Reason)
		}
		if got.RepoURL != "https://example.test/r" || len(got.Kinds) != 1 || got.Kinds[0] != work {
			t.Errorf("repo/kinds = %q/%v, want the config's", got.RepoURL, got.Kinds)
		}
		if len(got.Slots) != 2 {
			t.Fatalf("slots = %+v, want two", got.Slots)
		}
		if g := got.Slots[1]; g.Slot != 1 || g.Phase != PhaseIdle || g.Busy || g.Since != rfc(now) {
			t.Errorf("slots = %+v, want two bare idle slots", got.Slots)
		}
		if len(got.Checks) != 1 || got.Checks[0].NextCheck != "" {
			t.Errorf("checks = %+v, want one kind with no nextCheck", got.Checks)
		}
		if got.SelfUpdate != nil || got.Trackers != nil {
			t.Errorf("self_update/trackers = %v/%v, want none", got.SelfUpdate, got.Trackers)
		}
	})

	t.Run("shut Awake window is asleep and floors nextCheck", func(t *testing.T) {
		got := projectStatus(&state{slots: idle(1)}, Config{Kinds: []Kind{work}, Awake: win}, now)
		if got.State != StateAsleep || got.Reason != "" {
			t.Fatalf("state/reason = %q/%q, want asleep and none", got.State, got.Reason)
		}
		if len(got.Checks) != 1 {
			t.Fatalf("checks = %+v, want one kind", got.Checks)
		}
		if want := rfc(now.Add(time.Hour)); got.Checks[0].NextCheck != want {
			t.Errorf("nextCheck = %q, want %q (the window opening)", got.Checks[0].NextCheck, want)
		}
	})

	t.Run("open Awake window is not asleep", func(t *testing.T) {
		got := projectStatus(&state{slots: idle(1)}, Config{Kinds: []Kind{work}, Awake: win}, now.Add(2*time.Hour))
		if got.State != StateChecking {
			t.Fatalf("state = %q, want checking", got.State)
		}
	})

	t.Run("a running slot outranks asleep and copies its issues", func(t *testing.T) {
		st := &state{slots: idle(2)}
		st.slots[1] = slotState{phase: PhaseRunning, since: now, flight: slotFlight{
			kind:     work,
			revision: "rev1",
			issues:   []string{"1", "2"},
			pass:     "implement",
			ciWait:   ciWait{pr: "pr7", runURL: "http://run"},

			imageBuildSince: now.Add(-time.Minute).In(time.FixedZone("x", 3600)),
		}}
		got := projectStatus(st, Config{Kinds: []Kind{work}, Awake: win}, now)
		if got.State != StateWorking {
			t.Fatalf("state = %q, want working", got.State)
		}
		if g := got.Slots[0]; g.Busy || g.Kind != "" || g.Issues != nil || g.ImageBuildSince != "" {
			t.Errorf("idle slot = %+v, want bare", g)
		}
		if len(got.Slots) != 2 {
			t.Fatalf("slots = %+v, want two", got.Slots)
		}
		g := got.Slots[1]
		if want := rfc(now.Add(-time.Minute)); g.ImageBuildSince != want {
			t.Errorf("ImageBuildSince = %q, want %q (RFC3339 UTC)", g.ImageBuildSince, want)
		}
		if !g.Busy || g.Kind != work || g.Revision != "rev1" || g.Pass != "implement" || !g.CIWait || g.PRURL != "pr7" || g.CIRunURL != "http://run" {
			t.Errorf("running slot = %+v", g)
		}
		st.slots[1].flight.issues[0] = "mutated"
		if len(g.Issues) != 2 || g.Issues[0] != "1" {
			t.Errorf("issues = %v, want a copy unaffected by later writes", g.Issues)
		}
	})

	t.Run("halted outranks everything and carries the halt reason", func(t *testing.T) {
		h := Halt{Class: HaltBreaker, Detail: "d"}
		st := &state{slots: idle(1), halted: true, reason: h}
		got := projectStatus(st, Config{Kinds: []Kind{work}, Awake: win}, now)
		if got.State != StateHalted || got.Reason != h.String() {
			t.Fatalf("state/reason = %q/%q, want halted/%q", got.State, got.Reason, h.String())
		}
	})

	t.Run("self-update carries its since", func(t *testing.T) {
		since := now.Add(-time.Minute)
		st := &state{slots: idle(1), self: SelfUpdate{Phase: SelfUpdateBuilding, Revision: "r"}, selfSince: since}
		got := projectStatus(st, Config{Kinds: []Kind{work}}, now)
		if got.SelfUpdate == nil || got.SelfUpdate.Phase != SelfUpdateBuilding || got.SelfUpdate.Since != rfc(since) {
			t.Fatalf("self_update = %+v, want building since %s", got.SelfUpdate, rfc(since))
		}
	})

	t.Run("a walk-exhausted window names the reason on asleep and working", func(t *testing.T) {
		w, at := exhaustingWindow(t)
		for _, running := range []bool{false, true} {
			st := &state{slots: idle(1)}
			want := StateAsleep
			if running {
				st.slots[0].phase, want = PhaseRunning, StateWorking
			}
			got := projectStatus(st, Config{Kinds: []Kind{work}, Awake: w}, at)
			if got.State != want || got.Reason != walkExhaustedReason {
				t.Errorf("running=%v: state/reason = %q/%q, want %q/%q", running, got.State, got.Reason, want, walkExhaustedReason)
			}
		}
	})

	// One tracker for work and research: a limit on either pauses both and
	// shows once under Trackers.
	t.Run("a schedule projects jammed, rate limits and per-kind fields", func(t *testing.T) {
		research, butler := KindOf(dispatchkind.Research), KindOf(dispatchkind.Butler)
		kinds := []Kind{work, research, butler}
		sched := newSchedule(kinds, 0, time.Minute, time.Hour,
			map[Kind]time.Duration{work: time.Minute, research: time.Minute},
			map[Kind]string{work: "gh", research: "gh"})
		due := now.Add(2 * time.Hour)
		for _, ev := range []schedEvent{
			demandProbed{Kind: work, Ready: 2},
			childDone{Kind: work, Result: childJammed},
			demandProbed{Kind: research, Ready: 3},
			demandRateLimited{Kind: research, Reset: now.Add(10 * time.Minute)},
			childDone{Kind: butler, Result: childEmpty, NextDue: NextDue{At: due}},
		} {
			sched, _ = sched.Observe(now, ev)
		}

		got := projectStatus(&state{slots: idle(1), sched: sched}, Config{Kinds: kinds}, now)
		if got.State != StateJammed {
			t.Fatalf("state = %q, want jammed", got.State)
		}
		if len(got.Checks) != 3 {
			t.Fatalf("checks = %+v, want three kinds", got.Checks)
		}
		limit := rfc(now.Add(10 * time.Minute))
		if len(got.Trackers) != 1 || got.Trackers[0].Tracker != "gh" || got.Trackers[0].RateLimitedUntil != limit {
			t.Errorf("trackers = %+v, want gh limited until %s once", got.Trackers, limit)
		}
		w := got.Checks[0]
		if !w.Jammed || w.Ready == nil || *w.Ready != 2 || w.ReadyAtJam == nil || *w.ReadyAtJam != 2 ||
			w.ProbedAt != rfc(now) || w.NextProbe != limit || w.JamUntil != rfc(now.Add(time.Minute)) || w.NextCheck != limit {
			t.Errorf("jammed work = %+v, want ready/ready_at_jam 2, probed now, next_probe and nextCheck at the limit, jam_until +1m", w)
		}
		if r := got.Checks[1]; r.Jammed || r.Ready == nil || *r.Ready != 3 || r.JamUntil != "" || r.NextProbe != limit {
			t.Errorf("research = %+v, want unjammed, ready 3, next_probe at the limit", r)
		}
		if b := got.Checks[2]; b.Ready != nil || b.NextDue != rfc(due) || b.NextCheck != rfc(due) {
			t.Errorf("butler = %+v, want no count and next_due/nextCheck %s", b, rfc(due))
		}
	})

	t.Run("a gated kind with no jam is waiting", func(t *testing.T) {
		sched := newSchedule([]Kind{work}, 0, time.Minute, time.Hour, map[Kind]time.Duration{work: time.Minute}, nil)
		sched, _ = sched.Observe(now, demandProbed{Kind: work, Ready: 0})
		got := projectStatus(&state{slots: idle(1), sched: sched}, Config{Kinds: []Kind{work}}, now)
		if got.State != StateWaiting {
			t.Fatalf("state = %q, want waiting", got.State)
		}
		if len(got.Checks) != 1 || got.Checks[0].Jammed || got.Checks[0].NextCheck != rfc(now.Add(time.Minute)) {
			t.Errorf("checks = %+v, want unjammed, nextCheck +1m", got.Checks)
		}
		if len(got.Trackers) != 1 || got.Trackers[0].RateLimitedUntil != "" {
			t.Errorf("trackers = %+v, want one unlimited", got.Trackers)
		}
	})

	t.Run("kinds are copied, not aliased", func(t *testing.T) {
		cfg := Config{Kinds: []Kind{work}}
		got := projectStatus(&state{slots: idle(1)}, cfg, now)
		got.Kinds[0] = KindOf(dispatchkind.Research)
		if cfg.Kinds[0] != work {
			t.Errorf("cfg.Kinds[0] = %v after editing the Status, want the projection to copy", cfg.Kinds[0])
		}
	})
}
