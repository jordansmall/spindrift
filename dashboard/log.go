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

// logOwner is the Dispatch a Pass log tab was opened from: its slot and the
// dispatchKey of its child_start.
type logOwner struct {
	slot int
	key  dispatchKey
}

// logState reports whether the Daemon announced path, and whether the child
// that owns it has finished. A child_start names it as its child_log and a box
// as its pass_log; only a path the Daemon announced is served, so the query
// string cannot be used to read arbitrary files under the checkout. The child
// finished once its slot saw a child_finish, or a later child_start (a Daemon
// that died without one). The latest naming wins, and a naming that carries no
// slot cannot be followed to a finish, so it counts as finished already.
//
// Pass log paths are fixed per issue or Chore, so a later Dispatch reuses the
// path an earlier one named. With a non-nil owner the answers are that
// Dispatch's own: named means one of its events named path, finished means its
// slot has since finished, and superseded means a different Dispatch (on any
// slot) named path after it, so the file at path is no longer its run's. A nil
// owner never reports superseded.
func (s *server) logState(path string, owner *logOwner) (named, finished, superseded bool) {
	var slot *int
	owners := dispatchOwners{}
	var e historyEntry // claim's required out-param; only owners is read
	scanGenerations(s.eventsPath, func(ev Event) {
		owners.claim(ev, &e)
		naming := (ev.Event == "child_start" && ev.ChildLog == path) ||
			(ev.Event == "box" && ev.PassLog == path)
		switch {
		case naming && owner == nil:
			named, finished, slot = true, ev.Slot == nil, ev.Slot
			return
		case naming && ev.Slot != nil && *ev.Slot == owner.slot &&
			owners[*ev.Slot] == owner.key:
			// The owner is the latest namer again, so the file is its run's after all.
			named, finished, superseded, slot = true, false, false, ev.Slot
			return
		case naming:
			superseded = superseded || named
			return
		}
		if named && slot != nil && ev.Slot != nil && *ev.Slot == *slot &&
			(ev.Event == "child_finish" || ev.Event == "child_start") {
			finished = true
		}
	})
	return named, finished, superseded
}

// serveLog streams one Child log or Pass log as Server-Sent Events: its
// existing bytes and then each append as log frames. A named log not yet on
// disk while its child runs gets a waiting frame, then streams once it appears;
// one missing for good gets a single pruned frame, an answer rather than an
// error, and so does one deleted mid-follow. An unreadable log ends the stream
// after logging, and so does a log replaced at its path (a retried Pass
// rotating the old one aside); either way the client's reconnect opens
// whatever file the path names now.
//
// A Pass log tab also passes its Dispatch (slot, at and n, as the dispatch page
// takes them but with at required), because the path is reused by later
// Dispatches of the same issue.
// Once another Dispatch has named the path, or the file is replaced after this
// Dispatch finished (it never retries, so the replacement is a later
// Dispatch's, possibly before its box event reached the Events file), the
// stream sends what its open file held and a single superseded frame, never
// the newer run's bytes.
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
	owner, ok := logOwnerOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	named, finished, superseded := s.logState(path, owner)
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
		if superseded {
			send("superseded", path)
			return
		}
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
		var stillNamed, fin bool
		stillNamed, fin, superseded = s.logState(path, owner)
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
				if owner != nil {
					if _, fin, sup := s.logState(path, owner); fin || sup {
						if drain() {
							send("superseded", path)
						}
					}
				}
				return
			}
		} else if errors.Is(statErr, fs.ErrNotExist) {
			if stillNamed, fin, sup := s.logState(path, owner); sup || !stillNamed || fin {
				// The child may have appended its last bytes and finished, and the
				// file been deleted, within one poll; the open descriptor still has them.
				if drain() {
					if sup {
						send("superseded", path)
					} else {
						send("pruned", path)
					}
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

// logOwnerOf reads the optional Dispatch a Pass log tab names, as dispatchIDOf
// reads one. No slot means no owner, as an old page or a Child log sends; ok is
// false for an at or n without the slot they qualify. Unlike the dispatch page,
// a slot must come with a concrete at, since a tab names one Dispatch, not
// whichever ran last on the slot.
func logOwnerOf(r *http.Request) (owner *logOwner, ok bool) {
	q := r.URL.Query()
	if !q.Has("slot") {
		return nil, !q.Has("at") && !q.Has("n")
	}
	slot, at, n, ok := dispatchIDOf(q)
	if !ok || at == "" {
		return nil, false
	}
	return &logOwner{slot: slot, key: dispatchKey{started: at, n: n}}, true
}
