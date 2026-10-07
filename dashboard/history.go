package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// eventsFileName is the Daemon's Events file beside its status file; at its
// size cap the Daemon renames it to eventsFileName+rotatedSuffix, the older
// generation.
const (
	eventsFileName = "spindrift-daemon.events"
	rotatedSuffix  = ".1"
)

// historyLimit caps how many timeline entries one page shows.
const historyLimit = 200

// Event is the subset of an Events-file line the timeline reads. Like Status,
// it mirrors the wire shape rather than importing the launcher module.
type Event struct {
	Time     string `json:"time"`
	Event    string `json:"event"`
	Kind     string `json:"kind"`
	Slot     *int   `json:"slot"`
	Phase    string `json:"phase"`
	Issue    string `json:"issue"`
	Chore    string `json:"chore"`
	Reason   string `json:"reason"`
	Wait     string `json:"wait"`
	State    string `json:"state"`
	Note     string `json:"note"`
	Failures *int   `json:"failures"`
}

// historyEntry is one rendered timeline row.
type historyEntry struct {
	Time    string
	Event   string
	Kind    string
	Subject string
	Slot    string
	Outcome string // settled's state, which styles the row
	Detail  string
	Note    string
}

// timeline maps each event worth a row to what it adds to the row; the rest
// (heartbeats, demand probes, baton and awake bookkeeping) are noise in a
// history. The .history .ev-* selectors in style.css repeat these names, so
// change both together.
var timeline = map[string]func(ev Event, e *historyEntry, add func(string)){
	"child_start": func(Event, *historyEntry, func(string)) {},
	"box": func(ev Event, _ *historyEntry, add func(string)) {
		add(ev.Phase)
	},
	"settled": func(ev Event, e *historyEntry, _ func(string)) {
		e.Outcome, e.Note = ev.State, ev.Note
	},
	"backoff": func(ev Event, _ *historyEntry, add func(string)) {
		add(ev.Wait)
		add(ev.Reason)
	},
	// A jam and a breaker trip pause the pool for Wait, so the row says how long.
	"jam": func(ev Event, _ *historyEntry, add func(string)) {
		add(ev.Wait)
		add(ev.Reason)
	},
	"breaker_trip": func(ev Event, _ *historyEntry, add func(string)) {
		if ev.Failures != nil {
			add(strconv.Itoa(*ev.Failures) + " failures")
		}
		add(ev.Wait)
	},
	"halt": func(ev Event, _ *historyEntry, add func(string)) {
		add(ev.Reason)
	},
}

func (ev Event) entry() historyEntry {
	e := historyEntry{Time: ev.Time, Event: ev.Event, Kind: ev.Kind}
	switch {
	case ev.Issue != "":
		e.Subject = issueLabel(ev.Issue)
	case ev.Chore != "":
		e.Subject = ev.Chore
	}
	if ev.Slot != nil {
		e.Slot = strconv.Itoa(*ev.Slot)
	}
	var detail []string
	add := func(s string) {
		if s != "" {
			detail = append(detail, s)
		}
	}
	timeline[ev.Event](ev, &e, add)
	e.Detail = strings.Join(detail, " ")
	return e
}

// readHistory returns the newest historyLimit timeline entries across the
// older generation then the current one, newest first. An absent generation
// contributes nothing and a malformed line is skipped; any other failure to
// read a generation is returned alongside whatever was read.
func readHistory(eventsPath string) ([]historyEntry, error) {
	// Current first: a rotation between the two opens then leaves the older
	// name on the file already open, rather than on one neither open sees.
	cur, curErr := os.Open(eventsPath)
	older, olderErr := os.Open(eventsPath + rotatedSuffix)
	older = dropRotatedAlias(older, cur)
	var chrono []historyEntry
	var errs []error
	for _, g := range []struct {
		f   *os.File
		err error
	}{{older, olderErr}, {cur, curErr}} {
		if g.err != nil {
			if !errors.Is(g.err, fs.ErrNotExist) {
				errs = append(errs, g.err)
			}
			continue
		}
		if g.f == nil {
			continue
		}
		var err error
		chrono, err = appendTimeline(chrono, g.f)
		g.f.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(chrono) > historyLimit {
		chrono = chrono[len(chrono)-historyLimit:]
	}
	out := make([]historyEntry, len(chrono))
	for i, e := range chrono {
		out[len(chrono)-1-i] = e
	}
	return out, errors.Join(errs...)
}

// dropRotatedAlias closes older and returns nil when it is the same file as
// cur, so a rotation between the two opens does not read one file twice.
func dropRotatedAlias(older, cur *os.File) *os.File {
	if older == nil || cur == nil {
		return older
	}
	oi, oerr := older.Stat()
	ci, cerr := cur.Stat()
	if oerr == nil && cerr == nil && os.SameFile(oi, ci) {
		older.Close()
		return nil
	}
	return older
}

func appendTimeline(entries []historyEntry, f *os.File) ([]historyEntry, error) {
	r := bufio.NewReader(f)
	for {
		// A line is read whole however long; the file can reach tens of MB.
		line, err := r.ReadBytes('\n')
		var ev Event
		if json.Unmarshal(line, &ev) == nil && timeline[ev.Event] != nil {
			entries = append(entries, ev.entry())
			// Trim lazily so a big file never holds more than twice the cap.
			if len(entries) > 2*historyLimit {
				entries = append(entries[:0], entries[len(entries)-historyLimit:]...)
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return entries, err
		}
	}
}
