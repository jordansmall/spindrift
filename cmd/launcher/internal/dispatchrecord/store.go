package dispatchrecord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

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
	-- The sentinel (forceReparseSize in Go) makes statSame false on that re-parse, so do not ship this
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
	// v6: unstamped fix and conflict-resolve logs join an inferred Record by
	// time (issue #4782), so a pass names the log it was read from, and a file
	// remembers its segment so a moved provisional one can be cleared. Only an
	// inferred Record's segment is its claim time; a stamped row gets 0.
	`ALTER TABLE passes ADD COLUMN log TEXT NOT NULL DEFAULT ''; -- source log base name; empty when migrated
	ALTER TABLE ingested_files ADD COLUMN log_start INTEGER NOT NULL DEFAULT 0;
	UPDATE ingested_files SET log_start = COALESCE((SELECT claim_time FROM records
		WHERE records.record_id = ingested_files.record_id AND records.attribution = 'inferred'), 0);
	-- Rows from the older schema were parsed under older rules, so re-read them (mtime -1 never matches a real
	-- file, so each row re-parses).
	UPDATE ingested_files SET mtime_ns = -1;`,
}

// Store holds the per-root Dispatch Records. A Record outlives the logs it was
// inferred from (ADR 0061), so deleting a log never removes its Record. The
// only Records the store discards are ones its own still-present log has since
// replaced with a different ID: a provisional (mtime-derived) one, one whose
// stat-identical log re-parses differently under Reingest, one whose
// timestamped log re-parses to the same start under a new ID, or an orphan
// satellite's own Record once a Dispatch of its key starts before it. Accepted
// residual: deleting a provisional log and reusing its path with no Ingest in
// between reads as the same Dispatch growing, so that earlier Record, which has
// no real claim time, is replaced.
type Store struct {
	db   *sql.DB
	root string
}

// forceReparseSize never matches a real file's size, so the row's stat check
// fails and the log re-parses.
const forceReparseSize int64 = -1

// segmentTables are the per-segment child tables upsert clears alongside a Record.
var segmentTables = []string{"passes", "prompt_hashes"}

// busyTimeoutMs is how long a connection waits on another process's write lock
// before failing.
const busyTimeoutMs = 5000

// walRetryInterval paces enableWAL's retries while another connection holds the
// database during creation.
const walRetryInterval = 5 * time.Millisecond

// primaryCodeMask reduces an extended SQLite result code to its primary code
// (SQLITE_BUSY_RECOVERY and friends all carry SQLITE_BUSY in the low byte).
const primaryCodeMask = 0xff

// Open opens (creating and migrating if needed) the store under root.
func Open(root string) (*Store, error) {
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// _txlock=immediate takes the write lock at BEGIN, where busy_timeout
	// applies. A deferred transaction that reads and then upgrades fails at once
	// with SQLITE_BUSY when another process committed in between (WAL).
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_txlock=immediate", path, busyTimeoutMs))
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
	if err := s.enableWAL(); err != nil {
		return err
	}
	for {
		done, err := s.migrateOnce()
		if err != nil || done {
			return err
		}
	}
}

// enableWAL switches a fresh database to WAL. SQLite reports SQLITE_BUSY from
// that switch without consulting busy_timeout when another connection is
// creating the same database, so retry until busyTimeoutMs elapses. Once any
// connection has won, the mode persists and the pragma is a no-op.
func (s *Store) enableWAL() error {
	deadline := time.Now().Add(busyTimeoutMs * time.Millisecond)
	for {
		var mode string
		err := s.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode)
		var se *sqlite.Error
		if err == nil || !errors.As(err, &se) || se.Code()&primaryCodeMask != sqlite3.SQLITE_BUSY || time.Now().After(deadline) {
			return err
		}
		time.Sleep(walRetryInterval)
	}
}

// migrateOnce applies the next pending migration, reporting done when none is
// left. The version is read inside the write transaction, so a concurrent Open
// that already applied it is seen rather than re-run.
func (s *Store) migrateOnce() (done bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return false, err
	}
	if v > len(migrations) {
		return false, fmt.Errorf("dispatchrecord: database schema v%d is newer than this binary (v%d)", v, len(migrations))
	}
	if v == len(migrations) {
		return true, nil
	}
	// user_version cannot be bound as a parameter.
	if _, err := tx.Exec(migrations[v] + fmt.Sprintf("; PRAGMA user_version = %d", v+1)); err != nil {
		return false, fmt.Errorf("dispatchrecord: migrate to v%d: %w", v+1, err)
	}
	return false, tx.Commit()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Ingest parses every pass log under the root's log directory that is new or
// changed since it was last ingested and upserts its Record: a chain log's
// inferred one, or any stamped log's own (issue #4783). It returns how
// many distinct files it parsed, counting satellites re-parsed to re-window;
// a file whose path, size, and mtime match its ingested_files row is skipped
// without being opened. Primary logs go first, so an unstamped satellite log
// (fix pass, conflict resolve) finds the Dispatch it started under. Rows for
// logs no longer on disk, including every row when the whole log directory is
// gone, are forgotten (their Records are kept), so a reused path starts fresh.
// A log it cannot read is skipped and reported in the returned error, and
// retried next ingest.
func (s *Store) Ingest() (parsed int, err error) { return s.ingest(false, "") }

// IngestChain is Ingest restricted to the chain key of the log named logName:
// that key's primary log, rotations, and satellites. Other keys' logs are never
// opened and their ingested_files rows are left alone. A name logKey cannot
// classify ingests everything.
func (s *Store) IngestChain(logName string) (parsed int, err error) {
	key, _ := logKey(logName)
	return s.ingest(false, key)
}

// Reingest is Ingest without the skip: it re-parses every pass log still on
// disk, so a parser fix repairs whatever history remains. Records whose logs
// are gone are untouched.
func (s *Store) Reingest() (parsed int, err error) { return s.ingest(true, "") }

// ingest walks the log directory; a non-empty chainKey restricts it to the logs
// classified under that chain key.
func (s *Store) ingest(force bool, chainKey string) (parsed int, err error) {
	dir := hostpaths.LogDir(s.root)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	seen := []string{}
	type satelliteLog struct{ path, key string }
	var primaries []string
	var satellites []satelliteLog
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !PassLogName(e.Name()) {
			continue
		}
		key, satellite := logKey(e.Name())
		if chainKey != "" && key != chainKey {
			continue
		}
		path := filepath.Join(dir, e.Name())
		seen = append(seen, path)
		if satellite {
			satellites = append(satellites, satelliteLog{path, key})
		} else {
			primaries = append(primaries, path)
		}
	}
	// Forget first so a deleted log's row never reads as a live satellite to
	// detachSatellites.
	if err := s.forgetMissing(dir, chainKey, seen); err != nil {
		return 0, err
	}
	parsedPaths := map[string]bool{}
	// Per-file read failures are collected so one bad log never starves the
	// logs after it; a satellite revisited in a later round reports once.
	var unreadable []error
	failed := map[string]bool{}
	visit := func(path string, satellite, reparse bool) (string, error) {
		did, shifted, err := s.ingestFile(path, satellite, reparse)
		if did {
			parsedPaths[path] = true
		}
		var re *readError
		if errors.As(err, &re) {
			if !failed[path] {
				failed[path] = true
				unreadable = append(unreadable, re.err)
			}
			return "", nil
		}
		return shifted, err
	}
	// Primaries first, so a satellite finds the Dispatch it joins.
	// shifted key -> the path of its last shifter, which already saw the key's
	// final Record set.
	pending := map[string]string{}
	for _, path := range primaries {
		shifted, err := visit(path, false, force)
		if err != nil {
			return len(parsedPaths), err
		}
		if shifted != "" {
			pending[shifted] = path
		}
	}
	// A new or dropped Record moves the window of every satellite of its key, so
	// those re-window until a round shifts nothing; a key's last shifter already
	// saw its final Record set, so it is not revisited.
	// Rounds settle well within this bound; it only guards against a shift
	// cycle looping forever.
	maxRounds := 2*len(satellites) + 2
	for round := 0; round == 0 || len(pending) > 0; round++ {
		if round >= maxRounds {
			return len(parsedPaths), fmt.Errorf("dispatchrecord: satellite re-windowing did not settle after %d rounds", maxRounds)
		}
		first := round == 0
		next := map[string]string{}
		for _, sat := range satellites {
			last, shifting := pending[sat.key]
			if !first && (!shifting || last == sat.path) {
				continue
			}
			shifted, err := visit(sat.path, true, force || shifting)
			if err != nil {
				return len(parsedPaths), err
			}
			if shifted != "" {
				next[shifted] = sat.path
			}
		}
		pending = next
	}
	return len(parsedPaths), errors.Join(unreadable...)
}

// readError marks a failure to read one log, which ingest skips past; any other
// ingestFile error is a store failure and aborts.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// ingestFile parses and upserts one log unless it is unchanged, and reports
// whether it parsed it and the dispatch key whose satellite windows it shifted
// (see upsert).
func (s *Store) ingestFile(path string, satellite, reparse bool) (bool, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, "", nil
		}
		return false, "", &readError{err}
	}
	if !reparse {
		fresh, err := s.isIngested(path, info)
		if err != nil || fresh {
			return false, "", err
		}
	}
	var recp *Record
	rec, provisional, segment, err := parseLog(path)
	switch {
	case err == nil:
		recp = &rec
	case errors.Is(err, ErrEmptyLog), errors.Is(err, ErrUnstamped):
		// Remember the file so it is not reopened until it changes.
	default:
		return false, "", &readError{fmt.Errorf("dispatchrecord: %s: %w", path, err)}
	}
	shifted, err := s.upsert(path, info, recp, segment, provisional, satellite)
	if err != nil {
		return false, "", err
	}
	return true, shifted, nil
}

// rotatedPrimary matches a primary log's "<path>.N" rotation, which ChainKey
// does not classify.
var rotatedPrimary = regexp.MustCompile(`^(issue-.+\.log)\.\d+((?:\.prior-run\.\d+)?)$`)

// logKey is ChainKey extended to a primary's rotated attempts, which belong to
// the key of the log they rotated from.
func logKey(name string) (key string, satellite bool) {
	key, satellite, ok := ChainKey(name)
	if !ok {
		key, satellite, _ = ChainKey(rotatedPrimary.ReplaceAllString(name, "$1$2"))
	}
	return key, satellite
}

// forgetMissing drops the ingested_files rows for paths not in seen, leaving
// Records and passes alone. A non-empty chainKey limits it to the rows of
// dir's logs under that chain key.
func (s *Store) forgetMissing(dir, chainKey string, seen []string) error {
	if chainKey != "" {
		return s.forgetMissingChain(dir, chainKey, seen)
	}
	keep, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM ingested_files WHERE path NOT IN (SELECT value FROM json_each(?))", string(keep))
	return err
}

// forgetMissingChain is forgetMissing for one chain key: it drops the rows of
// dir's logs under chainKey that are not in seen.
func (s *Store) forgetMissingChain(dir, chainKey string, seen []string) error {
	rows, err := s.db.Query("SELECT path FROM ingested_files")
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		if key, _ := logKey(filepath.Base(path)); filepath.Dir(path) == dir && key == chainKey && !slices.Contains(seen, path) {
			stale = append(stale, path)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	drop, err := json.Marshal(stale)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("DELETE FROM ingested_files WHERE path IN (SELECT value FROM json_each(?))", string(drop))
	return err
}

// The window rule placing a satellite log on a Dispatch: the latest claim at or
// before the log's start, ties by record_id. Shared by the join and the
// deleted-peer re-window so they cannot drift. start and exclude are SQL
// expressions; the dispatch key binds first.
func windowQuery(start, exclude string) string {
	return `SELECT record_id FROM records WHERE dispatch_key = ? AND claim_time <= ` + start +
		` AND ` + exclude + ` ORDER BY claim_time DESC, record_id LIMIT 1`
}

// passColumns is shared by the passes INSERT and SELECT so they cannot drift.
const passColumns = `record_id, log_start, log, ordinal, role, models, usd, input_tokens, output_tokens,
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
// ingested_files row is written, with an empty record_id. An unstamped
// satellite joins the Record of its key with the greatest claim time not after
// its own start, other than a Record it owns, and only becomes a Record of its
// own when no other Dispatch of that key started by then. It returns the
// Record's dispatch key when it inserted a Record or replaced a stale one,
// which moves the window of every satellite of that key; otherwise "".
func (s *Store) upsert(path string, info fs.FileInfo, rec *Record, segment time.Time, provisional, satellite bool) (shifted string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var oldID string
	var oldProvisional bool
	var oldSize, oldStart, oldMtime int64
	err = tx.QueryRow("SELECT record_id, provisional, size, log_start, mtime_ns FROM ingested_files WHERE path = ?", path).Scan(&oldID, &oldProvisional, &oldSize, &oldStart, &oldMtime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	newID := ""
	var logStart int64
	var sameFile, joined bool
	if rec != nil {
		newID = rec.ID
		logStart = segment.UnixMilli()
		statSame := oldSize == info.Size() && oldMtime == info.ModTime().UnixNano()
		// The stored row describes this same file's earlier read: a provisional
		// start moves as the log grows, a timestamped one never does.
		sameFile = oldID != "" && (oldProvisional || statSame || oldStart == logStart)
		if satellite && rec.Attribution == AttributionInferred && !sameFile {
			// The stored row, if any, is another file's that held this path, so its
			// segment stays put. Adopt any same-key segment starting in the same
			// millisecond as this file's earlier read (a renamed log's), the
			// coincident-start collision class accepted elsewhere.
			oldID, oldStart, oldProvisional = "", 0, false
			for _, table := range segmentTables {
				err := tx.QueryRow(
					`SELECT record_id FROM `+table+` WHERE log_start = ? AND record_id IN (SELECT record_id FROM records WHERE dispatch_key = ?)
						 ORDER BY record_id LIMIT 1`, logStart, rec.DispatchKey).Scan(&oldID)
				if err == nil {
					oldStart, sameFile = logStart, true
					break
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return "", err
				}
			}
		}
		// A satellite that joined a Record does not own it; an orphan
		// satellite's own Record starts at the file's start.
		if satellite && rec.Attribution == AttributionInferred && oldID != "" {
			var oldClaim int64
			err := tx.QueryRow("SELECT claim_time FROM records WHERE record_id = ?", oldID).Scan(&oldClaim)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			joined = err == nil && oldClaim != oldStart
		}
		writesRecord := true
		if satellite && rec.Attribution == AttributionInferred {
			// Never its own Record: an orphan re-parsed would otherwise capture itself
			// and never move onto a Dispatch that has since appeared.
			owned := rec.ID
			if oldID != "" && !joined {
				owned = oldID
			}
			err := tx.QueryRow(
				windowQuery("?", "record_id NOT IN (?, ?)"),
				rec.DispatchKey, logStart, rec.ID, owned).Scan(&newID)
			switch {
			case err == nil:
				writesRecord = false
			case errors.Is(err, sql.ErrNoRows):
				newID = rec.ID
			default:
				return "", err
			}
		}
		for _, table := range segmentTables {
			if oldID == newID || sameFile {
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ? AND log_start = ?", oldID, oldStart); err != nil {
					return "", err
				}
			}
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ? AND log_start = ?", newID, logStart); err != nil {
				return "", err
			}
		}
		for role, hash := range rec.PromptHashes {
			if _, err := tx.Exec(
				`INSERT INTO prompt_hashes (record_id, log_start, role, hash) VALUES (?, ?, ?, ?)`,
				newID, logStart, role, hash); err != nil {
				return "", err
			}
		}
		if writesRecord {
			var count int
			if err := tx.QueryRow("SELECT COUNT(*) FROM records WHERE record_id = ?", rec.ID).Scan(&count); err != nil {
				return "", err
			}
			if count == 0 {
				shifted = rec.DispatchKey
			}
			roleModels, err := marshalMap(rec.RoleModels)
			if err != nil {
				return "", err
			}
			knobs, err := marshalMap(rec.Knobs)
			if err != nil {
				return "", err
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
				return "", err
			}
		}
		for _, p := range rec.Passes {
			if _, err := tx.Exec(
				`INSERT INTO passes (`+passColumns+`)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				newID, logStart, p.Log, p.Ordinal, p.Role, strings.Join(p.Models, ","), p.USD, p.InputTokens, p.OutputTokens,
				p.CacheReadInputTokens, p.CacheCreationInputTokens, p.APICalls, p.Turns, p.DurationMs,
				p.APIDurationMs, p.Verdict, p.VerdictText, p.Dispositions); err != nil {
				return "", err
			}
		}
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO ingested_files (path, size, mtime_ns, record_id, provisional, log_start) VALUES (?, ?, ?, ?, ?, ?)`,
		path, info.Size(), info.ModTime().UnixNano(), newID, provisional, logStart); err != nil {
		return "", err
	}
	// A provisional ID is the same Dispatch under a stale mtime-derived ID; a
	// changed timestamp-derived ID is a new Dispatch reusing the path. A nil rec
	// (a log emptied in place) overwrote oldID with "" above, so whatever that
	// log regrows into is a new Dispatch and the old provisional Record stays.
	// A stat-identical re-parse (only a forced one reaches here) is the same
	// Dispatch too, so an ID change means the parser changed and the old Record
	// is stale.
	if rec != nil && oldID != newID && sameFile && !joined {
		// A satellite joined to the stale Record would keep it alive and, on a
		// claim-time tie, win the window against the replacement. An orphan
		// satellite leaving its own Record unlinks its peers too: they re-window.
		shifted = rec.DispatchKey
		if err := detachSatellites(tx, oldID, path); err != nil {
			return "", err
		}
		var refs int
		if err := tx.QueryRow("SELECT COUNT(*) FROM ingested_files WHERE record_id = ?", oldID).Scan(&refs); err != nil {
			return "", err
		}
		if refs == 0 {
			// Rows still on oldID belong to logs no longer on disk; deleting a
			// log never removes its pass, so each re-windows by its own start
			// like a live satellite, falling back to the replacement.
			// Only a coincident-start key collision is left to the DELETE.
			for _, table := range segmentTables {
				if _, err := tx.Exec("UPDATE OR IGNORE "+table+` SET record_id = COALESCE((`+windowQuery(table+".log_start", "record_id <> ?")+`), ?) WHERE record_id = ?`,
					rec.DispatchKey, oldID, newID, oldID); err != nil {
					return "", err
				}
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ?", oldID); err != nil {
					return "", err
				}
			}
			if _, err := tx.Exec("DELETE FROM records WHERE record_id = ?", oldID); err != nil {
				return "", err
			}
		}
	}
	return shifted, tx.Commit()
}

// Records returns every stored Record with its passes, ordered by claim time
// then ID. A Record's passes run in the order their log segments began, then
// by position within a segment, renumbered 1..n across the segments.
func (s *Store) Records() ([]Record, error) {
	// ReadOnly issues a plain deferred BEGIN: one snapshot across the three
	// queries without the write lock _txlock=immediate gives Begin().
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
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
		r.Root = s.root
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

	prows, err := tx.Query(
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
		if err := prows.Scan(&id, &logStart, &p.Log, &p.Ordinal, &p.Role, &models, &p.USD, &p.InputTokens, &p.OutputTokens,
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
		p.Ordinal = len(out[i].Passes) + 1
		out[i].Passes = append(out[i].Passes, p)
	}
	if err := prows.Err(); err != nil {
		return nil, err
	}
	return out, loadPromptHashes(tx, out, byID)
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
func loadPromptHashes(tx *sql.Tx, recs []Record, byID map[string]int) error {
	rows, err := tx.Query(`SELECT record_id, role, hash FROM prompt_hashes ORDER BY record_id, log_start`)
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

// detachSatellites unlinks the satellite logs joined to recordID other than
// except: their segments go and their ingested_files rows lose the Record and
// the stat match, so each re-parses later this ingest and rejoins by time
// window. forceReparseSize breaks the match.
func detachSatellites(tx *sql.Tx, recordID, except string) error {
	rows, err := tx.Query("SELECT path, log_start FROM ingested_files WHERE record_id = ? AND path <> ?", recordID, except)
	if err != nil {
		return err
	}
	type joined struct {
		path  string
		start int64
	}
	var sats []joined
	for rows.Next() {
		var j joined
		if err := rows.Scan(&j.path, &j.start); err != nil {
			rows.Close()
			return err
		}
		if _, satellite, _ := ChainKey(filepath.Base(j.path)); satellite {
			sats = append(sats, j)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, j := range sats {
		for _, table := range segmentTables {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE record_id = ? AND log_start = ?", recordID, j.start); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("UPDATE ingested_files SET record_id = '', size = ? WHERE path = ?", forceReparseSize, j.path); err != nil {
			return err
		}
	}
	return nil
}
