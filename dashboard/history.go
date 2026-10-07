package main

import (
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
	ChildLog string `json:"child_log"`
	PassLog  string `json:"pass_log"`
	Exit     *int   `json:"exit"`
	Revision string `json:"revision"`
	PRURL    string `json:"pr_url"`
}

// historyEntry is one rendered timeline row.
type historyEntry struct {
	Time    string
	Event   string
	Kind    string
	Subject subject
	Slot    string
	PRURL   string // settled's PR, when it opened one
	Outcome string // settled's state, which styles the row
	Detail  string
	Note    string
	// Started and StartN name the Dispatch the row belongs to: the time of its
	// child_start and how many earlier child_starts on the slot share that time.
	// Started is empty when the scan saw no child_start for the slot.
	Started string
	StartN  int
}

// dispatchKey is the owning child_start of a slot's latest Dispatch.
type dispatchKey struct {
	started string
	n       int
}

// dispatchOwners tracks each slot's latest child_start across a scan, so every
// later row on the slot can name its Dispatch. Event times have one-second
// resolution, so a row's own time cannot: a child_finish and the next
// child_start can share a second, and so can two child_starts.
type dispatchOwners map[int]dispatchKey

// claim sets e's owning Dispatch from ev, which must be e's event.
func (o dispatchOwners) claim(ev Event, e *historyEntry) {
	if ev.Slot == nil {
		return
	}
	if ev.Event == "child_start" {
		k := dispatchKey{started: ev.Time}
		if prev, ok := o[*ev.Slot]; ok && prev.started == ev.Time {
			k.n = prev.n + 1
		}
		o[*ev.Slot] = k
	}
	if k, ok := o[*ev.Slot]; ok {
		e.Started, e.StartN = k.started, k.n
	}
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
		e.Outcome, e.Note, e.PRURL = ev.State, ev.Note, ev.PRURL
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
	e.Subject = subjectOf(ev.Issue, ev.Chore)
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
	f, entries, err := openEvents(eventsPath)
	f.Close()
	return entries, err
}

// eventsFollower is the history layer over an eventsTail: it keeps the
// timeline events as entries and the Dispatch ownership they were claimed by.
type eventsFollower struct {
	tail   eventsTail
	owners dispatchOwners
}

// entryOf returns ev's timeline entry, claimed against fol's owners; ok is
// false for an event the timeline does not show.
func (fol *eventsFollower) entryOf(ev Event) (e historyEntry, ok bool) {
	if timeline[ev.Event] == nil {
		return e, false
	}
	e = ev.entry()
	fol.owners.claim(ev, &e)
	return e, true
}

// openEvents returns the newest historyLimit timeline entries (newest first,
// as readHistory) and a follower positioned just after them. The follower must
// be closed.
func openEvents(eventsPath string) (*eventsFollower, []historyEntry, error) {
	fol := &eventsFollower{tail: eventsTail{path: eventsPath}, owners: dispatchOwners{}}
	var chrono []historyEntry
	err := fol.tail.open(func(ev Event) {
		if e, ok := fol.entryOf(ev); ok {
			chrono = append(chrono, e)
			// Cut back lazily so a big file never holds more than twice the cap.
			if len(chrono) > 2*historyLimit {
				chrono = append(chrono[:0], chrono[len(chrono)-historyLimit:]...)
			}
		}
	})
	if len(chrono) > historyLimit {
		chrono = chrono[len(chrono)-historyLimit:]
	}
	out := make([]historyEntry, len(chrono))
	for i, e := range chrono {
		out[len(chrono)-1-i] = e
	}
	return fol, out, err
}

// poll returns the timeline entries appended since the last call (or since
// openEvents), oldest first, with eventsTail.poll's rules for a trailing line,
// rotation and an error.
func (fol *eventsFollower) poll() ([]historyEntry, error) {
	var out []historyEntry
	err := fol.tail.poll(func(ev Event) {
		if e, ok := fol.entryOf(ev); ok {
			out = append(out, e)
		}
	})
	return out, err
}

func (fol *eventsFollower) Close() { fol.tail.Close() }

// linkEntries links entries' subjects in place, so a snapshot read off disk
// renders the same issue links as live-tailed events.
func linkEntries(entries []historyEntry, repoURL string) {
	for i := range entries {
		entries[i].Subject = entries[i].Subject.linked(repoURL)
	}
}
