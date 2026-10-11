package butler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
	"spindrift.dev/launcher/internal/tuning"
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

// storeSnapshot writes a tuning sweep's digest to the Dispatch Records store,
// creating it if this is the first write. The Ledger carries only the ref
// (ADR 0062): the digest itself is never committed or pushed.
func (p Policy) storeSnapshot(snap dispatchrecord.TuningSnapshot) error {
	return p.withStore(func(s *dispatchrecord.Store) error {
		if err := s.PutTuningSnapshot(snap); err != nil {
			return fmt.Errorf("store tuning snapshot: %w", err)
		}
		return nil
	})
}

// loadSnapshot reads back the snapshot stored under recordID; ok is false when
// the store, or the row, does not exist.
func (p Policy) loadSnapshot(recordID string) (snap dispatchrecord.TuningSnapshot, ok bool, err error) {
	err = p.withExistingStore(func(s *dispatchrecord.Store) error {
		var lerr error
		if snap, ok, lerr = s.TuningSnapshot(recordID); lerr != nil {
			return fmt.Errorf("load tuning snapshot: %w", lerr)
		}
		return nil
	})
	if err != nil {
		return snap, false, err
	}
	return snap, ok, nil
}

// tuningSnapshot is a records-scoped sweep's digest. ref goes on the Done
// commit and names the stored row; anchors are every anchor that row offers a
// finding to cite and rows its aggregate rows, both filled by load at settle;
// policy and tree reach the store, the sample floor and the scanned HEAD's
// tracked files.
type tuningSnapshot struct {
	ref     ledger.Snapshot
	anchors map[string]bool
	rows    map[string]tuning.Row
	policy  Policy
	tree    Tree
}

// load fills rows and anchors from the stored snapshot rather than the digest
// the Box was handed, so validation holds the Box to what the store says it was
// served. A missing row, or one whose text no longer hashes to ref.SHA256, is
// an error: the settle fails and the claim stands.
func (t *tuningSnapshot) load() error {
	snap, ok, err := t.policy.loadSnapshot(t.ref.Sweep)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tuning snapshot %s is missing from the dispatch records store", t.ref.Sweep)
	}
	sum := sha256.Sum256([]byte(snap.Rendered))
	if got := hex.EncodeToString(sum[:]); snap.SHA256 != t.ref.SHA256 || got != t.ref.SHA256 {
		return fmt.Errorf("tuning snapshot %s does not match its ledger ref %s", t.ref.Sweep, t.ref.SHA256)
	}
	t.rows = tuning.Rows(snap.Rendered)
	t.anchors = tuning.Anchors(snap.Rendered)
	return nil
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
		if !t.anchors[c] {
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
	if in.Class != evidenceGapClass && !slices.ContainsFunc(in.Cites, func(c string) bool { return t.rows[c].N >= t.policy.TuningMinSample }) {
		return dropThinEvidence, nil
	}
	return "", nil
}

// tuningPatternTerms keys a tuning finding on the pattern it names, one
// class:file:metric per file, not on the code site (ADR 0062 §6): two patterns
// on one prompt file stay distinct. It feeds only settle's dedup; f.DedupTerms
// stays the files, which butlerBacklink still reads.
func tuningPatternTerms(f settle.Finding) []string {
	var terms []string
	for _, file := range butlerFiles(f.DedupTerms) {
		terms = append(terms, f.Class+":"+file+":"+f.Metric)
	}
	return terms
}
