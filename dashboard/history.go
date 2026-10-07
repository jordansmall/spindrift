package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
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
	ChildLog string `json:"child_log"`
	PassLog  string `json:"pass_log"`
	Exit     *int   `json:"exit"`
	Revision string `json:"revision"`
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
	f, entries, err := openEvents(eventsPath)
	f.Close()
	return entries, err
}

// eventsFollower tails the current Events generation across the Daemon's
// rotation. It holds the current file open and the offset consumed through the
// last newline, so a rotation (our file renamed to the older name, a fresh one
// created) is told apart from an append by file identity, not by size.
type eventsFollower struct {
	path   string
	f      *os.File // nil until the current generation exists
	offset int64
	owners dispatchOwners
}

// openEvents returns the newest historyLimit timeline entries (newest first,
// as readHistory) and a follower positioned just after them. The follower must
// be closed.
func openEvents(eventsPath string) (*eventsFollower, []historyEntry, error) {
	// Current first: a rotation between the two opens then leaves the older
	// name on the file already open, rather than on one neither open sees.
	cur, curErr := os.Open(eventsPath)
	older, olderErr := os.Open(eventsPath + rotatedSuffix)
	older = dropRotatedAlias(older, cur)
	fol := &eventsFollower{path: eventsPath, owners: dispatchOwners{}}
	var chrono []historyEntry
	var errs []error
	for _, g := range []struct {
		f       *os.File
		err     error
		rotated bool
	}{{older, olderErr, true}, {cur, curErr, false}} {
		if g.err != nil {
			if !errors.Is(g.err, fs.ErrNotExist) {
				errs = append(errs, g.err)
			}
			continue
		}
		if g.f == nil {
			continue
		}
		var n int64
		var err error
		opts := scanTrim
		if g.rotated {
			opts |= scanFinal
		}
		chrono, n, err = scanTimeline(chrono, g.f, fol.owners, opts)
		if err != nil {
			errs = append(errs, err)
		}
		if g.rotated {
			g.f.Close()
		} else {
			fol.f, fol.offset = g.f, n
		}
	}
	if len(chrono) > historyLimit {
		chrono = chrono[len(chrono)-historyLimit:]
	}
	out := make([]historyEntry, len(chrono))
	for i, e := range chrono {
		out[len(chrono)-1-i] = e
	}
	return fol, out, errors.Join(errs...)
}

// poll returns the timeline entries appended since the last call (or since
// openEvents), oldest first. A trailing line without its newline is left for a
// later poll, except on a generation the Daemon has rotated away, which is
// closed for writing. Entries read before an error are returned with it.
func (fol *eventsFollower) poll() ([]historyEntry, error) {
	var out []historyEntry
	// Open before comparing, and compare the opened file rather than a stat of
	// the path: a rotation between a stat and a later open would otherwise
	// leave the generation in between unread.
	f, openErr := os.Open(fol.path)
	if openErr != nil && !errors.Is(openErr, fs.ErrNotExist) {
		return nil, openErr
	}
	// f is nil exactly when the path is missing.
	if fol.f != nil {
		heldInfo, err := fol.f.Stat()
		if err != nil {
			closeIfOpen(f)
			return nil, err
		}
		if f != nil {
			newInfo, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, err
			}
			if os.SameFile(newInfo, heldInfo) {
				f.Close()
				if heldInfo.Size() < fol.offset {
					// Truncated in place. The Daemon only rotates by rename, so
					// a copytruncate that outgrows the old offset before this
					// poll is out of contract and loses entries.
					fol.offset = 0
				}
				return fol.drain(out, false)
			}
		}
		rotated := f != nil
		out, err = fol.drain(out, rotated)
		if err != nil || !rotated {
			// A missing path is the gap between the Daemon's rename and its
			// re-create: keep the file and wait for the new generation.
			closeIfOpen(f)
			return out, err
		}
		fol.f.Close()
		fol.f, fol.offset = nil, 0
	}
	if f == nil {
		return out, nil
	}
	fol.f = f
	return fol.drain(out, false)
}

func closeIfOpen(f *os.File) {
	if f != nil {
		f.Close()
	}
}

// drain appends the timeline entries from the held file's offset to out and
// advances the offset past the lines consumed.
func (fol *eventsFollower) drain(out []historyEntry, final bool) ([]historyEntry, error) {
	var opts scanOpts
	if final {
		opts = scanFinal
	}
	out, n, err := scanTimeline(out, io.NewSectionReader(fol.f, fol.offset, math.MaxInt64-fol.offset), fol.owners, opts)
	fol.offset += n
	return out, err
}

func (fol *eventsFollower) Close() {
	if fol.f != nil {
		fol.f.Close()
		fol.f = nil
	}
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

type scanOpts uint8

const (
	// scanFinal treats a last line without its newline as complete, for a
	// generation the Daemon has closed for writing.
	scanFinal scanOpts = 1 << iota
	// scanTrim cuts entries back lazily so a big file never holds more than
	// twice the cap.
	scanTrim
)

// scanTimeline appends r's timeline entries to entries and returns how many
// bytes it consumed: through the last newline, or to EOF under scanFinal.
func scanTimeline(entries []historyEntry, r io.Reader, owners dispatchOwners, opts scanOpts) ([]historyEntry, int64, error) {
	br := bufio.NewReader(r)
	var consumed int64
	for {
		// A line is read whole however long; the file can reach tens of MB.
		line, err := br.ReadBytes('\n')
		if err == nil || (err == io.EOF && opts&scanFinal != 0) {
			consumed += int64(len(line))
			var ev Event
			if json.Unmarshal(line, &ev) == nil && timeline[ev.Event] != nil {
				e := ev.entry()
				owners.claim(ev, &e)
				entries = append(entries, e)
				if opts&scanTrim != 0 && len(entries) > 2*historyLimit {
					entries = append(entries[:0], entries[len(entries)-historyLimit:]...)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return entries, consumed, err
		}
	}
}
