package dispatch

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// stampBoxOutput is what each fake Box writes: one pass with a turn and a
// result, enough for the ingester to count a pass in that log.
func stampBoxOutput(n int) []byte {
	return []byte(claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) +
		`{"type":"assistant","message":{"id":"a` + fmt.Sprint(n) + `","model":"m","content":[]}}` + "\n" +
		`{"type":"result","timestamp":"2026-05-01T09:00:00Z","num_turns":1,"total_cost_usd":1,` +
		`"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"m":{}}}` + "\n")
}

// TestDispatchStamp_EveryPassLogCarriesOneRecordID drives a real Factory
// through a retried attempt, a fix pass and a conflict resolve and reads the
// logs back through the ingester: all four are one stamped Record whose ID
// matches the record_id every box report carried.
func TestDispatchStamp_EveryPassLogCarriesOneRecordID(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	cfg := retryConfig(3, 0, 0)
	cfg.Kind = "work"
	cfg.Stamp = claude.DispatchStart{
		Revision:      "abc123",
		RoleModels:    map[string]string{"main": "opus"},
		DriverVersion: "9.9.9",
		Knobs:         map[string]string{"MAX_PASSES": "4"},
	}

	fr := runner.NewFake()
	var mu sync.Mutex
	calls := 0
	fr.RunFunc = func(box runner.Box) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		box.Output.Write(stampBoxOutput(n)) //nolint:errcheck
		if n == 1 {
			return boxErr
		}
		return nil
	}
	// Only the first attempt's failure is transient; a later classification
	// is terminal so the retry loop ends after the one rotated attempt.
	classified := false
	drv := fakeDriver{ClassifyFn: func(string) (driver.Classification, error) {
		if !classified {
			classified = true
			return driver.Classification{Class: driver.Transient, Reason: driver.Overloaded}, nil
		}
		return driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed}, nil
	}}
	// Each Now() moves forward so every log's Started stamp differs, as the
	// ingester keys a log's passes on it.
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	var tick int
	clock := Clock{
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			tick++
			return base.Add(time.Duration(tick) * time.Minute)
		},
		Sleep: func(time.Duration) {},
	}
	d := newTestDispatch(t, cfg, fr, drv, clock)

	testutil.CaptureStdout(t, func() {
		d.Run()
		d.Fix(1, "")
		if err := d.ResolveConflict("https://example.test/pr/1"); err != nil {
			t.Errorf("ResolveConflict: %v", err)
		}
	})

	recs := readRecords()
	if len(recs) != 4 {
		t.Fatalf("box records = %d, want 4 (two attempts, fix, conflict): %+v", len(recs), recs)
	}
	id := recs[0].RecordID
	if id == "" {
		t.Fatalf("first box record has no record_id: %+v", recs[0])
	}
	for _, r := range recs {
		if r.Event != "box" || r.RecordID != id {
			t.Errorf("record %+v: want a box record with record_id %q", r, id)
		}
	}

	s, err := dispatchrecord.Open(d.pwd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Ingest(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("records = %+v, want exactly one", got)
	}
	r := got[0]
	if r.ID != id || r.Attribution != dispatchrecord.AttributionStamped {
		t.Errorf("record id/attribution = %q/%q, want %q/stamped", r.ID, r.Attribution, id)
	}
	if r.Kind != "work" || r.DispatchKey != "1" {
		t.Errorf("kind/key = %q/%q, want work/1", r.Kind, r.DispatchKey)
	}
	if r.Revision != "abc123" || r.Driver != "fake" || r.DriverVersion != "9.9.9" ||
		!reflect.DeepEqual(r.RoleModels, cfg.Stamp.RoleModels) || !reflect.DeepEqual(r.Knobs, cfg.Stamp.Knobs) {
		t.Errorf("stamp facts lost: %+v", r)
	}
	if len(r.Passes) != 4 {
		t.Errorf("passes = %d, want 4 (one per log)", len(r.Passes))
	}
	if want := dispatchrecord.RecordID("work", "1", r.ClaimTime); r.ID != want {
		t.Errorf("id %q does not derive from kind/key/claim: want %q", r.ID, want)
	}
	if recs[0].Key != dispatchkey.Issue("1") {
		t.Errorf("key = %v", recs[0].Key)
	}
}

// afterStamp checks that a log opens with the dispatch_start stamp and returns
// everything after it, so byte-exactness tests keep pinning the Box's output.
func afterStamp(t *testing.T, log []byte) string {
	t.Helper()
	line, rest, ok := strings.Cut(string(log), "\n")
	if !ok || !strings.Contains(line, `"op":"dispatch_start"`) {
		t.Fatalf("log does not open with a dispatch_start stamp: %q", log)
	}
	return rest
}

// A ReadOnlyBox kind runs read-only whatever the raw knob says, so its stamp
// must record the effective value without touching the Stamp every Dispatch
// shares.
func TestDispatchStart_ReadOnlyBoxKindStampsEffectiveAccess(t *testing.T) {
	cfg := retryConfig(1, 0, 0)
	cfg.Kind = "butler"
	cfg.BoxForgeAndIssueAccess = "read-write"
	cfg.Stamp = claude.DispatchStart{Knobs: map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-write", "MAX_PASSES": "4"}}
	d := newTestDispatch(t, cfg, runner.NewFake(), fakeDriver{}, Clock{Now: time.Now, Sleep: func(time.Duration) {}})

	got := d.dispatchStart().Knobs
	want := map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-only", "MAX_PASSES": "4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stamped knobs = %v, want %v", got, want)
	}
	if cfg.Stamp.Knobs["BOX_FORGE_AND_ISSUE_ACCESS"] != "read-write" {
		t.Errorf("Config.Stamp.Knobs mutated: %v", cfg.Stamp.Knobs)
	}
}

// TestDispatchStamp_AdoptedFixContinuesPriorRecord pins that a Dispatch which
// never Ran (recover adopting an open PR) continues the Record stamped at the
// head of the primary log, so its fix and conflict logs and a later
// dispatch_settled op land on that Record, not a fresh one.
func TestDispatchStamp_AdoptedFixContinuesPriorRecord(t *testing.T) {
	cfg := retryConfig(1, 0, 0)
	cfg.Kind = "work"
	claim := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	prior := dispatchrecord.RecordID("work", "1", claim)

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		box.Output.Write(stampBoxOutput(1)) //nolint:errcheck
		return nil
	}
	clock := Clock{Now: func() time.Time { return time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC) }, Sleep: func(time.Duration) {}}
	d := newTestDispatch(t, cfg, fr, fakeDriver{}, clock)
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{
		RecordID: prior, Kind: "work", DispatchKey: "1", ClaimTime: claim, Started: claim,
	}})
	if err := writeFile(d.logPath(), stamp+string(stampBoxOutput(0))); err != nil {
		t.Fatal(err)
	}

	testutil.CaptureStdout(t, func() { d.Fix(1, "") })

	if got := d.RecordID(); got != prior {
		t.Fatalf("RecordID() = %q, want the primary log's %q", got, prior)
	}
	if got, _ := dispatchrecord.ReadStamp(d.fixLogPath(1)); got.RecordID != prior {
		t.Errorf("fix log stamp = %q, want %q", got.RecordID, prior)
	}
	rec, _, err := dispatchrecord.ParseLog(d.fixLogPath(1))
	if err != nil || !rec.ClaimTime.Equal(claim) {
		t.Errorf("fix log claim time = %v (err %v), want %v", rec.ClaimTime, err, claim)
	}
}

// TestDispatchStamp_FixWithoutPrimaryLogMintsFresh covers the orphan PR: no
// stamped primary log survives, so Fix mints a Record of its own.
func TestDispatchStamp_FixWithoutPrimaryLogMintsFresh(t *testing.T) {
	cfg := retryConfig(1, 0, 0)
	cfg.Kind = "work"
	fr := runner.NewFake()
	now := time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC)
	clock := Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}
	d := newTestDispatch(t, cfg, fr, fakeDriver{}, clock)

	testutil.CaptureStdout(t, func() { d.Fix(1, "") })

	if want := dispatchrecord.RecordID("work", "1", now); d.RecordID() != want {
		t.Errorf("RecordID() = %q, want freshly minted %q", d.RecordID(), want)
	}
}

// TestEnsureRecordID_WarnsOnUnreadablePrimaryLog: a primary log that exists but
// cannot be read may belong to a Record the fix pass is about to split, so
// minting fresh must not be silent. A missing or unstamped log is the
// ordinary orphan case and stays quiet.
func TestEnsureRecordID_WarnsOnUnreadablePrimaryLog(t *testing.T) {
	cfg := retryConfig(1, 0, 0)
	cfg.Kind = "work"
	now := time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC)
	clock := Clock{Now: func() time.Time { return now }, Sleep: func(time.Duration) {}}

	t.Run("unreadable", func(t *testing.T) {
		d := newTestDispatch(t, cfg, runner.NewFake(), fakeDriver{}, clock)
		if err := os.MkdirAll(d.logPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		stderr := captureStderr(t, d.ensureRecordID)
		if !strings.Contains(stderr, "#"+d.number+": primary log") {
			t.Errorf("stderr = %q, want a primary-log warning", stderr)
		}
		if d.RecordID() == "" {
			t.Error("RecordID() empty, want a fresh mint")
		}
	})
	t.Run("missing", func(t *testing.T) {
		d := newTestDispatch(t, cfg, runner.NewFake(), fakeDriver{}, clock)
		if stderr := captureStderr(t, d.ensureRecordID); stderr != "" {
			t.Errorf("stderr = %q, want none", stderr)
		}
	})
}

// The host token is the provenance a dispatch_settled op must echo (issue
// #4812): fresh per stamp, and never part of the env a Box can read.
func TestDispatchStart_MintsFreshHostTokenKeptOutOfBoxEnv(t *testing.T) {
	cfg := retryConfig(1, 0, 0)
	cfg.Kind = "work"
	d := newTestDispatch(t, cfg, runner.NewFake(), fakeDriver{}, Clock{Now: time.Now, Sleep: func(time.Duration) {}})

	a, b := d.dispatchStart(), d.dispatchStart()
	if a.HostToken == "" || b.HostToken == "" {
		t.Fatalf("stamps carry no host token: %q, %q", a.HostToken, b.HostToken)
	}
	if a.HostToken == b.HostToken {
		t.Errorf("two stamps share host token %q", a.HostToken)
	}

	// The token a real run stamps into its log is the one no Box env may hold.
	fr := runner.NewFake()
	run := newTestDispatch(t, cfg, fr, fakeDriver{}, Clock{Now: time.Now, Sleep: func(time.Duration) {}})
	testutil.CaptureStdout(t, func() { run.Run() })
	stamp, ok := dispatchrecord.ReadStamp(run.logPath())
	if !ok || stamp.HostToken == "" {
		t.Fatalf("run log stamp = %+v ok %v, want a host token", stamp, ok)
	}
	if len(fr.RunCalls) == 0 {
		t.Fatal("fake runner saw no Box")
	}
	for _, box := range fr.RunCalls {
		for k, v := range box.Env {
			if strings.Contains(v, stamp.HostToken) {
				t.Errorf("Box env %s leaks the host token", k)
			}
		}
	}
}
