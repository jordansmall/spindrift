package butler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
)

// fakeTree is Tree's test double: a fixed Head and TrackedFiles answer, or an
// error for either.
type fakeTree struct {
	head     string
	headErr  error
	files    []string
	filesErr error
}

func (f fakeTree) Head(branch string) (string, error)           { return f.head, f.headErr }
func (f fakeTree) TrackedFiles(commit string) ([]string, error) { return f.files, f.filesErr }

func testPolicy() Policy {
	return Policy{
		Branch:       "main",
		Host:         "test-host",
		Chores:       []chore.Chore{{Name: "bugs"}},
		ClaimTimeout: time.Hour,
		Budgets:      chore.Budgets{},
		Zone:         time.UTC,
	}
}

// TestSweep_NotDue pins the reason text and Outcome shape when nothing in
// chores is due: the head equals the (empty) tip's LastSwept with an empty
// Cursor, so chore.Check reports NothingToScan for it.
func TestSweep_NotDue(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	tree := fakeTree{head: ""}
	it := forge.NewFake().AsIssueFiler()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := New(backend, tree, it, func(dispatch.Chore) dispatch.Dispatcher { return dispatch.NewFake() }, testPolicy(), func() time.Time { return now })

	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != NotDue {
		t.Fatalf("Kind = %v, want NotDue: %+v", out.Kind, out)
	}
	want := `chore "bugs" not due: nothing to scan`
	if len(out.Reasons) != 1 || out.Reasons[0] != want {
		t.Errorf("Reasons = %v, want [%q]", out.Reasons, want)
	}
}

// TestSweep_EmptyChoresErrors pins the exact error text for an empty chores
// list.
func TestSweep_EmptyChoresErrors(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	r := New(backend, fakeTree{}, forge.NewFake().AsIssueFiler(), func(dispatch.Chore) dispatch.Dispatcher { return dispatch.NewFake() }, testPolicy(), time.Now)

	_, err := r.Sweep(nil)
	if err == nil || err.Error() != "butler: no chores to check" {
		t.Errorf("err = %v, want %q", err, "butler: no chores to check")
	}
}

// TestSweep_UnconfiguredChoreErrors rejects a candidate absent from
// Policy.Chores rather than sweeping it with a zero Every and no Classes.
func TestSweep_UnconfiguredChoreErrors(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	r := New(backend, fakeTree{}, forge.NewFake().AsIssueFiler(), func(dispatch.Chore) dispatch.Dispatcher { return dispatch.NewFake() }, testPolicy(), time.Now)

	_, err := r.Sweep([]string{"refactor"})
	if err == nil || !strings.Contains(err.Error(), `chore "refactor" is not configured`) {
		t.Errorf("err = %v, want it to name the unconfigured chore", err)
	}
}

// claimRace wraps a ledger.Backend and, on the first Claimed-phase Append,
// lands a rival claim on top of the same old tip first -- so the caller's own
// Claim legitimately loses its compare-and-swap, simulating a real worker
// claiming the Chore between Sweep's due-check Read and its own Claim call.
type claimRace struct {
	ledger.Backend
	fired bool
}

func (w *claimRace) Append(choreName, old string, s ledger.State, at time.Time) (string, error) {
	if !w.fired && s.Phase == ledger.Claimed {
		w.fired = true
		by := ledger.ClaimedBy{Host: "rival", Start: at}
		if _, err := w.Backend.Append(choreName, old, ledger.State{Phase: ledger.Claimed, ClaimedBy: &by}, at); err != nil {
			return "", err
		}
	}
	return w.Backend.Append(choreName, old, s, at)
}

// TestSweep_LostRace pins the LostRace Outcome: a due Chore whose Claim call
// loses its compare-and-swap to a rival claim reports LostRace, not an error.
func TestSweep_LostRace(t *testing.T) {
	inner := ledger.Local{Repo: newButlerBareRepo(t)}
	backend := &claimRace{Backend: inner}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}
	it := forge.NewFake().AsIssueFiler()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r := New(backend, tree, it, func(dispatch.Chore) dispatch.Dispatcher { return dispatch.NewFake() }, testPolicy(), func() time.Time { return now })

	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != LostRace || out.Chore != "bugs" {
		t.Errorf("Outcome = %+v, want Kind=LostRace Chore=bugs", out)
	}
}

// TestSweep_ClaimLeft pins the ClaimLeft Outcome: a due Chore whose Box
// crashes (no ready outcome) files nothing and leaves the claim standing --
// Sweep learns this from the settle step's own return, not a re-read.
func TestSweep_ClaimLeft(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go"}}
	it := forge.NewFake().AsIssueFiler()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{Success: false}

	r := New(backend, tree, it, func(dispatch.Chore) dispatch.Dispatcher { return d }, testPolicy(), func() time.Time { return now })

	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != ClaimLeft || out.Chore != "bugs" {
		t.Errorf("Outcome = %+v, want Kind=ClaimLeft Chore=bugs", out)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Phase != ledger.Claimed {
		t.Errorf("Phase = %q, want %q (claim left standing)", tip.State.Phase, ledger.Claimed)
	}
}

// TestSweep_Swept pins the Swept Outcome: a due Chore whose Box reports ready
// runs to completion, with Filed/Promoted/Dropped read off the settle step's
// return, never a Ledger re-read.
func TestSweep_Swept(t *testing.T) {
	backend := ledger.Local{Repo: newButlerBareRepo(t)}
	tree := fakeTree{head: "headsha", files: []string{"a.go", "b.go"}}
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/900"

	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{
		Success:           true,
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"finding one","body":"repro","dedupTerms":["a.go:Foo"]}`,
		},
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(backend, tree, fc.AsIssueFiler(), func(dispatch.Chore) dispatch.Dispatcher { return d }, testPolicy(), func() time.Time { return now })

	out, err := r.Sweep([]string{"bugs"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if out.Kind != Swept || out.Chore != "bugs" || out.Filed != 1 || out.Promoted != 0 || out.Dropped != 0 {
		t.Errorf("Outcome = %+v, want Kind=Swept Chore=bugs Filed=1 Promoted=0 Dropped=0", out)
	}

	tip, err := backend.Read("bugs")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if tip.State.Phase != ledger.Done {
		t.Errorf("Phase = %q, want %q", tip.State.Phase, ledger.Done)
	}
}

// erroringBackend always fails Read, exercising the per-candidate wrap text
// in Sweep's loop (DayTotalsAll only ever calls History, never Read, so this
// doesn't trip until the candidate loop itself).
type erroringBackend struct{ ledger.Backend }

func (erroringBackend) Read(string) (ledger.Tip, error) { return ledger.Tip{}, errors.New("boom") }

// TestSweep_ReadLedgerError pins the wrap text for a per-candidate Read
// failure.
func TestSweep_ReadLedgerError(t *testing.T) {
	backend := erroringBackend{ledger.Local{Repo: newButlerBareRepo(t)}}
	tree := fakeTree{head: "headsha"}
	it := forge.NewFake().AsIssueFiler()

	r := New(backend, tree, it, func(dispatch.Chore) dispatch.Dispatcher { return dispatch.NewFake() }, testPolicy(), time.Now)

	_, err := r.Sweep([]string{"bugs"})
	if err == nil || err.Error() != `butler: read bugs ledger: boom` {
		t.Errorf("err = %v, want %q", err, `butler: read bugs ledger: boom`)
	}
}
