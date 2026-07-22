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

// TestReconcileWatches_NilWatcher_IsNoOp verifies reconcileWatches leaves a
// launch-less session (or one where fsnotify.NewWatcher failed at startup,
// newTeaModel's own nil-watcher fallback) alone rather than panicking on the
// nil watcher — the console's other refresh paths still cover it, per-Msg,
// off the poll tick alone (issue #1748).
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

// TestReconcileWatches_AddsRunningPickLogPath verifies reconcileWatches adds
// an fsnotify watch on a running pick's current log path — the "watches are
// added when a pick starts running / its log path appears" acceptance
// criterion (issue #1748).
func TestReconcileWatches_AddsRunningPickLogPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-9.log")
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

// TestReconcileWatches_RemovesWatchWhenPickStopsRunning verifies
// reconcileWatches drops the fsnotify watch (and the watchedPaths bookkeeping
// entry) for a pick that has left PickRunning — the "removed ... when it
// stops" half of the same acceptance criterion (issue #1748).
func TestReconcileWatches_RemovesWatchWhenPickStopsRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-9.log")
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

// TestReconcileWatches_NewPassPath_WatchesNewDropsOld verifies a pick whose
// latest pass log moves — a fix pass starting after the initial run — gets
// its new pass log watched and its old pass log's watch dropped, rather than
// watching both or neither ("a new Dispatch pass's new log path gets
// watched", issue #1748 AC).
func TestReconcileWatches_NewPassPath_WatchesNewDropsOld(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	initialPath := filepath.Join(dir, "logs", "issue-9.log")
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

	fixPath := filepath.Join(dir, "logs", "issue-9-fix-1.log")
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

// TestWaitLogWrite_TranslatesWriteEventToLogWriteMsg verifies the tea.Cmd
// waitLogWrite returns blocks until a write to a watched path, then
// translates it into logWriteMsg — the signal that lets Update's
// post-switch refreshPickDecorations call run within moments of new log
// bytes landing, rather than waiting for the next pollTickMsg (issue #1748).
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

// TestTea_LogWrite_RefreshesRunningRowHeartbeat_NotPoll verifies a running
// pick's row heartbeat updates off a real fsnotify write event, with the
// pollTickMsg fallback interval set far longer than the test's own timeout —
// so a passing waitForOutput here can only be explained by the fsnotify path,
// not the poll (issue #1748 AC1/AC2).
func TestTea_LogWrite_RefreshesRunningRowHeartbeat_NotPoll(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueOpen})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", "issue-42.log")
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

	// A live Dispatch (Pick #42 is still PickRunning) routes "q" through the
	// quit-confirm prompt rather than quitting directly — "d" (drain)
	// confirms it, same as every other running-launch teatest quit in this
	// package.
	sendKey(tm, "q")
	waitForOutput(t, tm, "quit with live Dispatches")
	sendKey(tm, "d")
	waitFinished(t, tm)
}
