package console

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/fsnotify/fsnotify"

	"spindrift.dev/launcher/internal/dispatch"
)

// logWriteMsg reports that a watched running pick's log grew. It carries no
// payload because refreshPickDecorations re-checks every running pick's own
// cached offset, so reaching Update is enough (issue #1748).
type logWriteMsg struct{}

// waitLogWrite turns the next write on a watched path into a logWriteMsg.
// Non-write events (Create/Rename/Chmod) and watch errors loop rather than
// wake Update for nothing; only done closing returns nil instead of a Msg.
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

// reconcileWatches watches every running pick's current log path and unwatches
// the rest. A pick moving to a new Dispatch pass reads as one removal plus one
// add, since dispatch.LogPaths returns only the latest pass (issue #1748). A
// nil watcher makes this a no-op, and a log deleted then recreated at a path
// watchedPaths still names is never re-Added; both ride the poll fallback.
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
