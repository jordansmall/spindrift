package tuning

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/passmachine"
	"spindrift.dev/launcher/internal/settle"
)

var now = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

var settleSeq int64

// rec builds a settled Record claimed at now minus ago, settling after every
// Record rec built before it, with one implement pass of the given USD.
func rec(id string, ago time.Duration, usd float64) dispatchrecord.Record {
	settleSeq++
	return dispatchrecord.Record{
		ID: id, Kind: dispatchkind.Work.Name, ClaimTime: now.Add(-ago), OutcomeSource: dispatchrecord.OutcomeSourceSettled, SettledSeq: settleSeq,
		Passes: []dispatchrecord.Pass{{Role: string(passmachine.RoleImplement), USD: usd, DurationMs: 120000}},
	}
}

const hour = time.Hour

func line(t *testing.T, text, anchor string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "| "+anchor+" |") {
			return l
		}
	}
	t.Fatalf("no row %q in:\n%s", anchor, text)
	return ""
}

func TestRenderWindowAndBaseline(t *testing.T) {
	records := []dispatchrecord.Record{
		rec("old", 10*24*hour, 100), // outside the 7-day baseline
		rec("b1", 5*24*hour, 1),     // baseline
		rec("b2", 4*24*hour, 3),     // baseline
		rec("cur", 3*24*hour, 50),   // the cursor itself: neither window nor baseline-excluded
		rec("w1", 2*24*hour, 4),     // window
		rec("w2", 1*24*hour, 6),     // window
	}
	d := Render(records, "cur", now, 2)
	if d.Records != 2 || d.Latest != "w2" {
		t.Fatalf("Records=%d Latest=%q, want 2 and w2", d.Records, d.Latest)
	}
	// "cur" is before the cursor, so it is baseline: (1+3+50)/3 = 18 avg; window (4+6)/2 = 5.
	got := line(t, d.Text, "role:implement:avg-usd")
	want := "| role:implement:avg-usd | implement avg USD per pass | 2 | $5.00 | $18.00 | -13.00 |  |"
	if got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
	if !strings.Contains(d.Text, "Baseline: 3 settled Records claimed 2026-10-02 to 2026-10-09") {
		t.Fatalf("baseline header wrong:\n%s", d.Text)
	}
	if !strings.Contains(d.Text, "after cur through w2 — 2 settled Records, 2 passes, notional cost $10.00") {
		t.Fatalf("window header wrong:\n%s", d.Text)
	}
}

func TestRenderBaselineBounds(t *testing.T) {
	records := []dispatchrecord.Record{
		rec("edge", BaselineWindow, 10),   // exactly now-7d: included
		rec("past", BaselineWindow+1, 99), // just outside
		rec("c", 3*hour, 1),
		rec("w", 2*hour, 2),
	}
	d := Render(records, "c", now, 1)
	got := line(t, d.Text, "role:implement:avg-usd")
	if !strings.Contains(got, "| $2.00 | $5.50 |") {
		t.Fatalf("baseline is edge and c only (past excluded): row = %q", got)
	}
}

func TestRenderBaselineExcludesFutureAndUnsettled(t *testing.T) {
	unsettled := rec("u", 5*hour, 77)
	unsettled.OutcomeSource = dispatchrecord.OutcomeSourceNone
	records := []dispatchrecord.Record{unsettled, rec("fut", -hour, 88), rec("c", 3*hour, 4), rec("w", 2*hour, 6)}
	d := Render(records, "c", now, 1)
	got := line(t, d.Text, "role:implement:avg-usd")
	if !strings.Contains(got, "| $6.00 | $4.00 | +2.00 |") {
		t.Fatalf("row = %q", got)
	}
}

func TestRenderEmptyBaselineIsDash(t *testing.T) {
	d := Render([]dispatchrecord.Record{rec("w", hour, 2)}, "", now, 1)
	for _, a := range []string{"summary:usd-per-record", "role:implement:avg-usd", "role:implement:passes-per-record"} {
		got := line(t, d.Text, a)
		if !strings.Contains(got, "| — | — |") {
			t.Fatalf("%s: baseline/delta should be dashes: %q", a, got)
		}
	}
	if !strings.Contains(d.Text, "after (start)") {
		t.Fatalf("empty cursor should render (start):\n%s", d.Text)
	}
}

func TestRenderThinFloorBoundary(t *testing.T) {
	var records []dispatchrecord.Record
	for i := 0; i < 3; i++ {
		records = append(records, rec(fmt.Sprintf("w%d", i), hour, 1))
	}
	for _, tc := range []struct {
		floor int
		thin  bool
	}{{3, false}, {4, true}} {
		d := Render(records, "", now, tc.floor)
		got := line(t, d.Text, "role:implement:avg-usd")
		if strings.HasSuffix(got, "| thin |") != tc.thin {
			t.Fatalf("floor %d: row = %q, thin want %v", tc.floor, got, tc.thin)
		}
		if !strings.Contains(d.Text, fmt.Sprintf("n < %d are marked thin", tc.floor)) {
			t.Fatalf("floor missing from header:\n%s", d.Text)
		}
	}
}

func TestRenderBlockRateUsesVerdictCount(t *testing.T) {
	review := func(id string, ago time.Duration, verdict string) dispatchrecord.Record {
		r := rec(id, ago, 1)
		r.Passes = []dispatchrecord.Pass{{Role: string(passmachine.RoleReview), Verdict: verdict}}
		return r
	}
	records := []dispatchrecord.Record{
		review("b", 5*hour, string(passmachine.VerdictApprove)),
		review("w1", 3*hour, string(passmachine.VerdictBlock)),
		review("w2", 2*hour, string(passmachine.VerdictApprove)),
	}
	d := Render(records, "b", now, 1)
	got := line(t, d.Text, "role:review:block-rate")
	if want := "| role:review:block-rate | review block rate | 2 | 50% | 0% | +50pp |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
	if strings.Contains(d.Text, "role:implement:block-rate") {
		t.Fatal("non-review role must not get a block-rate row")
	}
}

func TestRenderEmptyWindow(t *testing.T) {
	d := Render([]dispatchrecord.Record{rec("a", hour, 1)}, "a", now, 5)
	if d.Records != 0 || d.Latest != "" {
		t.Fatalf("Records=%d Latest=%q, want empty", d.Records, d.Latest)
	}
	if strings.Contains(d.Text, "| Anchor |") || !strings.Contains(d.Text, "No new settled Records") {
		t.Fatalf("empty window text:\n%s", d.Text)
	}
}

func TestRenderDeterministicAndOrdered(t *testing.T) {
	r := rec("w", hour, 1)
	r.Passes = append(r.Passes,
		dispatchrecord.Pass{Role: "zeta", USD: 1},
		dispatchrecord.Pass{Role: string(passmachine.RoleReview), USD: 1},
		dispatchrecord.Pass{Role: "alpha", USD: 1})
	records := []dispatchrecord.Record{r}
	a, b := Render(records, "", now, 1), Render(records, "", now, 1)
	if a != b {
		t.Fatal("two renders of the same input differ")
	}
	var order []string
	for _, role := range []string{"implement", "review", "alpha", "zeta"} {
		order = append(order, "role:"+role+":passes-per-record")
	}
	last := -1
	for _, anchor := range order {
		i := strings.Index(a.Text, "| "+anchor+" |")
		if i <= last {
			t.Fatalf("%s out of order in:\n%s", anchor, a.Text)
		}
		last = i
	}
	if strings.Contains(a.Text, "```") {
		t.Fatal("digest must not be fenced")
	}
}

// A Record claimed before the cursor but settled after it belongs to the
// window: the window follows settle order, not claim order.
func TestRenderWindowIncludesRecordClaimedBeforeCursorButSettledAfter(t *testing.T) {
	cur := rec("cur", 1*24*hour, 1)
	slow := rec("slow", 3*24*hour, 9)
	d := Render([]dispatchrecord.Record{slow, cur}, "cur", now, 1)
	if d.Records != 1 || d.Latest != "slow" {
		t.Fatalf("Records=%d Latest=%q, want 1 and slow", d.Records, d.Latest)
	}
}

// A window and baseline of different sizes must not make the passes row a
// measure of window length: both sides are passes per Record.
func TestRenderPassesPerRecordNormalisesBySetSize(t *testing.T) {
	var records []dispatchrecord.Record
	for i := 0; i < 4; i++ {
		records = append(records, rec(fmt.Sprintf("b%d", i), 5*24*hour, 1)) // 1 implement pass each
	}
	w := rec("w", hour, 1)
	w.Passes = append(w.Passes, dispatchrecord.Pass{Role: string(passmachine.RoleImplement), USD: 1})
	records = append(records, w)
	d := Render(records, "b3", now, 1)
	got := line(t, d.Text, "role:implement:passes-per-record")
	if want := "| role:implement:passes-per-record | implement passes per Record | 1 | 2.0 | 1.0 | +1.0 |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
}

// A role absent from a non-empty baseline ran zero passes per Record there.
func TestRenderPassesPerRecordZeroWhenRoleAbsentFromBaseline(t *testing.T) {
	records := []dispatchrecord.Record{rec("b", 5*hour, 1), rec("w", hour, 1)}
	records[1].Passes = append(records[1].Passes, dispatchrecord.Pass{Role: string(passmachine.RoleReview), Verdict: string(passmachine.VerdictApprove)})
	d := Render(records, "b", now, 1)
	got := line(t, d.Text, "role:review:passes-per-record")
	if want := "| role:review:passes-per-record | review passes per Record | 1 | 1.0 | 0.0 | +1.0 |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
}

// Research and butler Records never land code, so a window full of them must
// not read as a drop in the share of work Dispatches that landed.
func TestRenderLandedShareCountsWorkRecordsOnly(t *testing.T) {
	landed := func(id, key string) dispatchrecord.Record {
		r := rec(id, hour, 1)
		r.DispatchKey, r.Outcome, r.Reason = key, forge.Complete.String(), settle.ReasonMerged
		return r
	}
	records := []dispatchrecord.Record{landed("b", "1"), landed("w1", "2")}
	for i := 0; i < 8; i++ {
		r := rec(fmt.Sprintf("r%d", i), hour, 1)
		r.Kind = dispatchkind.Research.Name
		records = append(records, r)
	}
	d := Render(records, "b", now, 1)
	got := line(t, d.Text, "summary:landed-share")
	if want := "| summary:landed-share | Landed share of work Records | 1 | 100% | 100% | +0pp |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
}

func TestRenderLandedShareEmptyWithoutWorkRecords(t *testing.T) {
	r := rec("w", hour, 1)
	r.Kind = dispatchkind.Research.Name
	d := Render([]dispatchrecord.Record{r}, "", now, 1)
	got := line(t, d.Text, "summary:landed-share")
	if want := "| summary:landed-share | Landed share of work Records | 0 | — | — | — | thin |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
}

func ptr[T any](v T) *T { return &v }

func atRev(r dispatchrecord.Record, rev string) dispatchrecord.Record {
	r.Revision = rev
	return r
}

func TestRenderQualityRowsDashWhenImmature(t *testing.T) {
	d := Render([]dispatchrecord.Record{rec("w", hour, 1)}, "", now, 1)
	for anchor, want := range map[string]string{
		"summary:reverted":        "| summary:reverted | Reverted share of merged work | 0 | — | — | — | thin |",
		"summary:churn":           "| summary:churn | Mean 14-day churn | 0 | — | — | — | thin |",
		"role:implement:reverted": "| role:implement:reverted | implement reverted share | 0 | — | — | — | thin |",
		"role:implement:churn":    "| role:implement:churn | implement mean 14-day churn | 0 | — | — | — | thin |",
	} {
		if got := line(t, d.Text, anchor); got != want {
			t.Fatalf("row = %q\nwant  %q", got, want)
		}
	}
}

func TestRenderQualityRowsFilled(t *testing.T) {
	b := rec("b", 5*hour, 1)
	b.Reverted, b.Churn14d = ptr(false), nil
	w1, w2, w3 := rec("w1", 3*hour, 1), rec("w2", 2*hour, 1), rec("w3", hour, 1)
	w1.Reverted, w1.Churn14d = ptr(true), ptr(0.1)
	w2.Reverted, w2.Churn14d = ptr(false), ptr(0.3)
	// w3 is not yet matured: it must count toward neither n nor the figure.
	d := Render([]dispatchrecord.Record{b, w1, w2, w3}, "b", now, 2)
	for anchor, want := range map[string]string{
		"summary:reverted":        "| summary:reverted | Reverted share of merged work | 2 | 50% | 0% | +50pp |  |",
		"summary:churn":           "| summary:churn | Mean 14-day churn | 2 | 20% | — | — |  |",
		"role:implement:reverted": "| role:implement:reverted | implement reverted share | 2 | 50% | 0% | +50pp |  |",
		"role:implement:churn":    "| role:implement:churn | implement mean 14-day churn | 2 | 20% | — | — |  |",
	} {
		if got := line(t, d.Text, anchor); got != want {
			t.Fatalf("row = %q\nwant  %q", got, want)
		}
	}
}

// A role's quality is read over the Records that ran that role.
func TestRenderRoleQualityCoversOnlyRecordsHoldingTheRole(t *testing.T) {
	impl := rec("w1", 2*hour, 1)
	impl.Reverted = ptr(true)
	rev := rec("w2", hour, 1)
	rev.Passes = []dispatchrecord.Pass{{Role: string(passmachine.RoleReview)}}
	rev.Reverted = ptr(false)
	d := Render([]dispatchrecord.Record{impl, rev}, "", now, 1)
	if got, want := line(t, d.Text, "role:implement:reverted"), "| role:implement:reverted | implement reverted share | 1 | 100% | — | — |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
	if got, want := line(t, d.Text, "summary:reverted"), "| summary:reverted | Reverted share of merged work | 2 | 50% | — | — |  |"; got != want {
		t.Fatalf("row = %q\nwant  %q", got, want)
	}
}

func TestRenderNoSplitsWhenDimensionsHaveOneValue(t *testing.T) {
	b, w := atRev(rec("b", 5*hour, 1), "r1"), atRev(rec("w", hour, 1), "r1")
	b.Passes[0].Models, w.Passes[0].Models = []string{"opus"}, []string{"opus"}
	b.PromptHashes, w.PromptHashes = map[string]string{"implement": "h"}, map[string]string{"implement": "h"}
	d := Render([]dispatchrecord.Record{b, w}, "b", now, 1)
	if strings.Contains(d.Text, "Splits") || strings.Contains(d.Text, "###") {
		t.Fatalf("single-valued dimensions must not render:\n%s", d.Text)
	}
	// Unlabelled Records are no value at all.
	d = Render([]dispatchrecord.Record{rec("b", 5*hour, 1), rec("w", hour, 1)}, "b", now, 1)
	if strings.Contains(d.Text, "Splits") {
		t.Fatalf("unlabelled Records must not render a split:\n%s", d.Text)
	}
}

func TestRenderRevisionSplitIncludesBaselineOnlyValue(t *testing.T) {
	records := []dispatchrecord.Record{
		atRev(rec("b", 5*hour, 1), "r1"),
		atRev(rec("w1", 3*hour, 4), "r2"),
		atRev(rec("w2", 2*hour, 6), "r2"),
	}
	d := Render(records, "b", now, 1)
	for _, want := range []string{
		"| revision:r2:usd-per-record | USD per Record (r2) | 2 | $5.00 | — | — |  |",
		"| revision:r2:passes-per-record | Passes per Record (r2) | 2 | 1.0 | — | — |  |",
		"| revision:r2:landed-share | Landed share of work Records (r2) | 2 | 0% | — | — |  |",
		"| revision:r2:reverted | Reverted share of merged work (r2) | 0 | — | — | — | thin |",
		"| revision:r2:churn | Mean 14-day churn (r2) | 0 | — | — | — | thin |",
		"| revision:r1:usd-per-record | USD per Record (r1) | 0 | — | $1.00 | — | thin |",
	} {
		if !strings.Contains(d.Text, want+"\n") {
			t.Fatalf("digest lacks %q:\n%s", want, d.Text)
		}
	}
	idx := func(s string) int { return strings.Index(d.Text, s) }
	if !(idx("## Roles") < idx("## Splits") && idx("## Splits") < idx("### revision") && idx("| revision:r2:usd-per-record") < idx("| revision:r1:usd-per-record")) {
		t.Fatalf("sections out of order (Roles, Splits, revision; window values before baseline-only):\n%s", d.Text)
	}
	if strings.Contains(d.Text, "### model") || strings.Contains(d.Text, "### prompt:") {
		t.Fatalf("only revision varies:\n%s", d.Text)
	}
}

// A Record with no revision stamp is not a value of the dimension, so one
// stamped revision beside unstamped Records is still a single value.
func TestRenderSplitDropsUnlabelledGroup(t *testing.T) {
	records := []dispatchrecord.Record{atRev(rec("b", 5*hour, 1), "r1"), rec("w", hour, 1)}
	if d := Render(records, "b", now, 1); strings.Contains(d.Text, "Splits") {
		t.Fatalf("one revision plus unlabelled Records must not split:\n%s", d.Text)
	}
}

func TestRenderModelSplitIsPassLevel(t *testing.T) {
	r := rec("w1", 2*hour, 3)
	r.Passes[0].Models = []string{"opus"}
	r.Passes = append(r.Passes, dispatchrecord.Pass{Role: string(passmachine.RoleReview), USD: 1, Models: []string{"sonnet", "haiku"}})
	r2 := rec("w2", hour, 5)
	r2.Passes[0].Models = []string{"opus"}
	d := Render([]dispatchrecord.Record{r, r2}, "", now, 1)
	for _, want := range []string{
		"### model",
		"| model:opus:usd-per-record | USD per Record (opus) | 2 | $4.00 | — | — |  |",
		"| model:haiku+sonnet:usd-per-record | USD per Record (haiku+sonnet) | 1 | $1.00 | — | — |  |",
	} {
		if !strings.Contains(d.Text, want) {
			t.Fatalf("digest lacks %q:\n%s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "### revision") {
		t.Fatalf("revision does not vary:\n%s", d.Text)
	}
}

func TestRenderPromptSplitPerRole(t *testing.T) {
	b, w := rec("b", 5*hour, 1), rec("w", hour, 2)
	b.PromptHashes = map[string]string{"implement": "h1", "review": "x"}
	w.PromptHashes = map[string]string{"implement": "h2", "review": "x"}
	d := Render([]dispatchrecord.Record{b, w}, "b", now, 1)
	for _, want := range []string{
		"### prompt:implement",
		"| prompt:implement:h2:usd-per-record | USD per Record (h2) | 1 | $2.00 | — | — |  |",
		"| prompt:implement:h1:usd-per-record | USD per Record (h1) | 0 | — | $1.00 | — | thin |",
	} {
		if !strings.Contains(d.Text, want) {
			t.Fatalf("digest lacks %q:\n%s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "### prompt:review") {
		t.Fatalf("review prompt has one hash:\n%s", d.Text)
	}
}

var anchorGrammar = regexp.MustCompile(`^[a-z]+(:[A-Za-z0-9._+-]+)+$`)

// Anchors are what a finding cites: each must be unique in a digest and made
// of safe characters whatever a Record's revision, model or role was stamped
// with, and no value may add a cell to its row.
func TestRenderAnchorsAreUniqueAndTableSafe(t *testing.T) {
	var records []dispatchrecord.Record
	for i, rev := range []string{"a b", "a_b", "x|y", "`z`", "(none)", "r\tq"} {
		r := atRev(rec(fmt.Sprintf("r%d", i), time.Duration(10-i)*hour, 1), rev)
		r.Passes[0].Role = "im|pl:" + rev
		r.Passes[0].Models = []string{"m " + rev}
		r.PromptHashes = map[string]string{"implement": rev}
		records = append(records, r)
	}
	d := Render(records, "", now, 1)
	seen := map[string]bool{}
	rows := 0
	for _, l := range strings.Split(d.Text, "\n") {
		if !strings.HasPrefix(l, "| ") || strings.HasPrefix(l, "| Anchor ") {
			continue
		}
		rows++
		if n := strings.Count(l, "|"); n != 8 {
			t.Errorf("row has %d pipes, want 8: %q", n, l)
		}
		anchor := strings.SplitN(l, " ", 3)[1]
		if !anchorGrammar.MatchString(anchor) {
			t.Errorf("anchor %q outside the grammar", anchor)
		}
		if seen[anchor] {
			t.Errorf("anchor %q repeated", anchor)
		}
		seen[anchor] = true
	}
	if rows == 0 || !strings.Contains(d.Text, "### revision") {
		t.Fatalf("no revision split rendered:\n%s", d.Text)
	}
	if strings.Contains(d.Text, "`") {
		t.Fatalf("backtick survived into the digest:\n%s", d.Text)
	}
}
