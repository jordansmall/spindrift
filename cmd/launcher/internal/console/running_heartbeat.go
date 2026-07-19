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

// parseHeartbeat reads path and replays it through drv's heartbeat parser,
// returning the last line it emitted plus the os.FileInfo the read saw — the
// stat HeartbeatCache pins to detect whether a later call can skip this same
// work. ok is false when the file can't be read or written through drv's
// parser (the same "no heartbeat yet" cases HeartbeatCache.RunningHeartbeat
// returns "" for).
func parseHeartbeat(drv driver.Driver, path, number string) (line string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var buf bytes.Buffer
	w := drv.NewHeartbeatWriter(io.Discard, number, &buf)
	if _, err := w.Write(data); err != nil {
		return "", false
	}
	return lastLine(buf.String()), true
}

// heartbeatCacheEntry pins the stat HeartbeatCache last saw for one pick's
// latest pass log, plus the line that stat's read parsed to.
type heartbeatCacheEntry struct {
	path    string
	size    int64
	modTime time.Time
	line    string
}

// HeartbeatCache remembers each running pick's last-parsed heartbeat line,
// keyed by pick number. refreshPickDecorations calls RunningHeartbeat on every tea.Msg —
// every keypress, poll tick, and refresh signal, not just a fixed render
// cadence — so most calls see the exact same on-disk log as last time. A
// stat (size + mtime) that matches the cached entry skips the ReadFile and
// re-parse entirely and returns the cached line (issue #731); a changed stat
// still pays the full reparse. This assumes the log at path is append-only:
// dispatch.LogPaths always points at the one pass log a single
// dispatch.runOnce writes (os.Create once, then append-only for the life of
// the pass), so identical (size, mtime) implies identical content. That's
// not true of os.Stat in general — some filesystems only resolve mtime to
// the second, so an in-place rewrite that lands on the same byte count
// within that window would keep both fields identical and get served stale
// from cache.
type HeartbeatCache struct {
	entries map[string]heartbeatCacheEntry
}

// NewHeartbeatCache returns an empty cache ready to use.
func NewHeartbeatCache() *HeartbeatCache {
	return &HeartbeatCache{entries: make(map[string]heartbeatCacheEntry)}
}

// RunningHeartbeat returns the live status line a running pick's queue row
// shows: it replays the on-disk log of number's most recent Dispatch pass
// (the initial run, or its latest fix/conflict-resolve pass) through drv's
// own heartbeat parser — the exact machinery the live dispatch's stdout
// heartbeat already uses (#647 AC2) — and returns the last line it emitted.
// Returns "" when drv is nil (a launch-less session, or a Launcher built
// without a Factory), no log exists on disk yet (claimed but not yet
// launched), or the log carries no complete heartbeat line yet. A repeat
// call for number whose latest pass log's path/size/mtime match what was
// cached last time skips the ReadFile+reparse and returns the cached line.
func (c *HeartbeatCache) RunningHeartbeat(drv driver.Driver, pwd, number string) string {
	if drv == nil {
		return ""
	}
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return ""
	}
	path := passes[len(passes)-1].Path
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// (path, size, modTime) match implies same content only because the log
	// is append-only (see HeartbeatCache doc comment above).
	if cached, ok := c.entries[number]; ok && cached.path == path && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		return cached.line
	}
	line, ok := parseHeartbeat(drv, path, number)
	if !ok {
		return ""
	}
	c.entries[number] = heartbeatCacheEntry{path: path, size: info.Size(), modTime: info.ModTime(), line: line}
	return line
}

// lastLine returns the last non-empty line of s, or "" when s carries none.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
