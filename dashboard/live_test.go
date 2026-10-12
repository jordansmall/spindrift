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

func (st *stream) ended(t *testing.T) {
	t.Helper()
	select {
	case f, ok := <-st.frames:
		if ok {
			t.Fatalf("stream still open, got %s frame:\n%s", f.event, f.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end within 5s")
	}
}

// openStream serves a temp checkout dir over httptest and connects to /events.
// The status file starts as initial, or absent when empty.
func openStream(t *testing.T, initial, events string) *stream {
	t.Helper()
	return openStreamAt(t, "/events", initial, events)
}

// openStreamAt is openStream for any SSE route, path including its query.
func openStreamAt(t *testing.T, path, initial, events string) *stream {
	t.Helper()
	return openStreamOver(t, path, initial, "", events)
}

// openStreamOver is openStreamAt with an older Events generation on disk too.
func openStreamOver(t *testing.T, path, initial, older, events string) *stream {
	t.Helper()
	dir := t.TempDir()
	alive := &atomic.Bool{}
	alive.Store(true)
	st := &stream{frames: make(chan frame, 64), dir: dir, alive: alive}
	if initial != "" {
		st.writeStatus(t, initial)
	}
	if older != "" {
		appendTo(t, st.events()+rotatedSuffix, older)
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

	resp, err := http.Get(ts.URL + path)
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

func TestEventsSelfUpdatePhaseChangeSendsFrame(t *testing.T) {
	st := openStream(t, withSelfUpdate(statusJSON(t, "working", "", noSlots), "building", selfRev), "")
	st.expect(t, "status", "rebuilding")
	st.expect(t, "history")
	st.writeStatus(t, withSelfUpdate(statusJSON(t, "working", "", noSlots), "waiting", selfRev))
	st.expect(t, "status", "waiting to restart", "<code>0123456789ab</code>")
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

func TestEventsLiveRowCarriesOwningDispatch(t *testing.T) {
	st := openStream(t, statusJSON(t, "working", "", noSlots), evLine("2026-10-07T11:00:00Z", "child_start"))
	st.expect(t, "status")
	st.expect(t, "history")
	appendTo(t, st.events(), evLine("2026-10-07T11:02:00Z", "backoff"))
	f := st.expect(t, "entry", "ev-backoff")
	if want := `href="/dispatch?slot=0&at=2026-10-07T11%3a00%3a00Z"`; !strings.Contains(f.data, want) {
		t.Errorf("entry does not link to the snapshot's child_start:\n%s", f.data)
	}
}

// TestEventsDaemonLiveAcrossInPlaceRestart pins that a restart which execs the
// new image under the same pid and rewrites the status file never reads as a
// stopped Daemon: the pid stays alive and the inherited Slot stays busy.
func TestEventsDaemonLiveAcrossInPlaceRestart(t *testing.T) {
	busy := `[{"slot":0,"phase":"running","busy":true,"kind":"work","issues":["42"],` +
		`"child_start":"2026-10-07T09:00:00Z"}]`
	inherited := `[{"slot":0,"phase":"running","busy":true,"kind":"work","issues":["42"],` +
		`"revision":"fedcba9876543210","child_start":"2026-10-07T09:00:00Z"}]`
	// The status file a daemon holds between deciding to restart and the exec.
	midHandoff := strings.Replace(statusJSON(t, "waiting", "", busy), `"checks":[]`,
		`"checks":[],"self_update":{"phase":"restarting","revision":"0123456789abcdef"}`, 1)
	notLive := []string{"no Daemon running", "no longer running"}

	for _, content := range []string{
		statusJSON(t, "working", "", busy),
		midHandoff,
		statusJSON(t, "checking", "", inherited),
	} {
		code, body := get(t, &content, "/")
		if code != 200 {
			t.Fatalf("code = %d", code)
		}
		for _, bad := range notLive {
			if strings.Contains(body, bad) {
				t.Errorf("live Daemon rendered as %q", bad)
			}
		}
		if !strings.Contains(body, `data-slot="0" data-phase="running"`) {
			t.Errorf("busy Slot card missing:\n%s", body)
		}
	}

	st := openStream(t, statusJSON(t, "working", "", busy), restartPre)
	st.expect(t, "status", "state-working")
	st.expect(t, "history")

	st.writeStatus(t, midHandoff)
	f := st.expect(t, "status", "state-waiting")
	for _, bad := range notLive {
		if strings.Contains(f.data, bad) {
			t.Fatalf("mid-handoff status frame reports the Daemon gone (%q):\n%s", bad, f.data)
		}
	}

	appendTo(t, st.events(), restartHandoff)
	st.writeStatus(t, statusJSON(t, "checking", "", inherited))
	var sawStatus, sawLink bool
	entries := 0
	for !sawStatus || entries < 3 {
		f := st.next(t)
		switch f.event {
		case "status":
			sawStatus = true
			for _, bad := range notLive {
				if strings.Contains(f.data, bad) {
					t.Fatalf("status frame reports the Daemon gone (%q):\n%s", bad, f.data)
				}
			}
			if !strings.Contains(f.data, "state-checking") || !strings.Contains(f.data, `data-slot="0" data-phase="running"`) {
				t.Fatalf("status frame lacks the rewritten live state:\n%s", f.data)
			}
		case "entry":
			entries++
			if strings.Contains(f.data, "ev-child_inherited") &&
				strings.Contains(f.data, `href="/dispatch?slot=0&at=2026-10-07T09%3a00%3a00Z"`) {
				sawLink = true
			}
		default:
			t.Fatalf("unexpected %s frame:\n%s", f.event, f.data)
		}
	}
	if !sawLink {
		t.Error("no child_inherited entry links to slot 0's pre-restart Dispatch")
	}
	st.quiet(t)
}
