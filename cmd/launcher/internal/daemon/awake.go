package daemon

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	// time/tzdata embeds the IANA zone database in the binary, so an
	// explicitly configured DAEMON_AWAKE_WINDOW zone resolves identically
	// on a minimal host, in a CI runner, and inside the Nix build sandbox
	// -- none of which is guaranteed to ship /usr/share/zoneinfo. Without
	// this import, time.LoadLocation depends on the host's zoneinfo files
	// and these very tests are not hermetic.
	_ "time/tzdata"
)

// windowPrefix names the operator knob every Window parse error belongs
// to, so the operator can tell where the error came from wherever it
// surfaces.
const windowPrefix = "DAEMON_AWAKE_WINDOW: "

// windowFormat is the strict "HH:MM-HH:MM Zone" shape: two-digit hour,
// two-digit minute, a literal '-', one space, then a non-empty zone with
// no embedded whitespace (IANA zone names never have one).
var windowFormat = regexp.MustCompile(`^([0-9]{2}):([0-9]{2})-([0-9]{2}):([0-9]{2}) (\S+)$`)

// Window is a daily local-time span, e.g. "sleep 22:00-06:00 London time".
// start/end are minutes since local midnight rather than a time.Duration
// or wall-clock struct: comparing "now" against the window reduces to two
// integer comparisons, and the end < start wraparound case (a window that
// crosses midnight) falls out of that comparison in Open rather than
// needing a separate code path.
type Window struct {
	start, end int // minutes since local midnight, each in [0, minutesPerDay)
	loc        *time.Location
}

// minutesPerDay is the number of minutes in a day: the bound on the
// Window fields above, and what Until's minutes-since-midnight
// arithmetic wraps against.
const minutesPerDay = 1440

// ParseWindow parses the DAEMON_AWAKE_WINDOW knob. An empty (or
// whitespace-only) string is not an error: it means no window is
// configured, so the daemon is always awake, and ParseWindow reports that
// with (nil, nil). Open and Until both give a nil *Window the always-awake
// answer (see their comments) so the loop never has to branch on nil
// itself at every call site.
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
// minutes since midnight. The regex already guarantees exactly two ASCII
// digits each, so strconv.Atoi cannot fail here; only the range remains to
// check.
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

// Open reports whether the window is open at now. A nil Window is the
// always-awake case: there's no configured window to be closed against,
// so every instant is open.
func (w *Window) Open(now time.Time) bool {
	if w == nil {
		return true
	}
	minutes := minutesSinceMidnight(now.In(w.loc))
	if w.start < w.end {
		return minutes >= w.start && minutes < w.end
	}
	// Wraps past midnight (e.g. 22:00-06:00): one continuous span from
	// start through midnight to end, rather than two separate spans.
	return minutes >= w.start || minutes < w.end
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
// Otherwise it walks forward one zone period (a span of constant UTC
// offset) at a time via ZoneBounds, computing the next start-of-window
// wall clock by exact arithmetic within the period. Every DST wrinkle --
// a skipped or repeated hour -- lives exactly at a period boundary, so
// when the computed candidate would fall past the boundary, Until checks
// Open at the boundary instant itself instead: that's what makes a
// window opening mid-skip resolve to the transition, and a window
// re-opening inside a repeated hour get seen at all.
func (w *Window) Until(now time.Time) time.Duration {
	if w == nil || w.Open(now) {
		return 0
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
			return cand.Sub(now)
		}
		t = zoneEnd
		if w.Open(t) {
			return t.Sub(now)
		}
	}
	// Degrade to a re-check rather than taking a long-lived daemon down.
	return untilHorizon
}
