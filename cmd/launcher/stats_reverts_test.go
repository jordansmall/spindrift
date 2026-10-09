package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
)

var revertEpoch = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func revertDay(n int) time.Time { return revertEpoch.AddDate(0, 0, n) }

// statsClock pins statsNow for one test.
func statsClock(t *testing.T, now time.Time) {
	t.Helper()
	old := statsNow
	statsNow = func() time.Time { return now }
	t.Cleanup(func() { statsNow = old })
}

func revertGit(t *testing.T, dir string, at time.Time, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	d := strconv.FormatInt(at.Unix(), 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+d, "GIT_AUTHOR_DATE="+d,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func revertCommit(t *testing.T, dir string, at time.Time, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	revertGit(t, dir, at, "add", file)
	revertGit(t, dir, at, "commit", "-m", msg)
	return revertGit(t, dir, at, "rev-parse", "HEAD")
}

// statsRevertFixtureRoot makes the root a Target clone holding four merged
// work Dispatches: m1 undone by a git revert (trailer), m2 undone by a hand
// made inverse commit with no trailer, m3 never undone, and m4 merged too
// late to have matured at day 31. The returned map names each merge commit.
func statsRevertFixtureRoot(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	revertGit(t, root, revertDay(0), "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".spindrift/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	revertCommit(t, root, revertDay(0), "base.txt", "base\n", "base")
	m1 := revertCommit(t, root, revertDay(1), "one.txt", "one\n", "work 1")
	revertGit(t, root, revertDay(3), "revert", "--no-edit", m1)
	revertCommit(t, root, revertDay(4), "two.txt", "two-before\n", "pre 2")
	m2 := revertCommit(t, root, revertDay(5), "two.txt", "two-after\n", "work 2")
	revertCommit(t, root, revertDay(7), "two.txt", "two-before\n", "undo work 2")
	m3 := revertCommit(t, root, revertDay(8), "three.txt", "three\n", "work 3")
	m4 := revertCommit(t, root, revertDay(25), "four.txt", "four\n", "work 4")
	revertCommit(t, root, revertDay(30), "tip.txt", "tip\n", "tip")

	logs := map[string]string{}
	for i, m := range []struct{ sha, rev string }{{m1, "rev-a"}, {m2, "rev-a"}, {m3, "rev-b"}, {m4, "rev-b"}} {
		key := strconv.Itoa(i + 1)
		claim := revertDay(i)
		id := dispatchrecord.RecordID("work", key, claim)
		logs["issue-"+key+".log"] = statsStampedLog(key, claim, claude.DispatchStart{Revision: m.rev}, nil, [2]string{"implement", "claude-opus"}) +
			statsOp(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
				RecordID: id, State: "complete", Reason: "merged", MergeCommit: m.sha,
			}})
	}
	statsWriteLogs(t, root, logs)
	return root, map[string]string{"$M1": m1, "$M2": m2, "$M3": m3, "$M4": m4}
}

func revertNormalize(text, root string, shas map[string]string) string {
	for name, sha := range shas {
		text = strings.ReplaceAll(text, sha, name)
	}
	return statsNormalize(text, map[string]string{"$ROOT": root})
}

func TestStats_RevertsShowDashBeforeMaturityGolden(t *testing.T) {
	root, shas := statsRevertFixtureRoot(t)
	statsClock(t, revertDay(10))

	text, stderr := runStats(t, root)
	if strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want no warning", stderr)
	}
	statsGolden(t, "stats-reverts-immature.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--json")
	if strings.Contains(jsonl, `"reverted"`) || strings.Contains(jsonl, `"matured_at"`) {
		t.Errorf("immature Records carry a verdict:\n%s", jsonl)
	}
}

func TestStats_RevertsFillAfterTheWindowGolden(t *testing.T) {
	root, shas := statsRevertFixtureRoot(t)
	statsClock(t, revertDay(31))

	text, stderr := runStats(t, root)
	if strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want no warning", stderr)
	}
	statsGolden(t, "stats-reverts.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--json")
	statsGolden(t, "stats-reverts.jsonl", revertNormalize(jsonl, root, shas))
}

func TestStats_RevertsAppearInBySplitsGolden(t *testing.T) {
	root, shas := statsRevertFixtureRoot(t)
	statsClock(t, revertDay(31))

	text, _ := runStats(t, root, "--by", "revision")
	statsGolden(t, "stats-reverts-by-revision.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--by", "revision", "--json")
	statsGolden(t, "stats-reverts-by-revision.jsonl", revertNormalize(jsonl, root, shas))
}

func TestStats_RevertsStayDashWithoutATargetClone(t *testing.T) {
	root := t.TempDir()
	statsClock(t, revertDay(31))
	claim := revertDay(0)
	statsWriteLogs(t, root, map[string]string{
		"issue-1.log": statsStampedLog("1", claim, claude.DispatchStart{}, nil, [2]string{"implement", "claude-opus"}) +
			statsOp(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
				RecordID: dispatchrecord.RecordID("work", "1", claim), State: "complete", Reason: "merged",
				MergeCommit: strings.Repeat("a", 40),
			}}),
	})

	text, stderr := runStats(t, root)
	if !strings.Contains(text, "Reverted: —") {
		t.Errorf("summary lacks the em dash:\n%s", text)
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want no warning for a root that is not a checkout", stderr)
	}
}

func TestStats_RevertsWarnWhenTheCloneIsUnusable(t *testing.T) {
	root := t.TempDir()
	revertGit(t, root, revertDay(0), "init", "-q", "-b", "main")
	statsClock(t, revertDay(31))
	claim := revertDay(0)
	statsWriteLogs(t, root, map[string]string{
		"issue-1.log": statsStampedLog("1", claim, claude.DispatchStart{}, nil, [2]string{"implement", "claude-opus"}) +
			statsOp(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
				RecordID: dispatchrecord.RecordID("work", "1", claim), State: "complete", Reason: "merged",
				MergeCommit: strings.Repeat("a", 40),
			}}),
	})

	text, stderr := runStats(t, root)
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want a warning", stderr)
	}
	if !strings.Contains(text, "Reverted: —") {
		t.Errorf("summary lacks the em dash:\n%s", text)
	}
}

// Each Record FillMaturity has to leave behind earns its own prefixed line,
// not one warning with the rest of the joined errors trailing unprefixed.
func TestStats_RevertsWarnOncePerUnfilledRecord(t *testing.T) {
	root := t.TempDir()
	revertGit(t, root, revertDay(0), "init", "-q", "-b", "main")
	revertCommit(t, root, revertDay(0), "base.txt", "base\n", "base")
	logs := map[string]string{}
	var blobs []string
	for i, n := range []string{"1", "2"} {
		file := "f" + n + ".txt"
		merge := revertCommit(t, root, revertDay(i+1), file, "merge "+n+"\n", "merge "+n)
		blobs = append(blobs, revertGit(t, root, revertDay(0), "rev-parse", merge+":"+file))
		claim := revertDay(i + 1)
		logs["issue-"+n+".log"] = statsStampedLog(n, claim, claude.DispatchStart{}, nil, [2]string{"implement", "claude-opus"}) +
			statsOp(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
				RecordID: dispatchrecord.RecordID("work", n, claim), State: "complete", Reason: "merged",
				MergeCommit: merge,
			}})
	}
	// Only a candidate commit inside the window gets as far as the diff.
	revertCommit(t, root, revertDay(5), "later.txt", "later\n", "inside both windows")
	revertCommit(t, root, revertDay(20), "tip.txt", "tip\n", "tip")
	// The revert check has to diff each merge's blob, so losing it fails the check.
	for _, obj := range blobs {
		if err := os.Remove(filepath.Join(root, ".git", "objects", obj[:2], obj[2:])); err != nil {
			t.Fatal(err)
		}
	}
	statsClock(t, revertDay(31))
	statsWriteLogs(t, root, logs)

	_, stderr := runStats(t, root)
	if got := strings.Count(stderr, "warning: reverts and churn left unfilled:"); got != 2 {
		t.Errorf("stderr has %d unfilled warnings, want 2:\n%s", got, stderr)
	}
}
