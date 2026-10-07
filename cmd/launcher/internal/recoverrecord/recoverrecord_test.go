package recoverrecord

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/seambundle"
)

const unit = time.Minute

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func seedBundle(t *testing.T, pwd, key string) {
	t.Helper()
	dir := hostpaths.OutboxDir(pwd, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, seambundle.FileName), []byte("bundle-"+key), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedRecord(t *testing.T, pwd, num string, r Record) {
	t.Helper()
	id, err := BundleID(pwd, num)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bundle == "" {
		r.Bundle = id
	}
	if err := os.MkdirAll(hostpaths.LogDir(pwd), 0o755); err != nil {
		t.Fatal(err)
	}
	loaded := Load(pwd, num, r.Bundle)
	loaded.Count, loaded.Last, loaded.GaveUp = r.Count, r.Last, r.GaveUp
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, pwd string, max int) int {
	t.Helper()
	n, err := CountEligible(pwd, max, unit, now)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCountEligible(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, pwd string)
		want int
	}{
		{"no outbox dir", func(*testing.T, string) {}, 0},
		{"fresh bundle", func(t *testing.T, pwd string) { seedBundle(t, pwd, "1") }, 1},
		{"in backoff", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 1, Last: now.Add(-30 * time.Second)})
		}, 0},
		{"backoff elapsed", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 1, Last: now.Add(-2 * time.Minute)})
		}, 1},
		{"at bound, give-up unposted, backoff elapsed", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 3, Last: now.Add(-time.Hour)})
		}, 1},
		{"at bound, give-up unposted, in backoff", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 3, Last: now})
		}, 1},
		{"past a lowered bound, give-up unposted", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 7, Last: now.Add(-time.Hour)})
		}, 1},
		{"at bound, gave up", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 3, Last: now.Add(-time.Hour), GaveUp: true})
		}, 0},
		{"gave up", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Count: 1, Last: now.Add(-time.Hour), GaveUp: true})
		}, 0},
		{"record for another bundle", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedRecord(t, pwd, "1", Record{Bundle: "older", Count: 3, Last: now, GaveUp: true})
		}, 1},
		{"butler key", func(t *testing.T, pwd string) { seedBundle(t, pwd, "butler-bugs") }, 0},
		{"dir without bundle", func(t *testing.T, pwd string) {
			if err := os.MkdirAll(hostpaths.OutboxDir(pwd, "1"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"stray file is not a dir", func(t *testing.T, pwd string) {
			if err := os.MkdirAll(hostpaths.OutboxRoot(pwd), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hostpaths.OutboxRoot(pwd), "7"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"counts each eligible bundle", func(t *testing.T, pwd string) {
			seedBundle(t, pwd, "1")
			seedBundle(t, pwd, "2")
			seedBundle(t, pwd, "3")
			seedRecord(t, pwd, "3", Record{Count: 3, GaveUp: true})
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pwd := t.TempDir()
			tc.seed(t, pwd)
			if got := count(t, pwd, 3); got != tc.want {
				t.Errorf("CountEligible = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCountEligible_OutboxRootUnreadable(t *testing.T) {
	pwd := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(hostpaths.OutboxRoot(pwd)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostpaths.OutboxRoot(pwd), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CountEligible(pwd, 3, unit, now); err == nil {
		t.Fatal("want error when the outbox root is not a directory")
	}
}

func TestExhaustedAndBackingOff(t *testing.T) {
	if !(Record{GaveUp: true}).Exhausted(5) || !(Record{Count: 5}).Exhausted(5) || (Record{Count: 4}).Exhausted(5) {
		t.Error("Exhausted: GaveUp or Count >= max only")
	}
	r := Record{Count: 2, Last: now}
	if !r.BackingOff(now.Add(time.Minute), unit) || r.BackingOff(now.Add(2*time.Minute), unit) {
		t.Error("BackingOff: held until Last + unit*Count")
	}
}

func TestDue(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want bool
	}{
		{"fresh", Record{}, true},
		{"below bound, in backoff", Record{Count: 1, Last: now}, false},
		{"below bound, backoff elapsed", Record{Count: 1, Last: now.Add(-2 * time.Minute)}, true},
		{"at bound, give-up unposted, in backoff", Record{Count: 3, Last: now}, true},
		{"at bound, give-up unposted, backoff elapsed", Record{Count: 3, Last: now.Add(-time.Hour)}, true},
		{"at bound, gave up", Record{Count: 3, Last: now.Add(-time.Hour), GaveUp: true}, false},
		{"below bound, gave up", Record{Count: 1, Last: now.Add(-time.Hour), GaveUp: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rec.Due(now, 3, unit); got != tc.want {
				t.Errorf("Due = %v, want %v", got, tc.want)
			}
		})
	}
}
