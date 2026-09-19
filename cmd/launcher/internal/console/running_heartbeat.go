package console

import (
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// appendHeartbeat reads the bytes appended to entry.path since entry.offset
// and returns the last line the tail emitted, or the prior line when the tail
// held no complete one (issue #1776). A truncation between RunningHeartbeat's
// stat and the tailer's Seek reads zero bytes rather than erroring, so the
// cached line stands for one more refresh and corrects itself as the file grows.
func appendHeartbeat(drv driver.Driver, number string, entry *heartbeatCacheEntry) (line string, ok bool) {
	data, ok := entry.readAppended(drv, number)
	if !ok {
		return "", false
	}
	if l := lastLine(data); l != "" {
		entry.line = l
	}
	return entry.line, true
}

// heartbeatCacheEntry holds the per-log-path parser state HeartbeatCache keeps
// alive across refreshes (issue #1776).
type heartbeatCacheEntry struct {
	tailer
	line string
}

// HeartbeatCache remembers each running pick's heartbeat parser, keyed by pick
// number. refreshPickDecorations calls RunningHeartbeat on every tea.Msg, so
// most calls see an unchanged log: a size equal to the cached offset skips the
// read (issue #731), and a larger size parses only the appended tail (issue
// #1747), which holds only because the pass log is append-only.
type HeartbeatCache struct {
	entries map[string]heartbeatCacheEntry
}

// NewHeartbeatCache returns an empty cache.
func NewHeartbeatCache() *HeartbeatCache {
	return &HeartbeatCache{entries: make(map[string]heartbeatCacheEntry)}
}

// RunningHeartbeat returns the live status line for a running pick's queue
// row, parsed from the most recent Dispatch pass log by drv's heartbeat
// parser (#647 AC2). Returns "" when drv is nil, no log exists yet, or no
// complete line has been written. A new pass path or a size below the cached
// offset restarts the parser at offset 0 rather than reusing stale state.
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

	entry, ok := c.entries[number]
	if !ok || entry.path != path || info.Size() < entry.offset {
		entry = heartbeatCacheEntry{tailer: tailer{path: path}}
	}

	if info.Size() == entry.offset {
		c.entries[number] = entry
		return entry.line
	}

	line, ok := appendHeartbeat(drv, number, &entry)
	if !ok {
		return ""
	}
	c.entries[number] = entry
	return line
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
