package main

import (
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

// logState reports whether the Daemon announced path, and whether the child
// that owns it has finished. A child_start names it as its child_log and a box
// as its pass_log; only a path the Daemon announced is served, so the query
// string cannot be used to read arbitrary files under the checkout. The child
// finished once its slot saw a child_finish, or a later child_start (a Daemon
// that died without one). The latest naming wins, and a naming that carries no
// slot cannot be followed to a finish, so it counts as finished already.
func (s *server) logState(path string) (named, finished bool) {
	var slot *int
	scanGenerations(s.eventsPath, func(ev Event) {
		if (ev.Event == "child_start" && ev.ChildLog == path) ||
			(ev.Event == "box" && ev.PassLog == path) {
			named, finished, slot = true, ev.Slot == nil, ev.Slot
			return
		}
		if named && slot != nil && ev.Slot != nil && *ev.Slot == *slot &&
			(ev.Event == "child_finish" || ev.Event == "child_start") {
			finished = true
		}
	})
	return named, finished
}

// serveLog streams one Child log or Pass log as Server-Sent Events: its
// existing bytes and then each append as log frames. A named log not yet on
// disk while its child runs gets a waiting frame, then streams once it appears;
// one missing for good gets a single pruned frame, an answer rather than an
// error, and so does one deleted mid-follow. An unreadable log ends the stream
// after logging, and so does a log replaced at its path (a retried Pass
// rotating the old one aside); either way the client's reconnect opens
// whatever file the path names now.
func (s *server) serveLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if !filepath.IsLocal(path) {
		http.NotFound(w, r)
		return
	}
	named, finished := s.logState(path)
	if !named {
		http.NotFound(w, r)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	send := func(event, data string) bool {
		return writeFrame(w, event, data) == nil && rc.Flush() == nil
	}

	full := filepath.Join(s.checkout, path)
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	var f *os.File
	var err error
	waited := false
	for {
		f, err = os.Open(full)
		if !errors.Is(err, fs.ErrNotExist) {
			break
		}
		if finished {
			send("pruned", path)
			return
		}
		if !waited {
			if !send("waiting", path) {
				return
			}
			waited = true
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		// A naming event that aged out of the Events file leaves nothing to create
		// the file, so it counts as finished too.
		stillNamed, fin := s.logState(path)
		finished = !stillNamed || fin
	}
	if err != nil {
		log.Printf("dashboard: log %s: %v", path, err)
		if !waited {
			http.Error(w, "log unreadable", http.StatusInternalServerError)
		}
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
					log.Printf("dashboard: log %s: %v", path, err)
					return false
				}
				return true
			}
		}
	}

	for drain() {
		// A retried Pass rotates its log aside and creates a fresh one at the same
		// path; ending the stream lets the client reconnect onto the new file. A
		// missing path is the gap between rename and create while the child runs, so
		// keep following; once it has finished the path is gone for good, and drain
		// has already sent what the open file held.
		cur, statErr := os.Stat(full)
		if statErr == nil {
			if openInfo, openErr := f.Stat(); openErr == nil && !os.SameFile(cur, openInfo) {
				return
			}
		} else if errors.Is(statErr, fs.ErrNotExist) {
			if stillNamed, fin := s.logState(path); !stillNamed || fin {
				// The child may have appended its last bytes and finished, and the
				// file been deleted, within one poll; the open descriptor still has them.
				if drain() {
					send("pruned", path)
				}
				return
			}
		}
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
