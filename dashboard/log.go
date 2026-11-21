package main

import (
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// logStatus is what the Events file says about one Pass or Child log path; see
// logState.
type logStatus struct {
	named, finished, superseded bool
	// owned is set when the status was read for a Dispatch (a non-nil owner).
	owned bool
	// namedAt and finishedAt are the times of the events that set named and
	// finished, second-precision; zero when unset or unparsable. finishedAt is
	// also zero when finished is inferred (a slotless or aged-out naming),
	// which skips the modified-after-finish check.
	namedAt, finishedAt time.Time
}

// eventTime parses an Event's time, or returns the zero time.
func eventTime(ev Event) time.Time {
	t, err := time.Parse(time.RFC3339, ev.Time)
	if err != nil {
		return time.Time{}
	}
	return t
}

// finishSlack is how far past its stamped finish a finished Dispatch's last
// write to its Pass log may land: event times are truncated to the second.
const finishSlack = time.Second

// priorRunInfix joins a Pass log's path to the N of the copy a later
// Dispatch's launcher moves it to; the launcher owns the naming
// (quarantinePriorRunLogs in cmd/launcher/internal/dispatch/box.go).
const priorRunInfix = ".prior-run."

// modifiedAfterFinish reports whether a Dispatch's tab must answer superseded
// for the file open at info: another Dispatch named the path, or the file was
// modified past this owner's finish. A finished owner never writes its Pass
// log again (an orphan launcher still writing would read as superseded), so
// such a file is a later run's whose box event has not yet reached the Events
// file.
//
// Residual: a newer run writing only within finishSlack of the old finish
// streams to the old tab until the file grows past its open size.
func (st logStatus) modifiedAfterFinish(info os.FileInfo) bool {
	if !st.owned {
		return false
	}
	return st.superseded || st.finished && !st.finishedAt.IsZero() &&
		info.ModTime().After(st.finishedAt.Add(finishSlack))
}

// movedAside reports whether a Dispatch's tab must answer superseded for the
// missing file at full: another Dispatch named the path, or a later launcher
// moved the issue's Pass logs aside as <path>.prior-run.N. The rename keeps
// mtime, so a sibling written at or after this owner named the path is its own.
//
// Residual: namedAt is second-truncated, so an earlier copy last written in
// the same second also answers superseded rather than pruned.
func (st logStatus) movedAside(full string) bool {
	if !st.owned {
		return false
	}
	if st.superseded {
		return true
	}
	if !st.finished || st.namedAt.IsZero() {
		return false
	}
	dir := filepath.Dir(full)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("dashboard: log %s: %v", full, err)
		return false
	}
	prefix := filepath.Base(full) + priorRunInfix
	for _, e := range entries {
		n, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("dashboard: log %s: %v", filepath.Join(dir, e.Name()), err)
			}
			continue
		}
		if !info.ModTime().Before(st.namedAt) {
			return true
		}
	}
	return false
}

// logState reports whether the Daemon announced path (a child_start's
// child_log or a box's pass_log), and whether its child has finished. Only an
// announced path is served, so the query string cannot read arbitrary files.
// The child finished once its slot saw a child_finish or a later child_start
// (its Daemon died without a child_finish). The latest naming wins; a slotless
// naming cannot be followed to a finish, so it counts as finished already.
//
// Pass log paths are fixed per issue or Chore, so later Dispatches reuse
// them. With a non-nil owner the answers are that Dispatch's own, and
// superseded means a different Dispatch (on any slot) named path after it, so
// the file at path is no longer its run's. A nil owner never reports
// superseded.
func (s *server) logState(path string, owner *logOwner) logStatus {
	st := logStatus{owned: owner != nil}
	var slot *int
	owners := dispatchOwners{}
	var e historyEntry // claim's required out-param; only owners is read
	scanGenerations(s.eventsPath, func(ev Event) {
		owners.claim(ev, &e)
		naming := (ev.Event == "child_start" && ev.ChildLog == path) ||
			(ev.Event == "box" && ev.PassLog == path)
		switch {
		case naming && owner == nil:
			st.named, st.finished, slot = true, ev.Slot == nil, ev.Slot
			st.namedAt, st.finishedAt = eventTime(ev), time.Time{}
			return
		case naming && ev.Slot != nil && *ev.Slot == owner.slot &&
			owners[*ev.Slot] == owner.key:
			// The owner is the latest namer again, so the file is its run's after all.
			st.named, st.finished, st.superseded, slot = true, false, false, ev.Slot
			st.namedAt, st.finishedAt = eventTime(ev), time.Time{}
			return
		case naming:
			st.superseded = st.superseded || st.named
			return
		}
		if st.named && slot != nil && ev.Slot != nil && *ev.Slot == *slot &&
			(ev.Event == "child_finish" || ev.Event == "child_start") {
			if !st.finished {
				st.finishedAt = eventTime(ev)
			}
			st.finished = true
		}
	})
	return st
}

// serveLog streams one Child log or Pass log as Server-Sent Events: its
// existing bytes and then each append as log frames. A named log not yet on
// disk while its child runs gets a waiting frame, then an opened frame and the
// stream once it appears; one missing for good gets a single pruned frame, an
// answer rather than an error, and so does one deleted mid-follow. An
// unreadable log ends the stream after logging, and so does a log replaced at
// its path (a retried Pass rotating the old one aside); either way the
// client's reconnect opens whatever file the path names now.
//
// A Pass log tab also passes its Dispatch (slot, at and n, with at required),
// because later Dispatches of the issue reuse the path. Once another Dispatch
// has named it, the stream sends what its open file held and one superseded
// frame, never the newer run's bytes. Superseded also covers the file moved to
// <path>.prior-run.N, a file modified after the finish (none of it sent), and
// a file grown past its size when a finished tab opened.
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
	st := s.logState(path, owner)
	if !st.named {
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
		if st.superseded {
			send("superseded", path)
			return
		}
		f, err = os.Open(full)
		if !errors.Is(err, fs.ErrNotExist) {
			break
		}
		if st.finished {
			if st.movedAside(full) {
				send("superseded", path)
			} else {
				send("pruned", path)
			}
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
		st = s.logState(path, owner)
		st.finished = st.finished || !st.named
	}
	if err != nil {
		log.Printf("dashboard: log %s: %v", path, err)
		if !waited {
			http.Error(w, "log unreadable", http.StatusInternalServerError)
		}
		return
	}
	defer f.Close()

	openInfo, err := f.Stat()
	if err != nil {
		log.Printf("dashboard: log %s: %v", path, err)
		return
	}
	if st.modifiedAfterFinish(openInfo) {
		send("superseded", path)
		return
	}
	// An empty log sends no bytes, which would leave the client's waiting note up.
	if waited && !send("opened", path) {
		return
	}
	// A tab opened after its Dispatch finished is capped at the size it found.
	var rd io.Reader = f
	capped := st.owned && st.finished
	if capped {
		rd = io.LimitReader(f, openInfo.Size())
	}

	// held bytes are an incomplete trailing rune, or a trailing CR, carried to the
	// next read, so a frame never splits a character or a CRLF (writeFrame would
	// turn each half into its own line break).
	buf := make([]byte, logFrameMax+utf8.UTFMax)
	held := 0
	drain := func() bool {
		for {
			n, err := rd.Read(buf[held : held+logFrameMax])
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
		// A retried Pass rotates its log aside and creates a fresh one; ending
		// the stream lets the client reconnect onto it. A missing path while
		// the child runs is the gap between rename and create, so keep following.
		if capped {
			if fi, err := f.Stat(); err == nil && fi.Size() > openInfo.Size() {
				send("superseded", path)
				return
			}
		}
		cur, statErr := os.Stat(full)
		if statErr == nil {
			if !os.SameFile(cur, openInfo) {
				if owner != nil {
					if now := s.logState(path, owner); now.finished || now.superseded {
						if drain() {
							send("superseded", path)
						}
					}
				}
				return
			}
		} else if errors.Is(statErr, fs.ErrNotExist) {
			if now := s.logState(path, owner); now.superseded || !now.named || now.finished {
				// The child may have appended its last bytes and finished, and the
				// file been deleted, within one poll; the open descriptor still has them.
				if drain() {
					if now.movedAside(full) {
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
