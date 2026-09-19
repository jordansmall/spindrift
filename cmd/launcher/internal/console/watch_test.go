package console

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	teatest "github.com/charmbracelet/x/exp/teatest"
	"github.com/fsnotify/fsnotify"

	"spindrift.dev/launcher/internal/forge"
)

// reconcileWatches must leave a launch-less session alone (or one where
// fsnotify.NewWatcher failed at startup and newTeaModel fell back to a nil
// watcher) rather than panicking on that nil watcher. The console's other
// refresh paths still cover it (issue #1748).
func TestReconcileWatches_NilWatcher_IsNoOp(t *testing.T) {
	tm := teaModel{
		pwd:          t.TempDir(),
		launch:       &Launcher{},
		watchedPaths: map[string]struct{}{},
	}
	tm.m.Picks = []Pick{{Number: "9", State: PickRunning}}

	got := tm.reconcileWatches()

	if len(got.watchedPaths) != 0 {
		t.Fatalf("watchedPaths = %v, want it to stay empty with a nil watcher", got.watchedPaths)
	}
}

// reconcileWatches adds a watch on a running pick's current log path as soon
// as that log appears (issue #1748).
func TestReconcileWatches_AddsRunningPickLogPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	if err := os.WriteFile(logPath, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	tm := teaModel{
		pwd:          dir,
		launch:       &Launcher{},
		watcher:      watcher,
		watchedPaths: map[string]struct{}{},
	}
	tm.m.Picks = []Pick{{Number: "9", State: PickRunning}}

	tm = tm.reconcileWatches()

	if _, ok := tm.watchedPaths[logPath]; !ok {
		t.Fatalf("watchedPaths = %v, want it to contain %q", tm.watchedPaths, logPath)
	}
	if list := watcher.WatchList(); !slices.Contains(list, logPath) {
		t.Fatalf("watcher.WatchList() = %v, want it to contain %q", list, logPath)
	}
}

// reconcileWatches drops both the fsnotify watch and the watchedPaths
// bookkeeping entry once a pick leaves PickRunning (issue #1748).
func TestReconcileWatches_RemovesWatchWhenPickStopsRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	if err := os.WriteFile(logPath, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	tm := teaModel{
		pwd:          dir,
		launch:       &Launcher{},
		watcher:      watcher,
		watchedPaths: map[string]struct{}{},
	}
	tm.m.Picks = []Pick{{Number: "9", State: PickRunning}}
	tm = tm.reconcileWatches()

	tm.m.Picks = []Pick{{Number: "9", State: PickSettled}}
	tm = tm.reconcileWatches()

	if _, ok := tm.watchedPaths[logPath]; ok {
		t.Fatalf("watchedPaths = %v, want it to no longer contain %q", tm.watchedPaths, logPath)
	}
	if list := watcher.WatchList(); slices.Contains(list, logPath) {
		t.Fatalf("watcher.WatchList() = %v, want it to no longer contain %q", list, logPath)
	}
}

// When a pick's latest pass log moves, as it does when a fix pass starts after
// the initial run, the new pass log gets watched and the old one's watch is
// dropped, rather than watching both or neither (issue #1748).
func TestReconcileWatches_NewPassPath_WatchesNewDropsOld(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	initialPath := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	if err := os.WriteFile(initialPath, []byte("first line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	t.Cleanup(func() { watcher.Close() })

	tm := teaModel{
		pwd:          dir,
		launch:       &Launcher{},
		watcher:      watcher,
		watchedPaths: map[string]struct{}{},
	}
	tm.m.Picks = []Pick{{Number: "9", State: PickRunning}}
	tm = tm.reconcileWatches()

	fixPath := filepath.Join(dir, ".spindrift", "logs", "issue-9-fix-1.log")
	if err := os.WriteFile(fixPath, []byte("fix pass line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm = tm.reconcileWatches()

	if _, ok := tm.watchedPaths[fixPath]; !ok {
		t.Fatalf("watchedPaths = %v, want it to contain the new pass path %q", tm.watchedPaths, fixPath)
	}
	if _, ok := tm.watchedPaths[initialPath]; ok {
		t.Fatalf("watchedPaths = %v, want it to no longer contain the old pass path %q", tm.watchedPaths, initialPath)
	}
	list := watcher.WatchList()
	if !slices.Contains(list, fixPath) {
		t.Fatalf("watcher.WatchList() = %v, want it to contain %q", list, fixPath)
	}
	if slices.Contains(list, initialPath) {
		t.Fatalf("watcher.WatchList() = %v, want it to no longer contain %q", list, initialPath)
	}
}

// waitLogWrite returns a tea.Cmd that blocks until a write to a watched path,
// then translates it into logWriteMsg. That message lets Update call
// refreshPickDecorations within moments of new log bytes landing instead of
// waiting for the next pollTickMsg (issue #1748).
func TestWaitLogWrite_TranslatesWriteEventToLogWriteMsg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issue-9.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	t.Cleanup(func() { watcher.Close() })
	if err := watcher.Add(path); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	cmd := waitLogWrite(watcher, done)

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("a line\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgCh:
		if _, ok := msg.(logWriteMsg); !ok {
			t.Fatalf("cmd() = %#v (%T), want logWriteMsg", msg, msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for logWriteMsg")
	}
}

// A running pick's row heartbeat updates off a real fsnotify write event. The
// pollTickMsg fallback interval is deliberately set far longer than the test's
// own timeout, so a passing waitForOutput here can only be explained by the
// fsnotify path and not by the poll (issue #1748).
func TestTea_LogWrite_RefreshesRunningRowHeartbeat_NotPoll(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, ".spindrift", "logs", "issue-42.log")
	first := `{"type":"result","num_turns":17,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}

	launch := newTestLauncher(t, f)
	launch.pollInterval = time.Hour
	launch.queue.Add(Pick{Number: "42", Title: "fix the thing", State: PickRunning})

	tm := teatest.NewTestModel(t, newTeaModel(f, dir, launch), teatest.WithInitialTermSize(80, 24))
	waitForOutput(t, tm, "fix the thing")

	sendKey(tm, "2")
	waitForOutput(t, tm, "17 turn")

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	second := `{"type":"result","num_turns":99,"total_cost_usd":0.02,"duration_ms":9000}` + "\n"
	if _, err := logFile.WriteString(second); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	waitForOutput(t, tm, "99 turn")

	// Pick #42 is still PickRunning, so the live Dispatch routes "q" through the
	// quit-confirm prompt rather than quitting directly; "d" (drain) confirms it.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)
}
