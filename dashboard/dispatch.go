package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	ChildLog string
	Rev      string
	Subject  []string // box events' issue/chore, deduplicated
	Phase    string   // the latest box phase
	Outcome  string   // settled's state
	Note     string
	Exit     *int // child_finish's exit; nil until the child finishes
}

// absorb folds a later event on the Dispatch's slot into v.
func (v *dispatchView) absorb(ev Event) {
	switch ev.Event {
	case "box":
		v.Phase = ev.Phase
		subject := ev.Chore
		if ev.Issue != "" {
			subject = issueLabel(ev.Issue)
		}
		if subject == "" {
			return
		}
		for _, s := range v.Subject {
			if s == subject {
				return
			}
		}
		v.Subject = append(v.Subject, subject)
	case "settled":
		v.Outcome, v.Note = ev.State, ev.Note
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
			cur = &dispatchView{Slot: slot, Kind: ev.Kind, Started: ev.Time,
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

func (s *server) dispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	slot, err := strconv.Atoi(q.Get("slot"))
	if err != nil || slot < 0 {
		http.NotFound(w, r)
		return
	}
	// Parsed only to reject junk; findDispatch compares the raw string.
	at := q.Get("at")
	if at != "" {
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	n := 0
	if raw := q.Get("n"); raw != "" {
		if n, err = strconv.Atoi(raw); err != nil || n < 0 {
			http.NotFound(w, r)
			return
		}
	}
	d, ok := s.findDispatch(slot, at, n)
	if !ok {
		http.NotFound(w, r)
		return
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
