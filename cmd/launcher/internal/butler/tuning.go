package butler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/recordsdigest"
)

// withStore runs fn against the Dispatch Records store, creating the store if
// it does not exist yet.
func (p Policy) withStore(fn func(*dispatchrecord.Store) error) error {
	if p.RecordsRoot == "" {
		return errNoRecordsRoot
	}
	store, err := dispatchrecord.Open(p.RecordsRoot)
	if err != nil {
		return fmt.Errorf("open dispatch records store: %w", err)
	}
	defer store.Close()
	return fn(store)
}

// withExistingStore is withStore except a store that does not exist yet is
// never opened, since dispatchrecord.Open would create it, and fn does not run.
func (p Policy) withExistingStore(fn func(*dispatchrecord.Store) error) error {
	if p.RecordsRoot == "" {
		return errNoRecordsRoot
	}
	if _, err := os.Stat(hostpaths.DispatchRecordsDB(p.RecordsRoot)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat dispatch records store: %w", err)
	}
	return p.withStore(fn)
}

var errNoRecordsRoot = errors.New("no dispatch records root configured")

// loadRecords reads the Dispatch Records store a records-scoped Chore sweeps
// (ADR 0062). A store that does not exist yet is no Records, not an error.
func (p Policy) loadRecords() (records []dispatchrecord.Record, err error) {
	if p.RecordsRoot == "" {
		return nil, nil
	}
	err = p.withExistingStore(func(s *dispatchrecord.Store) error {
		var rerr error
		if records, rerr = s.Records(); rerr != nil {
			return fmt.Errorf("read dispatch records: %w", rerr)
		}
		return nil
	})
	return records, err
}

// storeSnapshot writes a records Chore sweep's digest to the Dispatch Records
// store, creating it if this is the first write. The Ledger carries only the
// ref (ADR 0062, ADR 0070): the digest itself is never committed or pushed.
func (p Policy) storeSnapshot(snap dispatchrecord.Snapshot) error {
	return p.withStore(func(s *dispatchrecord.Store) error {
		if err := s.PutSnapshot(snap); err != nil {
			return fmt.Errorf("store %s snapshot: %w", snap.Chore, err)
		}
		return nil
	})
}

// loadSnapshot reads back the snapshot stored under the Chore and recordID; ok
// is false when the store, or the row, does not exist.
func (p Policy) loadSnapshot(chore, recordID string) (snap dispatchrecord.Snapshot, ok bool, err error) {
	err = p.withExistingStore(func(s *dispatchrecord.Store) error {
		var lerr error
		if snap, ok, lerr = s.Snapshot(chore, recordID); lerr != nil {
			return fmt.Errorf("load %s snapshot: %w", chore, lerr)
		}
		return nil
	})
	if err != nil {
		return snap, false, err
	}
	return snap, ok, nil
}

// tuningSnapshot is a records-scoped sweep's digest. chore and ref.Sweep key
// the stored row; ref goes on the Done commit; rendered is the text of that
// row, filled by load at settle; digest is the Strategy whose hooks judge the
// findings; policy and tree reach the store, the sample floor and the scanned
// HEAD's tracked files.
type tuningSnapshot struct {
	chore    string
	ref      ledger.Snapshot
	rendered string
	digest   recordsdigest.Strategy
	policy   Policy
	tree     Tree
}

// digestLimits is what the Digest's Render is handed and its Validate judges
// under, so both see the same sample floor.
func (p Policy) digestLimits() recordsdigest.Limits {
	return recordsdigest.Limits{MinSample: p.TuningMinSample, MaxBytes: p.TuningDigestBytes}
}

// load fills rendered from the stored snapshot rather than the digest the Box
// was handed, so validation holds the Box to what the store says it was served.
// A missing row, or one whose text no longer hashes to ref.SHA256, is an error:
// the settle fails and the claim stands.
func (t *tuningSnapshot) load() error {
	snap, ok, err := t.policy.loadSnapshot(t.chore, t.ref.Sweep)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s snapshot %s is missing from the dispatch records store", t.chore, t.ref.Sweep)
	}
	sum := sha256.Sum256([]byte(snap.Rendered))
	if got := hex.EncodeToString(sum[:]); snap.SHA256 != t.ref.SHA256 || got != t.ref.SHA256 {
		return fmt.Errorf("%s snapshot %s does not match its ledger ref %s", t.chore, t.ref.Sweep, t.ref.SHA256)
	}
	t.rendered = snap.Rendered
	return nil
}
