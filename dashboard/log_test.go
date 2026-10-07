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

func openLog(t *testing.T, ts *httptest.Server, path string) (*http.Response, chan frame) {
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
	return resp, frames
}

// collect joins log frames until the text so far contains want.
func collect(t *testing.T, frames chan frame, want string) string {
	t.Helper()
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		select {
		case f, ok := <-frames:
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
	resp, frames := openLog(t, ts, testLogPath)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := collect(t, frames, "first"); got != "first\n" {
		t.Fatalf("existing bytes = %q", got)
	}
	appendTo(t, p, "second\n")
	if got := collect(t, frames, "second"); got != "second\n" {
		t.Fatalf("appended bytes = %q", got)
	}
}

func TestLogNotNamedOrNotLocalIs404(t *testing.T) {
	ts, checkout := logServer(t)
	writeLog(t, checkout, testLogPath, "x")
	writeLog(t, checkout, ".spindrift/logs/daemon/other.log", "secret")
	// Named by an event but escaping the checkout.
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName), childStartLine("../../etc/passwd")+childStartLine("/etc/passwd"))
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
	_, frames := openLog(t, ts, other)
	collect(t, frames, "old")
}

func TestLogMissingFileSendsPruned(t *testing.T) {
	ts, _ := logServer(t)
	resp, frames := openLog(t, ts, testLogPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	f := <-frames
	if f.event != "pruned" || f.data != testLogPath {
		t.Fatalf("frame = %+v, want pruned %q", f, testLogPath)
	}
	if _, ok := <-frames; ok {
		t.Fatal("stream stayed open after pruned")
	}
}

func TestLogNeverSplitsARuneAcrossAppends(t *testing.T) {
	ts, checkout := logServer(t)
	p := writeLog(t, checkout, testLogPath, "a")
	_, frames := openLog(t, ts, testLogPath)
	collect(t, frames, "a")
	euro := "€" // e2 82 ac
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(euro[:2])
	select {
	case fr := <-frames:
		t.Fatalf("partial rune sent as %s frame %q", fr.event, fr.data)
	case <-time.After(150 * time.Millisecond):
	}
	f.WriteString(euro[2:])
	if got := collect(t, frames, euro); got != euro {
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
	_, frames := openLog(t, ts, testLogPath)
	if got := collect(t, frames, "b\n"); got != head+"\nb\n" {
		t.Fatalf("line break at the frame boundary = %q, want a single newline", got[len(head)-1:])
	}
}

func boxLine(path string) string {
	return `{"time":"2026-10-07T12:03:05Z","event":"box","slot":0,"phase":"fix-pass-1","issue":"42","pass_log":"` + path + `"}` + "\n"
}

func TestLogServesPassLogNamedOnlyByBoxEvent(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42-fix-1.log"
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName), boxLine(pass))
	writeLog(t, checkout, pass, "pass line\n")
	_, frames := openLog(t, ts, pass)
	if got := collect(t, frames, "pass line"); got != "pass line\n" {
		t.Fatalf("bytes = %q", got)
	}
}

func TestLogMissingPassLogSendsPruned(t *testing.T) {
	ts, checkout := logServer(t)
	pass := ".spindrift/logs/issue-42.log"
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName), boxLine(pass))
	_, frames := openLog(t, ts, pass)
	if f := <-frames; f.event != "pruned" || f.data != pass {
		t.Fatalf("frame = %+v, want pruned %q", f, pass)
	}
}

func TestLogPassLogNotNamedIs404(t *testing.T) {
	ts, checkout := logServer(t)
	writeLog(t, checkout, ".spindrift/logs/issue-42.log", "x")
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName), boxLine(".spindrift/logs/issue-42-fix-1.log"))
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
	appendTo(t, filepath.Join(checkout, ".git", eventsFileName), boxLine(pass))
	p := writeLog(t, checkout, pass, "dead attempt\n")
	_, frames := openLog(t, ts, pass)
	collect(t, frames, "dead attempt")

	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	writeLog(t, checkout, pass, "new attempt\n")
	for open := true; open; {
		select {
		case _, open = <-frames:
		case <-time.After(5 * time.Second):
			t.Fatal("stream stayed open after the Pass log was rotated aside")
		}
	}

	_, frames = openLog(t, ts, pass)
	if got := collect(t, frames, "new attempt"); got != "new attempt\n" {
		t.Fatalf("reconnect bytes = %q", got)
	}
}
