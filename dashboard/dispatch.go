package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// dispatchView is one Dispatch as the drill-in page shows it: the child_start
// that began it and what the same slot reported before its next child_start.
type dispatchView struct {
	Slot     int
	Kind     string
	Started  string
	StartN   int  // how many earlier child_starts on the slot share Started
	Closed   bool // the child finished or a later child_start took over the slot
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
type passLog struct {
	Phase string `json:"phase"`
	Path  string `json:"path"`
}

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
		if !v.Closed { // the tab set is final once the Dispatch ends
			v.addPassLog(ev)
		}
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
		v.Closed = true
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
	f := s.followDispatch(slot, at, n)
	f.Close()
	return f.d, f.d != nil
}

// dispatchFollow is findDispatch's scan kept open: the Dispatch it found and the
// tail positioned after what the scan read, so later events fold in without
// rereading. The caller closes tail.
type dispatchFollow struct {
	slot     int
	at       string
	n        int
	owners   dispatchOwners
	d        *dispatchView // nil when no child_start matched
	cur      *dispatchView // nil once a later child_start has closed the candidate
	startGen int           // the tail generation that held d's child_start
	pinned   bool          // the scan is over: d is the Dispatch, whatever starts next
	tail     eventsTail
}

func (s *server) followDispatch(slot int, at string, n int) *dispatchFollow {
	f := &dispatchFollow{slot: slot, at: at, n: n, owners: dispatchOwners{}, tail: eventsTail{path: s.eventsPath}}
	f.tail.open(f.fold) // an unreadable generation just yields no Dispatch, so not found
	f.pinned = true
	return f
}

// poll folds in what the Events file gained since the last scan or poll.
func (f *dispatchFollow) poll() error { return f.tail.poll(f.fold) }

func (f *dispatchFollow) Close() { f.tail.Close() }

// rotatedAway reports whether rotation has taken the generation holding the
// child_start: findDispatch no longer resolves the Dispatch, so a reload 404s.
// It assumes at most one rotation per poll, as the Daemon's size cap allows.
func (f *dispatchFollow) rotatedAway() bool { return f.tail.gen-f.startGen >= 2 }

func (f *dispatchFollow) fold(ev Event) {
	if ev.Slot == nil || *ev.Slot != f.slot {
		return
	}
	var e historyEntry
	if !f.pinned {
		f.owners.claim(ev, &e)
	}
	if ev.Event != "child_start" {
		if f.cur != nil {
			f.cur.absorb(ev)
		}
		return
	}
	if f.cur != nil {
		f.cur.Closed = true
	}
	f.cur = nil
	if !f.pinned && (f.at == "" || (e.Started == f.at && e.StartN == f.n)) {
		f.cur = &dispatchView{Slot: f.slot, Kind: ev.Kind, Started: ev.Time, StartN: e.StartN,
			ChildLog: ev.ChildLog, Rev: shortRev(ev.Revision)}
		f.d, f.startGen = f.cur, f.tail.gen
	}
}

// scanGenerations calls fn for each well-formed event in the older Events
// generation then the current one, oldest first, reading as openEvents does so
// nothing is read twice. The current generation's last line is skipped while it
// lacks its newline; a generation that fails to read partway yields only the
// events before the failure.
func scanGenerations(eventsPath string, fn func(Event)) {
	t := eventsTail{path: eventsPath}
	t.open(fn)
	t.Close()
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
	d, found := s.findDispatch(slot, at, n)
	if !found {
		http.NotFound(w, r)
		return
	}
	// A missing, unreadable or skewed status only costs the issue links.
	st, _, _ := readStatus(s.statusPath)
	linkSubjects(d, repoURLOf(st))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "dispatch.html.tmpl", d); err != nil {
		writeRenderError(w, err)
	}
}

// linkSubjects links d's subjects in place, so the page and the head frame
// render the same issue links.
func linkSubjects(d *dispatchView, repoURL string) {
	for i := range d.Subject {
		d.Subject[i] = d.Subject[i].linked(repoURL)
	}
}

// writeRenderError leaves a comment where a template failed midway; the
// headers are gone by then, so the page can only be cut short.
func writeRenderError(w io.Writer, err error) {
	fmt.Fprintf(w, "<!-- render error: %v -->", err)
}

// dispatchEvents streams a Dispatch's header and Pass logs as Server-Sent
// Events so an open drill-in page follows it live: a head frame with the
// header's HTML on connect and whenever it changes, a pass frame per Pass log
// so the page can add a tab, all current ones on connect, then each new one
// as the Events file grows, and a closed frame once the Dispatch has ended.
// A query without at is pinned to the Dispatch it first resolves, so a later
// child_start closes the stream rather than swapping it. Pass frames carry
// JSON, as dispatch.js builds the tabs from text.
func (s *server) dispatchEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slot, at, n, ok := dispatchIDOf(r.URL.Query())
	if !ok {
		http.NotFound(w, r)
		return
	}
	f := s.followDispatch(slot, at, n)
	defer f.Close()
	d := f.d
	if d == nil {
		http.NotFound(w, r)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	// Read once, like the page: a missing, unreadable or skewed status only
	// costs the links.
	st, _, _ := readStatus(s.statusPath)
	repo := repoURLOf(st)

	// A child_start without a time cannot be named again, so the loop could
	// only drift to another Dispatch: treat it as the last word on this one.
	unnameable := d.Started == ""
	sendClosed := func() {
		writeFrame(w, "closed", "")
		rc.Flush()
	}

	// PassLogs only ever grows and each poll mutates d in place, so a count
	// says which entries have gone out.
	sent := 0
	// Empty at first, so the connect always sends the header: a reconnecting
	// client re-syncs one that went stale during the gap.
	lastHead := ""
	// step sends what d adds and reports whether the stream goes on.
	step := func() bool {
		linkSubjects(d, repo)
		// Before closed, so the exit child_finish brings is not lost.
		if head := s.render("dispatch-head", d); head != lastHead {
			lastHead = head
			if writeFrame(w, "head", head) != nil || rc.Flush() != nil {
				return false
			}
		}
		for ; sent < len(d.PassLogs); sent++ {
			data, err := json.Marshal(d.PassLogs[sent])
			if err != nil || writeFrame(w, "pass", string(data)) != nil || rc.Flush() != nil {
				return false
			}
		}
		if d.Closed || unnameable {
			sendClosed()
			return false
		}
		return true
	}
	if !step() {
		return
	}

	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		// A failed poll is retried by the next tick, even forever: the stream then
		// lives until the client leaves rather than closing as a rescan would.
		f.poll()
		if !step() {
			return
		}
		// End as findDispatch would once rotation has taken the child_start.
		if f.rotatedAway() {
			sendClosed()
			return
		}
	}
}
