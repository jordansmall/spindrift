package dispatchrecord

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
)

var mergedAt = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

type gitRepo struct {
	t   *testing.T
	dir string
}

func (g gitRepo) git(at time.Time, args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", g.dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	d := strconv.FormatInt(at.Unix(), 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+d, "GIT_AUTHOR_DATE="+d,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g gitRepo) commitFile(at time.Time, name, content, msg string) string {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.git(at, "add", name)
	g.git(at, "commit", "-m", msg)
	return g.git(at, "rev-parse", "HEAD")
}

// newMergedRepo builds main with a --no-ff merge of a one-file feature branch
// committed at mergedAt, and returns the repo and the merge commit.
func newMergedRepo(t *testing.T) (gitRepo, string) {
	t.Helper()
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.txt", "a\n", "base")
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.commitFile(mergedAt.Add(-time.Hour), "f.txt", "feature\n", "feature work")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	return g, g.git(mergedAt, "rev-parse", "HEAD")
}

func mergeRecordStore(t *testing.T, merge string) *Store {
	t.Helper()
	root := t.TempDir()
	id := RecordID("work", "42", stampClaim)
	putLog(t, root, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		opLine(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
			RecordID: id, State: "complete", Reason: "merged", MergeCommit: merge,
		}}))...)
	s := openStore(t, root)
	ingest(t, s)
	return s
}

// insertMergeRecord adds a merged Record with the given id straight to the
// store, for tests that need an id log ingest would not produce.
func insertMergeRecord(t *testing.T, s *Store, id, merge string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome, merge_commit) VALUES (?, 'work', '41', 1000, 'inferred', 'unknown', ?)`, id, merge); err != nil {
		t.Fatal(err)
	}
}

func onlyRecord(t *testing.T, s *Store) Record {
	t.Helper()
	recs := records(t, s)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	return recs[0]
}

func fill(t *testing.T, s *Store, g gitRepo, now time.Time) Record {
	t.Helper()
	if err := s.FillMaturity(g.dir, now); err != nil {
		t.Fatal(err)
	}
	return onlyRecord(t, s)
}

func wantVerdict(t *testing.T, r Record, reverted bool, now time.Time) {
	t.Helper()
	if r.Reverted == nil || *r.Reverted != reverted || r.MaturedAt == nil || !r.MaturedAt.Equal(now) {
		t.Fatalf("reverted=%v matured_at=%v; want reverted=%v matured_at=%v", r.Reverted, r.MaturedAt, reverted, now)
	}
}

func wantUnfilled(t *testing.T, r Record) {
	t.Helper()
	if r.Reverted != nil || r.MaturedAt != nil || r.Churn14d != nil {
		t.Fatalf("reverted=%v matured_at=%v churn=%v; want all unfilled", r.Reverted, r.MaturedAt, r.Churn14d)
	}
}

func TestMergeCommitIsStoredFromSettledOp(t *testing.T) {
	_, merge := newMergedRepo(t)
	if r := onlyRecord(t, mergeRecordStore(t, merge)); r.MergeCommit != merge {
		t.Fatalf("merge commit = %q, want %q", r.MergeCommit, merge)
	}
}

func TestFillMaturityLeavesImmatureRecordUnfilled(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(15*24*time.Hour), "b.txt", "b\n", "later")
	s := mergeRecordStore(t, merge)
	wantUnfilled(t, fill(t, s, g, mergedAt.Add(RevertWindow-time.Minute)))
}

func TestFillMaturityMarksUntouchedMergeNotReverted(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(2*24*time.Hour), "b.txt", "b\n", "unrelated")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), false, now)
}

func TestFillMaturityFindsRevertTrailer(t *testing.T) {
	g, merge := newMergedRepo(t)
	at := mergedAt.Add(24 * time.Hour)
	g.git(at, "revert", "--no-edit", "-m", "1", merge)
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), true, now)
}

func TestFillMaturityFindsInverseDiffWithoutTrailer(t *testing.T) {
	g, merge := newMergedRepo(t)
	at := mergedAt.Add(24 * time.Hour)
	g.git(at, "rm", "-q", "f.txt")
	g.git(at, "commit", "-q", "-m", "drop the feature")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), true, now)
}

func TestFillMaturityIgnoresPartialInverse(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(24*time.Hour), "f.txt", "feature, edited\n", "edit the feature")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), false, now)
}

func TestFillMaturityIgnoresRevertAfterTheWindow(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	g.git(mergedAt.Add(RevertWindow+2*time.Hour), "revert", "--no-edit", "-m", "1", merge)
	now := mergedAt.Add(RevertWindow + 24*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), false, now)
}

func TestFillMaturitySkipsMergeCommitUnknownToClone(t *testing.T) {
	g, _ := newMergedRepo(t)
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	s := mergeRecordStore(t, strings.Repeat("ab", 20))
	wantUnfilled(t, fill(t, s, g, mergedAt.Add(RevertWindow+2*time.Hour)))
}

func TestFillMaturitySkipsMergeCommitOffTheBaseBranch(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "other", merge+"~1")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip on a branch without the merge")
	s := mergeRecordStore(t, merge)
	wantUnfilled(t, fill(t, s, g, mergedAt.Add(RevertWindow+2*time.Hour)))
}

func TestFillMaturitySkipsStaleCloneWhoseTipPredatesTheWindow(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(24*time.Hour), "b.txt", "b\n", "last commit the clone has")
	s := mergeRecordStore(t, merge)
	wantUnfilled(t, fill(t, s, g, mergedAt.Add(RevertWindow+24*time.Hour)))
}

func TestFillMaturityRejectsNonSHAMergeCommit(t *testing.T) {
	g, _ := newMergedRepo(t)
	s := mergeRecordStore(t, "--output=/tmp/x")
	wantUnfilled(t, fill(t, s, g, mergedAt.Add(RevertWindow+24*time.Hour)))
}

func TestFillMaturityKeepsAnswerOnceFilled(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	s := mergeRecordStore(t, merge)
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, s, g, now), false, now)
	g.git(now, "revert", "--no-edit", "-m", "1", merge)
	wantVerdict(t, fill(t, s, g, now.Add(time.Hour)), false, now)
}

func TestFilledRevertsSurviveReingest(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	s := mergeRecordStore(t, merge)
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, s, g, now), false, now)
	if _, err := s.Reingest(); err != nil {
		t.Fatal(err)
	}
	r := onlyRecord(t, s)
	wantVerdict(t, r, false, now)
	if r.MergeCommit != merge {
		t.Fatalf("merge commit = %q after reingest", r.MergeCommit)
	}
}

func TestFillMaturityWithNothingPendingNeedsNoRepo(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-7.log", workLog("2026-05-01T09:00:00Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	if err := s.FillMaturity(t.TempDir(), mergedAt); err != nil {
		t.Fatal(err)
	}
}

func TestFillMaturityErrorsOnANonRepo(t *testing.T) {
	_, merge := newMergedRepo(t)
	s := mergeRecordStore(t, merge)
	if err := s.FillMaturity(t.TempDir(), mergedAt.Add(RevertWindow)); err == nil {
		t.Fatal("want an error for a directory that is not a git repo")
	}
}

func TestRecordJSONShowsRevertFieldsOnlyWhenFilled(t *testing.T) {
	enc := func(r Record) string {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if out := enc(Record{}); strings.Contains(out, "reverted") || strings.Contains(out, "matured_at") || strings.Contains(out, "merge_commit") {
		t.Fatalf("unfilled record json = %s", out)
	}
	no, at := false, mergedAt
	out := enc(Record{MergeCommit: "abc", Reverted: &no, MaturedAt: &at})
	for _, want := range []string{`"merge_commit":"abc"`, `"reverted":false`, `"matured_at":"2026-05-01T12:00:00Z"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json %s lacks %s", out, want)
		}
	}
}

func TestStoreMigratesV7DatabaseAddingRevertColumns(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{}, migrations[:7]...)
	stmts = append(stmts, "PRAGMA user_version = 7",
		`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome, pr_url) VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown', 'https://x/pr/1')`)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	r := onlyRecord(t, openStore(t, root))
	if r.PRURL != "https://x/pr/1" || r.MergeCommit != "" || r.Reverted != nil || r.MaturedAt != nil {
		t.Fatalf("record = %+v", r)
	}
}

// The trailer alone counts: a revert whose conflict resolution changed the
// patch no longer matches the inverse diff.
func TestFillMaturityFindsTrailerOnACommitWithADifferentPatch(t *testing.T) {
	g, merge := newMergedRepo(t)
	g.commitFile(mergedAt.Add(24*time.Hour), "other.txt", "x\n", "Revert feature\n\nThis reverts commit "+merge+".")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), true, now)
}

// patch-id ignores whitespace by default; a later gofmt-style retab of the
// lines the merge reindented is not an undo of the merge.
func TestFillMaturityIgnoresWhitespaceOnlyChangeThatPatchIDWouldMatch(t *testing.T) {
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.go", "func f() {\nfoo()\n}\n", "base")
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.commitFile(mergedAt.Add(-time.Hour), "a.go", "func f() {\n    foo()\n}\n", "reindent")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	merge := g.git(mergedAt, "rev-parse", "HEAD")
	g.commitFile(mergedAt.Add(24*time.Hour), "a.go", "func f() {\n\tfoo()\n}\n", "gofmt")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	wantVerdict(t, fill(t, mergeRecordStore(t, merge), g, now), false, now)
}

// At a shallow clone's boundary the merge's parent is missing, so the clone
// cannot say whether a hand-made inverse exists.
func TestFillMaturityLeavesRecordUnfilledWhenFirstParentIsMissing(t *testing.T) {
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.txt", "a\n", "base")
	merge := g.commitFile(mergedAt, "f.txt", "feature\n", "squashed feature")
	g.git(mergedAt.Add(24*time.Hour), "rm", "-q", "f.txt")
	g.git(mergedAt.Add(24*time.Hour), "commit", "-q", "-m", "drop the feature")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")

	shallow := gitRepo{t: t, dir: filepath.Join(t.TempDir(), "shallow")}
	cmd := exec.Command("git", "clone", "-q", "--depth=3", "file://"+g.dir, shallow.dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v\n%s", err, out)
	}
	if shallow.git(mergedAt, "cat-file", "-t", merge) != "commit" {
		t.Fatal("merge commit missing from the shallow clone")
	}
	s := mergeRecordStore(t, merge)
	wantUnfilled(t, fill(t, s, shallow, mergedAt.Add(RevertWindow+2*time.Hour)))
}

func TestRunGitErrorCarriesStderrAndStillUnwrapsToExitError(t *testing.T) {
	_, err := runGit(t.TempDir(), "", "rev-parse", "--git-dir")
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want it to unwrap to *exec.ExitError", err)
	}
	for _, want := range []string{"git rev-parse --git-dir", "not a git repository"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to contain %q", err, want)
		}
	}
}

// One Record's broken history must not stop the pass for the Records after it.
func TestFillMaturityFillsOtherRecordsWhenOneCheckErrors(t *testing.T) {
	g, broken := newMergedRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "feature2")
	g.commitFile(mergedAt.Add(time.Minute), "g.txt", "second\n", "second feature")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt.Add(2*time.Minute), "merge", "--no-ff", "-m", "merge feature2", "feature2")
	good := g.git(mergedAt, "rev-parse", "HEAD")
	g.commitFile(mergedAt.Add(24*time.Hour), "b.txt", "b\n", "inside the window")
	g.commitFile(mergedAt.Add(RevertWindow+3*time.Hour), "c.txt", "c\n", "tip")
	// The first merge's diff has to read f.txt, which is now gone.
	obj := g.git(mergedAt, "rev-parse", broken+":f.txt")
	if err := os.Remove(filepath.Join(g.dir, ".git", "objects", obj[:2], obj[2:])); err != nil {
		t.Fatal(err)
	}

	// work:41 sorts before the good Record's work:42, so a pass that aborts on
	// the first failure never reaches the good one.
	const brokenID = "work:41@x"
	s := mergeRecordStore(t, good)
	insertMergeRecord(t, s, brokenID, broken)
	now := mergedAt.Add(RevertWindow + 4*time.Hour)
	err := s.FillMaturity(g.dir, now)
	if err == nil || !strings.Contains(err.Error(), brokenID) {
		t.Fatalf("err = %v, want one naming %s", err, brokenID)
	}
	var sawGood, sawBroken bool
	for _, r := range records(t, s) {
		switch r.MergeCommit {
		case good:
			sawGood = true
			wantVerdict(t, r, false, now)
		case broken:
			sawBroken = true
			wantUnfilled(t, r)
		}
	}
	if !sawGood || !sawBroken {
		t.Fatalf("saw good=%v broken=%v, want both Records", sawGood, sawBroken)
	}
}
