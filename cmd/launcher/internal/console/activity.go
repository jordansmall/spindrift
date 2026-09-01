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

// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser and returns the whole ordered sequence of status lines it
// emitted, rather than just the last one. Consecutive identical lines collapse
// to one entry, so the feed reads as one line per distinct Driver step.
// Returns nil when no log exists yet for number (claimed but not yet launched)
// or when the log can't be read or parsed -- the same graceful-empty contract
// RunningHeartbeat uses, rather than an error every caller must handle.
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

// collapseActivityLines splits s on newlines, drops a trailing empty line,
// strips ANSI/control sequences as the Transcript render does — narration
// traces back to untrusted issue/agent text, and the sidebar's fullscreen
// render joins these lines directly rather than through the table rows'
// clip() — and collapses runs of consecutive identical lines, since the
// heartbeat writer emits one line per parsed event, not per distinct step.
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
// lines. Deliberately not a length-only "grew" check: a Dispatch rolling from
// a finished pass onto a fresh fix/conflict-resolve pass gets a *shorter* feed
// (the feed keys on only the latest pass log), which "grew" would miss.
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

// activityCacheEntry holds the persistent per-log-path parser state
// SidebarActivityCache keeps alive across refreshes: the embedded tailer's
// open/seek/read/write/offset mechanics plus the feed accumulated so far.
type activityCacheEntry struct {
	tailer
	lines []ActivityLine
}

// appendActivity reads the bytes appended to entry.path since entry.offset,
// collapses them, then merges onto entry.lines. When the tail's first line
// repeats entry.lines' last, it is dropped before appending, so a narration
// split across two append calls still collapses to one entry exactly as a
// whole-file parse would. ok is false when the file can't be read or written
// through drv's parser; entry.offset and entry.lines are left unmodified then,
// so a transient read hiccup doesn't clobber the accumulated feed — Refresh
// only commits its local entry copy back to c.entry once ok.
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

// SidebarActivityCache remembers the open sidebar's last-refreshed Activity
// feed via a persistent append-tail parser, mirroring HeartbeatCache: a path
// change or a size behind the cached offset (truncation/rotation, or a
// different Dispatch selected) starts a fresh parser at offset 0, an unchanged
// size skips the read, and a grown size parses only the appended tail.
// Single-entry rather than a map: only one sidebar can be open at a time.
type SidebarActivityCache struct {
	number string
	entry  activityCacheEntry
}

// NewSidebarActivityCache returns an empty cache ready to use.
func NewSidebarActivityCache() *SidebarActivityCache {
	return &SidebarActivityCache{}
}

// Refresh returns number's current Activity feed — cached when its pass log
// hasn't grown, extended by parsing just the appended tail when it has. ok is
// false when no log exists yet for number, so the caller can skip the refresh
// rather than clobbering an already-loaded feed with an empty one on a
// claimed-but-not-yet-launched race.
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
