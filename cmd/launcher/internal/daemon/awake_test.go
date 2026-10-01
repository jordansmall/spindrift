package daemon

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseWindowEmptyIsAlwaysAwake(t *testing.T) {
	w, err := ParseWindow("")
	if err != nil {
		t.Fatalf("ParseWindow(\"\") err = %v, want nil", err)
	}
	if w != nil {
		t.Fatalf("ParseWindow(\"\") = %+v, want nil", w)
	}

	now := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	if !w.Open(now) {
		t.Fatalf("nil Window.Open() = false, want true (always awake)")
	}
	if got, exhausted := w.Until(now); got != 0 || exhausted {
		t.Fatalf("nil Window.Until() = (%v, %v), want (0, false)", got, exhausted)
	}
}

func TestWindowLocationNilIsUTC(t *testing.T) {
	var w *Window
	if got := w.Location(); got != time.UTC {
		t.Fatalf("nil Window.Location() = %v, want time.UTC", got)
	}
}

func TestWindowLocationConfigured(t *testing.T) {
	w, err := ParseWindow("22:00-06:00 Europe/London")
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	// time.LoadLocation does not guarantee returning the same *Location
	// pointer across calls, so compare by name rather than identity.
	if got := w.Location().String(); got != "Europe/London" {
		t.Fatalf("Window.Location() = %v, want Europe/London", got)
	}
}

func TestParseWindowWhitespaceTrimmed(t *testing.T) {
	w, err := ParseWindow("  22:00-06:00 UTC  ")
	if err != nil {
		t.Fatalf("ParseWindow with surrounding whitespace: err = %v, want nil", err)
	}
	if w == nil {
		t.Fatalf("ParseWindow with surrounding whitespace = nil, want a Window")
	}
}

func TestParseWindowErrors(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantSub string // substring the error must contain
	}{
		{
			name:    "missing space before zone",
			raw:     "22:00-06:00Europe/London",
			wantSub: `want "HH:MM-HH:MM Zone"`,
		},
		{
			name:    "double space before zone",
			raw:     "22:00-06:00  UTC",
			wantSub: `want "HH:MM-HH:MM Zone"`,
		},
		{
			name:    "single-digit hour",
			raw:     "2:00-06:00 Europe/London",
			wantSub: `want "HH:MM-HH:MM Zone"`,
		},
		{
			name:    "hour out of range",
			raw:     "24:00-06:00 UTC",
			wantSub: `hour "24" out of range 0-23`,
		},
		{
			name:    "minute out of range",
			raw:     "22:00-06:60 UTC",
			wantSub: `minute "60" out of range 0-59`,
		},
		{
			name:    "start equals end",
			raw:     "09:00-09:00 UTC",
			wantSub: "ambiguous between an all-day and a never-open window",
		},
		{
			name:    "Local rejected by name",
			raw:     "22:00-06:00 Local",
			wantSub: "resolves to the host's own zone",
		},
		{
			name:    "unknown zone",
			raw:     "22:00-06:00 Not/AZone",
			wantSub: "must be an IANA zone name",
		},
		{
			// Pins the Go side of the eval/runtime parity
			// nix/checks/awake-window.nix's own tab case pins: \S+
			// rejects every whitespace byte inside the zone token, not
			// just a space.
			name:    "tab embedded in the zone",
			raw:     "22:00-06:00 Europe/Lon\tdon",
			wantSub: `want "HH:MM-HH:MM Zone"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := ParseWindow(c.raw)
			if w != nil {
				t.Fatalf("ParseWindow(%q) = %+v, want nil on error", c.raw, w)
			}
			if err == nil {
				t.Fatalf("ParseWindow(%q) err = nil, want an error containing %q", c.raw, c.wantSub)
			}
			if !strings.HasPrefix(err.Error(), windowPrefix) {
				t.Fatalf("ParseWindow(%q) err = %q, want prefix %q", c.raw, err.Error(), windowPrefix)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("ParseWindow(%q) err = %q, want it to contain %q", c.raw, err.Error(), c.wantSub)
			}
			// The error quotes raw with %q, so the expected substring is
			// the quoted form -- a raw value carrying a tab appears
			// escaped, never as the literal byte.
			if !strings.Contains(err.Error(), strconv.Quote(c.raw)) {
				t.Fatalf("ParseWindow(%q) err = %q, want it to quote the input value", c.raw, err.Error())
			}
		})
	}
}

// TestWindowOpenAndUntil is the pinned-instant table the acceptance
// criteria call for. Every "now" is built as an unambiguous UTC instant
// via time.Date(..., time.UTC); Open/Until then apply the window's own
// location internally. Every subtest below whose name mentions the
// missing or the duplicated hour pins a real transition date for
// America/New_York in 2026: 2026-03-08 is the US spring-forward (02:00 EST
// jumps to 03:00 EDT, an hour that never happens), and 2026-11-01 is the US
// fall-back (01:00 EDT is immediately followed by a second 01:00, now EST,
// so that hour happens twice). Offsets were verified against Go's own
// tzdata (embedded via the awake.go time/tzdata import) before being
// pinned here, not assumed from memory.
func TestWindowOpenAndUntil(t *testing.T) {
	mustWindow := func(t *testing.T, raw string) *Window {
		t.Helper()
		w, err := ParseWindow(raw)
		if err != nil {
			t.Fatalf("ParseWindow(%q) err = %v", raw, err)
		}
		return w
	}
	utc := func(y int, mo time.Month, d, h, m int) time.Time {
		return time.Date(y, mo, d, h, m, 0, 0, time.UTC)
	}

	t.Run("same-day window boundaries", func(t *testing.T) {
		w := mustWindow(t, "09:00-17:00 UTC")
		cases := []struct {
			name      string
			now       time.Time
			wantOpen  bool
			wantUntil time.Duration
		}{
			{"before", utc(2026, 6, 1, 8, 59), false, time.Minute},
			{"open boundary", utc(2026, 6, 1, 9, 0), true, 0},
			{"inside", utc(2026, 6, 1, 12, 0), true, 0},
			{"close boundary exclusive", utc(2026, 6, 1, 17, 0), false, 16 * time.Hour},
			{"after", utc(2026, 6, 1, 18, 0), false, 15 * time.Hour},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := w.Open(c.now); got != c.wantOpen {
					t.Errorf("Open(%v) = %v, want %v", c.now, got, c.wantOpen)
				}
				if got, exhausted := w.Until(c.now); got != c.wantUntil || exhausted {
					t.Errorf("Until(%v) = (%v, %v), want (%v, false)", c.now, got, exhausted, c.wantUntil)
				}
			})
		}
	})

	t.Run("wraparound window", func(t *testing.T) {
		w := mustWindow(t, "22:00-06:00 UTC")
		cases := []struct {
			name      string
			now       time.Time
			wantOpen  bool
			wantUntil time.Duration
		}{
			{"evening open", utc(2026, 6, 1, 23, 0), true, 0},
			{"past-midnight open", utc(2026, 6, 2, 3, 0), true, 0},
			{"close boundary exclusive", utc(2026, 6, 2, 6, 0), false, 16 * time.Hour},
			{"midday closed", utc(2026, 6, 2, 12, 0), false, 10 * time.Hour},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := w.Open(c.now); got != c.wantOpen {
					t.Errorf("Open(%v) = %v, want %v", c.now, got, c.wantOpen)
				}
				if got, exhausted := w.Until(c.now); got != c.wantUntil || exhausted {
					t.Errorf("Until(%v) = (%v, %v), want (%v, false)", c.now, got, exhausted, c.wantUntil)
				}
			})
		}
	})

	t.Run("timezone actually applied", func(t *testing.T) {
		// 2026-07-01 is British Summer Time: Europe/London is UTC+1.
		utcWindow := mustWindow(t, "22:00-06:00 UTC")
		londonWindow := mustWindow(t, "22:00-06:00 Europe/London")

		// 21:30 UTC = 22:30 BST: inside the London window (opens 22:00
		// local) but before the UTC window opens (22:00 UTC).
		now := utc(2026, 7, 1, 21, 30)
		if !londonWindow.Open(now) {
			t.Errorf("London window Open(%v) = false, want true (22:30 local)", now)
		}
		if utcWindow.Open(now) {
			t.Errorf("UTC window Open(%v) = true, want false (21:30 UTC, before 22:00)", now)
		}
		if got, exhausted := utcWindow.Until(now); got != 30*time.Minute || exhausted {
			t.Errorf("UTC window Until(%v) = (%v, %v), want (%v, false)", now, got, exhausted, 30*time.Minute)
		}
	})

	t.Run("until near and inside an opening", func(t *testing.T) {
		w := mustWindow(t, "09:00-17:00 UTC")

		before := utc(2026, 6, 1, 8, 0)
		if got, exhausted := w.Until(before); got != time.Hour || exhausted {
			t.Errorf("Until(%v) = (%v, %v), want (%v, false)", before, got, exhausted, time.Hour)
		}

		inside := utc(2026, 6, 1, 10, 0)
		if got, exhausted := w.Until(inside); got != 0 || exhausted {
			t.Errorf("Until(%v) = (%v, %v), want (0, false) (already open)", inside, got, exhausted)
		}
	})

	t.Run("spring forward missing hour inside the window", func(t *testing.T) {
		w := mustWindow(t, "22:00-06:00 America/New_York")
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// 2026-03-07 21:00 EST, one hour before the window opens.
		before := time.Date(2026, 3, 7, 21, 0, 0, 0, loc)
		if got, exhausted := w.Until(before); got != time.Hour || exhausted {
			t.Errorf("Until(%v) = (%v, %v), want (%v, false)", before, got, exhausted, time.Hour)
		}

		// Either side of the 2am->3am gap: 01:30 EST (just before) and
		// 03:30 EDT (just after) are both inside the window.
		beforeGap := time.Date(2026, 3, 8, 1, 30, 0, 0, loc)
		afterGap := time.Date(2026, 3, 8, 3, 30, 0, 0, loc)
		if !w.Open(beforeGap) {
			t.Errorf("Open(%v) = false, want true", beforeGap)
		}
		if !w.Open(afterGap) {
			t.Errorf("Open(%v) = false, want true", afterGap)
		}

		// The window's real elapsed length that night is 7h, not the
		// naive 8h, because the clock skips an hour inside it.
		open := time.Date(2026, 3, 7, 22, 0, 0, 0, loc)
		shut := time.Date(2026, 3, 8, 6, 0, 0, 0, loc)
		if got, want := shut.Sub(open), 7*time.Hour; got != want {
			t.Fatalf("window elapsed length = %v, want %v (sanity check on the pinned dates)", got, want)
		}
	})

	t.Run("fall back duplicated hour inside the window", func(t *testing.T) {
		w := mustWindow(t, "22:00-06:00 America/New_York")
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// The window's real elapsed length that night is 9h, not the
		// naive 8h, because the clock repeats an hour inside it.
		open := time.Date(2026, 10, 31, 22, 0, 0, 0, loc)
		shut := time.Date(2026, 11, 1, 6, 0, 0, 0, loc)
		if got, want := shut.Sub(open), 9*time.Hour; got != want {
			t.Fatalf("window elapsed length = %v, want %v (sanity check on the pinned dates)", got, want)
		}

		// Both occurrences of the duplicated 01:30 wall-clock instant
		// (first EDT, then EST after the fallback) are inside the
		// window.
		first := open.Add(3*time.Hour + 30*time.Minute) // 01:30 EDT
		second := first.Add(time.Hour)                  // 01:30 EST, the repeat
		if got, want := first.In(loc).Format("15:04 MST"), "01:30 EDT"; got != want {
			t.Fatalf("sanity check: first = %s, want %s", got, want)
		}
		if got, want := second.In(loc).Format("15:04 MST"), "01:30 EST"; got != want {
			t.Fatalf("sanity check: second = %s, want %s", got, want)
		}
		if !w.Open(first) {
			t.Errorf("Open(%v) = false, want true (first 01:30, EDT)", first)
		}
		if !w.Open(second) {
			t.Errorf("Open(%v) = false, want true (second 01:30, EST)", second)
		}
	})

	t.Run("window entirely inside the missing hour", func(t *testing.T) {
		w := mustWindow(t, "02:15-02:45 America/New_York")
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// 02:15-02:45 never happens on 2026-03-08: the clock jumps
		// straight from 01:59:59 EST to 03:00:00 EDT. No real instant
		// that day should read as open.
		for _, h := range []struct{ h, m int }{{1, 30}, {3, 30}} {
			instant := time.Date(2026, 3, 8, h.h, h.m, 0, 0, loc)
			if w.Open(instant) {
				t.Errorf("Open(%v) = true, want false (inside the skipped hour's day)", instant)
			}
		}

		now := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
		until, exhausted := w.Until(now)
		if until <= 0 {
			t.Fatalf("Until(%v) = %v, want a strictly positive duration", now, until)
		}
		if exhausted {
			t.Fatalf("Until(%v) exhausted = true, want false", now)
		}
		next := now.Add(until)
		if !w.Open(next) {
			t.Fatalf("Until(%v) landed on %v, which is not open", now, next)
		}
		if got, want := next.Format("2006-01-02"), "2026-03-09"; got != want {
			t.Fatalf("Until(%v) landed on %s, want the following day (2026-03-09), not the skipped day itself", now, got)
		}
	})

	t.Run("spring forward window starting inside the missing hour", func(t *testing.T) {
		w := mustWindow(t, "02:30-06:00 America/New_York")
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// 02:30 EST never happens on 2026-03-08 -- the clock jumps
		// straight from 01:59:59 EST to 03:00:00 EDT -- so the window
		// becomes open at the transition instant itself, 03:00 EDT, not
		// at some literal 02:30 the day rolls forward to. The real
		// elapsed time from 00:30 EST to 03:00 EDT is 1h30m, not the 2h30m
		// the wall-clock digits alone would suggest, because that hour is
		// skipped rather than lived through.
		now := time.Date(2026, 3, 8, 0, 30, 0, 0, loc)
		if got, exhausted := w.Until(now); got != time.Hour+30*time.Minute || exhausted {
			t.Errorf("Until(%v) = (%v, %v), want (%v, false)", now, got, exhausted, time.Hour+30*time.Minute)
		}
		opened := time.Date(2026, 3, 8, 4, 0, 0, 0, loc)
		if !w.Open(opened) {
			t.Errorf("Open(%v) = false, want true (04:00 EDT, inside the window)", opened)
		}
	})

	t.Run("fall back until across the duplicated hour", func(t *testing.T) {
		w := mustWindow(t, "01:00-01:30 America/New_York")
		loc, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// 01:00 EDT is followed by a second 01:00, now EST, an hour
		// later. Construct 01:40 EDT by adding to a pinned pre-transition
		// instant rather than time.Date, which would pick the first
		// (EDT) occurrence of an ambiguous wall time.
		edtOpen := time.Date(2026, 11, 1, 1, 0, 0, 0, loc) // 01:00 EDT
		now := edtOpen.Add(40 * time.Minute)               // 01:40 EDT
		if got, want := now.Format("15:04 MST"), "01:40 EDT"; got != want {
			t.Fatalf("sanity check: now = %s, want %s", got, want)
		}
		if got, exhausted := w.Until(now); got != 20*time.Minute || exhausted {
			t.Errorf("Until(%v) = (%v, %v), want (%v, false)", now, got, exhausted, 20*time.Minute)
		}
	})
}

// buildTZif assembles a minimal TZif v1 blob by hand per tzfile(5), rather
// than pulling one from a real zone, so a test can pin an offset-change
// schedule no real IANA zone has. isutcnt/isstdcnt/leapcnt are left at 0:
// Go's own TZif reader (zoneinfo_read.go) treats those arrays as optional
// and guards every read on the actual counts, so omitting them is valid,
// not a shortcut that happens to parse.
func buildTZif(t *testing.T, transTimes []int32, transTypes []byte, offsets []int32, isdst []byte, abbrevIdx []byte, abbrev string) []byte {
	t.Helper()
	if len(offsets) != len(isdst) || len(offsets) != len(abbrevIdx) {
		t.Fatalf("buildTZif: mismatched type slice lengths")
	}
	if len(transTimes) != len(transTypes) {
		t.Fatalf("buildTZif: mismatched transition slice lengths")
	}

	var buf bytes.Buffer
	buf.WriteString("TZif")
	buf.WriteByte(0)            // version 1: 4-byte transition times, no footer
	buf.Write(make([]byte, 15)) // reserved

	be4 := func(n int) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		buf.Write(b[:])
	}
	be4(0)               // isutcnt
	be4(0)               // isstdcnt
	be4(0)               // leapcnt
	be4(len(transTimes)) // timecnt
	be4(len(offsets))    // typecnt
	be4(len(abbrev))     // charcnt

	for _, tt := range transTimes {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(tt))
		buf.Write(b[:])
	}
	buf.Write(transTypes)

	for i := range offsets {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(offsets[i]))
		buf.Write(b[:])
		buf.WriteByte(isdst[i])
		buf.WriteByte(abbrevIdx[i])
	}
	buf.WriteString(abbrev)

	return buf.Bytes()
}

// TestWindowUntilExhaustedSyntheticZone drives the untilHorizon fallback
// with a synthetic zone (no real IANA zone behaves this way) that jumps
// the local clock straight from 02:59:59 to 04:00:00 every day (+0 -> +1
// at 03:00Z) and repeats 11:00-11:59 on the way back (+1 -> +0 at
// 11:00Z). A 03:15-03:45 window's local minutes therefore never occur on
// any day, so Until must walk every zone period out to untilHorizon and
// report it exhausted rather than silently returning a plausible-looking
// duration for a window that in fact never opens.
func TestWindowUntilExhaustedSyntheticZone(t *testing.T) {
	const abbrev = "P0\x00P1\x00"
	offsets := []int32{0, 3600}
	isdst := []byte{0, 1}
	abbrevIdx := []byte{0, 3}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	const days = 30 // well past now+untilHorizon (8d), with margin so the
	// walk never runs off the end of the explicit transition table into
	// TZif's "last type persists" extension before exhausting.
	var times []int32
	var types []byte
	for d := 0; d < days; d++ {
		dayStart := base + int64(d)*86400
		times = append(times, int32(dayStart+3*3600)) // 03:00Z: +0 -> +1
		types = append(types, 1)
		times = append(times, int32(dayStart+11*3600)) // 11:00Z: +1 -> +0
		types = append(types, 0)
	}

	data := buildTZif(t, times, types, offsets, isdst, abbrevIdx, abbrev)
	loc, err := time.LoadLocationFromTZData("Synthetic/Until", data)
	if err != nil {
		t.Fatalf("LoadLocationFromTZData: %v", err)
	}

	w := &Window{start: 195, end: 225, loc: loc} // 03:15-03:45 local
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, loc)

	wait, exhausted := w.Until(now)
	if !exhausted {
		t.Fatalf("Until(%v) exhausted = false, want true", now)
	}
	if wait != untilHorizon {
		t.Fatalf("Until(%v) wait = %v, want untilHorizon (%v)", now, wait, untilHorizon)
	}
	if w.Open(now.Add(wait)) {
		t.Fatalf("Open(now+wait) = true, want false: the window never actually opens in this zone")
	}
}
