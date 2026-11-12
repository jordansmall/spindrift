package dispatch

import (
	"os"
	"sync"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// decodedLogs memoizes the Pass logs a Dispatch decodes on the host, so the
// readers that follow one attempt (classify, then each usage read) share one
// decode instead of each re-reading the log through the Driver's decoder.
//
// An entry is keyed by file identity, not path: a retry rotates log X to X.1
// and writes a new X (#561), so the same path names different files over a
// run. A rename keeps the inode, so X.1 still hits the entry decoded as X.
type decodedLogs struct {
	mu      sync.Mutex
	entries []decodedEntry
}

type decodedEntry struct {
	info os.FileInfo
	log  driverkit.PassLog
}

func (e decodedEntry) matches(info os.FileInfo) bool {
	return os.SameFile(e.info, info) && e.info.Size() == info.Size() && e.info.ModTime().Equal(info.ModTime())
}

// decoded returns the Pass log at path, decoded once while the file is
// unchanged. A log that cannot be stat'd (e.g. missing, which may be written
// next) is decoded uncached. The mutex is held across Decode on purpose: a
// concurrent caller for the same log then hits the first caller's entry rather
// than decoding it twice.
func (d *Dispatch) decoded(path string) (driverkit.PassLog, error) {
	d.decodedLogs.mu.Lock()
	defer d.decodedLogs.mu.Unlock()

	before, err := os.Stat(path)
	if err != nil {
		return d.driver.Decode(path)
	}
	for _, e := range d.decodedLogs.entries {
		if e.matches(before) {
			return e.log, nil
		}
	}
	pl, err := d.driver.Decode(path)
	if err != nil {
		return pl, err
	}
	// A log still being appended must not be cached stale: keep the decode only
	// if the file did not change under it.
	if after, statErr := os.Stat(path); statErr == nil && (decodedEntry{info: before}).matches(after) {
		d.decodedLogs.entries = append(d.decodedLogs.entries, decodedEntry{info: before, log: pl})
	}
	return pl, nil
}

// classify is Driver.ClassifyTransient over the shared decode of the log at
// path; a decode error is the classify error.
func (d *Dispatch) classify(path string) (driver.Classification, error) {
	pl, err := d.decoded(path)
	if err != nil {
		return driver.Classification{}, err
	}
	return pl.ClassifyTransient()
}
