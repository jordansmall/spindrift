package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/seambundle"
)

// recoverAttempts is queue-mode recover's host-side record of failed landings
// for one issue's outbox bundle (issue #4655). A different Bundle identity is a
// new Box run and starts the count over.
type recoverAttempts struct {
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
	Bundle string    `json:"bundle"`
	// GaveUp records that the give-up comment was posted, so a failed post is
	// retried by a later pass and a landed one never repeats.
	GaveUp bool `json:"gave_up"`
	// LastError is the last failed attempt's cause, so a give-up posted on a
	// later pass with no settle can still name it.
	LastError string `json:"last_error,omitempty"`
	// Set only by loadRecoverAttempts, and where save writes: a literal
	// recoverAttempts{} cannot be saved.
	path string
}

func recoverAttemptsPath(pwd, num string) string {
	return filepath.Join(dispatch.HostLogDirFor(pwd), "issue-"+num+".recover.json")
}

func recoverBundleID(pwd, num string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dispatch.OutboxDirFor(pwd, num), seambundle.FileName))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// loadRecoverAttempts treats a missing, unreadable, or corrupt record, or one
// for another bundle, as no attempts yet, so a damaged file can only cost a
// retry, never wedge the issue.
func loadRecoverAttempts(pwd, num, bundle string) recoverAttempts {
	path := recoverAttemptsPath(pwd, num)
	fresh := recoverAttempts{Bundle: bundle, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	var rec recoverAttempts
	if json.Unmarshal(data, &rec) != nil || rec.Bundle != bundle {
		return fresh
	}
	rec.path = path
	return rec
}

func (r recoverAttempts) save() error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("recover: save attempt record: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		return fmt.Errorf("recover: save attempt record: %w", err)
	}
	return nil
}

// maxCauseBytes bounds a stored cause: it lands verbatim in a public issue
// comment and a one-line stdout note.
const maxCauseBytes = 500

// normalizeCause collapses s to one line and caps it at maxCauseBytes without
// cutting a rune.
func normalizeCause(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxCauseBytes {
		return s
	}
	cut := maxCauseBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// giveUp posts the give-up comment and sets GaveUp. A failed post is logged and
// leaves GaveUp false, so a later pass retries. It does not save; the caller does.
func (r *recoverAttempts) giveUp(it forge.IssueTracker, num string, stderr io.Writer) {
	cause := ": the outbox bundle relay or draft PR creation kept failing"
	if r.LastError != "" {
		cause = "; the last one failed with: " + r.LastError
	}
	body := fmt.Sprintf("Auto-recover gave up on this issue after %d attempts%s. It will not retry this bundle again; fix the cause, then run `spindrift recover %s`.", r.Count, cause, num)
	if err := it.Comment(num, body); err != nil {
		fmt.Fprintf(stderr, "    ?? #%s: comment: %v\n", num, err)
		return
	}
	r.GaveUp = true
}
