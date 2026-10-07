package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

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
