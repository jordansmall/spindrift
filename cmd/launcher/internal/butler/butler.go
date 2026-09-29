// Package butler runs one Butler Chore sweep end to end -- claim, scope,
// Box, file, finish -- behind a single Sweep call (issue #3990). The pure
// due/scope logic stays in internal/chore and finding filing in
// internal/settle; promotion, budget room and the settle step are this
// package's own unexported seams.
package butler

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
)

// Tree is the checkout a Butler scans -- a thin seam over internal/chore's
// git-shelling Head/TrackedFiles so Sweep is testable without a real repo.
type Tree interface {
	Head(branch string) (string, error)
	TrackedFiles(commit string) ([]string, error)
}

// GitTree is Tree's production implementation: Repo identifies the bare
// Accumulation repo internal/chore's git wrapper shells out to. The branch
// comes from Sweep's own r.policy.Branch, not a field here, so it's never
// passed twice (issue #3990).
type GitTree struct {
	Repo string
}

// Head resolves branch's tip in g.Repo.
func (g GitTree) Head(branch string) (string, error) { return chore.Head(g.Repo, branch) }

// TrackedFiles returns every path git tracks in commit's tree.
func (g GitTree) TrackedFiles(commit string) ([]string, error) {
	return chore.TrackedFiles(g.Repo, commit)
}

// Policy is a Runner's resolved butler knobs (ADR 0056).
type Policy struct {
	// Branch is the branch a run's Box clones and scans (forwarded as
	// dispatch.Chore.Branch).
	Branch string
	// Host identifies this claimant in the Ledger's ClaimedBy.
	Host string
	// Chores is every BUTLER_CHORES entry resolved by chore.Load, not just
	// this Sweep's candidates: budgets are summed across all of them, even
	// under a single --chore, and each Chore carries its own Every and
	// Classes.
	Chores []chore.Chore
	// ClaimTimeout is the age past which a live claim is treated as stale.
	ClaimTimeout time.Duration
	// Budgets caps a local day's claims/findings/tokens across every enabled
	// Chore (ADR 0056).
	Budgets chore.Budgets
	// Zone is DAEMON_AWAKE_WINDOW's zone, where the budgets' day starts.
	Zone *time.Location
	// PromotionMaxFiles is BUTLER_PROMOTION_MAX_FILES, the host limit on how
	// many files an auto-promoted finding may touch.
	PromotionMaxFiles int
	// MaxPromotionsPerDay is BUTLER_MAX_PROMOTIONS_PER_DAY; 0 (the default)
	// means promotion is off regardless of Classes.
	MaxPromotionsPerDay int
	// PromotionLabel is the Consumer's configured work dispatch label
	// (LABEL), captured before the butler kind's own (label-less) family
	// swapped it out -- the label a promoted finding must carry for the work
	// path to pick it up (issue #3880).
	PromotionLabel string
}

// Kind names which of Sweep's four outcomes happened.
type Kind int

const (
	// NotDue means no candidate chore was due; Reasons names why each one
	// wasn't.
	NotDue Kind = iota
	// LostRace means the chosen chore's Claim lost its compare-and-swap to a
	// rival claim between Sweep's due check and the Claim call.
	LostRace
	// Swept means a Box ran and the done Ledger commit landed.
	Swept
	// ClaimLeft means a Box ran but its settle step never wrote a done
	// commit (a crashed run, or a Ledger write that failed): the claim
	// stands for the next run to resume from.
	ClaimLeft
)

// Outcome is Sweep's result.
type Outcome struct {
	Kind  Kind
	Chore string
	// Reasons holds one `chore %q not due: %s` line per candidate, only set
	// when Kind is NotDue.
	Reasons []string
	// Filed, Promoted, Dropped are only set when Kind is Swept.
	Filed, Promoted, Dropped int
}

// Runner sweeps one due Chore per Sweep call.
type Runner struct {
	backend ledger.Backend
	tree    Tree
	it      forge.IssueTracker
	newBox  func(dispatch.Chore) dispatch.Dispatcher
	policy  Policy
	now     func() time.Time
}

// New constructs a Runner. newBox builds the Dispatcher for one Chore run (a
// real *dispatch.Factory.NewChore in production, dispatch.Fake in tests).
func New(backend ledger.Backend, tree Tree, it forge.IssueTracker, newBox func(dispatch.Chore) dispatch.Dispatcher, policy Policy, now func() time.Time) *Runner {
	return &Runner{backend: backend, tree: tree, it: it, newBox: newBox, policy: policy, now: now}
}

// Sweep picks the first due Chore out of chores (in order) and runs it, or
// reports why none is due (ADR 0056). chores is either the single name given
// on --chore, or every Policy.Chores entry in configured order when none was
// given; a name absent from Policy.Chores is an error, since its Every and
// Classes would otherwise silently read as zero. tree.Head is read once up
// front, and the due check (internal/chore.Check) shares a single now()
// reading across every candidate, so the picture of "what's due" is
// consistent across the whole pass rather than drifting chore to chore. The
// due check -- today's totals plus each candidate's Read/History -- shares
// one ledger.Snapshot, so a Remote backend costs one fetch for the whole pass
// rather than one per candidate; the Claim and the settle step still go
// through the backend directly, so they see a fresh tip. The tip handed to
// the run is the snapshot's, so a rival commit since then just loses the
// Claim compare-and-swap (Outcome{Kind: LostRace}) rather than being
// overwritten.
func (r *Runner) Sweep(chores []string) (Outcome, error) {
	if len(chores) == 0 {
		return Outcome{}, errors.New("butler: no chores to check")
	}
	candidates := make([]chore.Chore, len(chores))
	for i, name := range chores {
		j := slices.IndexFunc(r.policy.Chores, func(c chore.Chore) bool { return c.Name == name })
		if j < 0 {
			return Outcome{}, fmt.Errorf("butler: chore %q is not configured", name)
		}
		candidates[i] = r.policy.Chores[j]
	}

	head, err := r.tree.Head(r.policy.Branch)
	if err != nil {
		return Outcome{}, err
	}
	whenNow := r.now()

	snap, err := ledger.Snapshot(r.backend)
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: snapshot ledger: %w", err)
	}

	today, err := ledger.DayTotalsAll(snap, r.policy.choreNames(), whenNow.In(r.policy.Zone))
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: total today's ledgers: %w", err)
	}

	var reasons []string
	for _, c := range candidates {
		tip, err := snap.Read(c.Name)
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger: %w", c.Name, err)
		}
		recent, err := snap.History(c.Name, whenNow.Add(-c.Every))
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger history: %w", c.Name, err)
		}
		verdict := chore.Check(tip, recent, head, whenNow, today, chore.DueConfig{Every: c.Every, ClaimTimeout: r.policy.ClaimTimeout, Budgets: r.policy.Budgets})
		if verdict != chore.Due {
			reasons = append(reasons, fmt.Sprintf("chore %q not due: %s", c.Name, verdict))
			continue
		}
		return r.run(c, tip, head, whenNow)
	}

	return Outcome{Kind: NotDue, Reasons: reasons}, nil
}

// choreNames returns every Policy.Chores name, in order -- the slice
// ledger.DayTotalsAll needs to sum budgets across every enabled Chore, not
// just this Sweep's candidates.
func (p Policy) choreNames() []string {
	names := make([]string, len(p.Chores))
	for i, c := range p.Chores {
		names[i] = c.Name
	}
	return names
}

// run claims c's Ledger (already read as tip, at head), computes this
// run's scan Scope (internal/chore.NextScope), dispatches one Box through
// r.newBox, and settles the result (ADR 0056). claimedAt is Sweep's whenNow,
// reused for ClaimedBy.Start so the claim is stamped at the same instant its
// due decision was made; r.now is called fresh at settle time instead. Once
// the Box has run, whether its settle step actually wrote the done commit
// (Box success) or left the claim standing (Box crash, ADR 0056) is read off
// the settle step's own return, never by re-reading the Ledger -- a crashed
// run's settle writes nothing at all, so there would be nothing new there to
// read back anyway.
func (r *Runner) run(c chore.Chore, tip ledger.Tip, head string, claimedAt time.Time) (Outcome, error) {
	choreName := c.Name
	files, err := r.tree.TrackedFiles(head)
	if err != nil {
		return Outcome{}, err
	}

	claim, err := ledger.Claim(r.backend, choreName, tip, ledger.ClaimedBy{Host: r.policy.Host, Slot: 0, Start: claimedAt})
	if err != nil {
		if errors.Is(err, ledger.ErrLostRace) {
			// Another worker claimed it between the due check's Read and this
			// Claim: exactly the "nothing to do right now" case, not a real
			// error.
			return Outcome{Kind: LostRace, Chore: choreName}, nil
		}
		return Outcome{}, fmt.Errorf("butler: claim %s: %w", choreName, err)
	}

	scope := chore.NextScope(claim.State, head, files, chore.DefaultSliceSize)
	promo := newPromotion(c.Classes, r.policy.PromotionMaxFiles, r.policy.MaxPromotionsPerDay, r.policy.PromotionLabel)

	// The Box only ever sees a class list when promotion is actually on:
	// with promotion off nothing can promote regardless of class, so there
	// is nothing useful to tell the Box, and settle re-checks a finding's
	// class against promo's own allow-list itself.
	var classes []string
	if promo.enabled {
		classes = c.Classes
	}
	d := r.newBox(dispatch.Chore{Name: choreName, Branch: r.policy.Branch, Scope: scope, Classes: classes})
	defer d.Close()
	result := d.Run()

	room := newDayRoom(r.policy)
	step := newSettleRun(r.it, r.backend, choreName, claim, scope, r.now, r.policy.Budgets.MaxFindingsPerSweep, promo, room)
	s := step.settle(d, result)
	if !s.done {
		return Outcome{Kind: ClaimLeft, Chore: choreName}, nil
	}
	return Outcome{Kind: Swept, Chore: choreName, Filed: s.filed, Promoted: s.promoted, Dropped: s.dropped}, nil
}
