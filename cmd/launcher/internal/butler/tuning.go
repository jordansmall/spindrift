package butler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/tuning"
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

// storeSnapshot writes a tuning sweep's digest to the Dispatch Records store,
// creating it if this is the first write. The Ledger carries only the ref
// (ADR 0062): the digest itself is never committed or pushed.
func (p Policy) storeSnapshot(snap dispatchrecord.TuningSnapshot) error {
	if p.RecordsRoot == "" {
		return errors.New("no dispatch records root configured")
	}
	store, err := dispatchrecord.Open(p.RecordsRoot)
	if err != nil {
		return fmt.Errorf("open dispatch records store: %w", err)
	}
	defer store.Close()
	if err := store.PutTuningSnapshot(snap); err != nil {
		return fmt.Errorf("store tuning snapshot: %w", err)
	}
	return nil
}

// tuningSnapshot is a records-scoped sweep's stored digest: ref goes on the
// Done commit, rows are the aggregate rows of the text the Box was served,
// and minSample is the floor below which a row is thin.
type tuningSnapshot struct {
	ref       ledger.Snapshot
	rows      map[string]tuning.Row
	minSample int
	tree      Tree
}

func newTuningSnapshot(ref ledger.Snapshot, digest string, minSample int, tree Tree) *tuningSnapshot {
	return &tuningSnapshot{ref: ref, rows: tuning.Rows(digest), minSample: minSample, tree: tree}
}

// Drop reasons recorded in the Ledger for a tuning finding settle refused.
const (
	dropUnknownCite   = "unknown-cite"
	dropMissingTarget = "missing-target"
	dropThinEvidence  = "thin-evidence"
)

// evidenceGapClass is exempt from the sample floor: it reports a metric the
// digest cannot yet support, so thin rows are its whole point.
const evidenceGapClass = "evidence-gap"

// validate reports why in must not be filed, or "" when it may. tracked
// lists the scanned HEAD's files lazily, so a sweep whose findings all fail
// on their cites never reads the tree.
func (t *tuningSnapshot) validate(in signalwire.IssueIntent, tracked func() (map[string]bool, error)) (string, error) {
	// A tuning finding must trace back to rows the host served, so one with
	// no cites at all fails like one citing an unknown anchor.
	if len(in.Cites) == 0 {
		return dropUnknownCite, nil
	}
	for _, c := range in.Cites {
		if _, ok := t.rows[c]; !ok {
			return dropUnknownCite, nil
		}
	}
	files := butlerFiles(in.DedupTerms)
	if len(files) == 0 {
		return dropMissingTarget, nil
	}
	set, err := tracked()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if !set[f] {
			return dropMissingTarget, nil
		}
	}
	if in.Class != evidenceGapClass && !slices.ContainsFunc(in.Cites, func(c string) bool { return t.rows[c].N >= t.minSample }) {
		return dropThinEvidence, nil
	}
	return "", nil
}
