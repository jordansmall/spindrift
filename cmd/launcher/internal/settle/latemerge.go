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
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/flock"
	"spindrift.dev/launcher/internal/forge"
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
// stamps now as the last sweep. It is refused when the previous claim is
// within LateMergeSweepInterval of now, or when another process holds the
// flock on hostpaths.LateMergeSweepLock mid-claim; the stamp is written under
// that lock before any sweep starts, so concurrent claimants never both win.
// A sweep that crashes or fails after claiming still blocks others for the
// full interval, which is acceptable as rate-limit back-off. A stamp in the
// future (clock stepped back) counts as stale so the throttle heals itself.
// With no log directory there is nothing to sweep and nothing is created.
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
// (ReasonLeavesPROpen) whose PR has since merged and whose claim falls within
// LateMergeWindow of now. The host never re-settles such a PR, so without this
// a landed change keeps the open-PR reason forever. The upgrade is a second
// dispatch_settled op (reason merged) appended to the Record's primary log,
// where the parser's last-wins rule picks it up; each appended log's chain is
// re-ingested so the Record reads back upgraded. It returns the IDs it
// upgraded.
//
// A Record whose primary log is gone, or with no claim time (an older log
// without a claim stamp), is skipped and stays as stored, and a PR
// closed unmerged is not a landing. A PR URL that is not an absolute http(s)
// URL is skipped unqueried, since the forge client takes it as a command-line
// argument. A forge or append failure on one Record warns to warn and moves
// on, except a rate limit, which ends the sweep with an error wrapping it.
// A sequential re-run is a no-op: an upgraded Record's reason is merged, so it
// is no longer a candidate. Two sweeps running at once can both append the op,
// which is harmless since the parser keeps the last.
// mc, when non-nil, fills the upgraded Record's merge commit; a failed read
// warns and leaves it empty.
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
			!ReasonLeavesPROpen(r.Reason) || !queryablePRURL(r.PRURL) {
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
		ds := claude.DispatchSettled{
			RecordID: r.ID,
			State:    forge.Complete.String(),
			Reason:   ReasonMerged,
			Note:     "merged after settling " + r.Reason,
			PRURL:    r.PRURL,
		}
		ds.MergeCommit = readMergeCommit(mc, r.ID, r.PRURL, warn)
		if err := appendSettled(path, ds); err != nil {
			fmt.Fprintf(warn, "    ?? %s: could not append %s to %s: %v\n", r.ID, claude.OpDispatchSettled, path, err)
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

// queryablePRURL reports whether u is an absolute http(s) URL with a host. The
// Record's PR URL can come from text the Box printed, and the forge client
// passes it as an argument, so anything that could read as a flag is refused.
func queryablePRURL(u string) bool {
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
