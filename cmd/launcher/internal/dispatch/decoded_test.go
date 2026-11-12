package dispatch

import (
	"os"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/runner"
)

func countDecodes() (fakeDriver, map[string]int) {
	n := map[string]int{}
	return fakeDriver{SessionTotals: true, OnDecode: func(p string) { n[p]++ }}, n
}

// A non-zero exit with no outcome classifies the attempt log, then settle reads
// its usage twice over (CumulativeUsage, UsageReport). The host decodes the log
// once for all of them.
func TestDecoded_FailedAttemptDecodesEachLogOnce(t *testing.T) {
	fr := runner.NewFake()
	fr.RunErr = boxErr
	fr.WriteToOutput = []byte(sessionResult(3, 1000, 500, driverkit.Tokens{Input: 100, Output: 50, CostUSD: 0.10}))
	drv, decodes := countDecodes()
	d := newTestDispatch(t, retryConfig(3, 0, 0), fr, drv, RealClock())

	if d.Run().ok() {
		t.Fatal("want a failed dispatch")
	}
	for range 2 {
		if got := d.CumulativeUsage(); got.InputTokens != 100 {
			t.Fatalf("CumulativeUsage = %+v, want the attempt's usage", got)
		}
		d.UsageReport()
	}

	if len(decodes) != 1 {
		t.Fatalf("decoded logs = %v, want exactly the one attempt log", decodes)
	}
	for p, n := range decodes {
		if n != 1 {
			t.Errorf("%s decoded %d times, want once", p, n)
		}
	}
}

// Attempt logs rotate aside on retry (#561): the memo follows the file, not the
// path, so a rotated log hits and the new log at the old path misses.
func TestDecoded_FollowsFileIdentityAcrossRotation(t *testing.T) {
	drv, decodes := countDecodes()
	d := newTestDispatch(t, Config{}, runner.NewFake(), drv, RealClock())
	path := d.logPath()

	if err := writeFile(path, "first\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.decoded(path); err != nil {
		t.Fatal(err)
	}
	if _, err := d.decoded(path); err != nil {
		t.Fatal(err)
	}
	if got := decodes[path]; got != 1 {
		t.Fatalf("decodes of %s = %d, want 1", path, got)
	}

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, "second, longer\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.decoded(path + ".1"); err != nil {
		t.Fatal(err)
	}
	if got := decodes[path+".1"]; got != 0 {
		t.Errorf("rotated log decoded %d times, want a hit on the entry decoded as the bare path", got)
	}
	if _, err := d.decoded(path); err != nil {
		t.Fatal(err)
	}
	if got := decodes[path]; got != 2 {
		t.Errorf("new log at the old path: decodes = %d, want a miss (2)", got)
	}
}

// A log that grew since it was decoded is a different read, not a hit.
func TestDecoded_GrownLogMisses(t *testing.T) {
	drv, decodes := countDecodes()
	d := newTestDispatch(t, Config{}, runner.NewFake(), drv, RealClock())
	path := d.logPath()

	if err := writeFile(path, "a\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.decoded(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("b\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := d.decoded(path); err != nil {
		t.Fatal(err)
	}
	if got := decodes[path]; got != 2 {
		t.Errorf("decodes = %d, want 2 after the log grew", got)
	}
}

// A missing log decodes empty and is not remembered: it may be written next.
func TestDecoded_MissingLogNotCached(t *testing.T) {
	drv, decodes := countDecodes()
	d := newTestDispatch(t, Config{}, runner.NewFake(), drv, RealClock())
	path := d.logPath()

	for range 2 {
		if _, err := d.decoded(path); err != nil {
			t.Fatal(err)
		}
	}
	if got := decodes[path]; got != 2 {
		t.Errorf("decodes = %d, want 2 (a missing log is never cached)", got)
	}
}
