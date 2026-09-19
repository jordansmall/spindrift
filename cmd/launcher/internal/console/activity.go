package console

import (
	"bytes"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// ActivityLine is one distinct emitted status line of a Dispatch's Activity
// feed (ADR 0030).
type ActivityLine struct {
	Text string
}

// ActivityFeed returns every status line drv's heartbeat parser emits for the
// Dispatch's latest pass log (#647 AC2), with consecutive identical lines
// collapsed into one per distinct Driver step (#1501 AC1). It returns nil, not
// an error, when no log exists yet for number or when the log can't be parsed.
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
	w := drv.NewHeartbeatWriter(io.Discard, number, &buf, driverkit.RenderOptions{})
	if _, err := w.Write(data); err != nil {
		return nil
	}
	return collapseActivityLines(buf.String())
}

// collapseActivityLines strips ANSI and control sequences from each line of s,
// because narration traces back to untrusted issue and agent text and the
// sidebar's fullscreen render joins these lines directly instead of going
// through the table rows' clip(). It also collapses runs of identical lines:
// the heartbeat writer emits one line per parsed event, not per distinct step.
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

// activityEqual compares a and b line by line rather than by length alone: a
// Dispatch rolling onto a fresh fix or conflict-resolve pass gets a shorter
// feed, since the feed keys on the latest pass log alone, and a
// length-only "grew" check would miss that entirely (issue #1502).
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

// activityCacheEntry holds the per-log-path parser state SidebarActivityCache
// keeps alive across refreshes; the embedded tailer carries the shared
// open/seek/read/write/offset-advance mechanics (issue #1776).
type activityCacheEntry struct {
	tailer
	lines []ActivityLine
}

// appendActivity parses the bytes appended since entry.offset and merges them
// onto entry.lines, dropping a tail's first line that repeats the last line
// already held, so narration split across two reads still collapses to one
// entry (#1501 AC1). On failure entry.offset and entry.lines stay unmodified,
// so a transient read hiccup doesn't clobber the feed accumulated so far.
func appendActivity(drv driver.Driver, number string, entry *activityCacheEntry) (lines []ActivityLine, ok bool) {
	data, ok := entry.readAppended(drv, number)
	if !ok {
		return nil, false
	}
	tail := collapseActivityLines(data)
	if len(tail) > 0 && len(entry.lines) > 0 && entry.lines[len(entry.lines)-1].Text == tail[0].Text {
		tail = tail[1:]
	}
	entry.lines = append(entry.lines, tail...)
	return entry.lines, true
}

// SidebarActivityCache remembers the open sidebar's Activity feed and parses
// only the appended tail on each refresh (issues #1749 and #1747). A path
// change, or a size that fell behind the cached offset (truncation, rotation,
// or a different Dispatch selected), starts a fresh parser at offset 0. It
// holds one entry, not a map, because only one sidebar can be open at a time.
type SidebarActivityCache struct {
	number string
	entry  activityCacheEntry
}

// NewSidebarActivityCache returns an empty cache ready to use.
func NewSidebarActivityCache() *SidebarActivityCache {
	return &SidebarActivityCache{}
}

// Refresh returns number's current Activity feed, extended by parsing the
// appended tail when the pass log has grown. ok is false when no log exists
// yet, so the caller skips the refresh rather than clobbering an already
// loaded feed with an empty one on a claimed-but-not-yet-launched race.
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

	entry := c.entry
	if c.number != number || entry.path != path || info.Size() < entry.offset {
		entry = activityCacheEntry{tailer: tailer{path: path}}
	}

	if info.Size() > entry.offset {
		if _, ok := appendActivity(drv, number, &entry); !ok {
			return nil, false
		}
	}
	c.number, c.entry = number, entry
	return entry.lines, true
}
