// Package recoverrecord is queue-mode recover's host-side record of failed
// landings for one issue's outbox bundle, plus the one rule deciding whether
// that bundle is still eligible for another attempt (issues #4655, #4657). The
// launcher's recover pass and the daemon's scheduler both read it, and the
// daemon cannot import the launcher's package main, so it lives here; it
// imports only leaf packages to keep the daemon's graph small.
package recoverrecord

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/seambundle"
)

// Record is the attempt record for one issue's outbox bundle. A different
// Bundle identity is a new Box run and starts the count over.
type Record struct {
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
	Bundle string    `json:"bundle"`
	// GaveUp records that the give-up comment was posted, so a failed post is
	// retried by a later pass and a landed one never repeats.
	GaveUp bool `json:"gave_up"`
	// LastError is the last failed attempt's cause, so a give-up posted on a
	// later pass with no settle can still name it.
	LastError string `json:"last_error,omitempty"`
	// Set only by Load, and where Save writes: a literal Record{} cannot be
	// saved.
	path string
}

// Path is where num's record lives.
func Path(pwd, num string) string {
	return filepath.Join(hostpaths.LogDir(pwd), "issue-"+num+".recover.json")
}

// BundleID identifies num's current outbox bundle by content hash.
func BundleID(pwd, num string) (string, error) {
	data, err := os.ReadFile(filepath.Join(hostpaths.OutboxDir(pwd, num), seambundle.FileName))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Load treats a missing, unreadable, or corrupt record, or one for another
// bundle, as no attempts yet, so a damaged file can only cost a retry, never
// wedge the issue.
func Load(pwd, num, bundle string) Record {
	path := Path(pwd, num)
	fresh := Record{Bundle: bundle, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	var rec Record
	if json.Unmarshal(data, &rec) != nil || rec.Bundle != bundle {
		return fresh
	}
	rec.path = path
	return rec
}

// Save writes the record to the path Load set.
func (r Record) Save() error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("recover: save attempt record: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		return fmt.Errorf("recover: save attempt record: %w", err)
	}
	return nil
}

// Exhausted reports whether no further recover attempt will run: the give-up
// was posted, or Count reached maxAttempts. A gave-up record stays exhausted
// even if the bound is later raised. An unposted give-up comment is still
// retried (see Due).
func (r Record) Exhausted(maxAttempts int) bool {
	return r.GaveUp || r.Count >= maxAttempts
}

// BackingOff reports whether now still falls inside the wait after the last
// failed attempt: unit scaled by Count.
func (r Record) BackingOff(now time.Time, unit time.Duration) bool {
	wait := retry.LinearBackoff{Unit: unit}.Duration(r.Count)
	return now.Before(r.Last.Add(wait))
}

// Due reports whether a recover pass would act on the bundle now: attempt it,
// or retry a give-up comment that never posted (Count at the bound, GaveUp
// still false). Backoff gates only attempts, not that retry. A record at the
// bound stays Due until its give-up posts, and the child posts it only when the
// bundle still passes its other checks (failed label, ready self-report); a
// record whose issue lost the label or whose self-report is not success stays
// counted indefinitely, which the daemon's upper-bound backoff
// (DAEMON_IDLE_CAP) bounds to about one child per cap.
func (r Record) Due(now time.Time, maxAttempts int, unit time.Duration) bool {
	if r.GaveUp {
		return false
	}
	return r.Count >= maxAttempts || !r.BackingOff(now, unit)
}

// Eligible names the outbox bundles a recover pass would act on now: a bundle
// is present and its record is Due. Each is named by outbox key and BundleID,
// so a re-run that leaves a new bundle under the same key reads as a new item
// (issue #4705). Chore-keyed (butler) outboxes are never recover's work, and
// so is an entry whose key is in inFlight, which a live child holds. The
// result is an upper bound; the recover child re-checks everything else
// (self-report, open PR, claim).
func Eligible(pwd string, maxAttempts int, unit time.Duration, now time.Time, inFlight map[dispatchkey.Key]bool) ([]string, error) {
	entries, err := os.ReadDir(hostpaths.OutboxRoot(pwd))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		key := e.Name()
		if !e.IsDir() || dispatchkey.IsChoreKey(key) || inFlight[dispatchkey.Issue(key)] {
			continue
		}
		id, err := BundleID(pwd, key)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			// Unreadable but present: name it by key alone rather than hide work from the daemon.
			// Once readable it becomes key@id, a new identity that lifts the daemon's gate once more.
			ids = append(ids, key)
			continue
		}
		rec := Load(pwd, key, id)
		if rec.Due(now, maxAttempts, unit) {
			ids = append(ids, key+"@"+id)
		}
	}
	return ids, nil
}
