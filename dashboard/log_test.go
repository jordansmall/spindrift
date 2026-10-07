package main

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testLogPath = ".spindrift/logs/daemon/20261007T120304.123Z-slot0-dispatch.log"

func childStartLine(path string) string {
	return `{"time":"2026-10-07T12:03:04Z","event":"child_start","kind":"work","slot":0,"child_log":"` + path + `"}` + "\n"
}

// logServer serves a temp checkout whose Events file names testLogPath.
func logServer(t *testing.T) (ts *httptest.Server, checkout string) {
	t.Helper()
	checkout = t.TempDir()
	gitDir := filepath.Join(checkout, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	appendTo(t, filepath.Join(gitDir, eventsFileName), childStartLine(testLogPath))
	srv := newServer(checkout, filepath.Join(gitDir, statusFileName))
	srv.poll = 10 * time.Millisecond
	ts = httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, checkout
}

func writeLog(t *testing.T, checkout, rel, content string) string {
	t.Helper()
	p := filepath.Join(checkout, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	appendTo(t, p, content)
	return p
}

func openLog(t *testing.T, ts *httptest.Server, path string) (*http.Response, *stream) {
	t.Helper()
	return openLogQuery(t, ts, "path="+url.QueryEscape(path))
}

// openLogQuery opens /log with a raw query string.
func openLogQuery(t *testing.T, ts *httptest.Server, query string) (*http.Response, *stream) {
	t.Helper()
	resp, err := http.Get(ts.URL + "/log?" + query)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	frames := make(chan frame, 64)
	go func() {
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 2*logFrameMax) // one data line can be a whole frame
		var f frame
		var data []string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				f.data = strings.Join(data, "\n")
				frames <- f
				f, data = frame{}, nil
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return resp, &stream{frames: frames}
}

// collect joins log frames until the text so far contains want.
func collect(t *testing.T, st *stream, want string) string {
	t.Helper()
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		select {
		case f, ok := <-st.frames:
			if !ok {
				t.Fatalf("stream closed; got %q, want %q", got.String(), want)
			}
			if f.event != "log" {
				t.Fatalf("frame event = %q, want log", f.event)
			}
			got.WriteString(f.data)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out; got %q, want %q", got.String(), want)
		}
	}
	return got.String()
}

func TestLogSendsExistingBytesThenAppends(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "first\n")
	resp, st := openLog(t, ts, testLogPath)
	// collect fails on any non-log frame: a log present on first open gets no opened.
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := collect(t, st, "first"); got != "first\n" {
		t.Fatalf("existing bytes = %q", got)
	}
	appendTo(t, p, "second\n")
	if got := collect(t, st, "second"); got != "second\n" {
		t.Fatalf("appended bytes = %q", got)
	}
}

func TestLogNotNamedOrNotLocalIs404(t *testing.T) {
	ts, checkout := logServer(t)
	writeLog(t, checkout, testLogPath, "x")
	writeLog(t, checkout, ".spindrift/logs/daemon/other.log", "secret")
	// Named by an event but escaping the checkout.
	appendTo(t, eventsOf(checkout), childStartLine("../../etc/passwd")+childStartLine("/etc/passwd"))
	for _, path := range []string{"", ".spindrift/logs/daemon/other.log", "../x", "../../etc/passwd", "/etc/passwd", testLogPath + "/../other.log"} {
		resp, err := http.Get(ts.URL + "/log?path=" + url.QueryEscape(path))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestLogNamedInRotatedGeneration(t *testing.T) {
	ts, checkout := logServer(t)
	other := ".spindrift/logs/daemon/old.log"
	writeLog(t, checkout, other, "old\n")
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName+rotatedSuffix), childStartLine(other))
	_, st := openLog(t, ts, other)
	collect(t, st, "old")
}

func childFinishLine() string {
	return `{"time":"2026-10-07T12:03:09Z","event":"child_finish","kind":"work","slot":0,"exit":0}` + "\n"
}

func eventsOf(checkout string) string {
	return filepath.Join(checkout, ".git", eventsFileName)
}

func wantClosed(t *testing.T, st *stream) {
	t.Helper()
	select {
	case f, ok := <-st.frames:
		if ok {
			t.Fatalf("stream stayed open, got frame %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream stayed open")
	}
}

func TestLogMissingNamedWithoutSlotSendsPruned(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), `{"time":"2026-10-07T12:03:05Z","event":"box","pass_log":"`+pass+`"}`+"\n")
	_, st := openLog(t, ts, pass)
	st.expect(t, "pruned", pass)
	wantClosed(t, st)
}

func TestLogMissingAfterFinishSendsPruned(t *testing.T) {
	ts, checkout := logServer(t)
	appendTo(t, eventsOf(checkout), childFinishLine())
	resp, st := openLog(t, ts, testLogPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func TestLogMissingWhileRunningWaitsThenStreams(t *testing.T) {
	ts, checkout := logServer(t)
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "waiting", testLogPath)
	st.quiet(t)
	writeLog(t, checkout, testLogPath, "hello\n")
	st.expect(t, "opened", testLogPath)
	if got := collect(t, st, "hello"); got != "hello\n" {
		t.Fatalf("bytes = %q", got)
	}
}

func TestLogMissingPassLogWhileRunningWaitsThenStreams(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	_, st := openLog(t, ts, pass)
	st.expect(t, "waiting", pass)
	writeLog(t, checkout, pass, "pass line\n")
	st.expect(t, "opened", pass)
	if got := collect(t, st, "pass line"); got != "pass line\n" {
		t.Fatalf("bytes = %q", got)
	}
}

func TestLogEmptyLogCreatedAfterWaitingSendsOpened(t *testing.T) {
	ts, checkout := logServer(t)
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "waiting", testLogPath)
	p := writeLog(t, checkout, testLogPath, "")
	st.expect(t, "opened", testLogPath)
	st.quiet(t)
	appendTo(t, p, "late\n")
	if got := collect(t, st, "late"); got != "late\n" {
		t.Fatalf("bytes = %q", got)
	}
}

func TestLogWaitingEndsPrunedWhenChildFinishes(t *testing.T) {
	ts, checkout := logServer(t)
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "waiting", testLogPath)
	appendTo(t, eventsOf(checkout), childFinishLine())
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func TestLogLaterChildStartOnSlotCountsAsFinished(t *testing.T) {
	ts, checkout := logServer(t)
	appendTo(t, eventsOf(checkout), childStartLine(".spindrift/logs/daemon/next.log"))
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func TestLogRenamedChildLogIsRunningAgain(t *testing.T) {
	ts, checkout := logServer(t)
	appendTo(t, eventsOf(checkout), childFinishLine()+childStartLine(testLogPath))
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "waiting", testLogPath)
}

func TestLogDeletedMidFollowAfterFinishSendsPruned(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "last words\n")
	_, st := openLog(t, ts, testLogPath)
	collect(t, st, "last words")
	appendTo(t, eventsOf(checkout), childFinishLine())
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func TestLogDeletedMidFollowWhileRunningKeepsStreaming(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "line\n")
	_, st := openLog(t, ts, testLogPath)
	collect(t, st, "line")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	st.quiet(t)
}

func TestLogNeverSplitsARuneAcrossAppends(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "a")
	_, st := openLog(t, ts, testLogPath)
	collect(t, st, "a")
	euro := "€" // e2 82 ac
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(euro[:2])
	st.quiet(t)
	f.WriteString(euro[2:])
	if got := collect(t, st, euro); got != euro {
		t.Fatalf("got %q, want %q", got, euro)
	}
}

func TestLogRejectsPost(t *testing.T) {
	ts, _ := logServer(t)
	resp, err := http.Post(ts.URL+"/log?path="+url.QueryEscape(testLogPath), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestLogKeepsCRLFTogetherAcrossFrames(t *testing.T) {
	ts, checkout := logServer(t)
	// The CR is the last byte of the first read, the LF the first of the next.
	head := strings.Repeat("a", logFrameMax-1)
	writeLog(t, checkout, testLogPath, head+"\r\nb\n")
	_, st := openLog(t, ts, testLogPath)
	if got := collect(t, st, "b\n"); got != head+"\nb\n" {
		t.Fatalf("line break at the frame boundary = %q, want a single newline", got[len(head)-1:])
	}
}

func boxLine(path string) string {
	return `{"time":"2026-10-07T12:03:05Z","event":"box","slot":0,"phase":"fix-pass-1","issue":"42","pass_log":"` + path + `"}` + "\n"
}

func TestLogServesPassLogNamedOnlyByBoxEvent(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42-fix-1.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	writeLog(t, checkout, pass, "pass line\n")
	_, st := openLog(t, ts, pass)
	if got := collect(t, st, "pass line"); got != "pass line\n" {
		t.Fatalf("bytes = %q", got)
	}
}

func TestLogMissingPassLogAfterFinishSendsPruned(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
	_, st := openLog(t, ts, pass)
	st.expect(t, "pruned", pass)
}

func TestLogPassLogNotNamedIs404(t *testing.T) {
	ts, checkout := logServer(t)
	writeLog(t, checkout, ".spindrift/logs/issue-42.log", "x")
	appendTo(t, eventsOf(checkout), boxLine(".spindrift/logs/issue-42-fix-1.log"))
	resp, err := http.Get(ts.URL + "/log?path=" + url.QueryEscape(".spindrift/logs/issue-42.log"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLogEndsStreamWhenRetryRotatesPassLogAside(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	p := writeLog(t, checkout, pass, "dead attempt\n")
	_, st := openLog(t, ts, pass)
	collect(t, st, "dead attempt")

	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	writeLog(t, checkout, pass, "new attempt\n")
	for open := true; open; {
		select {
		case _, open = <-st.frames:
		case <-time.After(5 * time.Second):
			t.Fatal("stream stayed open after the Pass log was rotated aside")
		}
	}

	_, st = openLog(t, ts, pass)
	if got := collect(t, st, "new attempt"); got != "new attempt\n" {
		t.Fatalf("reconnect bytes = %q", got)
	}
}

func TestLogWaitingEndsPrunedWhenNamingEventAgesOut(t *testing.T) {
	ts, checkout := logServer(t)
	_, st := openLog(t, ts, testLogPath)
	st.expect(t, "waiting", testLogPath)
	if err := os.WriteFile(eventsOf(checkout), []byte(childStartLine(".spindrift/logs/daemon/other.log")), 0o644); err != nil {
		t.Fatal(err)
	}
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func TestLogFollowEndsPrunedWhenNamingEventAgesOut(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "line\n")
	_, st := openLog(t, ts, testLogPath)
	collect(t, st, "line")
	if err := os.WriteFile(eventsOf(checkout), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	st.expect(t, "pruned", testLogPath)
	wantClosed(t, st)
}

func slotLine(event string, slot int, at, extra string) string {
	return fmt.Sprintf(`{"time":%q,"event":%q,"kind":"work","slot":%d%s}`+"\n", at, event, slot, extra)
}

// ownerQuery is the query a Pass log tab sends for the Dispatch that began on
// slot at the given child_start time.
func ownerQuery(path string, slot int, at string, n int) string {
	return fmt.Sprintf("path=%s&slot=%d&at=%s&n=%d", url.QueryEscape(path), slot, url.QueryEscape(at), n)
}

// drainFrames reads st to its close, returning the log text and every
// non-log frame's event name.
func drainFrames(t *testing.T, st *stream) (text string, events []string) {
	t.Helper()
	var got strings.Builder
	for {
		select {
		case f, ok := <-st.frames:
			if !ok {
				return got.String(), events
			}
			if f.event == "log" {
				got.WriteString(f.data)
			} else {
				events = append(events, f.event)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("stream stayed open; log so far %q", got.String())
		}
	}
}

const (
	olderAt = "2026-10-07T12:03:04Z" // logServer's own child_start, slot 0
	newerAt = "2026-10-07T13:00:00Z"
)

// TestLogSupersededByLaterDispatchOnSamePath covers a Pass log path reused by a
// later Dispatch of the same issue or Chore: the older Dispatch's tab must never
// show the newer run.
func TestLogSupersededByLaterDispatchOnSamePath(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		extra       string // the box event's subject
		newerSlot   int
		finishOlder bool
	}{
		{"same slot", ".spindrift/logs/issue-42.log", `,"issue":"42"`, 0, true},
		{"different slot", ".spindrift/logs/issue-42.log", `,"issue":"42"`, 1, false},
		{"butler chore", ".spindrift/logs/issue-butler-docs.log", `,"chore":"docs"`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, checkout := logServer(t)
			box := func(slot int, at string) string {
				return slotLine("box", slot, at, `,"phase":"initial","pass_log":"`+tc.path+`"`+tc.extra)
			}
			ev := box(0, "2026-10-07T12:03:05Z")
			if tc.finishOlder {
				ev += slotLine("child_finish", 0, "2026-10-07T12:30:00Z", `,"exit":0`)
			}
			ev += slotLine("child_start", tc.newerSlot, newerAt, `,"child_log":"logs/newer.log"`) +
				box(tc.newerSlot, "2026-10-07T13:00:05Z")
			appendTo(t, eventsOf(checkout), ev)
			writeLog(t, checkout, tc.path, "newer run\n")

			_, st := openLogQuery(t, ts, ownerQuery(tc.path, 0, olderAt, 0))
			f := st.expect(t, "superseded", tc.path)
			if f.data != tc.path {
				t.Fatalf("superseded data = %q, want %q", f.data, tc.path)
			}
			if text, events := drainFrames(t, st); text != "" || len(events) != 0 {
				t.Fatalf("after superseded: log %q, events %v; want the stream closed", text, events)
			}

			_, st = openLogQuery(t, ts, ownerQuery(tc.path, tc.newerSlot, newerAt, 0))
			if got := collect(t, st, "newer run"); got != "newer run\n" {
				t.Fatalf("newer Dispatch bytes = %q", got)
			}
		})
	}
}

func TestLogSupersededWhileWaitingForFile(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	st.expect(t, "waiting", pass)
	appendTo(t, eventsOf(checkout), slotLine("child_start", 1, newerAt, "")+
		slotLine("box", 1, "2026-10-07T13:00:05Z", `,"issue":"42","pass_log":"`+pass+`"`))
	st.expect(t, "superseded", pass)
	wantClosed(t, st)
}

func TestLogFinishedDispatchSupersededByReplacementBeforeNewBoxEvent(t *testing.T) {
	ts, checkout := logServer(t)
	for _, pass := range []string{".spindrift/logs/issue-42.log", ".spindrift/logs/issue-butler-docs.log"} {
		appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
		p := writeLog(t, checkout, pass, "old run\n")
		stamp(t, p, "2026-10-07T12:03:08Z")
		_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
		collect(t, st, "old run")

		// The later Dispatch quarantines the file and starts a fresh one before its
		// box event reaches the Events file.
		if err := os.Rename(p, p+".prior-run.1"); err != nil {
			t.Fatal(err)
		}
		writeLog(t, checkout, pass, "newer run\n")
		text, events := drainFrames(t, st)
		if strings.Contains(text, "newer run") {
			t.Fatalf("%s: the older Dispatch streamed the newer run: %q", pass, text)
		}
		if len(events) != 1 || events[0] != "superseded" {
			t.Fatalf("%s: frames after the old bytes = %v, want [superseded]", pass, events)
		}
	}
}

// The newer Dispatch's box event lands before it quarantines the prior log, so
// a still-running older tab on another slot is superseded, not merely pruned.
func TestLogFollowingTabSupersededWhenPathRenamedAfterNewerBox(t *testing.T) {
	for _, pass := range []string{".spindrift/logs/issue-42.log", ".spindrift/logs/issue-butler-docs.log"} {
		subject := `,"issue":"42"`
		if strings.Contains(pass, "butler") {
			subject = `,"chore":"docs"`
		}
		for _, recreate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s recreate=%v", pass, recreate), func(t *testing.T) {
				ts, checkout := logServer(t)
				appendTo(t, eventsOf(checkout), boxLine(pass))
				p := writeLog(t, checkout, pass, "old run\n")
				_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
				collect(t, st, "old run")

				appendTo(t, eventsOf(checkout), slotLine("child_start", 1, newerAt, "")+
					slotLine("box", 1, "2026-10-07T13:00:05Z", subject+`,"pass_log":"`+pass+`"`))
				if err := os.Rename(p, p+".prior-run.1"); err != nil {
					t.Fatal(err)
				}
				if recreate {
					writeLog(t, checkout, pass, "newer run\n")
				}
				text, events := drainFrames(t, st)
				if strings.Contains(text, "newer run") {
					t.Fatalf("the older Dispatch streamed the newer run: %q", text)
				}
				if len(events) != 1 || events[0] != "superseded" {
					t.Fatalf("frames after the old bytes = %v, want [superseded]", events)
				}
			})
		}
	}
}

func TestLogRunningDispatchRetryStillEndsStreamForReconnect(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	p := writeLog(t, checkout, pass, "dead attempt\n")
	q := ownerQuery(pass, 0, olderAt, 0)
	_, st := openLogQuery(t, ts, q)
	collect(t, st, "dead attempt")

	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	writeLog(t, checkout, pass, "new attempt\n")
	if text, events := drainFrames(t, st); text != "" || len(events) != 0 {
		t.Fatalf("retry ended the stream with log %q, events %v; want a bare close", text, events)
	}

	_, st = openLogQuery(t, ts, q)
	if got := collect(t, st, "new attempt"); got != "new attempt\n" {
		t.Fatalf("reconnect bytes = %q", got)
	}
}

func TestLogOwnerParamsJunkOrUnnamedIs404(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	writeLog(t, checkout, pass, "x")
	esc := url.QueryEscape(pass)
	for _, q := range []string{
		"slot=abc&at=" + olderAt,
		"slot=-1&at=" + olderAt,
		"slot=0",
		"slot=0&at=yesterday",
		"slot=0&at=" + olderAt + "&n=x",
		"slot=0&at=" + olderAt + "&n=-1",
		"at=" + olderAt,
		"n=0",
		// Well-formed, but no such Dispatch named the path.
		"slot=1&at=" + olderAt,
		"slot=0&at=" + newerAt,
		"slot=0&at=" + olderAt + "&n=1",
	} {
		resp, err := http.Get(ts.URL + "/log?path=" + esc + "&" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%q: status = %d, want 404", q, resp.StatusCode)
		}
	}
}

// stamp sets p's mtime to the RFC3339 time at, so a fixture's age sits
// deterministically against the fixed times of its events.
func stamp(t *testing.T, p, at string) {
	t.Helper()
	mt, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// A later Dispatch's launcher quarantines every prior Pass log of the issue,
// but its box event names only the initial path; the older tab for a fix or
// conflict-resolve log must still hear superseded.
func TestLogOlderFixOrConflictLogQuarantinedByLaterDispatchIsSuperseded(t *testing.T) {
	cases := []struct{ name, path, phase string }{
		{"fix pass", ".spindrift/logs/issue-42-fix-1.log", "fix-pass-1"},
		{"conflict resolve", ".spindrift/logs/issue-42-conflict-resolve.log", "conflict-resolve"},
	}
	for _, tc := range cases {
		for _, newerSlot := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s newer slot %d", tc.name, newerSlot), func(t *testing.T) {
				ts, checkout := logServer(t)
				initial := ".spindrift/logs/issue-42.log"
				appendTo(t, eventsOf(checkout),
					slotLine("box", 0, "2026-10-07T12:03:05Z", `,"phase":"`+tc.phase+`","issue":"42","pass_log":"`+tc.path+`"`)+
						slotLine("child_finish", 0, "2026-10-07T12:30:00Z", `,"exit":0`)+
						slotLine("child_start", newerSlot, newerAt, `,"child_log":"logs/newer.log"`)+
						slotLine("box", newerSlot, "2026-10-07T13:00:05Z", `,"phase":"initial","issue":"42","pass_log":"`+initial+`"`))
				p := writeLog(t, checkout, tc.path, "old run\n")
				stamp(t, p, "2026-10-07T12:20:00Z")
				if err := os.Rename(p, p+".prior-run.1"); err != nil {
					t.Fatal(err)
				}

				_, st := openLogQuery(t, ts, ownerQuery(tc.path, 0, olderAt, 0))
				st.expect(t, "superseded", tc.path)
				wantClosed(t, st)
			})
		}
	}
}

func TestLogFinishedOwnerQuarantinedBeforeNewBoxEventIsSuperseded(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
	p := writeLog(t, checkout, pass, "old run\n")
	stamp(t, p, "2026-10-07T12:03:07Z")
	if err := os.Rename(p, p+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	st.expect(t, "superseded", pass)
	wantClosed(t, st)
}

func TestLogFollowingTabSupersededWhenFinishedOwnerQuarantined(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	p := writeLog(t, checkout, pass, "old run\n")
	stamp(t, p, "2026-10-07T12:03:07Z")
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	collect(t, st, "old run")

	appendTo(t, eventsOf(checkout), childFinishLine())
	if err := os.Rename(p, p+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	wantSupersededAfterOldBytes(t, st)
}

// A .prior-run sibling is only this owner's own copy when written at or after
// the owner named the path; earlier Dispatches' leftovers, and names that are
// not a quarantine suffix, leave a missing log pruned.
func TestLogMissingFileWithoutOwnQuarantineSiblingIsPruned(t *testing.T) {
	cases := []struct{ name, sibling, stampAt string }{
		{"older dispatch's copy", "issue-42.log.prior-run.1", "2026-10-07T12:00:00Z"},
		{"non-digit suffix", "issue-42.log.prior-run.x", "2026-10-07T12:20:00Z"},
		{"no suffix number", "issue-42.log.prior-run.", "2026-10-07T12:20:00Z"},
		{"other issue", "issue-43.log.prior-run.1", "2026-10-07T12:20:00Z"},
		{"no sibling", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, checkout := logServer(t)
			pass := ".spindrift/logs/issue-42.log"
			appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
			if tc.sibling != "" {
				p := writeLog(t, checkout, ".spindrift/logs/"+tc.sibling, "other run\n")
				stamp(t, p, tc.stampAt)
			}
			_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
			st.expect(t, "pruned", pass)
			wantClosed(t, st)
		})
	}
}

// A finished Dispatch never writes its Pass log again, so a file modified well
// past the finish is a later run's that reached the path before its box event.
func TestLogFinishedOwnerFileModifiedAfterFinishIsSupersededUnread(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
	p := writeLog(t, checkout, pass, "newer run\n")
	stamp(t, p, "2026-10-07T13:00:10Z")
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	st.expect(t, "superseded", pass)
	wantClosed(t, st)
}

func TestLogFinishedOwnerTabNeverReadsPastItsOpenSize(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
	p := writeLog(t, checkout, pass, "old run\n")
	stamp(t, p, "2026-10-07T12:03:09Z") // within the finish slack
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	collect(t, st, "old run")

	appendTo(t, p, "newer\n")
	wantSupersededAfterOldBytes(t, st)
}

func TestLogRunningOwnerTabStreamsAppends(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass))
	p := writeLog(t, checkout, pass, "first\n")
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	collect(t, st, "first")
	appendTo(t, p, "second\n")
	if got := collect(t, st, "second"); got != "second\n" {
		t.Fatalf("appended bytes = %q", got)
	}
}

func TestLogNilOwnerOnFinishedChildStreamsFreshFile(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+childFinishLine())
	p := writeLog(t, checkout, pass, "fresh\n")
	stamp(t, p, "2026-10-07T13:00:10Z")
	_, st := openLog(t, ts, pass)
	collect(t, st, "fresh")
	appendTo(t, p, "more\n")
	collect(t, st, "more")
}

// wantSupersededAfterOldBytes expects the stream to end with exactly one
// superseded frame and no bytes beyond what the caller already collected.
func wantSupersededAfterOldBytes(t *testing.T, st *stream) {
	t.Helper()
	text, events := drainFrames(t, st)
	if text != "" || len(events) != 1 || events[0] != "superseded" {
		t.Fatalf("after the old bytes: log %q, frames %v; want [superseded]", text, events)
	}
}

// A fix-pass or conflict-resolve tab follows its log while the owner runs; a
// later Dispatch's launcher then quarantines it, though its box event names
// only the initial log.
func TestLogFollowingFixOrConflictTabSupersededWhenQuarantinedByLaterDispatch(t *testing.T) {
	cases := []struct{ name, path, phase string }{
		{"fix pass", ".spindrift/logs/issue-42-fix-1.log", "fix-pass-1"},
		{"conflict resolve", ".spindrift/logs/issue-42-conflict-resolve.log", "conflict-resolve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, checkout := logServer(t)
			appendTo(t, eventsOf(checkout), slotLine("box", 0, "2026-10-07T12:03:05Z",
				`,"phase":"`+tc.phase+`","issue":"42","pass_log":"`+tc.path+`"`))
			p := writeLog(t, checkout, tc.path, "old run\n")
			stamp(t, p, "2026-10-07T12:03:07Z")
			_, st := openLogQuery(t, ts, ownerQuery(tc.path, 0, olderAt, 0))
			collect(t, st, "old run")

			appendTo(t, eventsOf(checkout), childFinishLine()+
				slotLine("child_start", 1, newerAt, `,"child_log":"logs/newer.log"`)+
				slotLine("box", 1, "2026-10-07T13:00:05Z",
					`,"phase":"initial","issue":"42","pass_log":".spindrift/logs/issue-42.log"`))
			if err := os.Rename(p, p+".prior-run.1"); err != nil {
				t.Fatal(err)
			}
			wantSupersededAfterOldBytes(t, st)
		})
	}
}

// With no child_finish (a Daemon that died), the slot's next child_start ends
// the Dispatch; a file modified past that start is a later run's.
func TestLogFinishInferredFromLaterChildStartSupersedesModifiedFile(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, eventsOf(checkout), boxLine(pass)+
		slotLine("child_start", 0, newerAt, `,"child_log":"logs/newer.log"`))
	p := writeLog(t, checkout, pass, "newer run\n")
	stamp(t, p, "2026-10-07T13:00:05Z")
	_, st := openLogQuery(t, ts, ownerQuery(pass, 0, olderAt, 0))
	st.expect(t, "superseded", pass)
	wantClosed(t, st)
}
