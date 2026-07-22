package console

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"spindrift.dev/launcher/internal/dispatch"
)

// logWriteMsg is the tea layer's signal that a watched running pick's log
// grew — landing it is enough to reach Update's post-switch
// refreshPickDecorations call, the same incremental heartbeat refresh a
// keypress or the pollTickMsg fallback already drives; the Msg itself
// carries no payload since refreshPickDecorations already re-checks every
// running pick's own cached offset (issue #1748).
type logWriteMsg struct{}

// waitLogWrite blocks on watcher's event stream for the next write to a
// watched path and translates it into logWriteMsg, mirroring
// waitRefreshSignal's own select-on-channel-plus-done shape (tea.go) so a
// quit unblocks this the same way. Non-write events (Create/Rename/Chmod)
// and watch errors loop back rather than waking Update for nothing; done
// closing (Quitting) is the only way this returns nil instead of a Msg.
func waitLogWrite(watcher *fsnotify.Watcher, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				if event.Op&fsnotify.Write != 0 {
					return logWriteMsg{}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
			case <-done:
				return nil
			}
		}
	}
}

// reconcileWatches adds an fsnotify watch for every PickRunning row's
// current log path not already watched, and removes every watch whose pick
// is no longer running (or whose path moved to a new Dispatch pass) — the
// same add-on-start/remove-on-stop lifecycle a new pass's fresh log path
// also drives, since dispatch.LogPaths returns the latest pass's path and a
// path change always reads as "old path no longer desired, new path is"
// (issue #1748). A nil watcher (launch-less session, or fsnotify.NewWatcher
// failed at startup) makes this a no-op — refreshPickDecorations still runs
// on every Msg regardless, so the console stays correct off the slower
// per-Msg/poll cadence alone. Same if a log is deleted and recreated at its
// existing path while its pick keeps running (not something a normal
// append-only pass does, see RunningHeartbeat's own doc comment, but not
// impossible): the kernel drops the watch on delete, watchedPaths still
// names the path so nothing re-Adds it, and that one row rides the poll
// fallback (plus RunningHeartbeat's own size-regression reset) until its
// pick's state or pass path actually changes.
func (t teaModel) reconcileWatches() teaModel {
	if t.watcher == nil {
		return t
	}
	desired := make(map[string]struct{}, len(t.m.Picks))
	for _, p := range t.m.Picks {
		if p.State != PickRunning {
			continue
		}
		passes := dispatch.LogPaths(t.pwd, p.Number)
		if len(passes) == 0 {
			continue
		}
		desired[passes[len(passes)-1].Path] = struct{}{}
	}
	for path := range desired {
		if _, ok := t.watchedPaths[path]; ok {
			continue
		}
		if err := t.watcher.Add(path); err == nil {
			t.watchedPaths[path] = struct{}{}
		}
	}
	for path := range t.watchedPaths {
		if _, ok := desired[path]; ok {
			continue
		}
		_ = t.watcher.Remove(path)
		delete(t.watchedPaths, path)
	}
	return t
}
