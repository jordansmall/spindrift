package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// defaultPoll is how often /events looks at the status and Events files; a
// change should reach the page within about a second.
const defaultPoll = 500 * time.Millisecond

// events streams the Dashboard's live view as Server-Sent Events: a status
// frame and a history frame on connect, then a status frame whenever the
// rendered header and cards change and an entry frame per new timeline entry.
// Frames carry HTML from the page's own templates, so a client only inserts.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	// The follower is opened before the status is rendered so an entry
	// appended in between still arrives, by poll, rather than being lost.
	fol, snapshot, histErr := openEvents(s.eventsPath)
	defer fol.Close()

	st, raw, rerr := readStatus(s.statusPath)
	send := func(event, html string) bool {
		return writeFrame(w, event, html) == nil && rc.Flush() == nil
	}
	renderStatus := func() string { return s.render("status", s.viewFrom(st, raw, rerr)) }

	linkEntries(snapshot, repoURLOf(st))
	lastStatus := renderStatus()
	if !send("status", lastStatus) ||
		!send("history", s.render("history", view{History: snapshot, HistoryErr: histErr})) {
		return
	}

	// A stuck read failure is logged once, not on every tick.
	lastEventsErr := ""
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}

		// Re-read and re-rendered every tick: the file is small, and a stat
		// comparison would miss two rewrites inside one timestamp tick. A
		// Daemon that dies without rewriting it only shows in the liveness
		// check and the uptime clocks.
		st, raw, rerr = readStatus(s.statusPath)
		if html := renderStatus(); html != lastStatus {
			lastStatus = html
			if !send("status", html) {
				return
			}
		}

		// Entries read before an error are real, so they go out either way; the
		// next tick retries the failure.
		entries, err := fol.poll()
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != lastEventsErr {
			lastEventsErr = msg
			if err != nil {
				log.Printf("dashboard: events: %v", err)
			}
		}
		repo := repoURLOf(st)
		for _, e := range entries {
			e.Subject = e.Subject.linked(repo)
			if !send("entry", s.render("entry", e)) {
				return
			}
		}
	}
}

func (s *server) render(name string, data any) string {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("dashboard: render %s: %v", name, err)
		writeRenderError(&buf, err)
	}
	return buf.String()
}

var sseLineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// writeFrame writes one SSE frame, a data line per line of data. A client
// joins them back with newlines.
func writeFrame(w io.Writer, event, data string) error {
	var buf strings.Builder
	buf.WriteString("event: " + event + "\n")
	for _, line := range strings.Split(sseLineBreaks.Replace(data), "\n") {
		buf.WriteString("data: " + line + "\n")
	}
	buf.WriteString("\n")
	_, err := io.WriteString(w, buf.String())
	return err
}
