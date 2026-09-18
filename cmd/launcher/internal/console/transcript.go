package console

import (
	"fmt"
	"os"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// DrillIn renders every pass log for number under pwd into one DrillInMsg, in
// both drv's rendered form and byte-exact raw form, so the raw toggle needs no
// further I/O. SidebarTranscriptCache re-calls it to live-tail a running
// Dispatch (issue #1736); Update preserves the toggle and offset (issue #719).
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

		text, err := drv.RenderTranscript(p.Path, driverkit.RenderOptions{})
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

type passStat struct {
	path    string
	size    int64
	modTime time.Time
}

// SidebarTranscriptCache caches the open sidebar's Transcript render, keyed by
// (Number, every pass log's path/size/modTime). The refresh runs on every
// tea.Msg while the Transcript view is active (issue #1736, extending #1502),
// so a stat match skips DrillIn's whole read and render (issue #731).
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

// Refresh returns number's current Transcript render, reusing the cached one
// when every pass log's (path, size, modTime) is unchanged, and ok false when
// no pass logs exist, a stat fails, or DrillIn failed. A failed DrillIn leaves
// the cache untouched rather than caching the failure, so a transient error
// mid-tail, such as a pass log created but not yet flushed, retries next tick.
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
