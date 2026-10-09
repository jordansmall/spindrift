package dispatchrecord

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/hostpaths"
)

const fourLines = "one\ntwo\nthree\nfour\n"

// newChurnRepo merges a feature branch that adds f.txt (fourLines) at mergedAt.
func newChurnRepo(t *testing.T) (gitRepo, string) {
	t.Helper()
	g := newBaseRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.commitFile(mergedAt.Add(-time.Hour), "f.txt", fourLines, "feature work")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	return g, g.git(mergedAt, "rev-parse", "HEAD")
}

// newBaseRepo is a main branch holding only a.txt, committed 48h before mergedAt.
func newBaseRepo(t *testing.T) gitRepo {
	t.Helper()
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.txt", "a\n", "base")
	return g
}

func wantNilChurn(t *testing.T, r Record) {
	t.Helper()
	if r.Churn14d != nil || r.MaturedAt == nil {
		t.Fatalf("churn=%v matured_at=%v; want nil churn on a matured record", r.Churn14d, r.MaturedAt)
	}
}

func wantChurn(t *testing.T, r Record, want float64) {
	t.Helper()
	if r.Churn14d == nil || math.Abs(*r.Churn14d-want) > 1e-9 || r.MaturedAt == nil {
		t.Fatalf("churn=%v matured_at=%v; want churn=%v and matured", r.Churn14d, r.MaturedAt, want)
	}
}

func fillAfterWindow(t *testing.T, g gitRepo, merge string) Record {
	t.Helper()
	g.commitFile(mergedAt.Add(RevertWindow+24*time.Hour), "tip.txt", "tip\n", "tip")
	return fill(t, mergeRecordStore(t, merge), g, mergedAt.Add(RevertWindow+48*time.Hour))
}

func TestChurnCountsLinesRewrittenInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	g.commitFile(mergedAt.Add(2*24*time.Hour), "f.txt", "one\nTWO\nthree\nfour\n", "rewrite a line")
	wantChurn(t, fillAfterWindow(t, g, merge), 0.25)
}

func TestChurnIsZeroWhenNothingIsRewritten(t *testing.T) {
	g, merge := newChurnRepo(t)
	g.commitFile(mergedAt.Add(2*24*time.Hour), "b.txt", "b\n", "unrelated")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

func TestChurnIgnoresRewritesAfterTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	g.commitFile(mergedAt.Add(2*24*time.Hour), "f.txt", "one\nTWO\nthree\nfour\n", "inside")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "f.txt", "ONE\nTWO\nTHREE\nfour\n", "outside")
	wantChurn(t, fillAfterWindow(t, g, merge), 0.25)
}

func TestChurnCountsAFileDeletedInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	g.git(mergedAt.Add(24*time.Hour), "rm", "-q", "f.txt")
	g.git(mergedAt.Add(24*time.Hour), "commit", "-q", "-m", "drop it")
	wantChurn(t, fillAfterWindow(t, g, merge), 1)
}

// mvAt moves from to to at the given time and commits it, rewriting its content
// when content is non-empty.
func mvAt(g gitRepo, at time.Time, from, to, content string) {
	g.t.Helper()
	g.git(at, "mv", from, to)
	if content != "" {
		if err := os.WriteFile(filepath.Join(g.dir, to), []byte(content), 0o644); err != nil {
			g.t.Fatal(err)
		}
		g.git(at, "add", to)
	}
	g.git(at, "commit", "-q", "-m", "move "+from)
}

// A later move rewrote no line, so blame follows the lines to their new path.
func TestChurnFollowsAFileRenamedInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	mvAt(g, mergedAt.Add(24*time.Hour), "f.txt", "g.txt", "")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

func TestChurnCountsOnlyTheEditedLinesOfAFileRenamedInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	mvAt(g, mergedAt.Add(24*time.Hour), "f.txt", "g.txt", "one\nTWO\nthree\nfour\n")
	wantChurn(t, fillAfterWindow(t, g, merge), 0.25)
}

func TestChurnFollowsAChainOfRenamesInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	mvAt(g, mergedAt.Add(24*time.Hour), "f.txt", "g.txt", "")
	mvAt(g, mergedAt.Add(48*time.Hour), "g.txt", "h.txt", "")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

func TestChurnIgnoresARenameAfterTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	mvAt(g, mergedAt.Add(RevertWindow+time.Hour), "f.txt", "g.txt", "")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

// A PR commit that rewrites another PR commit's lines is the merge's own work.
func TestChurnIgnoresTheMergesOwnRewrites(t *testing.T) {
	g := newBaseRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.commitFile(mergedAt.Add(-2*time.Hour), "f.txt", fourLines, "first draft")
	g.commitFile(mergedAt.Add(-time.Hour), "f.txt", "one\nTWO\nTHREE\nfour\n", "rework")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	merge := g.git(mergedAt, "rev-parse", "HEAD")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

func TestChurnLeavesAMergeWithNoAddedLinesNil(t *testing.T) {
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.txt", "a\nb\n", "base")
	merge := g.commitFile(mergedAt, "a.txt", "a\n", "delete a line")
	wantNilChurn(t, fillAfterWindow(t, g, merge))
}

const tenLines = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"

// renameRepo merges a feature branch whose only change is renaming r.txt to
// s.txt, edited to editedContent when that is non-empty.
func renameRepo(t *testing.T, editedContent string) (gitRepo, string) {
	t.Helper()
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "r.txt", tenLines, "base")
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.git(mergedAt, "mv", "r.txt", "s.txt")
	if editedContent != "" {
		if err := os.WriteFile(filepath.Join(g.dir, "s.txt"), []byte(editedContent), 0o644); err != nil {
			t.Fatal(err)
		}
		g.git(mergedAt, "add", "s.txt")
	}
	g.git(mergedAt.Add(-time.Hour), "commit", "-q", "-m", "rename")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	return g, g.git(mergedAt, "rev-parse", "HEAD")
}

// A rename the PR itself made rewrote no line, whatever blame says of the new path.
func TestChurnIgnoresAFileTheMergeOnlyRenamed(t *testing.T) {
	g, merge := renameRepo(t, "")
	wantNilChurn(t, fillAfterWindow(t, g, merge))
}

func TestChurnCountsOnlyTheEditedLinesOfARenamedFile(t *testing.T) {
	g, merge := renameRepo(t, "l1\nl2\nl3\nl4\nCHANGED\nl6\nl7\nl8\nl9\nl10\n")
	wantChurn(t, fillAfterWindow(t, g, merge), 0)
}

// A merge that reached the base branch only as a later merge's second parent
// is off the first-parent line at the window's end; the revert verdict is
// still settled and churn is left empty.
func TestChurnStaysNilButMaturesWhenMergeIsNotOnTheFirstParentLineAtWindowEnd(t *testing.T) {
	g := newBaseRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "pulled")
	merge := g.commitFile(mergedAt, "f.txt", fourLines, "feature")
	g.git(mergedAt, "checkout", "-q", "main")
	g.commitFile(mergedAt.Add(time.Hour), "b.txt", "b\n", "other work")
	g.git(mergedAt.Add(RevertWindow+24*time.Hour), "merge", "--no-ff", "-m", "pull", "pulled")
	now := mergedAt.Add(RevertWindow + 48*time.Hour)
	r := fill(t, mergeRecordStore(t, merge), g, now)
	if r.Churn14d != nil {
		t.Fatalf("churn = %v; want nil", *r.Churn14d)
	}
	wantVerdict(t, r, false, now)
}

// A PR that changes a base line and then restores it added nothing there;
// blame must credit the restored line to the base, not to the PR's own commit.
func TestChurnCountsRewritesOfLinesAPRRestoredToTheirBaseContent(t *testing.T) {
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "main")
	g.commitFile(mergedAt.Add(-48*time.Hour), "f.txt", "X\n", "base")
	g.git(mergedAt, "checkout", "-q", "-b", "feature")
	g.commitFile(mergedAt.Add(-2*time.Hour), "f.txt", "X'\n", "change X")
	g.commitFile(mergedAt.Add(-time.Hour), "f.txt", "X\nA\nB\n", "restore X, add A and B")
	g.git(mergedAt, "checkout", "-q", "main")
	g.git(mergedAt, "merge", "--no-ff", "-m", "merge feature", "feature")
	merge := g.git(mergedAt, "rev-parse", "HEAD")
	g.commitFile(mergedAt.Add(2*24*time.Hour), "f.txt", "X\nA\nZ\n", "rewrite B")
	wantChurn(t, fillAfterWindow(t, g, merge), 0.5)
}

// A merge that reached the base branch as a later merge's second parent before
// the window's end is just as far off the first-parent line.
func TestChurnStaysNilButMaturesWhenMergeIsNotOnTheFirstParentLineBeforeWindowEnd(t *testing.T) {
	g := newBaseRepo(t)
	g.git(mergedAt, "checkout", "-q", "-b", "pulled")
	merge := g.commitFile(mergedAt, "f.txt", fourLines, "feature")
	g.git(mergedAt, "checkout", "-q", "main")
	g.commitFile(mergedAt.Add(time.Hour), "b.txt", "b\n", "other work")
	g.git(mergedAt.Add(24*time.Hour), "merge", "--no-ff", "-m", "pull", "pulled")
	r := fillAfterWindow(t, g, merge)
	if r.Churn14d != nil {
		t.Fatalf("churn = %v; want nil", *r.Churn14d)
	}
	wantVerdict(t, r, false, mergedAt.Add(RevertWindow+48*time.Hour))
}

// When nothing on the base branch's first-parent line predates the window's
// end there is no state to blame; churn is empty but the verdict settles.
func TestChurnStaysNilButMaturesWhenNoFirstParentCommitPredatesWindowEnd(t *testing.T) {
	g := gitRepo{t: t, dir: t.TempDir()}
	g.git(mergedAt, "init", "-q", "-b", "side")
	g.commitFile(mergedAt.Add(-48*time.Hour), "a.txt", "a\n", "base")
	merge := g.commitFile(mergedAt, "f.txt", fourLines, "feature")
	g.git(mergedAt, "checkout", "-q", "--orphan", "main")
	g.git(mergedAt, "rm", "-rfq", ".")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "root.txt", "root\n", "late root")
	g.git(mergedAt.Add(RevertWindow+24*time.Hour), "merge", "--no-ff", "--allow-unrelated-histories", "-m", "pull", "side")
	now := mergedAt.Add(RevertWindow + 48*time.Hour)
	r := fill(t, mergeRecordStore(t, merge), g, now)
	if r.Churn14d != nil {
		t.Fatalf("churn = %v; want nil", *r.Churn14d)
	}
	wantVerdict(t, r, false, now)
}

func TestChurnLeavesImmatureRecordUnfilled(t *testing.T) {
	g, merge := newChurnRepo(t)
	g.commitFile(mergedAt.Add(15*24*time.Hour), "b.txt", "b\n", "later")
	wantUnfilled(t, fill(t, mergeRecordStore(t, merge), g, mergedAt.Add(RevertWindow-time.Minute)))
}

// Without a first parent the lines cannot be told, but the revert search finds
// nothing, so the verdict still stamps with churn left empty.
func TestChurnStaysNilButMaturesWhenFirstParentIsMissing(t *testing.T) {
	g := newBaseRepo(t)
	merge := g.commitFile(mergedAt, "f.txt", fourLines, "squashed feature")
	g.commitFile(mergedAt.Add(RevertWindow+time.Hour), "c.txt", "c\n", "tip")

	shallow := gitRepo{t: t, dir: filepath.Join(t.TempDir(), "shallow")}
	cmd := exec.Command("git", "clone", "-q", "--depth=2", "file://"+g.dir, shallow.dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v\n%s", err, out)
	}
	now := mergedAt.Add(RevertWindow + 2*time.Hour)
	r := fill(t, mergeRecordStore(t, merge), shallow, now)
	if r.Churn14d != nil {
		t.Fatalf("churn = %v; want nil without a first parent", *r.Churn14d)
	}
	wantVerdict(t, r, false, now)
}

func TestRecordJSONShowsChurnOnlyWhenFilled(t *testing.T) {
	b, err := json.Marshal(Record{})
	if err != nil || strings.Contains(string(b), "churn_14d") {
		t.Fatalf("unfilled record json = %s, %v", b, err)
	}
	c := 0.25
	b, err = json.Marshal(Record{Churn14d: &c})
	if err != nil || !strings.Contains(string(b), `"churn_14d":0.25`) {
		t.Fatalf("filled record json = %s, %v", b, err)
	}
}

// Records matured under v8 never had churn judged, so the migration sends them
// back through the maturity pass, but a v8 reverted verdict survives it.
func TestStoreMigratesV8DatabaseAddingChurnAndKeepingRevertedVerdicts(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{}, migrations[:8]...)
	stmts = append(stmts, "PRAGMA user_version = 8",
		`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome, merge_commit, reverted, matured_at) VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown', 'abc', 0, 5000)`)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	r := onlyRecord(t, openStore(t, root))
	if r.MergeCommit != "abc" || r.Reverted == nil || *r.Reverted || r.MaturedAt != nil || r.Churn14d != nil {
		t.Fatalf("record = %+v", r)
	}
}

const gitlinkSHA1 = "1111111111111111111111111111111111111111"
const gitlinkSHA2 = "2222222222222222222222222222222222222222"

func setGitlink(g gitRepo, at time.Time, sha string) string {
	g.git(at, "update-index", "--add", "--cacheinfo", "160000,"+sha+",sub")
	g.git(at, "commit", "-m", "point sub at "+sha)
	return g.git(at, "rev-parse", "HEAD")
}

// A submodule pointer has no lines to rewrite, and its commit is not in the
// clone for blame to find.
func TestChurnIgnoresABumpedSubmoduleGitlink(t *testing.T) {
	g := newBaseRepo(t)
	setGitlink(g, mergedAt.Add(-24*time.Hour), gitlinkSHA1)
	merge := setGitlink(g, mergedAt, gitlinkSHA2)
	wantNilChurn(t, fillAfterWindow(t, g, merge))
}

func TestChurnIgnoresAnAddedSubmoduleGitlink(t *testing.T) {
	g := newBaseRepo(t)
	merge := setGitlink(g, mergedAt, gitlinkSHA1)
	wantNilChurn(t, fillAfterWindow(t, g, merge))
}

func TestChurnIsNotInflatedByAGitlink(t *testing.T) {
	g := newBaseRepo(t)
	setGitlink(g, mergedAt.Add(-24*time.Hour), gitlinkSHA1)
	if err := os.WriteFile(filepath.Join(g.dir, "f.txt"), []byte(fourLines), 0o644); err != nil {
		t.Fatal(err)
	}
	g.git(mergedAt, "add", "f.txt")
	merge := setGitlink(g, mergedAt, gitlinkSHA2)
	g.commitFile(mergedAt.Add(2*24*time.Hour), "f.txt", "one\nTWO\nthree\nfour\n", "rewrite a line")
	wantChurn(t, fillAfterWindow(t, g, merge), 0.25)
}

func TestSurvivingLinesTreatsOnlyAMissingPathAsAbsent(t *testing.T) {
	g, merge := newChurnRepo(t)
	if n, err := survivingLines(g.dir, merge, "gone.txt", merge); err != nil || n != 0 {
		t.Fatalf("missing path: n=%d err=%v; want 0, nil", n, err)
	}
	if _, err := survivingLines(g.dir, strings.Repeat("0", 40), "f.txt", merge); err == nil {
		t.Fatal("an unreadable tree must be an error, not an absent path")
	}
}

// A path that a later commit turned into a directory is still listed by
// ls-tree, but blame cannot read it: the merge's lines there are rewritten.
func TestChurnCountsAFileReplacedByADirectoryInsideTheWindow(t *testing.T) {
	g, merge := newChurnRepo(t)
	at := mergedAt.Add(24 * time.Hour)
	g.git(at, "rm", "-q", "f.txt")
	if err := os.Mkdir(filepath.Join(g.dir, "f.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	g.commitFile(at, "f.txt/g", "g\n", "turn f.txt into a directory")
	wantChurn(t, fillAfterWindow(t, g, merge), 1)
}
