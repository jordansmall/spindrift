package console

import (
	"fmt"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// DrillIn loads and renders every pass log dispatch.LogPaths finds for
// number under pwd, concatenated in chronological order with a boundary
// line between passes — both in drv's rendered form and in byte-exact raw
// form, loaded together so the raw toggle needs no further I/O. Wraps the
// result into a DrillInMsg. Two callers: openSidebarCmd combines it with
// ActivityFeed's derivation into the SidebarLoadedMsg Update applies on
// open, matching Refresh and PickIssue's adapter shape; SidebarTranscriptCache
// calls it again, stat-gated, to live-tail the Transcript view while it's
// active on a running or orphan-flagged Dispatch (issue #1736). Update
// preserves the ShowTranscript/ShowRaw toggle and scroll Offset across a
// same-number SidebarLoadedMsg (issue #719).
func DrillIn(drv driver.Driver, pwd, number string) Msg {
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return DrillInMsg{Number: number, Err: fmt.Errorf("no logs found for issue #%s", number)}
	}

	var rendered, raw strings.Builder
	for _, p := range passes {
		boundary := fmt.Sprintf("=== pass: %s ===\n", p.Label)
		rendered.WriteString(boundary)
		raw.WriteString(boundary)

		text, err := drv.RenderTranscript(p.Path)
		if err != nil {
			return DrillInMsg{Number: number, Err: err}
		}
		rendered.WriteString(SanitizeControlSequences(text))

		bytes, err := os.ReadFile(p.Path)
		if err != nil {
			return DrillInMsg{Number: number, Err: err}
		}
		raw.Write(bytes)
	}
	return DrillInMsg{Number: number, Rendered: rendered.String(), Raw: raw.String()}
}

// passStat is one pass log's identity at the moment it was last read —
// SidebarTranscriptCache's per-pass analogue of SidebarActivityCache's own
// single-file (path, size, modTime) key, extended to a slice since DrillIn
// reads every pass log, not just the latest.
type passStat struct {
	path    string
	size    int64
	modTime time.Time
}

// SidebarTranscriptCache remembers the open sidebar's last-refreshed
// Transcript render, keyed by (Number, every pass log's path/size/modTime) —
// refreshPickDecorations's per-Msg refresh runs on every tea.Msg while the
// Transcript view is active (issue #1736, extending #1502's Activity-only
// live-tail), so most calls see the same on-disk pass logs as last time; a
// stat match across every pass skips DrillIn's full read+render and returns
// the cached forms, mirroring SidebarActivityCache's own skip (issue #731).
// Single-entry rather than a map: only one sidebar can be open at a time.
type SidebarTranscriptCache struct {
	number        string
	stats         []passStat
	rendered, raw string
}

// NewSidebarTranscriptCache returns an empty cache ready to use.
func NewSidebarTranscriptCache() *SidebarTranscriptCache {
	return &SidebarTranscriptCache{}
}

// Refresh returns number's current Transcript render — freshly re-derived
// via DrillIn, or the cached one when every pass log's (path, size, modTime)
// matches what was cached last time — plus ok, false when no pass logs exist
// yet, a pass log can't be stat'd, or DrillIn's own read/render failed. A
// failed DrillIn deliberately leaves the cache (and so the sidebar's current
// content) untouched rather than caching the failure, so a transient error
// mid-tail — e.g. a fresh pass log created but not yet flushed — retries on
// the next tick instead of blanking an already-good render.
func (c *SidebarTranscriptCache) Refresh(drv driver.Driver, pwd, number string) (rendered, raw string, ok bool) {
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return "", "", false
	}
	stats := make([]passStat, len(passes))
	for i, p := range passes {
		info, err := os.Stat(p.Path)
		if err != nil {
			return "", "", false
		}
		stats[i] = passStat{path: p.Path, size: info.Size(), modTime: info.ModTime()}
	}
	if c.number == number && passStatsEqual(c.stats, stats) {
		return c.rendered, c.raw, true
	}
	dm, _ := DrillIn(drv, pwd, number).(DrillInMsg)
	if dm.Err != nil {
		return "", "", false
	}
	c.number, c.stats, c.rendered, c.raw = number, stats, dm.Rendered, dm.Raw
	return c.rendered, c.raw, true
}

// passStatsEqual reports whether a and b carry the same ordered sequence of
// pass-log stats — Refresh's cache-hit check, false on any length, path,
// size, or modTime mismatch (a new pass appearing, mid-tail growth, or a
// rewritten pass log alike).
func passStatsEqual(a, b []passStat) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].path != b[i].path || a[i].size != b[i].size || !a[i].modTime.Equal(b[i].modTime) {
			return false
		}
	}
	return true
}
