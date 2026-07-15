package console

import (
	"bytes"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
)

// RunningHeartbeat returns the live status line a running pick's queue row
// shows: it replays the on-disk log of number's most recent Dispatch pass
// (the initial run, or its latest fix/conflict-resolve pass) through drv's
// own heartbeat parser — the exact machinery the live dispatch's stdout
// heartbeat already uses (#647 AC2), not a new one — and returns the last
// line it emitted. Returns "" when drv is nil (a launch-less session, or a
// Launcher built without a Factory), no log exists on disk yet (claimed but
// not yet launched), or the log carries no complete heartbeat line yet.
func RunningHeartbeat(drv driver.Driver, pwd, number string) string {
	if drv == nil {
		return ""
	}
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return ""
	}
	data, err := os.ReadFile(passes[len(passes)-1].Path)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	w := drv.NewHeartbeatWriter(io.Discard, number, &buf)
	if _, err := w.Write(data); err != nil {
		return ""
	}
	return lastLine(buf.String())
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
