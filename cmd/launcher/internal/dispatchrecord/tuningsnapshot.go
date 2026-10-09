package dispatchrecord

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TuningSnapshot is the digest a tuning Chore sweep was served (ADR 0062),
// keyed by the sweep's Record ID. It lives only in this host-private store:
// the Ledger carries just the Record ID and SHA256, because a hosted forge's
// Ledger ref is readable by anyone who can read the repo.
type TuningSnapshot struct {
	RecordID  string
	SHA256    string
	Rendered  string
	CreatedAt time.Time
}

// PutTuningSnapshot stores snap. It fails if a snapshot already exists under
// snap.RecordID: a Record ID is minted once per sweep, so a second write would
// mean two sweeps shared an ID.
func (s *Store) PutTuningSnapshot(snap TuningSnapshot) error {
	_, err := s.db.Exec(
		`INSERT INTO tuning_snapshots (record_id, sha256, rendered, created_at) VALUES (?, ?, ?, ?)`,
		snap.RecordID, snap.SHA256, snap.Rendered, snap.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("dispatchrecord: store tuning snapshot %s: %w", snap.RecordID, err)
	}
	return nil
}

// TuningSnapshot reads the snapshot stored under recordID; ok is false when
// there is none.
func (s *Store) TuningSnapshot(recordID string) (snap TuningSnapshot, ok bool, err error) {
	var ms int64
	err = s.db.QueryRow(
		`SELECT record_id, sha256, rendered, created_at FROM tuning_snapshots WHERE record_id = ?`, recordID).
		Scan(&snap.RecordID, &snap.SHA256, &snap.Rendered, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return TuningSnapshot{}, false, nil
	}
	if err != nil {
		return TuningSnapshot{}, false, fmt.Errorf("dispatchrecord: read tuning snapshot %s: %w", recordID, err)
	}
	snap.CreatedAt = time.UnixMilli(ms).UTC()
	return snap, true, nil
}
