package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
)

// eventsTail follows the current Events generation across the Daemon's
// rotation, yielding each well-formed event to a callback. It holds the current
// file open and the offset consumed through the last newline, so a rotation
// (our file renamed to the older name, a fresh one created) is told apart from
// an append by file identity, not by size.
type eventsTail struct {
	path   string
	f      *os.File // nil until the current generation exists
	offset int64
	// gen numbers the generation of the event fn is being handed: 0 for the
	// older generation read by open, 1 from the current one on, and one more for
	// each rotation poll crosses, bumped before the new file's events arrive.
	gen int
}

// open calls fn for each well-formed event in the older generation then the
// current one, oldest first, and leaves t holding the current one at the offset
// through its last newline. An absent generation contributes nothing; any other
// failure to read one is returned alongside whatever was read. t must be closed.
func (t *eventsTail) open(fn func(Event)) error {
	// Current first: a rotation between the two opens then leaves the older
	// name on the file already open, rather than on one neither open sees.
	cur, curErr := os.Open(t.path)
	older, olderErr := os.Open(t.path + rotatedSuffix)
	older = dropRotatedAlias(older, cur)
	var errs []error
	for _, g := range []struct {
		f       *os.File
		err     error
		rotated bool
	}{{older, olderErr, true}, {cur, curErr, false}} {
		t.gen = 1
		if g.rotated {
			t.gen = 0
		}
		if g.err != nil {
			if !errors.Is(g.err, fs.ErrNotExist) {
				errs = append(errs, g.err)
			}
			continue
		}
		if g.f == nil {
			continue
		}
		n, err := scanEvents(g.f, g.rotated, fn)
		if err != nil {
			errs = append(errs, err)
		}
		if g.rotated {
			g.f.Close()
		} else {
			t.f, t.offset = g.f, n
		}
	}
	t.gen = 1
	return errors.Join(errs...)
}

// poll calls fn for each event appended since the last call (or since open),
// oldest first. A trailing line without its newline is left for a later poll,
// except on a generation the Daemon has rotated away, which is closed for
// writing. Events read before an error are delivered before it is returned.
func (t *eventsTail) poll(fn func(Event)) error {
	// Open before comparing, and compare the opened file rather than a stat of
	// the path: a rotation between a stat and a later open would otherwise
	// leave the generation in between unread.
	f, openErr := os.Open(t.path)
	if openErr != nil && !errors.Is(openErr, fs.ErrNotExist) {
		return openErr
	}
	// f is nil exactly when the path is missing.
	if t.f != nil {
		heldInfo, err := t.f.Stat()
		if err != nil {
			closeIfOpen(f)
			return err
		}
		if f != nil {
			newInfo, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			if os.SameFile(newInfo, heldInfo) {
				f.Close()
				if heldInfo.Size() < t.offset {
					// Truncated in place. The Daemon only rotates by rename, so
					// a copytruncate that outgrows the old offset before this
					// poll is out of contract and loses entries.
					t.offset = 0
				}
				return t.drain(fn, false)
			}
		}
		rotated := f != nil
		err = t.drain(fn, rotated)
		if err != nil || !rotated {
			// A missing path is the gap between the Daemon's rename and its
			// re-create: keep the file and wait for the new generation.
			closeIfOpen(f)
			return err
		}
		t.f.Close()
		t.f, t.offset = nil, 0
		t.gen++
	}
	if f == nil {
		return nil
	}
	t.f = f
	return t.drain(fn, false)
}

// drain delivers the events from the held file's offset and advances the
// offset past the lines consumed.
func (t *eventsTail) drain(fn func(Event), final bool) error {
	n, err := scanEvents(io.NewSectionReader(t.f, t.offset, math.MaxInt64-t.offset), final, fn)
	t.offset += n
	return err
}

func (t *eventsTail) Close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}

func closeIfOpen(f *os.File) {
	if f != nil {
		f.Close()
	}
}

// dropRotatedAlias closes older and returns nil when it is the same file as
// cur, so a rotation between the two opens does not read one file twice.
// cmd/launcher/internal/daemon/events_file.go carries a copy.
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

// scanEvents calls fn for each well-formed line of r and returns how many
// bytes it consumed: through the last newline, or to EOF when final, which
// treats a last line without its newline as complete.
func scanEvents(r io.Reader, final bool, fn func(Event)) (int64, error) {
	br := bufio.NewReader(r)
	var consumed int64
	for {
		// A line is read whole however long; the file can reach tens of MB.
		line, err := br.ReadBytes('\n')
		if err == nil || (err == io.EOF && final) {
			consumed += int64(len(line))
			var ev Event
			if json.Unmarshal(line, &ev) == nil {
				fn(ev)
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return consumed, err
		}
	}
}
