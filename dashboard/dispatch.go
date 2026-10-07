package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// dispatchView is one Dispatch as the drill-in page shows it: the child_start
// that began it and what the same slot reported before its next child_start.
type dispatchView struct {
	Slot     int
	Kind     string
	Started  string
	StartN   int // how many earlier child_starts on the slot share Started
	ChildLog string
	Rev      string
	Subject  []subject // box events' issue/chore, deduplicated
	Phase    string    // the latest box phase
	Outcome  string    // settled's state
	Note     string
	PRURL    string // settled's PR, when it opened one
	Exit     *int   // child_finish's exit; nil until the child finishes
	PassLogs []passLog
}

// passLog is one Pass log a box event named; Phase labels its tab.
type passLog struct{ Phase, Path string }

// addPassLog records ev's Pass log once: a transient retry re-announces the
// same phase with the same path, and recover boxes name none.
func (v *dispatchView) addPassLog(ev Event) {
	if ev.PassLog == "" {
		return
	}
	for _, p := range v.PassLogs {
		if p.Path == ev.PassLog {
			return
		}
	}
	v.PassLogs = append(v.PassLogs, passLog{Phase: ev.Phase, Path: ev.PassLog})
}

// absorb folds a later event on the Dispatch's slot into v.
func (v *dispatchView) absorb(ev Event) {
	switch ev.Event {
	case "box":
		v.Phase = ev.Phase
		v.addPassLog(ev)
		s := subjectOf(ev.Issue, ev.Chore)
		if s.Label == "" {
			return
		}
		for _, have := range v.Subject {
			if have.Label == s.Label {
				return
			}
		}
		v.Subject = append(v.Subject, s)
	case "settled":
		v.Outcome, v.Note, v.PRURL = ev.State, ev.Note, ev.PRURL
	case "child_finish":
		v.Exit = ev.Exit
		if v.Rev == "" {
			v.Rev = shortRev(ev.Revision)
		}
	}
}

func shortRev(r string) string {
	if len(r) > revisionLen {
		return r[:revisionLen]
	}
	return r
}

// findDispatch returns the Dispatch on slot that began with the child_start
// whose time is exactly at, with that Dispatch's later events folded in; at is
// empty for the slot's latest. A Dispatch has no id of its own, so slot plus its
// child_start's time names it, and n picks the (n+1)-th of consecutive
// child_starts sharing that time. Ownership is dispatchOwners.claim's, so a
// link built from a history row resolves to the Dispatch the row was owned by;
// a time or n that names no child_start is not found.
func (s *server) findDispatch(slot int, at string, n int) (*dispatchView, bool) {
	owners := dispatchOwners{}
	var found *dispatchView
	var cur *dispatchView // nil once a later child_start has closed the candidate
	scanGenerations(s.eventsPath, func(ev Event) {
		if ev.Slot == nil || *ev.Slot != slot {
			return
		}
		var e historyEntry
		owners.claim(ev, &e)
		if ev.Event != "child_start" {
			if cur != nil {
				cur.absorb(ev)
			}
			return
		}
		cur = nil
		if at == "" || (e.Started == at && e.StartN == n) {
			cur = &dispatchView{Slot: slot, Kind: ev.Kind, Started: ev.Time, StartN: e.StartN,
				ChildLog: ev.ChildLog, Rev: shortRev(ev.Revision)}
			found = cur
		}
	})
	return found, found != nil
}

// scanGenerations calls fn for each well-formed event in the older Events
// generation then the current one, oldest first. As openEvents does, it opens
// current first and drops an older that a rotation made the same file, so
// nothing is read twice.
func scanGenerations(eventsPath string, fn func(Event)) {
	cur, _ := os.Open(eventsPath)
	older, _ := os.Open(eventsPath + rotatedSuffix)
	older = dropRotatedAlias(older, cur)
	for _, f := range []*os.File{older, cur} {
		if f != nil {
			scanEvents(f, fn)
			f.Close()
		}
	}
}

// scanEvents calls fn for each well-formed line, oldest first. A line is read
// whole however long, as scanTimeline does.
func scanEvents(r io.Reader, fn func(Event)) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		var ev Event
		if json.Unmarshal(line, &ev) == nil {
			fn(ev)
		}
		if err != nil {
			return
		}
	}
}

// dispatchIDOf reads the Dispatch identity a query names: slot, an optional
// at (empty, or RFC3339) and an optional n. ok is false for a missing or junk
// value. at is parsed only to reject junk; findDispatch compares the raw string.
func dispatchIDOf(q url.Values) (slot int, at string, n int, ok bool) {
	slot, err := strconv.Atoi(q.Get("slot"))
	if err != nil || slot < 0 {
		return 0, "", 0, false
	}
	at = q.Get("at")
	if at != "" {
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			return 0, "", 0, false
		}
	}
	if raw := q.Get("n"); raw != "" {
		if n, err = strconv.Atoi(raw); err != nil || n < 0 {
			return 0, "", 0, false
		}
	}
	return slot, at, n, true
}

func (s *server) dispatch(w http.ResponseWriter, r *http.Request) {
	slot, at, n, ok := dispatchIDOf(r.URL.Query())
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, ok := s.findDispatch(slot, at, n)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A missing, unreadable or skewed status only costs the issue links.
	st, _, _ := readStatus(s.statusPath)
	repo := repoURLOf(st)
	for i := range d.Subject {
		d.Subject[i] = d.Subject[i].linked(repo)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "dispatch.html.tmpl", d); err != nil {
		writeRenderError(w, err)
	}
}

// writeRenderError leaves a comment where a template failed midway; the
// headers are gone by then, so the page can only be cut short.
func writeRenderError(w io.Writer, err error) {
	fmt.Fprintf(w, "<!-- render error: %v -->", err)
}
