package settle

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/flock"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
)

// LateMergeWindow bounds how far back LateMerges looks, by Record claim time.
// Without it every run asks the forge about every historical Record whose PR
// never merged, so the cost grows with history. A PR merged after the window
// stays un-upgraded.
const LateMergeWindow = 14 * 24 * time.Hour

// LateMergeSweepInterval is the least gap between throttled sweeps. Each
// issue-less dispatch exit, every daemon pool child included, would otherwise
// sweep, one forge call per candidate Record.
const LateMergeSweepInterval = 15 * time.Minute

// ClaimLateMergeSweep reports whether a throttled sweep may run now, and if so
// stamps now as the last sweep. It is refused within LateMergeSweepInterval of
// the last claim or while another process holds hostpaths.LateMergeSweepLock's
// flock; the stamp is written under that lock, so concurrent claimants never
// both win. A crashed sweep still blocks others for the interval, acceptable
// as back-off. A future stamp (clock stepped back) counts as stale so the
// throttle heals itself. With no log directory nothing is created.
func ClaimLateMergeSweep(root string, now time.Time) (ok bool, err error) {
	if _, err := os.Stat(hostpaths.LogDir(root)); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	path := hostpaths.LateMergeSweepLock(root)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, fmt.Errorf("open late-merge sweep lock %s: %w", path, err)
	}
	// Closing the fd drops the lock; the file stays to hold the stamp.
	defer file.Close()
	if err := flock.TryExclusive(file); err != nil {
		if errors.Is(err, flock.ErrHeld) {
			return false, nil
		}
		return false, fmt.Errorf("flock late-merge sweep lock %s: %w", path, err)
	}
	stamp, err := io.ReadAll(file)
	if err != nil {
		return false, fmt.Errorf("read late-merge sweep lock %s: %w", path, err)
	}
	// An empty file, just created, parses as no stamp: never swept.
	last, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(stamp)))
	if perr == nil && !last.After(now) && now.Sub(last) < LateMergeSweepInterval {
		return false, nil
	}
	if err := file.Truncate(0); err != nil {
		return false, fmt.Errorf("truncate late-merge sweep lock %s: %w", path, err)
	}
	if _, err := file.WriteAt([]byte(now.UTC().Format(time.RFC3339)), 0); err != nil {
		return false, fmt.Errorf("write late-merge sweep lock %s: %w", path, err)
	}
	return true, nil
}

// LateMerges upgrades each Record settled with its PR left open
// (dispatchrecord.ReasonLeavesPROpen) whose PR has since merged, within
// LateMergeWindow; the host never re-settles such a PR otherwise. The upgrade
// is a second dispatch_settled op appended to the primary log, which the
// parser's last-wins rule picks up, so concurrent sweeps appending twice is
// harmless. It returns the upgraded IDs.
//
// A Record with no primary log or claim time is skipped, as is a PR URL that
// is not absolute http(s), since the forge client takes it as an argument. A
// per-Record failure warns and moves on, except a rate limit, which ends the
// sweep. mc, when non-nil, fills the merge commit.
func LateMerges(root string, pr forge.PRForge, mc forge.MergeCommitReader, now time.Time, warn io.Writer) (merged []string, err error) {
	// No log directory means nothing to upgrade; opening the store would
	// create an empty one.
	if _, err := os.Stat(hostpaths.LogDir(root)); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	store, err := dispatchrecord.Open(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err := store.Ingest(); err != nil {
		return nil, err
	}
	recs, err := store.Records()
	if err != nil {
		return nil, err
	}
	var appended []string
	var stopErr error
	for _, r := range recs {
		if r.OutcomeSource != dispatchrecord.OutcomeSourceSettled || r.Outcome != forge.Complete.String() ||
			!dispatchrecord.ReasonLeavesPROpen(r.Reason) || !QueryablePRURL(r.PRURL) {
			continue
		}
		if r.ClaimTime.IsZero() || r.ClaimTime.Before(now.Add(-LateMergeWindow)) {
			continue
		}
		path := primaryLog(root, r)
		if path == "" {
			continue
		}
		state, err := pr.PRState(r.PRURL)
		if errors.Is(err, forge.ErrRateLimit) {
			stopErr = fmt.Errorf("late-merge sweep stopped: %w", err)
			break
		}
		if err != nil {
			fmt.Fprintf(warn, "    ?? %s: could not read state of %s: %v\n", r.ID, r.PRURL, err)
			continue
		}
		if state != forge.PRMerged {
			continue
		}
		ds := hostop.DispatchSettled{
			RecordID: r.ID,
			State:    forge.Complete.String(),
			Reason:   dispatchrecord.ReasonMerged,
			Note:     "merged after settling " + r.Reason,
			PRURL:    r.PRURL,
		}
		ds.MergeCommit = readMergeCommit(mc, r.ID, r.PRURL, warn)
		if !dispatchrecord.AppendSettled(warn, r.ID, path, ds) {
			continue
		}
		merged = append(merged, r.ID)
		appended = append(appended, path)
	}
	ingestErr := stopErr
	for _, path := range appended {
		if _, err := store.IngestChain(filepath.Base(path)); err != nil {
			ingestErr = errors.Join(ingestErr, err)
		}
	}
	return merged, ingestErr
}

// primaryLog returns the path of the log the Record's dispatch_start stamp
// opens, "" when no pass names one that still exists. Satellite logs carry no
// stamp, so the stamp match picks the primary even after it was rotated.
func primaryLog(root string, r dispatchrecord.Record) string {
	for _, p := range r.Passes {
		if p.Log == "" {
			continue
		}
		path := filepath.Join(hostpaths.LogDir(root), p.Log)
		if s, ok := dispatchrecord.ReadStamp(path); ok && s.RecordID == r.ID {
			return path
		}
	}
	return ""
}

// QueryablePRURL reports whether u is an absolute http(s) URL with a host. The
// Record's PR URL can come from text the Box printed, and the forge client
// passes it as an argument, so anything that could read as a flag is refused.
func QueryablePRURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// readMergeCommit reads the commit the merged pr landed as, for the Record's
// revert judgement (issue #4950). Best-effort: a nil reader (a forge that
// cannot report one) or a failed read yields "" and never changes the settle
// outcome.
func readMergeCommit(r forge.MergeCommitReader, who, pr string, warn io.Writer) string {
	if r == nil {
		return ""
	}
	sha, err := r.MergeCommit(pr)
	if err != nil {
		fmt.Fprintf(warn, "    ?? %s: could not read merge commit of %s: %v\n", who, pr, err)
		return ""
	}
	return sha
}
