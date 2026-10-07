package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// getHistory serves a checkout holding the given status file (nil leaves it
// absent) and Events generations, keyed by file name.
func getHistory(t *testing.T, status *string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	statusPath := filepath.Join(dir, statusFileName)
	if status != nil {
		if err := os.WriteFile(statusPath, []byte(*status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newServer(statusPath)
	srv.now = func() time.Time { return testNow }
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func inOrder(t *testing.T, body string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(body[at:], p)
		if i < 0 {
			t.Fatalf("%q not found after offset %d in order", p, at)
		}
		at += i + len(p)
	}
}

func TestHistorySpansGenerationsNewestFirst(t *testing.T) {
	older := `{"time":"2026-10-06T09:00:00Z","event":"child_start","kind":"work","slot":0}` + "\n"
	newer := `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"research","slot":1}` + "\n" +
		`{"time":"2026-10-07T09:05:00Z","event":"box","kind":"work","slot":0,"phase":"fix-pass-1","issue":"42"}` + "\n"
	body := getHistory(t, nil, map[string]string{
		eventsFileName:                 newer,
		eventsFileName + rotatedSuffix: older,
	})
	inOrder(t, body, "2026-10-07T09:05:00Z", "2026-10-07T09:00:00Z", "2026-10-06T09:00:00Z")
	for _, want := range []string{"fix-pass-1", "#42", "research"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}

func TestHistorySettledShowsStateAndNote(t *testing.T) {
	ev := `{"time":"2026-10-07T09:10:00Z","event":"settled","kind":"work","slot":2,"issue":"7","state":"failed","note":"CI red <b>twice</b>"}` + "\n"
	body := getHistory(t, nil, map[string]string{eventsFileName: ev})
	for _, want := range []string{"settled", "outcome-failed", "CI red &lt;b&gt;twice&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if strings.Contains(body, "<b>twice") {
		t.Errorf("note not escaped")
	}
}

func TestHistoryDetails(t *testing.T) {
	ev := `{"time":"2026-10-07T09:01:00Z","event":"backoff","kind":"work","wait":"5m0s","reason":"exit 1"}` + "\n" +
		`{"time":"2026-10-07T09:02:00Z","event":"jam","kind":"research","wait":"7m0s","reason":"jam-reason-xyz"}` + "\n" +
		`{"time":"2026-10-07T09:03:00Z","event":"breaker_trip","kind":"work","failures":3,"wait":"31m0s"}` + "\n" +
		`{"time":"2026-10-07T09:04:00Z","event":"halt","reason":"halt-reason-xyz"}` + "\n" +
		`{"time":"2026-10-07T09:05:00Z","event":"box","kind":"butler","chore":"bugs","phase":"initial"}` + "\n"
	body := getHistory(t, nil, map[string]string{eventsFileName: ev})
	for _, want := range []string{"5m0s", "exit 1", "7m0s", "jam-reason-xyz", "breaker_trip", "3 failures", "31m0s", "halt-reason-xyz", "bugs"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
}

func TestHistorySurvivesRestartBoundary(t *testing.T) {
	// A restart appends to the same generation: the first run's halt, then the
	// next run's child_start.
	cur := `{"time":"2026-10-06T22:00:00Z","event":"halt","reason":"first-run-halt"}` + "\n" +
		`{"time":"2026-10-07T08:00:00Z","event":"child_start","kind":"work","slot":0}` + "\n"
	body := getHistory(t, nil, map[string]string{eventsFileName: cur})
	inOrder(t, body, "2026-10-07T08:00:00Z", "first-run-halt")
}

func TestHistoryUnreadableIsNotAbsent(t *testing.T) {
	dir := t.TempDir()
	// A directory opens but cannot be read, standing in for any non-absent failure.
	if err := os.Mkdir(filepath.Join(dir, eventsFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readHistory(filepath.Join(dir, eventsFileName)); err == nil {
		t.Fatal("readHistory err = nil for an unreadable Events file")
	}
	rec := httptest.NewRecorder()
	newServer(filepath.Join(dir, statusFileName)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "history unreadable") {
		t.Errorf("page lacks the unreadable-history message")
	}
	if strings.Contains(body, "no history yet") {
		t.Errorf("unreadable history shown as absent")
	}
}

func TestDropRotatedAlias(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, eventsFileName)
	if err := os.WriteFile(cur, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A rotation between the two opens leaves the older name on the file
	// already opened as current; a hard link is the same state.
	if err := os.Link(cur, cur+rotatedSuffix); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cur)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	alias, err := os.Open(cur + rotatedSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if got := dropRotatedAlias(alias, f); got != nil {
		t.Errorf("older handle kept for the same file")
	}

	other, err := os.Create(filepath.Join(dir, "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if got := dropRotatedAlias(other, f); got != other {
		t.Errorf("distinct older handle dropped")
	}
	if got := dropRotatedAlias(nil, f); got != nil {
		t.Errorf("nil older handle not preserved")
	}
}

func TestHistoryWithNoDaemon(t *testing.T) {
	ev := `{"time":"2026-10-07T09:00:00Z","event":"child_start","kind":"work","slot":0}` + "\n"
	files := map[string]string{eventsFileName: ev}
	t.Run("no status file", func(t *testing.T) {
		body := getHistory(t, nil, files)
		inOrder(t, body, "no Daemon running", "2026-10-07T09:00:00Z")
	})
	t.Run("dead pid", func(t *testing.T) {
		c := fmt.Sprintf(`{"pid":%d,"host":%q,"started":"2026-10-07T10:00:00Z","time":"2026-10-07T11:59:58Z","kinds":["work"],"state":"waiting","slots":[],"checks":[]}`,
			goneCommandPid(t), hostname(t))
		body := getHistory(t, &c, files)
		inOrder(t, body, "no Daemon running", "2026-10-07T09:00:00Z")
	})
}

func TestHistoryExcludesOtherEventsAndSkipsMalformed(t *testing.T) {
	ev := `{"time":"2026-10-07T09:00:00Z","event":"heartbeat"}` + "\n" +
		`not json` + "\n" +
		`{"time":"2026-10-07T09:01:00Z","event":"tip_moved","kinds":["work"]}` + "\n" +
		`{"time":"2026-10-07T09:02:00Z","event":"child_start","kind":"work","slot":0}` + "\n"
	body := getHistory(t, nil, map[string]string{eventsFileName: ev})
	if !strings.Contains(body, "2026-10-07T09:02:00Z") {
		t.Errorf("child_start missing")
	}
	for _, bad := range []string{"heartbeat", "tip_moved", "2026-10-07T09:00:00Z", "2026-10-07T09:01:00Z"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains excluded %q", bad)
		}
	}
}

func TestHistoryEmpty(t *testing.T) {
	body := getHistory(t, nil, nil)
	if !strings.Contains(body, "no history yet") {
		t.Errorf("no empty-history line")
	}
}

func TestHistoryCapKeepsNewest(t *testing.T) {
	var b strings.Builder
	for i := 0; i < historyLimit+5; i++ {
		fmt.Fprintf(&b, `{"time":"t%06d","event":"child_start","slot":0}`+"\n", i)
	}
	body := getHistory(t, nil, map[string]string{eventsFileName: b.String()})
	if !strings.Contains(body, fmt.Sprintf("t%06d", historyLimit+4)) {
		t.Errorf("newest entry missing")
	}
	if strings.Contains(body, "t000000") {
		t.Errorf("oldest entry survived the cap")
	}
}
