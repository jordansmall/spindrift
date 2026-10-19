package dispatchrecord

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Snapshot is the digest a records Chore sweep was served (ADR 0070), keyed by
// the Chore and the sweep's Record ID. It lives only in this host-private
// store: the Ledger carries just the Record ID and SHA256, because a hosted
// forge's Ledger ref is readable by anyone who can read the repo.
type Snapshot struct {
	Chore     string
	RecordID  string
	SHA256    string
	Rendered  string
	CreatedAt time.Time
}

// PutSnapshot stores snap. It fails if a snapshot already exists under
// (snap.Chore, snap.RecordID): a Record ID is minted once per sweep, so a
// second write would mean two sweeps shared an ID.
func (s *Store) PutSnapshot(snap Snapshot) error {
	_, err := s.db.Exec(
		`INSERT INTO snapshots (chore, record_id, sha256, rendered, created_at) VALUES (?, ?, ?, ?, ?)`,
		snap.Chore, snap.RecordID, snap.SHA256, snap.Rendered, snap.CreatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("dispatchrecord: store %s snapshot %s: %w", snap.Chore, snap.RecordID, err)
	}
	return nil
}

// Snapshot reads the snapshot stored under chore and recordID; ok is false
// when there is none.
func (s *Store) Snapshot(chore, recordID string) (snap Snapshot, ok bool, err error) {
	var ms int64
	err = s.db.QueryRow(
		`SELECT chore, record_id, sha256, rendered, created_at FROM snapshots WHERE chore = ? AND record_id = ?`,
		chore, recordID).
		Scan(&snap.Chore, &snap.RecordID, &snap.SHA256, &snap.Rendered, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("dispatchrecord: read %s snapshot %s: %w", chore, recordID, err)
	}
	snap.CreatedAt = time.UnixMilli(ms).UTC()
	return snap, true, nil
}
