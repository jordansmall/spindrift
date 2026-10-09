package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
)

// statsChurnFixtureRoot makes the root a Target clone holding four merged work
// Dispatches: m1 adds four lines of which a later commit rewrites one inside
// the window, m2 adds lines rewritten only after the window, m3 is a
// two-commit PR whose second commit rewrites the first commit's lines (the
// merge's own churn, not counted), and m4 only deletes a line.
func statsChurnFixtureRoot(t *testing.T) (string, map[string]string) {
	t.Helper()
	return statsChurnFixtureRootRevs(t, [4]string{"rev-a", "rev-b", "rev-b", "rev-a"})
}

// statsChurnFixtureRootRevs is statsChurnFixtureRoot with each merge's
// Dispatch stamped with the given revision, in m1..m4 order.
func statsChurnFixtureRootRevs(t *testing.T, revs [4]string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	revertGit(t, root, revertDay(0), "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".spindrift/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	revertCommit(t, root, revertDay(0), "base.txt", "keep\ndrop\n", "base")
	m1 := revertCommit(t, root, revertDay(1), "a.txt", "a1\na2\na3\na4\n", "work 1")
	m2 := revertCommit(t, root, revertDay(2), "b.txt", "b1\nb2\nb3\n", "work 2")
	revertCommit(t, root, revertDay(3), "a.txt", "a1 edited\na2\na3\na4\n", "rewrite inside window")
	revertGit(t, root, revertDay(4), "checkout", "-q", "-b", "pr")
	revertCommit(t, root, revertDay(4), "c.txt", "c1\nc2\n", "pr part 1")
	revertCommit(t, root, revertDay(4), "c.txt", "c1 fixed\nc2\n", "pr part 2")
	revertGit(t, root, revertDay(4), "checkout", "-q", "main")
	revertGit(t, root, revertDay(4), "merge", "--no-ff", "-m", "work 3", "pr")
	m3 := revertGit(t, root, revertDay(4), "rev-parse", "HEAD")
	m4 := revertCommit(t, root, revertDay(5), "base.txt", "keep\n", "work 4")
	revertCommit(t, root, revertDay(20), "b.txt", "b1 late\nb2\nb3\n", "rewrite after window")
	revertCommit(t, root, revertDay(30), "tip.txt", "tip\n", "tip")

	logs := map[string]string{}
	for i, m := range []struct{ sha, rev string }{{m1, revs[0]}, {m2, revs[1]}, {m3, revs[2]}, {m4, revs[3]}} {
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

func TestStats_ChurnShowsDashBeforeMaturityGolden(t *testing.T) {
	root, shas := statsChurnFixtureRoot(t)
	statsClock(t, revertDay(10))

	text, _ := runStats(t, root)
	if !strings.Contains(text, "Churn: —") {
		t.Errorf("summary lacks the churn em dash:\n%s", text)
	}
	statsGolden(t, "stats-churn-immature.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--json")
	if strings.Contains(jsonl, `"churn_14d"`) {
		t.Errorf("immature Records carry a churn share:\n%s", jsonl)
	}
}

func TestStats_ChurnFillsAfterTheWindowGolden(t *testing.T) {
	root, shas := statsChurnFixtureRoot(t)
	statsClock(t, revertDay(31))

	text, stderr := runStats(t, root)
	if strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want no warning", stderr)
	}
	// 25%, 0% and 0% over three Records; the deletion-only merge has no share.
	if !strings.Contains(text, "Churn: 8% of 3") {
		t.Errorf("summary lacks the mean churn share:\n%s", text)
	}
	statsGolden(t, "stats-churn.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--json")
	statsGolden(t, "stats-churn.jsonl", revertNormalize(jsonl, root, shas))

	for _, line := range strings.Split(strings.TrimSpace(jsonl), "\n") {
		if strings.Contains(line, shas["$M4"]) && strings.Contains(line, `"churn_14d"`) {
			t.Errorf("zero-added merge carries a churn share:\n%s", line)
		}
	}
	if !strings.Contains(jsonl, `"churn_14d":0.25`) {
		t.Errorf("rewritten-inside-window merge lacks churn_14d 0.25:\n%s", jsonl)
	}
}

func TestStats_ChurnAppearsInBySplitsGolden(t *testing.T) {
	root, shas := statsChurnFixtureRoot(t)
	statsClock(t, revertDay(31))

	text, _ := runStats(t, root, "--by", "revision")
	if !strings.Contains(text, "Churn: 25% of 1") || !strings.Contains(text, "Churn: 0% of 2") {
		t.Errorf("split lacks per-group churn:\n%s", text)
	}
	statsGolden(t, "stats-churn-by-revision.txt", revertNormalize(text, root, shas))
	jsonl, _ := runStats(t, root, "--by", "revision", "--json")
	statsGolden(t, "stats-churn-by-revision.jsonl", revertNormalize(jsonl, root, shas))
}

// m4 only deletes a line, so a group holding just that merge has no share to
// average, however mature it is.
func TestStats_ChurnShowsDashForAZeroAddedGroupGolden(t *testing.T) {
	root, shas := statsChurnFixtureRootRevs(t, [4]string{"rev-a", "rev-b", "rev-b", "rev-c"})
	statsClock(t, revertDay(31))

	text, _ := runStats(t, root, "--by", "revision")
	if !strings.Contains(text, "Reverted: 0% of 1  Churn: —  Outcome") {
		t.Errorf("zero-added group lacks the churn em dash beside a filled Reverted:\n%s", text)
	}
	statsGolden(t, "stats-churn-by-revision-zero-added.txt", revertNormalize(text, root, shas))
}

func TestStats_ChurnStaysDashWithoutATargetClone(t *testing.T) {
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

	text, _ := runStats(t, root)
	if !strings.Contains(text, "Churn: —") {
		t.Errorf("summary lacks the churn em dash:\n%s", text)
	}
}
