package console

import (
	"bytes"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// ActivityLine is one distinct emitted status line of a Dispatch's Activity
// feed (ADR 0030).
type ActivityLine struct {
	Text string
}

// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser -- the same driver.Driver.NewHeartbeatWriter machinery
// RunningHeartbeat feeds against the same latest pass log (#647 AC2) -- and
// returns the whole
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

// activityCacheEntry holds the persistent per-log-path parser state
// SidebarActivityCache keeps alive across refreshes, mirroring
// heartbeatCacheEntry: entry.tailer (issue #1776) carries the shared
// open/seek/read/write/offset-advance mechanics, plus the full ordered feed
// accumulated so far.
type activityCacheEntry struct {
	tailer
	lines []ActivityLine
}

// appendActivity reads the bytes appended to entry.path since entry.offset
// via entry.tailer (issue #1776). The tail's own output collapses through
// collapseActivityLines the same as a whole-file parse would, then merges
// onto entry.lines: when the merged tail's first line repeats entry.lines'
// last line, it's dropped before appending, so a narration split across two
// append calls by an in-between refresh still collapses to one entry
// exactly as a single whole-file parse would have (#1501 AC1). ok is false
// when the file can't be read or written through drv's parser, mirroring
// appendHeartbeat's own failure contract; entry.offset and entry.lines are
// left unmodified in that case (entry.writer/out may already be lazily
// created, which is harmless to reuse on the next call) so a transient read
// hiccup doesn't clobber the feed accumulated so far — Refresh itself only
// ever commits its local entry copy back to c.entry once appendActivity
// reports ok.
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
// feed via a persistent append-tail parser, mirroring HeartbeatCache: a
// path change or a size that fell behind the cached offset
// (truncation/rotation, or a different Dispatch selected) starts a fresh
// parser at offset 0, an unchanged size skips the read entirely, and a
// grown size reads and parses only the appended tail (issue #1749) — the
// same incremental machinery RunningHeartbeat already uses (issue #1747),
// rather than this cache's own prior whole-file reread on every stat
// change. Single-entry rather than a map: only one sidebar can be open at a
// time, so there is never more than one Dispatch to track.
type SidebarActivityCache struct {
	number string
	entry  activityCacheEntry
}

// NewSidebarActivityCache returns an empty cache ready to use.
func NewSidebarActivityCache() *SidebarActivityCache {
	return &SidebarActivityCache{}
}

// Refresh returns number's current Activity feed — the cached one when its
// pass log hasn't grown since the cached call, or freshly extended by
// parsing just the appended tail when it has — plus ok, false when no log
// exists yet for number (RunningHeartbeat's own no-log contract) so the
// caller can skip sending a refresh rather than clobbering an
// already-loaded feed with an empty one on a claimed-but-not-yet-launched
// race.
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
