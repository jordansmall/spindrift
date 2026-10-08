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
	// v3: stamped Records (issue #4783). A Record can now draw passes from
	// several logs, so passes are keyed by the log's segment identity too;
	// existing rows get their Record's claim time, the segment of an inferred
	// log.
	`ALTER TABLE records ADD COLUMN revision TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN driver TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN driver_version TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN role_models TEXT NOT NULL DEFAULT ''; -- JSON object, empty for none
	ALTER TABLE records ADD COLUMN knobs TEXT NOT NULL DEFAULT ''; -- JSON object, empty for none
	CREATE TABLE passes_v2 (
		record_id                   TEXT NOT NULL,
		log_start                   INTEGER NOT NULL, -- unix ms, UTC
		ordinal                     INTEGER NOT NULL,
		role                        TEXT NOT NULL,
		models                      TEXT NOT NULL,
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
		verdict_text                TEXT NOT NULL DEFAULT '',
		dispositions                TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (record_id, log_start, ordinal)
	);
	INSERT INTO passes_v2 (record_id, log_start, ordinal, role, models, usd, input_tokens, output_tokens,
		cache_read_input_tokens, cache_creation_input_tokens, api_calls, turns, duration_ms, api_duration_ms, verdict,
		verdict_text, dispositions)
	SELECT p.record_id, r.claim_time, p.ordinal, p.role, p.models, p.usd, p.input_tokens, p.output_tokens,
		p.cache_read_input_tokens, p.cache_creation_input_tokens, p.api_calls, p.turns, p.duration_ms, p.api_duration_ms, p.verdict,
		p.verdict_text, p.dispositions
	FROM passes p JOIN records r ON r.record_id = p.record_id;
	DROP TABLE passes;
	ALTER TABLE passes_v2 RENAME TO passes;`,
	// v4: Box-reported prompt template hashes (issue #4786). Keyed by log
	// segment like passes, so re-ingesting one changed log replaces only what it
	// reported and a Record reads back the union across its logs.
	`CREATE TABLE prompt_hashes (
		record_id TEXT NOT NULL,
		log_start INTEGER NOT NULL, -- unix ms, UTC
		role      TEXT NOT NULL,
		hash      TEXT NOT NULL,
		PRIMARY KEY (record_id, log_start, role)
	);`,
	// v5: the host's dispatch_settled outcome (issue #4785). outcome_source
	// 'none' is OutcomeSourceNone, the value for every pre-v5 row.
	`ALTER TABLE records ADD COLUMN outcome_source TEXT NOT NULL DEFAULT 'none';
	ALTER TABLE records ADD COLUMN reason TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN note TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';
	ALTER TABLE records ADD COLUMN box_status TEXT NOT NULL DEFAULT '';`,
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

// segmentTables are the per-segment child tables upsert clears alongside a Record.
var segmentTables = []string{"passes", "prompt_hashes"}

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

// Ingest parses every pass log under the root's log directory that is new or
// changed since it was last ingested and upserts its Record: a chain log's
// inferred one, or any stamped log's own (issue #4783). It returns how
// many files it parsed; a file whose path, size, and mtime match its
// ingested_files row is skipped without being opened. Rows for logs no longer
// on disk, including every row when the whole log directory is gone, are
// forgotten (their Records are kept), so a reused path starts fresh.
func (s *Store) Ingest() (parsed int, err error) { return s.ingest(false) }

// Reingest is Ingest without the skip: it re-parses every pass log still on
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
		if !PassLogName(e.Name()) {
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
		rec, provisional, segment, err := parseLog(path)
		switch {
		case err == nil:
			recp = &rec
		case errors.Is(err, ErrNoEvents), errors.Is(err, ErrUnstamped):
			// Remember the file so it is not reopened until it changes.
		default:
			return parsed, fmt.Errorf("dispatchrecord: %s: %w", path, err)
		}
		if err := s.upsert(path, info, recp, segment, provisional); err != nil {
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
const passColumns = `record_id, log_start, ordinal, role, models, usd, input_tokens, output_tokens,
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
func (s *Store) upsert(path string, info fs.FileInfo, rec *Record, segment time.Time, provisional bool) error {
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
		logStart := segment.UnixMilli()
		for _, table := range segmentTables {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ? AND log_start = ?", rec.ID, logStart); err != nil {
				return err
			}
		}
		for role, hash := range rec.PromptHashes {
			if _, err := tx.Exec(
				`INSERT INTO prompt_hashes (record_id, log_start, role, hash) VALUES (?, ?, ?, ?)`,
				rec.ID, logStart, role, hash); err != nil {
				return err
			}
		}
		roleModels, err := marshalMap(rec.RoleModels)
		if err != nil {
			return err
		}
		knobs, err := marshalMap(rec.Knobs)
		if err != nil {
			return err
		}
		// The outcome group is kept unless this log carries the settled outcome:
		// the Record's other logs (fix, conflict-resolve) never see it.
		keep := func(col string) string {
			return col + ` = CASE WHEN excluded.outcome_source = '` + OutcomeSourceSettled +
				`' THEN excluded.` + col + ` ELSE records.` + col + ` END`
		}
		if _, err := tx.Exec(
			`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome,
				revision, driver, driver_version, role_models, knobs,
				outcome_source, reason, note, pr_url, box_status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(record_id) DO UPDATE SET
				kind = excluded.kind, dispatch_key = excluded.dispatch_key, claim_time = excluded.claim_time,
				attribution = excluded.attribution, revision = excluded.revision, driver = excluded.driver,
				driver_version = excluded.driver_version, role_models = excluded.role_models, knobs = excluded.knobs,
				`+strings.Join([]string{keep("outcome"), keep("outcome_source"), keep("reason"), keep("note"),
				keep("pr_url"), keep("box_status")}, ", "),
			rec.ID, rec.Kind, rec.DispatchKey, rec.ClaimTime.UnixMilli(), rec.Attribution, rec.Outcome,
			rec.Revision, rec.Driver, rec.DriverVersion, roleModels, knobs,
			rec.OutcomeSource, rec.Reason, rec.Note, rec.PRURL, rec.BoxStatus); err != nil {
			return err
		}
		for _, p := range rec.Passes {
			if _, err := tx.Exec(
				`INSERT INTO passes (`+passColumns+`)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rec.ID, logStart, p.Ordinal, p.Role, strings.Join(p.Models, ","), p.USD, p.InputTokens, p.OutputTokens,
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
			for _, table := range segmentTables {
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ?", oldID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("DELETE FROM records WHERE record_id = ?", oldID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Records returns every stored Record with its passes, ordered by claim time
// then ID (passes by log segment, then ordinal).
func (s *Store) Records() ([]Record, error) {
	rows, err := s.db.Query(
		`SELECT record_id, kind, dispatch_key, claim_time, attribution, outcome,
			 outcome_source, reason, note, pr_url, box_status,
			 revision, driver, driver_version, role_models, knobs
		 FROM records ORDER BY claim_time, record_id`)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	byID := map[string]int{}
	for rows.Next() {
		var r Record
		var ms int64
		var roleModels, knobs string
		if err := rows.Scan(&r.ID, &r.Kind, &r.DispatchKey, &ms, &r.Attribution, &r.Outcome,
			&r.OutcomeSource, &r.Reason, &r.Note, &r.PRURL, &r.BoxStatus,
			&r.Revision, &r.Driver, &r.DriverVersion, &roleModels, &knobs); err != nil {
			rows.Close()
			return nil, err
		}
		if r.RoleModels, err = unmarshalMap(roleModels); err != nil {
			rows.Close()
			return nil, err
		}
		if r.Knobs, err = unmarshalMap(knobs); err != nil {
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
		 FROM passes ORDER BY record_id, log_start, ordinal`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var id, models string
		var logStart int64
		var p Pass
		if err := prows.Scan(&id, &logStart, &p.Ordinal, &p.Role, &models, &p.USD, &p.InputTokens, &p.OutputTokens,
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
	if err := prows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadPromptHashes(out, byID)
}

// marshalMap stores a nil or empty map as "", the column default.
func marshalMap(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func unmarshalMap(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]string
	err := json.Unmarshal([]byte(s), &m)
	return m, err
}

// loadPromptHashes fills each Record's PromptHashes with the union across its
// logs; ordering by segment lets a later log's hash for a role win.
func (s *Store) loadPromptHashes(recs []Record, byID map[string]int) error {
	rows, err := s.db.Query(`SELECT record_id, role, hash FROM prompt_hashes ORDER BY record_id, log_start`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, role, hash string
		if err := rows.Scan(&id, &role, &hash); err != nil {
			return err
		}
		i, ok := byID[id]
		if !ok {
			return fmt.Errorf("dispatchrecord: prompt_hashes row for unknown record %q", id)
		}
		if recs[i].PromptHashes == nil {
			recs[i].PromptHashes = map[string]string{}
		}
		recs[i].PromptHashes[role] = hash
	}
	return rows.Err()
}
