package console

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// ActivityLine is one distinct emitted status line of a Dispatch's Activity
// feed (ADR 0030).
type ActivityLine struct {
	Text string
}

// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser -- the same parseHeartbeat machinery RunningHeartbeat uses
// against the same latest pass log (#647 AC2) -- and returns the whole
// ordered sequence of status lines it emitted, rather than just the last one.
// Consecutive identical lines collapse to one entry, so the feed reads as one
// line per distinct Driver step (#1501 AC1). Returns nil when no log exists
// yet for number (claimed but not yet launched) or when the log can't be
// read or parsed -- the same graceful-empty contract RunningHeartbeat uses,
// rather than an error every caller must handle.
func ActivityFeed(drv driver.Driver, pwd, number string) []ActivityLine {
	if drv == nil {
		return nil
	}
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return nil
	}
	path := passes[len(passes)-1].Path
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	w := drv.NewHeartbeatWriter(io.Discard, number, &buf)
	if _, err := w.Write(data); err != nil {
		return nil
	}
	return collapseActivityLines(buf.String())
}

// collapseActivityLines splits s on newlines, drops a trailing empty line,
// strips ANSI/control sequences from each line the same way the Transcript
// render does (narration traces back to untrusted issue/agent text, and the
// sidebar's fullscreen render joins these lines directly, unlike the table
// rows' own clip()-based rendering), and collapses any run of consecutive
// identical lines to their first occurrence -- the heartbeat writer emits
// one line per parsed event, not per distinct step, so two events that
// narrate the same text back-to-back would otherwise duplicate the same
// line in the feed.
func collapseActivityLines(s string) []ActivityLine {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	var out []ActivityLine
	var last string
	first := true
	for _, line := range strings.Split(s, "\n") {
		line = SanitizeControlSequences(line)
		if !first && line == last {
			continue
		}
		out = append(out, ActivityLine{Text: line})
		last = line
		first = false
	}
	return out
}

// activityEqual reports whether a and b carry the same ordered sequence of
// lines — Update's SidebarActivityMsg gate for whether a refresh actually
// changed anything, deliberately not a length-only "grew" check: a Dispatch
// rolling from a finished pass onto a fresh fix/conflict-resolve pass gets a
// shorter feed (LogPaths/ActivityFeed key on only the latest pass log), which
// a "grew" check would miss entirely (issue #1502).
func activityEqual(a, b []ActivityLine) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

// SidebarActivityCache remembers the open sidebar's last-refreshed Activity
// feed, keyed by (Number, path, size, modTime) — refreshPickDecorations's
// per-Msg refresh runs on every tea.Msg (issue #1502, ADR 0030's
// "piggybacking the existing per-Msg sync tick"), so most calls see the
// exact same on-disk pass log as last time; a stat match skips the
// ReadFile+reparse and returns the cached feed, mirroring HeartbeatCache's
// own skip (issue #731). Single-entry rather than a map: only one sidebar
// can be open at a time, so there is never more than one Dispatch to track.
type SidebarActivityCache struct {
	number   string
	path     string
	size     int64
	modTime  time.Time
	activity []ActivityLine
}

// NewSidebarActivityCache returns an empty cache ready to use.
func NewSidebarActivityCache() *SidebarActivityCache {
	return &SidebarActivityCache{}
}

// Refresh returns number's current Activity feed — freshly re-derived, or
// the cached one when its pass log's (path, size, modTime) match what was
// cached last time — plus ok, false when no log exists yet for number
// (RunningHeartbeat's own no-log contract) so the caller can skip sending a
// refresh rather than clobbering an already-loaded feed with an empty one on
// a claimed-but-not-yet-launched race.
func (c *SidebarActivityCache) Refresh(drv driver.Driver, pwd, number string) ([]ActivityLine, bool) {
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return nil, false
	}
	path := passes[len(passes)-1].Path
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if c.number == number && c.path == path && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		return c.activity, true
	}
	activity := ActivityFeed(drv, pwd, number)
	c.number, c.path, c.size, c.modTime, c.activity = number, path, info.Size(), info.ModTime(), activity
	return activity, true
}
