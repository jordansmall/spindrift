package console

import (
	"bytes"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// appendHeartbeat reads the bytes appended to entry.path since entry.offset,
// feeding only that tail to entry.writer — creating writer and out on first
// use — and advances entry.offset by what it read. writer is drv's own
// stateful heartbeat parser (role, turn counts, phase all persist on it
// across calls), so this replays exactly what a whole-file reparse would
// have replayed, just without re-walking bytes already consumed. out is
// reset before every feed so it only ever holds this call's own emitted
// lines, not the pass's whole history: that keeps both the buffer and the
// lastLine scan bounded by one refresh's output rather than growing with
// the pass. entry.line only advances when this call emits a line, since a
// call that appends no complete new line must still return the prior one.
// ok is false when the file can't be read or written through drv's parser
// (the same "no heartbeat yet" cases HeartbeatCache.RunningHeartbeat
// returns "" for).
func appendHeartbeat(drv driver.Driver, number string, entry *heartbeatCacheEntry) (line string, ok bool) {
	f, err := os.Open(entry.path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if _, err := f.Seek(entry.offset, io.SeekStart); err != nil {
		return "", false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	if entry.writer == nil {
		entry.out = &bytes.Buffer{}
		entry.writer = drv.NewHeartbeatWriter(io.Discard, number, entry.out)
	} else {
		entry.out.Reset()
	}
	if _, err := entry.writer.Write(data); err != nil {
		return "", false
	}
	entry.offset += int64(len(data))
	if l := lastLine(entry.out.String()); l != "" {
		entry.line = l
	}
	return entry.line, true
}

// heartbeatCacheEntry holds the persistent per-log-path parser state
// HeartbeatCache keeps alive across refreshes: the byte offset already fed
// to writer, drv's own stateful heartbeat Writer, the scratch buffer that
// pins each call's own emitted output, and the last line ever emitted for
// this path.
type heartbeatCacheEntry struct {
	path   string
	offset int64
	writer io.Writer
	out    *bytes.Buffer
	line   string
}

// HeartbeatCache remembers each running pick's persistent heartbeat parser,
// keyed by pick number. refreshPickDecorations calls RunningHeartbeat on every tea.Msg —
// every keypress, poll tick, and refresh signal, not just a fixed render
// cadence — so most calls see the exact same on-disk log as last time. A
// size that matches the cached entry's offset means nothing new was
// appended; RunningHeartbeat skips the read entirely and returns the cached
// line (issue #731). A size ahead of the offset reads and parses only the
// appended tail (issue #1747) — drv's heartbeat Writer carries its own
// parse state (role, turn counts, phase) across that read, so this produces
// the same result a whole-file reparse would, without re-walking bytes
// already consumed. This assumes the log at path is append-only:
// dispatch.LogPaths always points at the one pass log a single
// dispatch.runOnce writes (os.Create once, then append-only for the life of
// the pass), so bytes at [0, offset) never change underneath a cached entry.
type HeartbeatCache struct {
	entries map[string]heartbeatCacheEntry
}

// NewHeartbeatCache returns an empty cache ready to use.
func NewHeartbeatCache() *HeartbeatCache {
	return &HeartbeatCache{entries: make(map[string]heartbeatCacheEntry)}
}

// RunningHeartbeat returns the live status line a running pick's queue row
// shows: it feeds the on-disk log of number's most recent Dispatch pass
// (the initial run, or its latest fix/conflict-resolve pass) through drv's
// own heartbeat parser — the exact machinery the live dispatch's stdout
// heartbeat already uses (#647 AC2) — and returns the last line it emitted.
// Returns "" when drv is nil (a launch-less session, or a Launcher built
// without a Factory), no log exists on disk yet (claimed but not yet
// launched), or the log carries no complete heartbeat line yet. A repeat
// call for number whose latest pass log's path is unchanged and hasn't grown
// since the cached call skips the read entirely and returns the cached
// line; a log that grew is read and parsed from the cached offset forward,
// not from byte 0. A path change (a new Dispatch pass) or a size that fell
// behind the cached offset (truncation/rotation) starts a fresh parser at
// offset 0 rather than reuse stale state.
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
		entry = heartbeatCacheEntry{path: path}
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

// lastLine returns the last non-empty line of s, or "" when s carries none.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}
