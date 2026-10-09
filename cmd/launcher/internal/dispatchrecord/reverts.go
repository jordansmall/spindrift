package dispatchrecord

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RevertWindow is how long after a merge commit a base-branch commit still
// counts as reverting it. It also bounds churn: the churn_14d column and the
// docs name these 14 days, so changing it means renaming both.
const RevertWindow = 14 * 24 * time.Hour

var fullSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// FillMaturity settles, for every Record with a merge commit and no verdict
// yet, whether a base-branch commit reverted that merge within RevertWindow
// and how much of its added lines other commits rewrote in that window, and
// stamps reverted, churn_14d and matured_at on the Record together. clone is a
// git checkout of the forge's base branch, judged by refs/remotes/origin/HEAD
// when it resolves and HEAD otherwise.
//
// A Record stays unfilled, without error, when the clone cannot yet answer for
// it: the merge commit is not in the clone or not on its base branch, the
// window has not elapsed at now, or the clone's tip is older than the window's
// end (a stale clone must never record a false "not reverted"). A revert is a
// commit in the window whose message carries git revert's "This reverts commit
// <sha>" trailer, or whose patch is the exact inverse of the merge's diff; a
// merge whose first parent is missing (a shallow clone's boundary) cannot be
// checked for an inverse, so it stays unfilled only when a candidate revert
// needs one, and its churn stays empty. Churn is judged by churnWithinWindow.
// An error means git could not run or the clone is not a usable repository. A
// Record whose own check fails is skipped and stays unfilled while the pass
// goes on to the rest; those errors come back joined once every Record has
// been tried.
func (s *Store) FillMaturity(clone string, now time.Time) error {
	rows, err := s.db.Query(`SELECT record_id, merge_commit FROM records WHERE merge_commit <> '' AND matured_at IS NULL ORDER BY record_id`)
	if err != nil {
		return err
	}
	type pending struct{ id, merge string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.merge); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}

	tip, err := baseTip(clone)
	if err != nil {
		return err
	}
	tipTime, err := commitTime(clone, tip)
	if err != nil {
		return err
	}
	var failed []error
	for _, p := range todo {
		m, matured, err := judgeMaturity(clone, p.merge, tip, tipTime, now)
		if err != nil {
			failed = append(failed, fmt.Errorf("dispatchrecord: maturity check for %s: %w", p.id, err))
			continue
		}
		if !matured {
			continue
		}
		if _, err := s.db.Exec(`UPDATE records SET reverted = ?, churn_14d = ?, matured_at = ? WHERE record_id = ?`,
			m.reverted, m.churn, now.UnixMilli(), p.id); err != nil {
			return err
		}
	}
	return errors.Join(failed...)
}

// baseTip resolves the commit the revert search runs up to.
func baseTip(clone string) (string, error) {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "HEAD"} {
		out, err := runGit(clone, "", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err == nil {
			return strings.TrimSpace(out), nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", err
		}
	}
	return "", fmt.Errorf("dispatchrecord: %s has no resolvable HEAD", clone)
}

func commitTime(clone, rev string) (time.Time, error) {
	out, err := runGit(clone, "", "show", "-s", "--format=%ct", rev)
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("dispatchrecord: commit time of %s: %w", rev, err)
	}
	return time.Unix(sec, 0), nil
}

// maturity is one Record's post-merge verdict; churn is nil when the merge's
// added lines cannot be told (see churnWithinWindow).
type maturity struct {
	reverted bool
	churn    *float64
}

// judgeMaturity settles both post-merge columns or neither: matured is false
// unless the clone can answer for the revert check and the churn check alike.
func judgeMaturity(clone, merge, tip string, tipTime, now time.Time) (m maturity, matured bool, err error) {
	// The merge commit comes from a log, so refuse anything that is not a full
	// object name before it can reach git as an option or revision expression.
	if !fullSHA.MatchString(merge) {
		return maturity{}, false, nil
	}
	if _, err := runGit(clone, "", "cat-file", "-e", merge+"^{commit}"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return maturity{}, false, nil
		}
		return maturity{}, false, err
	}
	if onTip, err := gitTest(clone, "merge-base", "--is-ancestor", merge, tip); err != nil || !onTip {
		return maturity{}, false, err
	}
	mergedAt, err := commitTime(clone, merge)
	if err != nil {
		return maturity{}, false, err
	}
	end := mergedAt.Add(RevertWindow)
	if now.Before(end) || tipTime.Before(end) {
		return maturity{}, false, nil
	}

	m.reverted, matured, err = revertedWithinWindow(clone, merge, tip, end)
	if err != nil || !matured {
		return maturity{}, false, err
	}
	m.churn, matured, err = churnWithinWindow(clone, merge, tip, end)
	if err != nil || !matured {
		return maturity{}, false, err
	}
	return m, true, nil
}

// revertedWithinWindow reports whether merge was reverted by a commit dated no
// later than end, and whether the clone could answer at all (matured).
func revertedWithinWindow(clone, merge, tip string, end time.Time) (reverted, matured bool, err error) {
	out, err := runGit(clone, "", "log", "--ancestry-path", "--format=%H %ct", merge+".."+tip)
	if err != nil {
		return false, false, err
	}
	var candidates []string
	for _, line := range strings.Split(out, "\n") {
		sha, ts, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return false, false, fmt.Errorf("dispatchrecord: commit time %q: %w", ts, err)
		}
		if !time.Unix(sec, 0).After(end) {
			candidates = append(candidates, sha)
		}
	}
	if len(candidates) == 0 {
		return false, true, nil
	}
	stdin := strings.Join(candidates, "\n") + "\n"

	// git revert's trailer names the full sha of the commit it reverts.
	msgs, err := runGit(clone, stdin, "log", "--no-walk=unsorted", "--stdin", "--format=%B%x00")
	if err != nil {
		return false, false, err
	}
	if strings.Contains(msgs, "This reverts commit "+merge) {
		return true, true, nil
	}

	// A hand-made or squashed revert has no trailer, but its patch is still the
	// merge's diff inverted. The merge's first parent is the base it landed on.
	// For a rebase merge the recorded commit is only the last rebased commit, so
	// a squashed revert of a multi-commit PR goes unseen.
	hasParent, err := hasFirstParent(clone, merge)
	if err != nil || !hasParent {
		// A shallow clone's boundary commit cannot answer.
		return false, false, err
	}
	inverse, err := runGit(clone, "", "diff", "--no-color", "--no-ext-diff", merge, merge+"^1")
	if err != nil {
		return false, false, err
	}
	if strings.TrimSpace(inverse) == "" {
		return false, true, nil
	}
	// --verbatim: the default patch-id strips whitespace, so a retab would match.
	want, err := runGit(clone, inverse, "patch-id", "--verbatim")
	if err != nil {
		return false, false, err
	}
	wantID, _, _ := strings.Cut(strings.TrimSpace(want), " ")
	if wantID == "" {
		return false, true, nil
	}
	patches, err := runGit(clone, stdin, "log", "--no-walk=unsorted", "--stdin", "--no-merges", "-p", "--no-color", "--no-ext-diff", "--format=commit %H")
	if err != nil {
		return false, false, err
	}
	ids, err := runGit(clone, patches, "patch-id", "--verbatim")
	if err != nil {
		return false, false, err
	}
	for _, line := range strings.Split(ids, "\n") {
		if id, _, _ := strings.Cut(strings.TrimSpace(line), " "); id == wantID {
			return true, true, nil
		}
	}
	return false, true, nil
}

// gitTest runs a git command used as a yes/no test: true on exit 0, false on
// exit 1, an error for anything else (git's fatal errors exit 128).
func gitTest(clone string, args ...string) (bool, error) {
	_, err := runGit(clone, "", args...)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// runGit runs git in clone and returns its stdout. A failure to start git
// comes back wrapped with the command; a nonzero exit comes back as an error
// that unwraps to an *exec.ExitError and whose message carries git's stderr.
func runGit(clone string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", clone}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
			}
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
