package daemon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	// Embeds the IANA zone database so a DAEMON_AWAKE_WINDOW zone resolves
	// identically on a minimal host, in CI and in the Nix sandbox, none of
	// which is guaranteed to ship /usr/share/zoneinfo.
	_ "time/tzdata"
)

// windowPrefix names the operator knob every Window parse error belongs to.
const windowPrefix = "DAEMON_AWAKE_WINDOW: "

// windowFormat is the strict "HH:MM-HH:MM Zone" shape: two-digit hour,
// two-digit minute, a literal '-', one space, then a non-empty zone with
// no embedded whitespace (IANA zone names never have one).
var windowFormat = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})-([0-9]{2}):([0-9]{2}) (\S+)$`)

// Window is a daily local-time span, e.g. "sleep 22:00-06:00 London time".
// start/end are minutes since local midnight, so Open is two integer
// comparisons and the end < start midnight wraparound needs no separate
// path.
type Window struct {
	start, end int // minutes since local midnight, each in [0, minutesPerDay)
	loc        *time.Location
}

// minutesPerDay is the number of minutes in a day: the bound on the
// Window fields above, and what Until's minutes-since-midnight
// arithmetic wraps against.
const minutesPerDay = 1440

// ParseWindow parses the DAEMON_AWAKE_WINDOW knob. An empty or
// whitespace-only string returns (nil, nil): no window, always awake, which
// Open and Until answer for a nil *Window.
func ParseWindow(raw string) (*Window, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	m := windowFormat.FindStringSubmatch(trimmed)
	if m == nil {
		return nil, fmt.Errorf("%swant %q (e.g. %q), got %q", windowPrefix, "HH:MM-HH:MM Zone", "22:00-06:00 Europe/London", raw)
	}

	start, err := clockMinutes(m[1], m[2])
	if err != nil {
		return nil, fmt.Errorf("%s%w (in %q)", windowPrefix, err, raw)
	}
	end, err := clockMinutes(m[3], m[4])
	if err != nil {
		return nil, fmt.Errorf("%s%w (in %q)", windowPrefix, err, raw)
	}
	if start == end {
		return nil, fmt.Errorf("%sstart and end are both %02d:%02d: ambiguous between an all-day and a never-open window (in %q)", windowPrefix, start/60, start%60, raw)
	}

	zone := m[5]
	if zone == "Local" {
		return nil, fmt.Errorf("%szone %q resolves to the host's own zone -- the exact inheritance this knob exists to prevent; use an IANA zone name (e.g. %q, %q) (in %q)", windowPrefix, zone, "Europe/London", "UTC", raw)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("%szone %q must be an IANA zone name (e.g. %q, %q): %w (in %q)", windowPrefix, zone, "Europe/London", "UTC", err, raw)
	}

	return &Window{start: start, end: end, loc: loc}, nil
}

// clockMinutes converts a regex-captured two-digit hour/minute pair into
// minutes since midnight. The regex guarantees the digits, so only the range
// is checked.
func clockMinutes(hh, mm string) (int, error) {
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	if h > 23 {
		return 0, fmt.Errorf("hour %q out of range 0-23", hh)
	}
	if m > 59 {
		return 0, fmt.Errorf("minute %q out of range 0-59", mm)
	}
	return h*60 + m, nil
}

// Open reports whether the window is open at now; a nil Window is always
// open.
func (w *Window) Open(now time.Time) bool {
	if w == nil {
		return true
	}
	minutes := minutesSinceMidnight(now.In(w.loc))
	if w.start < w.end {
		return minutes >= w.start && minutes < w.end
	}
	// Wraps past midnight (e.g. 22:00-06:00).
	return minutes >= w.start || minutes < w.end
}

// Location returns the Window's zone. A nil Window has none configured, so
// it gives UTC rather than inheriting the host's zone.
func (w *Window) Location() *time.Location {
	if w == nil {
		return time.UTC
	}
	return w.loc
}

func minutesSinceMidnight(t time.Time) int {
	h, m, _ := t.Clock()
	return h*60 + m
}

// untilHorizon bounds the zone-period walk in Until: no real IANA zone
// goes this long without the window validly opening, so exhausting it
// signals a bug in the walk rather than an operator input.
const untilHorizon = 8 * 24 * time.Hour

// Until reports how long until the window next opens, measured from now.
// It is 0 while the window is already open, including the nil-receiver
// always-awake case.
//
// Otherwise it walks forward one zone period (constant UTC offset) at a
// time via ZoneBounds. Every DST skip or repeat lies at a period boundary,
// so when the computed candidate falls past one, Until checks Open at the
// boundary instant instead: a window opening mid-skip resolves to the
// transition, and one re-opening inside a repeated hour is seen at all.
//
// exhausted is true only when the walk passes untilHorizon without an
// opening, so a caller can tell it from a genuine long park; wait is then
// untilHorizon, a re-check interval.
func (w *Window) Until(now time.Time) (wait time.Duration, exhausted bool) {
	if w == nil || w.Open(now) {
		return 0, false
	}

	t := now
	deadline := now.Add(untilHorizon)
	for t.Before(deadline) {
		local := t.In(w.loc)
		_, zoneEnd := local.ZoneBounds()

		delta := (w.start - minutesSinceMidnight(local)) % minutesPerDay
		if delta <= 0 {
			delta += minutesPerDay
		}
		cand := local.Add(time.Duration(delta) * time.Minute)
		cand = cand.Add(-time.Duration(cand.Second()) * time.Second)
		cand = cand.Add(-time.Duration(cand.Nanosecond()) * time.Nanosecond)

		if zoneEnd.IsZero() || cand.Before(zoneEnd) {
			return cand.Sub(now), false
		}
		t = zoneEnd
		if w.Open(t) {
			return t.Sub(now), false
		}
	}
	// Degrade to a re-check rather than taking a long-lived daemon down.
	return untilHorizon, true
}
