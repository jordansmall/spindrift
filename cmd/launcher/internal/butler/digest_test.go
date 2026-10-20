package butler

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/tuning"
)

const (
	noPriorChore   = "noprior"
	noPriorAnchor  = "row:a"
	withPriorChore = "withprior"
	evidenceChore  = "withevidence"
)

// The butler's tests exercise the tuning records path, which the production
// binary registers from the host bundle; a library test may not import that.
func init() {
	recordsdigest.Register(tuning.Strategy)
	recordsdigest.Register(recordsdigest.Strategy{
		Name: noPriorChore,
		// The cooldown is the Digest's own knob, so the cooldown test reads it here.
		ReadKnobs: func(recordsdigest.Lookup) (recordsdigest.Knobs, error) {
			return recordsdigest.Knobs{Cooldown: 720 * time.Hour}, nil
		},
		Render: func(in recordsdigest.Input) recordsdigest.Output {
			return recordsdigest.Output{Text: "digest", Latest: "latest"}
		},
		Anchors: func(string) map[string]bool { return map[string]bool{noPriorAnchor: true} },
	})
	recordsdigest.Register(recordsdigest.Strategy{
		Name: withPriorChore,
		Render: func(in recordsdigest.Input) recordsdigest.Output {
			return recordsdigest.Output{Text: fmt.Sprint("prior: ", in.Prior), Latest: "latest"}
		},
		Anchors:    func(string) map[string]bool { return map[string]bool{noPriorAnchor: true} },
		ParsePrior: func(f settle.PriorFinding) any { return f.Issue.Number },
	})
	recordsdigest.Register(recordsdigest.Strategy{
		Name: evidenceChore,
		Render: func(in recordsdigest.Input) recordsdigest.Output {
			return recordsdigest.Output{Text: "digest", Latest: "latest"}
		},
		Anchors: func(string) map[string]bool { return map[string]bool{noPriorAnchor: true} },
		Evidence: func(digest string, cites []string) string {
			return "host-evidence[" + digest + "|" + strings.Join(cites, ",") + "]\n"
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
	t.Parallel()
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
	t.Parallel()
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

// The adapter, not the Digest, decides a records Chore is due: it counts the
// settled Records past the tip's cursor. A Chore with no catalog row names no
// min-records knob, so one new Record is enough.
func TestRecordsSourceDueCountsNewRecordsForAnyDigest(t *testing.T) {
	t.Parallel()
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	for _, tc := range []struct {
		name     string
		seeded   int
		cursorAt int // index into the seeded Record IDs; -1 for no cursor
		want     chore.NotDue
	}{
		{"enough new Records", 3, -1, chore.Due},
		{"one new Record, no row floor", 1, -1, chore.Due},
		{"cursor at the latest Record", 3, 2, chore.TooFewRecords},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			ids := seedRecords(t, root, 1, tc.seeded, tuningNow.Add(-48*time.Hour))
			p := tuningPolicy(root, 1)
			p.Chores = []chore.Chore{{Name: noPriorChore, Every: time.Hour, Records: true}}
			r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, forge.NewFake().AsIssueFiler(), nil, p, func() time.Time { return tuningNow })
			src, err := newRecordsSource(r, noPriorChore, "head1", tuningNow)
			if err != nil {
				t.Fatal(err)
			}
			tip := ledger.Tip{}
			if tc.cursorAt >= 0 {
				tip = ledger.Tip{State: ledger.State{Cursor: ids[tc.cursorAt], Phase: ledger.Done}}
			}
			if got := src.Due(tip, nil, tuningNow, chore.Room{}, cfg); got != tc.want {
				t.Errorf("Due = %v, want %v", got, tc.want)
			}
		})
	}
}

// Prior findings are found by the Chore's dedup marker, open and closed alike:
// the issues carry only the generic butler provenance label, no Chore-specific
// finding label, so only the marker ties them to the Chore.
func TestRecordsSourcePrepareFindsPriorFindingsByMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
	p := tuningPolicy(root, 1)
	p.Chores = []chore.Chore{{Name: withPriorChore, Every: time.Hour, Records: true}}
	labels := []string{dispatchkind.Butler.FindingLabel}
	marker := func(name string) string { return "<!-- spindrift-dedup: chore=" + name + ", x -->" }
	open := []forge.Issue{
		{Number: "1", Body: marker(withPriorChore), Labels: labels},
		{Number: "2", Body: marker(noPriorChore), Labels: labels},
		{Number: "3", Body: "no marker here", Labels: labels},
	}
	closed := []forge.Issue{{Number: "4", Body: marker(withPriorChore), Labels: labels}}
	it := newBacklogTracker(t, forge.NewFake().AsIssueFiler(), open, closed)
	r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, it, nil, p, func() time.Time { return tuningNow })
	src, err := newRecordsSource(r, withPriorChore, "head1", tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Prepare(ledger.Tip{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "prior: [1 4]"; got.input != want {
		t.Errorf("input = %q, want %q", got.input, want)
	}
}

// A Digest that registers no Validate or DedupTerms gets neither tuning's
// settle rules nor its pattern keys: an untracked-target finding files, and
// findings on one site dedup on their own DedupTerms rather than
// class:file:metric.
func TestRecordsChoreWithoutValidateOrDedupTermsSettlesWithoutTuningRules(t *testing.T) {
	t.Parallel()
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
				tuningIntentMetric(t, "first", "prompt-gap", "usd", []string{noPriorAnchor}, "nowhere.go:Foo"),
				tuningIntentMetric(t, "second", "prompt-gap", "tokens", []string{noPriorAnchor}, "nowhere.go:Foo"),
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
		t.Errorf("filed %d findings, want 1: the untracked-target finding passes, its site-key twin dedups", n)
	}
	tip, err := backend.Read(noPriorChore)
	if err != nil {
		t.Fatal(err)
	}
	if tip.State.Dropped != 0 {
		t.Errorf("Drops = %v, want none: no Validate hook means no drop rules", tip.State.Drops)
	}
}

// The cite check belongs to the records adapter, not the Digest: a Digest with
// no Validate still has an uncited or unknown-anchor finding dropped.
func TestRecordsChoreWithNilValidateStillDropsBadCites(t *testing.T) {
	t.Parallel()
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
				tuningIntent(t, "uncited", "prompt-gap", nil, "a.go:Foo"),
				tuningIntent(t, "unknown", "prompt-gap", []string{noPriorAnchor, "row:nope"}, "b.go:Foo"),
				tuningIntent(t, "cited", "prompt-gap", []string{noPriorAnchor}, "c.go:Foo"),
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
		t.Errorf("filed %d findings, want 1: only the validly cited one", n)
	}
	tip, err := backend.Read(noPriorChore)
	if err != nil {
		t.Fatal(err)
	}
	want := []ledger.Drop{{Title: "uncited", Reason: "unknown-cite"}, {Title: "unknown", Reason: "unknown-cite"}}
	if !slices.Equal(tip.State.Drops, want) {
		t.Errorf("Drops = %v, want %v", tip.State.Drops, want)
	}
}

// The records adapter expires a closed-not-planned finding on the cooldown for
// any records Chore, not only tuning: a synthetic Digest's finding closed before
// now-cooldown no longer suppresses a re-filing, one closed after still does.
func TestRecordsChoreNotPlannedCooldownExpiresItsOwnFindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		age  time.Duration
		want int
	}{
		{"closed before the cutoff", 721 * time.Hour, 1},
		{"closed inside the cooldown", 24 * time.Hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			seedRecords(t, root, 1, 3, tuningNow.Add(-72*time.Hour))
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
					IssueIntents:      []string{tuningIntent(t, "first", "prompt-gap", []string{noPriorAnchor}, "nowhere.go:Foo")},
				})
				d.RecordIDResult = dispatchrecord.RecordID(noPriorChore, c.Name, c.ClaimTime)
				return d
			}
			sweep := func(it forge.IssueTracker, now time.Time) {
				t.Helper()
				r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, it, newBox, p, func() time.Time { return now })
				if out, err := r.Sweep([]string{noPriorChore}); err != nil || out.Kind != Swept {
					t.Fatalf("Sweep = %+v, %v, want Swept", out, err)
				}
			}
			sweep(fc.AsIssueFiler(), tuningNow.Add(-48*time.Hour))
			if len(fc.PostIssueCalls) != 1 {
				t.Fatalf("first Sweep filed %d, want 1", len(fc.PostIssueCalls))
			}
			// noprior is no catalog Chore, so the host stamps no chore term on its
			// finding; add the one a registered records Chore's finding carries.
			body := strings.TrimSuffix(fc.PostIssueCalls[0].Body, " -->") + ", chore=" + noPriorChore + " -->"
			filed := forge.Issue{
				Number: "77", Body: body,
				Labels:           []string{dispatchkind.Butler.FindingLabel, chore.FindingLabel(noPriorChore)},
				ClosedNotPlanned: true, ClosedAt: tuningNow.Add(-tc.age),
			}
			seedRecords(t, root, 10, 3, tuningNow.Add(-24*time.Hour))
			sweep(newBacklogTracker(t, fc.AsIssueFiler(), nil, []forge.Issue{filed}), tuningNow)
			if got := len(fc.PostIssueCalls) - 1; got != tc.want {
				t.Errorf("second Sweep filed %d, want %d", got, tc.want)
			}
		})
	}
}

// sweepOneCitedFinding settles one finding citing noPriorAnchor under the named
// synthetic records Chore and returns the body that was filed.
func sweepOneCitedFinding(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
	p := tuningPolicy(root, 1)
	p.Chores = []chore.Chore{{Name: name, Every: time.Hour, Records: true, FindingLabel: chore.FindingLabel(name)}}
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/77"
	backend := ledger.Local{Repo: ledgertest.NewRepo(t)}
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		d := readyDispatcher()
		d.RunResult = dispatch.Succeeded(dispatch.Result{
			Resolved:          outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: "butler-" + name, Status: outcome.StatusReady, Note: "swept"}},
			IssueIntentsFound: true,
			IssueIntents:      []string{tuningIntent(t, "cited", "prompt-gap", []string{noPriorAnchor}, "c.go:Foo")},
		})
		d.RecordIDResult = dispatchrecord.RecordID(name, c.Name, c.ClaimTime)
		return d
	}
	r := New(backend, fakeTree{head: "headsha", files: []string{"a.go"}}, fc.AsIssueFiler(), newBox, p, func() time.Time { return tuningNow })
	out, err := r.Sweep([]string{name})
	if err != nil || out.Kind != Swept {
		t.Fatalf("Sweep = %+v, %v, want Swept", out, err)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("filed %d findings, want 1", len(fc.PostIssueCalls))
	}
	return fc.PostIssueCalls[0].Body
}

// A Digest's Evidence hook, not the butler, writes the block appended to a
// filed finding, from the stored digest and the finding's cites.
func TestRecordsChoreEvidenceHookOutputEndsFiledBody(t *testing.T) {
	t.Parallel()
	body := sweepOneCitedFinding(t, evidenceChore)
	want := "host-evidence[digest|" + noPriorAnchor + "]"
	if !strings.Contains(body, "\n\n"+want+"\n") {
		t.Errorf("body = %q, want it to contain %q after the backlink", body, want)
	}
}

func TestRecordsChoreWithNilEvidenceAppendsNothing(t *testing.T) {
	t.Parallel()
	body := sweepOneCitedFinding(t, noPriorChore)
	if strings.Contains(body, "host-evidence") || strings.Contains(body, "## Evidence") {
		t.Errorf("body = %q, want no evidence block", body)
	}
}
