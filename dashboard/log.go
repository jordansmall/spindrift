package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// logFrameMax caps the bytes one log frame carries.
const logFrameMax = 64 << 10

// namesChildLog reports whether any child_start in either Events generation
// carries path as its child_log. Only a path the Daemon announced is served, so
// the query string cannot be used to read arbitrary files under the checkout.
func (s *server) namesChildLog(path string) bool {
	for _, name := range []string{s.eventsPath + rotatedSuffix, s.eventsPath} {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		found := scanChildLog(f, path)
		f.Close()
		if found {
			return true
		}
	}
	return false
}

func scanChildLog(r io.Reader, path string) bool {
	br := bufio.NewReader(r)
	for {
		// A line is read whole however long, as scanTimeline does.
		line, err := br.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"child_log"`)) {
			var ev Event
			if json.Unmarshal(line, &ev) == nil && ev.Event == "child_start" && ev.ChildLog == path {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// childLog streams one Child log as Server-Sent Events: its existing bytes and
// then each append as log frames. A named log since deleted (operators prune by
// hand) gets a single pruned frame instead, an answer rather than an error. An
// unreadable log ends the stream after logging; the client's reconnect retries.
func (s *server) childLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if !filepath.IsLocal(path) || !s.namesChildLog(path) {
		http.NotFound(w, r)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	send := func(event, data string) bool {
		return writeFrame(w, event, data) == nil && rc.Flush() == nil
	}

	f, err := os.Open(filepath.Join(s.checkout, path))
	if errors.Is(err, fs.ErrNotExist) {
		send("pruned", path)
		return
	}
	if err != nil {
		log.Printf("dashboard: child log %s: %v", path, err)
		http.Error(w, "child log unreadable", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// held bytes are an incomplete trailing rune, or a trailing CR, carried to the
	// next read, so a frame never splits a character or a CRLF (writeFrame would
	// turn each half into its own line break).
	buf := make([]byte, logFrameMax+utf8.UTFMax)
	held := 0
	drain := func() bool {
		for {
			n, err := f.Read(buf[held : held+logFrameMax])
			if n > 0 {
				total := held + n
				cut := completeRunes(buf[:total])
				if cut > 0 && buf[cut-1] == '\r' {
					cut--
				}
				if cut > 0 && !send("log", string(buf[:cut])) {
					return false
				}
				held = copy(buf, buf[cut:total])
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("dashboard: child log %s: %v", path, err)
					return false
				}
				return true
			}
		}
	}

	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for drain() {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// completeRunes returns how much of b ends on a rune boundary, leaving off an
// incomplete multi-byte sequence at the end.
func completeRunes(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
