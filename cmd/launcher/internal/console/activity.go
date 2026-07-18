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
	Time time.Time
	Text string
}

// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser -- the same parseHeartbeat machinery RunningHeartbeat uses
// against the same latest pass log (#647 AC2) -- and returns the whole
// ordered sequence of status lines it emitted, rather than just the last one.
// Consecutive identical lines collapse to one entry, so the feed reads as one
// line per distinct Driver step (#1501 AC1). Every returned line carries the
// same Time: the pass log's on-disk ModTime as this call observed it -- the
// raw stream-json carries no per-event timestamp of its own, so the file's
// mtime is the coarsest-but-real signal available without fabricating one.
// Returns nil when no log exists yet for number (claimed
// but not yet launched) or when the log can't be read or parsed -- the same
// graceful-empty contract RunningHeartbeat uses, rather than an error every
// caller must handle.
func ActivityFeed(drv driver.Driver, pwd, number string) []ActivityLine {
	if drv == nil {
		return nil
	}
	passes := dispatch.LogPaths(pwd, number)
	if len(passes) == 0 {
		return nil
	}
	path := passes[len(passes)-1].Path
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	w := drv.NewHeartbeatWriter(io.Discard, number, &buf)
	if _, err := w.Write(data); err != nil {
		return nil
	}
	return collapseActivityLines(buf.String(), info.ModTime())
}

// collapseActivityLines splits s on newlines, drops a trailing empty line,
// and collapses any run of consecutive identical lines to their first
// occurrence -- the heartbeat writer emits one line per parsed event, not
// per distinct step, so two events that narrate the same text back-to-back
// would otherwise duplicate the same line in the feed.
func collapseActivityLines(s string, t time.Time) []ActivityLine {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	var out []ActivityLine
	var last string
	first := true
	for _, line := range strings.Split(s, "\n") {
		if !first && line == last {
			continue
		}
		out = append(out, ActivityLine{Time: t, Text: line})
		last = line
		first = false
	}
	return out
}
