package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The Dashboard mirrors both names; nix/checks/dashboard-constant-parity.nix
// pins them.
const (
	eventsFileName = "spindrift-daemon.events"

	// rotatedSuffix names the single earlier generation beside the Events file.
	rotatedSuffix = ".1"

	// eventsFileCap is a fixed cap, deliberately not a knob: the stream
	// runs at about 2 MB a week, so tens of MB hold months across the two
	// generations.
	eventsFileCap = 32 << 20
)

// EventsFile is the durable copy of the Daemon's event stream, kept beside
// the status file in the checkout's git dir. It rolls to a ".1" sibling at
// the cap, replacing any earlier one, so exactly one earlier generation
// survives.
type EventsFile struct {
	path string
	max  int64 // eventsFileCap; tests lower it
}

// NewEventsFile returns the Events file in gitDirPath. Like the status
// writer, build it only once the checkout lock is held: a refused second
// daemon must not append to or rotate the live holder's file.
func NewEventsFile(gitDirPath string) *EventsFile {
	return &EventsFile{path: filepath.Join(gitDirPath, eventsFileName), max: eventsFileCap}
}

func (f *EventsFile) open() (*os.File, error) {
	return os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// append writes one whole line. It opens the file per line rather than
// holding a descriptor, so the file can vanish (or be rotated by hand)
// under a long-running daemon without wedging it. A line is never split
// across generations: rotation happens before the write that would
// overflow the cap, and an oversized line into an empty file is written
// as is rather than rotating forever.
func (f *EventsFile) append(line []byte) error {
	file, err := f.open()
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if info.Size() > 0 && info.Size()+int64(len(line)) > f.max {
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Rename(f.path, f.path+rotatedSuffix); err != nil {
			return fmt.Errorf("rotate: %w", err)
		}
		if file, err = f.open(); err != nil {
			return err
		}
	}
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// rotateBetweenOpensHook runs between ReadEvents' two opens, so a test can
// deterministically rotate the files in the window the open order guards
// (issue #4853). No-op in production.
var rotateBetweenOpensHook = func() {}

// ReadEvents returns the events in gitDirPath's Events file, oldest first: the
// rotated generation, then the live file. A missing file is not an error, and
// a line that fails to decode (a torn write, a corrupt key) is skipped.
//
// It opens the live file before `.1`: a rotation between the two opens renames
// the file already held, so `.1` then aliases it and is dropped, rather than the
// rotated generation going unread. Two rotations between the opens (~64 MB in
// microseconds) would still misorder or miss a generation, a gap the Dashboard
// shares.
func ReadEvents(gitDirPath string) ([]Event, error) {
	livePath := filepath.Join(gitDirPath, eventsFileName)
	live, err := openIfExists(livePath)
	if err != nil {
		return nil, err
	}
	defer closeIfOpen(live)
	rotateBetweenOpensHook()
	older, err := openIfExists(livePath + rotatedSuffix)
	if err != nil {
		return nil, err
	}
	older = dropRotatedAlias(older, live)
	defer closeIfOpen(older)

	var events []Event
	for _, f := range []*os.File{older, live} {
		if f == nil {
			continue
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, err
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			var ev Event
			if json.Unmarshal(line, &ev) == nil {
				events = append(events, ev)
			}
		}
	}
	return events, nil
}

// openIfExists returns a nil file, not an error, for a missing path.
func openIfExists(path string) (*os.File, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}

// closeIfOpen and dropRotatedAlias mirror their dashboard/tail.go namesakes (a
// separate module, so copied).
func closeIfOpen(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

// dropRotatedAlias closes and returns nil for older when it is the same file
// as live: a rotation between the two opens renamed the file live already
// holds.
func dropRotatedAlias(older, live *os.File) *os.File {
	if older == nil || live == nil {
		return older
	}
	oi, oerr := older.Stat()
	ci, cerr := live.Stat()
	if oerr == nil && cerr == nil && os.SameFile(oi, ci) {
		closeIfOpen(older)
		return nil
	}
	return older
}
