package butler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostpaths"
)

// loadRecords reads the Dispatch Records store a records-scoped Chore sweeps
// (ADR 0062). A store that does not exist yet is no Records, not an error --
// and is never opened, since dispatchrecord.Open would create it.
func (p Policy) loadRecords() ([]dispatchrecord.Record, error) {
	if p.RecordsRoot == "" {
		return nil, nil
	}
	if _, err := os.Stat(hostpaths.DispatchRecordsDB(p.RecordsRoot)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat dispatch records store: %w", err)
	}
	store, err := dispatchrecord.Open(p.RecordsRoot)
	if err != nil {
		return nil, fmt.Errorf("open dispatch records store: %w", err)
	}
	defer store.Close()
	records, err := store.Records()
	if err != nil {
		return nil, fmt.Errorf("read dispatch records: %w", err)
	}
	return records, nil
}
