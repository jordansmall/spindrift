package main

import (
	"bufio"
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
	resp, err := http.Get(ts.URL + "/log?path=" + url.QueryEscape(path))
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
	if got := collect(t, st, "pass line"); got != "pass line\n" {
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
