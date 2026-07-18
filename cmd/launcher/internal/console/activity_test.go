package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestActivityFeed_ReplaysLatestPassLog_ReturnsOrderedDistinctLines verifies
// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser -- the same machinery RunningHeartbeat uses -- and returns
// the whole ordered sequence of emitted status lines, not just the last one
// (#1501 AC1).
func TestActivityFeed_ReplaysLatestPassLog_ReturnsOrderedDistinctLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","id":"e1","input":{}}]}}`,
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":5000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := ActivityFeed(drv, dir, "9")

	if len(got) < 2 {
		t.Fatalf("ActivityFeed() = %d lines, want at least 2 (narration + final turns line)", len(got))
	}
	var sawNarration bool
	for _, line := range got[:len(got)-1] {
		if strings.Contains(line.Text, "Reading the config file") {
			sawNarration = true
		}
	}
	if !sawNarration {
		t.Errorf("ActivityFeed() = %v, want one line to contain the narration", got)
	}
	last := got[len(got)-1]
	if !strings.Contains(last.Text, "3 turn") {
		t.Errorf("last line = %q, want it to contain %q", last.Text, "3 turn")
	}
}

// TestSidebarActivityCache_UnchangedStat_SkipsReparse verifies a second
// Refresh call against a pass log whose (path, size, modTime) match what was
// cached last time returns the cached feed rather than re-deriving it —
// syncQueue's per-Msg refresh runs on every tea.Msg, so most calls see the
// same on-disk log as last time (issue #1502, mirroring HeartbeatCache's own
// skip, issue #731).
func TestSidebarActivityCache_UnchangedStat_SkipsReparse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "issue-9.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"aaaaa"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	first1, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("first Refresh: ok = false, want true")
	}
	if len(first1) == 0 {
		t.Fatal("first Refresh: activity = empty, want at least one line")
	}

	second := `{"type":"assistant","message":{"content":[{"type":"text","text":"bbbbb"}]}}` + "\n"
	if len(second) != len(first) {
		t.Fatalf("test setup: second log must be same length as first, got %d want %d", len(second), len(first))
	}
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	got, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("second Refresh: ok = false, want true")
	}
	if !activityEqual(got, first1) {
		t.Errorf("Refresh() after unchanged stat = %v, want cached %v (unchanged stat must skip reparse)", got, first1)
	}
}

// TestSidebarActivityCache_ChangedStat_Reparses verifies a call whose latest
// pass log grew since the cached call reparses and returns the new content,
// not the stale cached feed (issue #1502).
func TestSidebarActivityCache_ChangedStat_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "issue-9.log")
	first := `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	first1, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("first Refresh: ok = false, want true")
	}

	second := first + `{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("second Refresh: ok = false, want true")
	}
	if activityEqual(got, first1) {
		t.Error("Refresh() after a changed stat returned the stale cached feed, want a reparse")
	}
	var sawSecond bool
	for _, line := range got {
		if strings.Contains(line.Text, "second") {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Errorf("Refresh() = %v, want a line containing the new content", got)
	}
}

// TestSidebarActivityCache_NumberChange_Reparses verifies switching which
// Dispatch is cached (the operator selected a different running Dispatch)
// invalidates the cache even though the new Dispatch's own log stat has
// never been seen before — Refresh's cache-hit check compares Number too,
// not just (path, size, modTime), so a stale entry never leaks a wrong
// Dispatch's feed onto a freshly selected one (issue #1502).
func TestSidebarActivityCache_NumberChange_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLog := func(number, text string) {
		t.Helper()
		line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "logs", "issue-"+number+".log"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLog("9", "dispatch nine")
	writeLog("10", "dispatch ten")

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	nine, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("Refresh(9): ok = false, want true")
	}

	ten, ok := cache.Refresh(drv, dir, "10")
	if !ok {
		t.Fatal("Refresh(10): ok = false, want true")
	}

	if activityEqual(nine, ten) {
		t.Fatal("test setup: #9 and #10's feeds must differ")
	}
	var sawTen bool
	for _, line := range ten {
		if strings.Contains(line.Text, "dispatch ten") {
			sawTen = true
		}
	}
	if !sawTen {
		t.Errorf("Refresh(10) = %v, want #10's own content, not a cached #9 feed", ten)
	}
}

// TestSidebarActivityCache_NoLogsOnDisk_ReturnsFalse verifies Refresh reports
// ok=false when number has no pass log on disk yet — RunningHeartbeat's own
// no-log contract, so syncQueue's caller can skip sending a refresh rather
// than clobbering an already-loaded feed with an empty one (issue #1502).
func TestSidebarActivityCache_NoLogsOnDisk_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	got, ok := cache.Refresh(drv, dir, "9")
	if ok {
		t.Errorf("Refresh() = (%v, true), want ok = false with no log on disk", got)
	}
}

// TestActivityFeed_ConsecutiveIdenticalNarration_CollapsesToOneLine verifies
// two events that narrate the exact same trimmed text back-to-back (the
// heartbeat writer emits one line per parsed event, not per distinct step)
// collapse to a single feed entry, so the feed reads as one line per
// distinct step rather than a literal per-event replay (#1501 AC1).
func TestActivityFeed_ConsecutiveIdenticalNarration_CollapsesToOneLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":5000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := ActivityFeed(drv, dir, "9")

	var narrationCount int
	for _, line := range got {
		if strings.Contains(line.Text, "Reading the config file") {
			narrationCount++
		}
	}
	if narrationCount != 1 {
		t.Errorf("ActivityFeed() had %d narration lines, want exactly 1 (consecutive duplicates collapsed): %v", narrationCount, got)
	}
}

// TestActivityFeed_NoLogsOnDisk_ReturnsEmpty verifies a pick that hasn't
// written any log yet (claimed but not yet launched) renders no Activity
// feed rather than erroring (#1501 AC1).
func TestActivityFeed_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := ActivityFeed(drv, t.TempDir(), "9"); got != nil {
		t.Errorf("ActivityFeed() = %v, want nil with no log on disk", got)
	}
}

// TestActivityFeed_UnreadableLog_ReturnsEmpty verifies a pass log that
// exists on disk but can't be read (a directory in its place, standing in
// for any read failure) degrades to an empty feed instead of erroring
// (#1501 AC1).
func TestActivityFeed_UnreadableLog_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory at the log's path fails os.ReadFile with EISDIR, standing
	// in for any on-disk read failure LogPaths' existence check can't rule
	// out ahead of time.
	if err := os.MkdirAll(filepath.Join(logDir, "issue-9.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := ActivityFeed(drv, dir, "9"); got != nil {
		t.Errorf("ActivityFeed() = %v, want nil for an unreadable log", got)
	}
}
