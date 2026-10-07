package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type frame struct{ event, data string }

// stream is an open /events connection; frames arrive on a channel so a test
// can wait for one, or assert that none comes.
type stream struct {
	frames chan frame
	dir    string
	alive  *atomic.Bool
}

func (st *stream) status() string { return filepath.Join(st.dir, statusFileName) }
func (st *stream) events() string { return filepath.Join(st.dir, eventsFileName) }

// writeStatus replaces the status file by rename, as the Daemon does.
func (st *stream) writeStatus(t *testing.T, body string) {
	t.Helper()
	tmp := st.status() + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, st.status()); err != nil {
		t.Fatal(err)
	}
}

func (st *stream) next(t *testing.T) frame {
	t.Helper()
	select {
	case f, ok := <-st.frames:
		if !ok {
			t.Fatal("stream closed")
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5s")
	}
	return frame{}
}

func (st *stream) expect(t *testing.T, event string, contains ...string) frame {
	t.Helper()
	f := st.next(t)
	if f.event != event {
		t.Fatalf("frame event = %q, want %q (data %q)", f.event, event, f.data)
	}
	for _, c := range contains {
		if !strings.Contains(f.data, c) {
			t.Fatalf("%s frame lacks %q:\n%s", event, c, f.data)
		}
	}
	return f
}

// quiet fails if any frame arrives within several poll intervals.
func (st *stream) quiet(t *testing.T) {
	t.Helper()
	select {
	case f := <-st.frames:
		t.Fatalf("unexpected %s frame:\n%s", f.event, f.data)
	case <-time.After(150 * time.Millisecond):
	}
}

// openStream serves a temp checkout dir over httptest and connects to /events.
// The status file starts as initial, or absent when empty.
func openStream(t *testing.T, initial, events string) *stream {
	t.Helper()
	dir := t.TempDir()
	alive := &atomic.Bool{}
	alive.Store(true)
	st := &stream{frames: make(chan frame, 64), dir: dir, alive: alive}
	if initial != "" {
		st.writeStatus(t, initial)
	}
	if events != "" {
		appendTo(t, st.events(), events)
	}
	srv := newServer("", st.status())
	srv.now = func() time.Time { return testNow }
	srv.alive = func(int, string) bool { return alive.Load() }
	srv.poll = 10 * time.Millisecond
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	go func() {
		defer close(st.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var f frame
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				f.data = strings.Join(data, "\n")
				st.frames <- f
				f, data = frame{}, nil
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return st
}

const noSlots = `[]`

func TestEventsOpensWithStatusThenHistory(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots),
		evLine("2026-10-07T11:00:00Z", "child_start")+evLine("2026-10-07T11:01:00Z", "backoff"))
	f := st.expect(t, "status", `class="pill state-working"`)
	if strings.Contains(f.data, "<html") {
		t.Errorf("status frame is a whole page")
	}
	h := st.expect(t, "history", "2026-10-07T11:00:00Z", "2026-10-07T11:01:00Z")
	inOrder(t, h.data, "2026-10-07T11:01:00Z", "2026-10-07T11:00:00Z")
	st.quiet(t)
}

func TestEventsEmptyHistoryHasRemovablePlaceholder(t *testing.T) {
	st := openStream(t, "", "")
	st.expect(t, "status", "no Daemon running")
	st.expect(t, "history", `class="muted empty"`)
}

func TestEventsStatusRewriteSendsFrame(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), "")
	st.expect(t, "status")
	st.expect(t, "history")
	st.writeStatus(t, statusJSON(t, "jammed", "", `[{"slot":0,"phase":"running","busy":true,"kind":"work"}]`))
	st.expect(t, "status", `state-jammed`, `data-slot="0" data-phase="running"`)
	st.quiet(t)
}

func TestEventsAppendedEntrySendsOneFrame(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), evLine("2026-10-07T11:00:00Z", "child_start"))
	st.expect(t, "status")
	st.expect(t, "history")
	appendTo(t, st.events(), evLine("2026-10-07T11:02:00Z", "backoff"))
	st.expect(t, "entry", "2026-10-07T11:02:00Z", "ev-backoff")
	st.quiet(t)
}

func TestEventsRotationNeitherDropsNorDuplicates(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), evLine("2026-10-07T11:00:00Z", "child_start"))
	st.expect(t, "status")
	st.expect(t, "history")
	appendTo(t, st.events(), evLine("2026-10-07T11:01:00Z", "backoff"))
	if err := os.Rename(st.events(), st.events()+rotatedSuffix); err != nil {
		t.Fatal(err)
	}
	appendTo(t, st.events(), evLine("2026-10-07T11:02:00Z", "child_start"))
	st.expect(t, "entry", "2026-10-07T11:01:00Z")
	st.expect(t, "entry", "2026-10-07T11:02:00Z")
	st.quiet(t)
}

func TestEventsDaemonStopAndRestart(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), "")
	st.expect(t, "status", "state-working")
	st.expect(t, "history")

	if err := os.Remove(st.status()); err != nil {
		t.Fatal(err)
	}
	st.expect(t, "status", "no Daemon running")
	st.quiet(t)

	st.writeStatus(t, statusJSON(t, "waiting", "", noSlots))
	st.expect(t, "status", "state-waiting")
}

func TestEventsDaemonDiesWithoutRewritingStatus(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), "")
	st.expect(t, "status", "state-working")
	st.expect(t, "history")

	st.alive.Store(false)
	st.expect(t, "status", "no longer running")

	st.writeStatus(t, statusJSON(t, "waiting", "", noSlots))
	st.alive.Store(true)
	st.expect(t, "status", "state-waiting")
}

func TestEventsUnchangedStatusSendsNoRepeat(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), "")
	st.expect(t, "status")
	st.expect(t, "history")
	st.quiet(t)
	// Same content rewritten: a new file, but the same rendering.
	st.writeStatus(t, statusJSON(t, "working", "", noSlots))
	st.quiet(t)
}

func TestEventsSurvivesUnreadableStatus(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), "")
	st.expect(t, "status")
	st.expect(t, "history")
	st.writeStatus(t, "{not json")
	st.expect(t, "status", "could not be read")
	st.writeStatus(t, statusJSON(t, "working", "", noSlots))
	st.expect(t, "status", "state-working")
}

func TestEventsRejectsHeadAndPost(t *testing.T) {
	srv := newServer("", filepath.Join(t.TempDir(), statusFileName))
	for _, m := range []string{http.MethodHead, http.MethodPost} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(m, "/events", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /events = %d, want 405", m, rec.Code)
		}
	}
}

func TestPageCarriesLiveHooks(t *testing.T) {
	content := statusJSON(t, "working", "", `[{"slot":2,"phase":"running","busy":true,"kind":"work"}]`)
	_, body := get(t, &content, "/")
	for _, want := range []string{`id="status"`, `id="history"`, `data-limit="200"`, `data-slot="2" data-phase="running"`, `class="muted empty"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestEventsInPlaceRewriteKeepingSizeAndMtimeSendsFrame(t *testing.T) {
	st := openStream(t, statusJSON(t, "jammed", "", noSlots), "")
	st.expect(t, "status", "state-jammed")
	st.expect(t, "history")
	fi, err := os.Stat(st.status())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.status(), []byte(statusJSON(t, "halted", "", noSlots)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(st.status(), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	st.expect(t, "status", "state-halted")
}
