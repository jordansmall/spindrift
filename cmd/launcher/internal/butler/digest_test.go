package butler

import (
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/tuning"
)

const noPriorChore = "noprior"

// The butler's tests exercise the tuning records path, which the production
// binary registers from the host bundle; a library test may not import that.
func init() {
	recordsdigest.Register(tuning.Strategy)
	recordsdigest.Register(recordsdigest.Strategy{
		Name: noPriorChore,
		Render: func(in recordsdigest.Input) recordsdigest.Output {
			return recordsdigest.Output{Text: "digest", Latest: "latest"}
		},
	})
}

// listCountingTracker counts the backlog listings PriorFindings would make.
type listCountingTracker struct {
	forge.IssueTracker
	lists int
}

func (l *listCountingTracker) ListOpenIssues() ([]forge.Issue, error) {
	l.lists++
	return l.IssueTracker.ListOpenIssues()
}

func TestUnregisteredRecordsChoreErrorsNamingIt(t *testing.T) {
	p := tuningPolicy(t.TempDir(), 1)
	p.Chores = []chore.Chore{{Name: "undigested", Every: time.Hour, Records: true}}
	r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, forge.NewFake().AsIssueFiler(), nil, p, func() time.Time { return tuningNow })

	if _, err := r.scopeSourceFor(p.Chores[0], "head1", tuningNow); err == nil || !strings.Contains(err.Error(), `"undigested"`) {
		t.Fatalf("scopeSourceFor err = %v, want one naming the Chore", err)
	}
	if _, err := r.Sweep([]string{"undigested"}); err == nil || !strings.Contains(err.Error(), "undigested") {
		t.Fatalf("Sweep err = %v, want one naming the Chore", err)
	}
}

func TestRecordsSourceWithoutParsePriorSkipsBacklog(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
	p := tuningPolicy(root, 1)
	p.Chores = []chore.Chore{{Name: noPriorChore, Every: time.Hour, Records: true}}
	it := &listCountingTracker{IssueTracker: forge.NewFake().AsIssueFiler()}
	r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, it, nil, p, func() time.Time { return tuningNow })
	src, err := newRecordsSource(r, noPriorChore, "head1", tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Prepare(ledger.Tip{})
	if err != nil {
		t.Fatal(err)
	}
	if got.input != "digest" || got.scope.NextCursor != "latest" {
		t.Errorf("prepared = %+v, want the registered Digest's output", got)
	}
	if it.lists != 0 {
		t.Errorf("backlog listed %d times, want 0 for a Digest with no ParsePrior", it.lists)
	}
}

// A Digest that registers no Validate or DedupTerms gets neither tuning's
// settle rules nor its pattern keys: an uncited finding files, and findings on
// one site dedup on their own DedupTerms rather than class:file:metric.
func TestRecordsChoreWithNilHooksSettlesWithoutTuningRules(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
	p := tuningPolicy(root, 1)
	p.Chores = []chore.Chore{{Name: noPriorChore, Every: time.Hour, Records: true, FindingLabel: chore.FindingLabel(noPriorChore)}}
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/77"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		d := readyDispatcher()
		d.RunResult = dispatch.Succeeded(dispatch.Result{
			Resolved:          outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: "butler-" + noPriorChore, Status: outcome.StatusReady, Note: "swept"}},
			IssueIntentsFound: true,
			IssueIntents: []string{
				tuningIntentMetric(t, "first", "prompt-gap", "usd", nil, "nowhere.go:Foo"),
				tuningIntentMetric(t, "second", "prompt-gap", "tokens", nil, "nowhere.go:Foo"),
			},
		})
		d.RecordIDResult = dispatchrecord.RecordID(noPriorChore, c.Name, c.ClaimTime)
		return d
	}
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), newBox, p, func() time.Time { return tuningNow })
	out, err := r.Sweep([]string{noPriorChore})
	if err != nil || out.Kind != Swept {
		t.Fatalf("Sweep = %+v, %v, want Swept", out, err)
	}
	if n := len(fc.PostIssueCalls); n != 1 {
		t.Errorf("filed %d findings, want 1: the uncited, untracked-target finding passes, its site-key twin dedups", n)
	}
	tip, err := backend.Read(noPriorChore)
	if err != nil {
		t.Fatal(err)
	}
	if tip.State.Dropped != 0 {
		t.Errorf("Drops = %v, want none: no Validate hook means no drop rules", tip.State.Drops)
	}
}
