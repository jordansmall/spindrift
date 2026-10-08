package dispatchrecord

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"spindrift.dev/launcher/internal/hostpaths"
)

// migrations[i] upgrades a database from user_version i to i+1. Append a new
// entry to add a version; never edit a shipped one.
var migrations = []string{
	`CREATE TABLE records (
		record_id    TEXT PRIMARY KEY,
		kind         TEXT NOT NULL,
		dispatch_key TEXT NOT NULL,
		claim_time   INTEGER NOT NULL, -- unix ms, UTC
		attribution  TEXT NOT NULL,
		outcome      TEXT NOT NULL
	);
	CREATE TABLE passes (
		record_id                   TEXT NOT NULL,
		ordinal                     INTEGER NOT NULL,
		role                        TEXT NOT NULL,
		models                      TEXT NOT NULL, -- comma-joined, sorted
		usd                         REAL NOT NULL,
		input_tokens                INTEGER NOT NULL,
		output_tokens               INTEGER NOT NULL,
		cache_read_input_tokens     INTEGER NOT NULL,
		cache_creation_input_tokens INTEGER NOT NULL,
		api_calls                   INTEGER NOT NULL,
		turns                       INTEGER NOT NULL,
		duration_ms                 INTEGER NOT NULL,
		api_duration_ms             INTEGER NOT NULL,
		verdict                     TEXT NOT NULL,
		PRIMARY KEY (record_id, ordinal)
	);
	CREATE TABLE ingested_files (
		path      TEXT PRIMARY KEY,
		size      INTEGER NOT NULL,
		mtime_ns  INTEGER NOT NULL,
		record_id TEXT NOT NULL, -- empty for a log with no events yet
		provisional INTEGER NOT NULL -- 1 when record_id's claim time fell back to mtime
	);`,
	`ALTER TABLE passes ADD COLUMN verdict_text TEXT NOT NULL DEFAULT '';
	ALTER TABLE passes ADD COLUMN dispositions TEXT NOT NULL DEFAULT '';
	-- Forces one re-parse so pre-v2 Records gain their evidence; record_id and
	-- provisional stay, so the re-parse still replaces a provisional Record.
	-- The sentinel makes statSame false on that re-parse, so do not ship this
	-- alongside a change to Record ID derivation: the stale Record would stay.
	UPDATE ingested_files SET size = -1;`,
}

// Store holds the per-root Dispatch Records. A Record outlives the logs it was
// inferred from (ADR 0061), so deleting a log never removes its Record. The
// only Records the store discards are ones its own still-present log has since
// replaced with a different ID: a provisional (mtime-derived) one, or, under
// Reingest, one whose stat-identical log re-parses differently. Accepted
// residual: deleting a provisional log and reusing its path with no Ingest in
// between reads as the same Dispatch growing, so that earlier Record, which has
// no real claim time, is replaced.
type Store struct {
	db   *sql.DB
	root string
}

// busyTimeoutMs is how long a connection waits on another process's write lock
// before failing.
const busyTimeoutMs = 5000

// Open opens (creating and migrating if needed) the store under root.
func Open(root string) (*Store, error) {
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)", path, busyTimeoutMs))
	if err != nil {
		return nil, err
	}
	// One connection: WAL still lets other processes read, and it keeps the
	// per-connection pragmas and transactions trivially consistent.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, root: root}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("dispatchrecord: database schema v%d is newer than this binary (v%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		// user_version cannot be bound as a parameter.
		if _, err := tx.Exec(migrations[v] + fmt.Sprintf("; PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("dispatchrecord: migrate to v%d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Ingest parses every chain log under the root's log directory that is new or
// changed since it was last ingested and upserts its Record. It returns how
// many files it parsed; a file whose path, size, and mtime match its
// ingested_files row is skipped without being opened. Rows for logs no longer
// on disk, including every row when the whole log directory is gone, are
// forgotten (their Records are kept), so a reused path starts fresh.
func (s *Store) Ingest() (parsed int, err error) { return s.ingest(false) }

// Reingest is Ingest without the skip: it re-parses every chain log still on
// disk, so a parser fix repairs whatever history remains. Records whose logs
// are gone are untouched.
func (s *Store) Reingest() (parsed int, err error) { return s.ingest(true) }

func (s *Store) ingest(force bool) (parsed int, err error) {
	dir := hostpaths.LogDir(s.root)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	seen := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := ChainKey(e.Name()); !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		seen = append(seen, path)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return parsed, err
		}
		if !force {
			fresh, err := s.isIngested(path, info)
			if err != nil {
				return parsed, err
			}
			if fresh {
				continue
			}
		}
		var recp *Record
		rec, provisional, err := ParseLog(path)
		switch {
		case err == nil:
			recp = &rec
		case errors.Is(err, ErrNoEvents):
			// Remember the file so it is not reopened until it changes.
		default:
			return parsed, fmt.Errorf("dispatchrecord: %s: %w", path, err)
		}
		if err := s.upsert(path, info, recp, provisional); err != nil {
			return parsed, err
		}
		parsed++
	}
	return parsed, s.forgetMissing(seen)
}

// forgetMissing drops the ingested_files rows for paths not in seen, leaving
// Records and passes alone.
func (s *Store) forgetMissing(seen []string) error {
	keep, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM ingested_files WHERE path NOT IN (SELECT value FROM json_each(?))", string(keep))
	return err
}

// passColumns is shared by the passes INSERT and SELECT so they cannot drift.
const passColumns = `record_id, ordinal, role, models, usd, input_tokens, output_tokens,
	cache_read_input_tokens, cache_creation_input_tokens, api_calls, turns, duration_ms,
	api_duration_ms, verdict, verdict_text, dispositions`

func (s *Store) isIngested(path string, info fs.FileInfo) (bool, error) {
	var size, mtime int64
	err := s.db.QueryRow("SELECT size, mtime_ns FROM ingested_files WHERE path = ?", path).Scan(&size, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return size == info.Size() && mtime == info.ModTime().UnixNano(), nil
}

// upsert records one parsed log. A nil rec is an event-free log: only its
// ingested_files row is written, with an empty record_id.
func (s *Store) upsert(path string, info fs.FileInfo, rec *Record, provisional bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldID string
	var oldProvisional bool
	var oldSize, oldMtime int64
	err = tx.QueryRow("SELECT record_id, provisional, size, mtime_ns FROM ingested_files WHERE path = ?", path).Scan(&oldID, &oldProvisional, &oldSize, &oldMtime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	newID := ""
	if rec != nil {
		newID = rec.ID
		if _, err := tx.Exec("DELETE FROM passes WHERE record_id = ?", rec.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			rec.ID, rec.Kind, rec.DispatchKey, rec.ClaimTime.UnixMilli(), rec.Attribution, rec.Outcome); err != nil {
			return err
		}
		for _, p := range rec.Passes {
			if _, err := tx.Exec(
				`INSERT INTO passes (`+passColumns+`)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rec.ID, p.Ordinal, p.Role, strings.Join(p.Models, ","), p.USD, p.InputTokens, p.OutputTokens,
				p.CacheReadInputTokens, p.CacheCreationInputTokens, p.APICalls, p.Turns, p.DurationMs,
				p.APIDurationMs, p.Verdict, p.VerdictText, p.Dispositions); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO ingested_files (path, size, mtime_ns, record_id, provisional) VALUES (?, ?, ?, ?, ?)`,
		path, info.Size(), info.ModTime().UnixNano(), newID, provisional); err != nil {
		return err
	}
	// A provisional ID is the same Dispatch under a stale mtime-derived ID; a
	// changed timestamp-derived ID is a new Dispatch reusing the path. A nil rec
	// (a log emptied in place) overwrote oldID with "" above, so whatever that
	// log regrows into is a new Dispatch and the old provisional Record stays.
	// A stat-identical re-parse (only a forced one reaches here) is the same
	// Dispatch too, so an ID change means the parser changed and the old Record
	// is stale.
	statSame := oldSize == info.Size() && oldMtime == info.ModTime().UnixNano()
	if rec != nil && oldID != "" && oldID != newID && (oldProvisional || statSame) {
		var refs int
		if err := tx.QueryRow("SELECT COUNT(*) FROM ingested_files WHERE record_id = ?", oldID).Scan(&refs); err != nil {
			return err
		}
		if refs == 0 {
			if _, err := tx.Exec("DELETE FROM passes WHERE record_id = ?", oldID); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM records WHERE record_id = ?", oldID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Records returns every stored Record with its passes, ordered by claim time
// then ID (passes by ordinal).
func (s *Store) Records() ([]Record, error) {
	rows, err := s.db.Query(
		`SELECT record_id, kind, dispatch_key, claim_time, attribution, outcome
		 FROM records ORDER BY claim_time, record_id`)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	byID := map[string]int{}
	for rows.Next() {
		var r Record
		var ms int64
		if err := rows.Scan(&r.ID, &r.Kind, &r.DispatchKey, &ms, &r.Attribution, &r.Outcome); err != nil {
			rows.Close()
			return nil, err
		}
		r.ClaimTime = time.UnixMilli(ms).UTC()
		r.Passes = []Pass{}
		byID[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := s.db.Query(
		`SELECT ` + passColumns + `
		 FROM passes ORDER BY record_id, ordinal`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var id, models string
		var p Pass
		if err := prows.Scan(&id, &p.Ordinal, &p.Role, &models, &p.USD, &p.InputTokens, &p.OutputTokens,
			&p.CacheReadInputTokens, &p.CacheCreationInputTokens, &p.APICalls, &p.Turns, &p.DurationMs,
			&p.APIDurationMs, &p.Verdict, &p.VerdictText, &p.Dispositions); err != nil {
			return nil, err
		}
		p.Models = []string{}
		if models != "" {
			p.Models = strings.Split(models, ",")
		}
		i, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("dispatchrecord: passes row for unknown record %q", id)
		}
		out[i].Passes = append(out[i].Passes, p)
	}
	return out, prows.Err()
}
