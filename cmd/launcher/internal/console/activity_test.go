package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser, the same machinery RunningHeartbeat uses, and returns the
// whole ordered sequence of status lines, not just the last one (#1501 AC1).
func TestActivityFeed_ReplaysLatestPassLog_ReturnsOrderedDistinctLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Like every result fixture in this package, this omits the KindError echo
	// claude's decoder appends after a result; no console path reads it.
	log := strings.Join([]string{
		`{"Kind":"start"}`,
		`{"Kind":"model"}`,
		`{"Kind":"text","Text":"Reading the config file."}`,
		`{"Kind":"usage"}`,
		`{"Kind":"start"}`,
		`{"Kind":"model"}`,
		`{"Kind":"tool_call","ToolID":"e1","ToolName":"Edit","ToolInput":{}}`,
		`{"Kind":"usage"}`,
		`{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":3}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
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

// A second Refresh against a pass log whose size matches the cached offset
// returns the cached feed instead of re-deriving it. refreshPickDecorations
// refreshes on every tea.Msg, so most calls see the same on-disk log as last
// time (issue #1502, mirroring HeartbeatCache's own skip, issue #731).
func TestSidebarActivityCache_UnchangedStat_SkipsReparse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	first := `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"aaaaa"}` + "\n" + `{"Kind":"usage"}` + "\n"
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
	if len(first1) == 0 {
		t.Fatal("first Refresh: activity = empty, want at least one line")
	}

	second := `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"bbbbb"}` + "\n" + `{"Kind":"usage"}` + "\n"
	if len(second) != len(first) {
		t.Fatalf("test setup: second log must be same length as first, got %d want %d", len(second), len(first))
	}
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
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

// A call whose latest pass log grew since the cached call reparses and returns
// the new content, not the stale cached feed (issue #1502).
func TestSidebarActivityCache_ChangedStat_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	first := `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"first"}` + "\n" + `{"Kind":"usage"}` + "\n"
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

	second := first + `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"second"}` + "\n" + `{"Kind":"usage"}` + "\n"
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

// Refresh's cache-hit check compares Number as well as path and offset, so
// selecting a different Dispatch invalidates the cache even though the new
// Dispatch's own log stat has never been seen before. A stale entry must never
// leak one Dispatch's feed onto a freshly selected one (issue #1502).
func TestSidebarActivityCache_NumberChange_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLog := func(number, text string) {
		t.Helper()
		line := `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"` + text + `"}` + "\n" + `{"Kind":"usage"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-"+number+".log"), []byte(line), 0o644); err != nil {
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

// When the second of two consecutive identical narration events lands in a
// later append than the first, the incremental cache still collapses them to
// one feed entry, the same collapse ActivityFeed's whole-file parse gives two
// back-to-back duplicates (#1501 AC1). The append-tail switch must leak no
// duplicate line at the boundary between increments (issue #1749 AC3 parity).
func TestSidebarActivityCache_DuplicateNarrationAcrossIncrementalBoundary_CollapsesToOneLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	narration := `{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"text","Text":"Reading the config file."}` + "\n" + `{"Kind":"usage"}` + "\n"

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	if err := os.WriteFile(path, []byte(narration), 0o644); err != nil {
		t.Fatal(err)
	}
	first, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("first Refresh: ok = false, want true")
	}

	if err := os.WriteFile(path, []byte(narration+narration), 0o644); err != nil {
		t.Fatal(err)
	}
	second, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("second Refresh: ok = false, want true")
	}

	var narrationCount int
	for _, line := range second {
		if strings.Contains(line.Text, "Reading the config file") {
			narrationCount++
		}
	}
	if narrationCount != 1 {
		t.Errorf("Refresh() after a duplicate narration split across two appends had %d narration lines, want exactly 1 (collapsed across the append boundary): %v", narrationCount, second)
	}
	if !activityEqual(first, second) {
		t.Errorf("Refresh() after a duplicate append = %v, want unchanged from first Refresh %v (the second event added no new distinct line)", second, first)
	}
}

// When a watched log's size falls below the cache's stored offset (truncated
// or rotated out from under a running Dispatch), Refresh starts a fresh parser
// at 0 instead of seeking past the file's new end and mis-parsing. This is the
// sidebar feed's analogue of
// TestRunningHeartbeat_FileShorterThanOffset_ResetsAndReparses.
func TestSidebarActivityCache_FileShorterThanOffset_ResetsAndReparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	long := `{"Kind":"start"}` + "\n" +
		`{"Kind":"model"}` + "\n" +
		`{"Kind":"tool_call","ToolID":"r1","ToolName":"Read","ToolInput":{}}` + "\n" +
		`{"Kind":"usage"}` + "\n" +
		`{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":7}` + "\n"
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewSidebarActivityCache()

	if _, ok := cache.Refresh(drv, dir, "9"); !ok {
		t.Fatal("first Refresh: ok = false, want true")
	}

	short := `{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":1000,"NumTurns":2}` + "\n"
	if len(short) >= len(long) {
		t.Fatalf("test setup: short log must be shorter than long, got %d want < %d", len(short), len(long))
	}
	if err := os.WriteFile(path, []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := cache.Refresh(drv, dir, "9")
	if !ok {
		t.Fatal("second Refresh: ok = false, want true")
	}
	var sawTwoTurns bool
	for _, line := range got {
		if strings.Contains(line.Text, "2 turn") {
			sawTwoTurns = true
		}
	}
	if !sawTwoTurns {
		t.Errorf("Refresh() after truncation = %v, want it to reflect the shorter file's own content, not a mis-parse of stale offset state", got)
	}
}

// Three successive Refresh calls against the same growing pass log each hand
// the heartbeat parser only the appended bytes, not the whole file again, now
// that the Activity feed uses the same append-tail parser RunningHeartbeat
// uses (issue #1749). It mirrors
// TestRunningHeartbeat_IncrementalAppend_FeedsOnlyAppendedBytes.
func TestSidebarActivityCache_IncrementalAppend_FeedsOnlyAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")

	real, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	var fed int
	drv := spyHeartbeatDriver{Driver: real, fed: &fed}
	cache := NewSidebarActivityCache()

	chunks := []string{
		`{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"tool_call","ToolID":"r1","ToolName":"Read","ToolInput":{}}` + "\n" + `{"Kind":"usage"}` + "\n",
		`{"Kind":"start"}` + "\n" + `{"Kind":"model"}` + "\n" + `{"Kind":"tool_call","ToolID":"g1","ToolName":"Grep","ToolInput":{}}` + "\n" + `{"Kind":"usage"}` + "\n",
		`{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":3}` + "\n",
	}

	var written string
	for _, chunk := range chunks {
		written += chunk
		if err := os.WriteFile(path, []byte(written), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := cache.Refresh(drv, dir, "9"); !ok {
			t.Fatal("Refresh: ok = false, want true")
		}
	}

	if fed != len(written) {
		t.Errorf("bytes fed to heartbeat parser across 3 Refreshes = %d, want %d (exactly the appended bytes, not a whole-file reread each call)", fed, len(written))
	}
}

// Refresh reports ok=false when number has no pass log on disk yet, matching
// RunningHeartbeat's no-log contract, so refreshPickDecorations can skip
// sending a refresh rather than clobbering an already-loaded feed with an
// empty one (issue #1502).
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

// The heartbeat writer emits one line per parsed event, not per distinct step,
// so two events narrating the same trimmed text back-to-back collapse to a
// single feed entry rather than a literal per-event replay (#1501 AC1).
func TestActivityFeed_ConsecutiveIdenticalNarration_CollapsesToOneLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"Kind":"start"}`,
		`{"Kind":"model"}`,
		`{"Kind":"text","Text":"Reading the config file."}`,
		`{"Kind":"usage"}`,
		`{"Kind":"start"}`,
		`{"Kind":"model"}`,
		`{"Kind":"text","Text":"Reading the config file."}`,
		`{"Kind":"usage"}`,
		`{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":1}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
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

// A pick that has written no log yet (claimed but not yet launched) renders no
// Activity feed rather than erroring (#1501 AC1).
func TestActivityFeed_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := ActivityFeed(drv, t.TempDir(), "9"); got != nil {
		t.Errorf("ActivityFeed() = %v, want nil with no log on disk", got)
	}
}

// A pass log that exists on disk but can't be read degrades to an empty feed
// instead of erroring (#1501 AC1).
func TestActivityFeed_UnreadableLog_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".spindrift", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory at the log's path fails os.ReadFile with EISDIR, standing in
	// for any read failure LogPaths' existence check can't rule out ahead of time.
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

func TestActivityFeed_ShowsHostOpLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"Kind":"host_op","HostOp":{"op":"pass_start","pass":1,"role":"implement"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	for _, line := range ActivityFeed(drv, dir, "9") {
		if strings.Contains(line.Text, "pass 1 (implement) started") {
			return
		}
	}
	t.Error("ActivityFeed() has no pass-start host-op line")
}
