package settle

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
)

// lateMergeClaim is the claim time writeSettledLog stamps; lateMergeNow is a
// clock a day later, inside LateMergeWindow.
var (
	lateMergeClaim = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	lateMergeNow   = lateMergeClaim.Add(24 * time.Hour)
)

// writeSettledLog writes issue-<key>.log: a stamp, one pass, then a
// dispatch_settled op with the given reason and PR.
func writeSettledLog(t *testing.T, root, key, reason, prURL string) (id, path string) {
	t.Helper()
	return writeLogAt(t, root, key, lateMergeClaim, "complete", reason, prURL)
}

func writeLogAt(t *testing.T, root, key string, ts time.Time, state, reason, prURL string) (id, path string) {
	t.Helper()
	id = "work:" + key + "@x"
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "issue-"+key+".log")
	body := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{
		RecordID: id, Kind: "work", DispatchKey: key, ClaimTime: ts, Started: ts, HostToken: "tok-" + key,
	}}) +
		`{"type":"result","timestamp":"2026-05-01T09:00:00Z","num_turns":1,"result":"done"}` + "\n" +
		claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchSettled, Settled: &claude.DispatchSettled{
			RecordID: id, State: state, Reason: reason, PRURL: prURL, HostToken: "tok-" + key,
		}})
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return id, path
}

func storedRecordsByID(t *testing.T, root string) map[string]dispatchrecord.Record {
	t.Helper()
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Ingest(); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]dispatchrecord.Record{}
	for _, r := range recs {
		out[r.ID] = r
	}
	return out
}

func TestLateMerges(t *testing.T) {
	root := t.TempDir()
	fc := forge.NewFake()
	const prA, prB, prC, prD = "https://x/pull/1", "https://x/pull/2", "https://x/pull/3", "https://x/pull/4"
	idA, pathA := writeSettledLog(t, root, "1", ReasonAutoMergeEnqueued, prA)
	idB, _ := writeSettledLog(t, root, "2", ReasonManual, prB)
	idC, pathC := writeSettledLog(t, root, "3", ReasonMergeBlocked, prC)
	idD, _ := writeSettledLog(t, root, "4", ReasonManual, prD) // PR unknown to the forge: PRState errors
	fc.SetPR("a", forge.PR{URL: prA})
	fc.SetPR("b", forge.PR{URL: prB})
	fc.SetPR("c", forge.PR{URL: prC})
	fc.SetPRState(prA, forge.PRMerged)
	fc.SetPRState(prB, forge.PRClosed)
	fc.SetPRState(prC, forge.PRMerged)

	// Ingest, then lose C's log: its Record is stored but cannot be appended to.
	storedRecordsByID(t, root)
	if err := os.Remove(pathC); err != nil {
		t.Fatal(err)
	}

	var w bytes.Buffer
	merged, err := LateMerges(root, fc, fc, lateMergeNow, &w)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0] != idA {
		t.Fatalf("merged = %v, want [%s]", merged, idA)
	}
	if !strings.Contains(w.String(), idD) || strings.Count(w.String(), "??") != 1 {
		t.Fatalf("warnings = %q, want exactly one naming %s", w.String(), idD)
	}

	recs := storedRecordsByID(t, root)
	a := recs[idA]
	if a.Outcome != "complete" || a.Reason != ReasonMerged || a.PRURL != prA {
		t.Fatalf("A = %+v, want complete/merged keeping its PR", a)
	}
	if recs[idB].Reason != ReasonManual {
		t.Fatalf("B reason = %q, want unchanged manual (PR closed unmerged)", recs[idB].Reason)
	}
	if c, ok := recs[idC]; !ok || c.Reason != ReasonMergeBlocked {
		t.Fatalf("C reason = %q, want present and unchanged merge-blocked", recs[idC].Reason)
	}

	before, err := os.Stat(pathA)
	if err != nil {
		t.Fatal(err)
	}
	w.Reset()
	merged, err = LateMerges(root, fc, fc, lateMergeNow, &w)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(pathA)
	if len(merged) != 0 || after.Size() != before.Size() {
		t.Fatalf("second run merged %v, size %d -> %d; want no-op", merged, before.Size(), after.Size())
	}
}

func TestLateMerges_NoLogDirLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	merged, err := LateMerges(root, forge.NewFake(), forge.NewFake(), lateMergeNow, &bytes.Buffer{})
	if err != nil || len(merged) != 0 {
		t.Fatalf("LateMerges = %v, %v; want no-op", merged, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("root has %d entries, want none (store must not be created)", len(entries))
	}
}

// askedPR records the URLs LateMerges hands to PRState.
type askedPR struct {
	forge.PRForge
	asked []string
}

func (a *askedPR) PRState(u string) (forge.PRState, error) {
	a.asked = append(a.asked, u)
	return a.PRForge.PRState(u)
}

// Only a Complete settle with a ReasonLeavesPROpen reason, a usable PR URL, and a claim
// inside LateMergeWindow is asked about; the forge must never see the rest, and
// a PR that merged outside the filter stays as stored.
func TestLateMerges_SkipsRecordsOutsideTheCandidateSet(t *testing.T) {
	const pr = "https://x/pull/9"
	old := lateMergeNow.Add(-LateMergeWindow - time.Hour)
	for _, tc := range []struct {
		name  string
		claim time.Time
		state string
		url   string
	}{
		{"older than the window", old, "complete", pr},
		{"failed outcome", lateMergeClaim, "failed", pr},
		{"empty PR URL", lateMergeClaim, "complete", ""},
		{"flag-shaped PR URL", lateMergeClaim, "complete", "--repo=evil"},
		{"relative PR URL", lateMergeClaim, "complete", "/pull/9"},
		{"non-http PR URL", lateMergeClaim, "complete", "file:///etc/passwd"},
		{"hostless PR URL", lateMergeClaim, "complete", "https:///pull/9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fake := forge.NewFake()
			fake.SetPRState(pr, forge.PRMerged)
			fake.SetPRState(tc.url, forge.PRMerged)
			fc := &askedPR{PRForge: fake}
			id, path := writeLogAt(t, root, "9", tc.claim, tc.state, ReasonManual, tc.url)
			before, _ := os.Stat(path)

			var w bytes.Buffer
			merged, err := LateMerges(root, fc, nil, lateMergeNow, &w)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(path)
			if len(merged) != 0 || w.Len() != 0 || after.Size() != before.Size() {
				t.Fatalf("merged %v, warnings %q, size %d -> %d; want untouched", merged, w.String(), before.Size(), after.Size())
			}
			if got := storedRecordsByID(t, root)[id].Reason; got != ReasonManual {
				t.Fatalf("reason = %q, want unchanged manual", got)
			}
			if len(fc.asked) != 0 {
				t.Fatalf("forge asked about %v, want nothing", fc.asked)
			}
		})
	}
}

func TestLateMerges_UpgradesClaimJustInsideTheWindow(t *testing.T) {
	root := t.TempDir()
	fc := forge.NewFake()
	const pr = "https://x/pull/9"
	fc.SetPRState(pr, forge.PRMerged)
	id, _ := writeLogAt(t, root, "9", lateMergeNow.Add(-LateMergeWindow+time.Hour), "complete", ReasonManual, pr)
	merged, err := LateMerges(root, fc, fc, lateMergeNow, &bytes.Buffer{})
	if err != nil || len(merged) != 1 || merged[0] != id {
		t.Fatalf("merged = %v, %v; want [%s]", merged, err, id)
	}
}

// A rate-limited forge ends the sweep: one PRState call, no per-Record warning
// flood, and the error names the rate limit.
func TestLateMerges_StopsAtFirstRateLimit(t *testing.T) {
	root := t.TempDir()
	fake := forge.NewFake()
	const prA, prB, prC = "https://x/pull/1", "https://x/pull/2", "https://x/pull/3"
	for i, u := range []string{prA, prB, prC} {
		writeSettledLog(t, root, fmt.Sprint(i+1), ReasonManual, u)
		fake.SetPRState(u, forge.PRMerged)
	}
	fake.PRStateErr = &forge.RateLimitError{Err: errors.New("HTTP 403")}
	fc := &askedPR{PRForge: fake}

	var w bytes.Buffer
	merged, err := LateMerges(root, fc, nil, lateMergeNow, &w)
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("err = %v, want one wrapping forge.ErrRateLimit", err)
	}
	if len(fc.asked) != 1 || len(merged) != 0 {
		t.Fatalf("asked %v, merged %v; want exactly one PRState call and no merges", fc.asked, merged)
	}
	if n := strings.Count(w.String(), "??"); n != 0 {
		t.Fatalf("warnings = %q, want none", w.String())
	}
}

// Merges appended before the rate limit hits are kept and returned.
func TestLateMerges_RateLimitKeepsEarlierMerges(t *testing.T) {
	root := t.TempDir()
	fake := forge.NewFake()
	for _, k := range []string{"1", "2", "3"} {
		writeSettledLog(t, root, k, ReasonManual, "https://x/pull/"+k)
		fake.SetPRState("https://x/pull/"+k, forge.PRMerged)
	}
	fake.PRStateErrs = []error{nil, &forge.RateLimitError{Err: errors.New("HTTP 403")}}
	fc := &askedPR{PRForge: fake}

	merged, err := LateMerges(root, fc, nil, lateMergeNow, &bytes.Buffer{})
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("err = %v, want one wrapping forge.ErrRateLimit", err)
	}
	if len(fc.asked) != 2 || len(merged) != 1 {
		t.Fatalf("asked %v, merged %v; want two calls and one merge", fc.asked, merged)
	}
	if got := storedRecordsByID(t, root)[merged[0]].Reason; got != ReasonMerged {
		t.Fatalf("reason = %q, want merged (re-ingested before returning)", got)
	}
}

func TestClaimLateMergeSweep_Throttles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(hostpaths.LogDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	claim := func(at time.Time) bool {
		t.Helper()
		ok, err := ClaimLateMergeSweep(root, at)
		if err != nil {
			t.Fatalf("ClaimLateMergeSweep(%v): %v", at, err)
		}
		return ok
	}
	if !claim(lateMergeNow) {
		t.Fatal("first claim refused, want ok")
	}
	if claim(lateMergeNow.Add(time.Minute)) {
		t.Fatal("claim inside LateMergeSweepInterval ok, want refused")
	}
	if !claim(lateMergeNow.Add(LateMergeSweepInterval)) {
		t.Fatal("claim at LateMergeSweepInterval refused, want ok")
	}
}

// A stamp ahead of now (clock stepped back, or a hand-edited file) must not
// refuse claims until the clock catches up.
func TestClaimLateMergeSweep_FutureStampIsStale(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(hostpaths.LogDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := ClaimLateMergeSweep(root, lateMergeNow.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("seed claim = ok %v, err %v, want ok", ok, err)
	}
	if ok, err := ClaimLateMergeSweep(root, lateMergeNow); err != nil || !ok {
		t.Fatalf("claim behind a future stamp = ok %v, err %v, want ok", ok, err)
	}
}

func TestClaimLateMergeSweep_RefusedWhileAnotherClaimantHoldsTheLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(hostpaths.LogDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(hostpaths.LateMergeSweepLock(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if ok, err := ClaimLateMergeSweep(root, lateMergeNow); err != nil || ok {
		t.Fatalf("claim while locked = ok %v, err %v, want refused", ok, err)
	}
	_ = f.Close()
	if ok, err := ClaimLateMergeSweep(root, lateMergeNow); err != nil || !ok {
		t.Fatalf("claim after unlock = ok %v, err %v, want ok", ok, err)
	}
}

func TestClaimLateMergeSweep_NoLogDirCreatesNothing(t *testing.T) {
	root := t.TempDir()
	if ok, err := ClaimLateMergeSweep(root, lateMergeNow); err != nil || ok {
		t.Fatalf("claim = ok %v, err %v, want refused", ok, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("root has %d entries, want none", len(entries))
	}
}

// A late merge's re-settle carries the forge's merge commit onto the Record;
// a failed read still upgrades the Record, warns, and leaves it empty.
func TestLateMerges_FillsMergeCommit(t *testing.T) {
	root := t.TempDir()
	fc := forge.NewFake()
	const prA, prB = "https://x/pull/1", "https://x/pull/2"
	idA, _ := writeSettledLog(t, root, "1", ReasonManual, prA)
	fc.SetPR("a", forge.PR{URL: prA})
	fc.SetPRState(prA, forge.PRMerged)
	fc.SetMergeCommit(prA, "abc123")

	merged, err := LateMerges(root, fc, fc, lateMergeNow, &bytes.Buffer{})
	if err != nil || len(merged) != 1 {
		t.Fatalf("LateMerges = %v, %v; want one merged", merged, err)
	}
	if got := storedRecordsByID(t, root)[idA].MergeCommit; got != "abc123" {
		t.Fatalf("MergeCommit = %q, want abc123", got)
	}

	idB, _ := writeSettledLog(t, root, "2", ReasonManual, prB)
	fc.SetPR("b", forge.PR{URL: prB})
	fc.SetPRState(prB, forge.PRMerged)
	fc.MergeCommitErr = errors.New("boom")
	var w bytes.Buffer
	merged, err = LateMerges(root, fc, fc, lateMergeNow, &w)
	if err != nil || len(merged) != 1 || merged[0] != idB {
		t.Fatalf("LateMerges = %v, %v; want %s merged despite the failed read", merged, err, idB)
	}
	b := storedRecordsByID(t, root)[idB]
	if b.Reason != ReasonMerged || b.MergeCommit != "" {
		t.Fatalf("B = reason %q merge commit %q, want merged with none", b.Reason, b.MergeCommit)
	}
	if !strings.Contains(w.String(), "boom") || strings.Count(w.String(), "??") != 1 {
		t.Fatalf("warnings = %q, want one naming the read failure", w.String())
	}
}
