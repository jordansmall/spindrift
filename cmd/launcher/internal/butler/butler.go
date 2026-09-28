// Package butler runs one Butler Chore sweep end to end -- claim, scope,
// Box, file, finish -- behind a single Sweep call (issue #3990). It replaces
// cmd/launcher's runButler/runOneButlerChore, keeping the pure Chore due/scope
// logic in internal/chore and the finding-filing/promotion logic in
// internal/settle, and owns only the orchestration between them.
package butler

import (
	"errors"
	"fmt"
	"os"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
)

// Tree is the checkout a Butler scans -- a thin seam over internal/chore's
// git-shelling Head/TrackedFiles so Sweep is testable without a real repo.
type Tree interface {
	Head() (string, error)
	TrackedFiles(commit string) ([]string, error)
}

// GitTree is Tree's production implementation: Repo/Branch identify the bare
// Accumulation repo and branch internal/chore's git wrapper shells out to.
type GitTree struct {
	Repo, Branch string
}

// Head resolves g.Branch's tip in g.Repo.
func (g GitTree) Head() (string, error) { return chore.Head(g.Repo, g.Branch) }

// TrackedFiles returns every path git tracks in commit's tree.
func (g GitTree) TrackedFiles(commit string) ([]string, error) {
	return chore.TrackedFiles(g.Repo, commit)
}

// Policy is a Runner's resolved butler knobs (ADR 0056), mirroring
// cmd/launcher's butlerPolicy/butlerRun.
type Policy struct {
	// Branch is the branch a run's Box clones and scans (forwarded as
	// dispatch.Chore.Branch).
	Branch string
	// Host identifies this claimant in the Ledger's ClaimedBy.
	Host string
	// Every reports a Chore's due interval (BUTLER_EVERY, with per-Chore
	// overrides already resolved).
	Every func(chore string) time.Duration
	// ClaimTimeout is the age past which a live claim is treated as stale.
	ClaimTimeout time.Duration
	// Budgets caps a local day's claims/findings/tokens across every enabled
	// Chore (ADR 0056).
	Budgets chore.Budgets
	// Zone is DAEMON_AWAKE_WINDOW's zone, where the budgets' day starts.
	Zone *time.Location
	// Enabled is every BUTLER_CHORES entry, not just this Sweep's
	// candidates: budgets are summed across all of them, even under a single
	// --chore.
	Enabled []string
	// Classes is BUTLER_CHORE_CLASSES parsed: each enabled Chore's host-side
	// finding-class allow-list (issue #3880).
	Classes map[string][]string
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
// on --chore, or every BUTLER_CHORES entry in configured order when none was
// given. tree.Head is read once up front, and the due check
// (internal/chore.Check) shares a single now() reading across every
// candidate, so the picture of "what's due" is consistent across the whole
// pass rather than drifting chore to chore. The due check -- today's totals
// plus each candidate's Read/History -- shares one ledger.Snapshot, so a
// Remote backend costs one fetch for the whole pass rather than one per
// candidate; the Claim and the settle step still go through the backend
// directly, so they see a fresh tip. The tip handed to the run is the
// snapshot's, so a rival commit since then just loses the Claim
// compare-and-swap (Outcome{Kind: LostRace}) rather than being overwritten.
func (r *Runner) Sweep(chores []string) (Outcome, error) {
	if len(chores) == 0 {
		return Outcome{}, errors.New("butler: no chores to check")
	}

	head, err := r.tree.Head()
	if err != nil {
		return Outcome{}, err
	}
	whenNow := r.now()

	snap, err := ledger.Snapshot(r.backend)
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: snapshot ledger: %w", err)
	}

	today, err := ledger.DayTotalsAll(snap, r.policy.Enabled, whenNow.In(r.policy.Zone))
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: total today's ledgers: %w", err)
	}

	var reasons []string
	for _, choreName := range chores {
		tip, err := snap.Read(choreName)
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger: %w", choreName, err)
		}
		interval := r.policy.Every(choreName)
		recent, err := snap.History(choreName, whenNow.Add(-interval))
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger history: %w", choreName, err)
		}
		verdict := chore.Check(tip, recent, head, whenNow, today, chore.DueConfig{Every: interval, ClaimTimeout: r.policy.ClaimTimeout, Budgets: r.policy.Budgets})
		if verdict != chore.Due {
			reasons = append(reasons, fmt.Sprintf("chore %q not due: %s", choreName, verdict))
			continue
		}
		return r.run(choreName, tip, head, whenNow)
	}

	return Outcome{Kind: NotDue, Reasons: reasons}, nil
}

// run claims choreName's Ledger (already read as tip, at head), computes this
// run's scan Scope (internal/chore.NextScope), dispatches one Box through
// r.newBox, and settles the result (ADR 0056). claimedAt is Sweep's whenNow,
// reused for ClaimedBy.Start so the claim is stamped at the same instant its
// due decision was made; r.now is called fresh at settle time instead. Once
// the Box has run, whether its settle step actually wrote the done commit
// (Box success) or left the claim standing (Box crash, ADR 0056) is read off
// the settle step's own return, never by re-reading the Ledger -- a crashed
// run's settle writes nothing at all, so there would be nothing new there to
// read back anyway.
func (r *Runner) run(choreName string, tip ledger.Tip, head string, claimedAt time.Time) (Outcome, error) {
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
	promo := r.promotionPolicy(choreName)

	// The Box only ever sees promo.Classes when promotion is actually on
	// (promo.Room set, i.e. MaxPromotionsPerDay > 0): with promotion off
	// nothing can promote regardless of class, so there is nothing useful to
	// tell the Box, and settle re-checks a finding's class against
	// promo.Classes itself.
	var classes []string
	if promo.Room != nil {
		classes = promo.Classes
	}
	d := r.newBox(dispatch.Chore{Name: choreName, Branch: r.policy.Branch, Scope: scope, Classes: classes})
	defer d.Close()
	result := d.Run()

	step := newSettleRun(r.it, r.backend, choreName, claim, scope, r.now, r.policy.Budgets.MaxFindingsPerSweep, promo)
	s := step.settle(d, dispatchkey.Chore(choreName).String(), result)
	if !s.done {
		return Outcome{Kind: ClaimLeft, Chore: choreName}, nil
	}
	return Outcome{Kind: Swept, Chore: choreName, Filed: s.filed, Promoted: s.promoted, Dropped: s.dropped}, nil
}

// promotionPolicy builds choreName's PromotionPolicy (issue #3880): Classes
// and MaxFiles come straight from r.policy, and Room -- when promotion is on
// at all -- re-walks today's Ledger at settle time (r.backend, r.policy.Enabled,
// r.now().In(r.policy.Zone)), the same call Sweep makes at run start, so a
// promotion whose done commit already landed is counted here. Room takes its
// own fresh ledger.Snapshot each call rather than reusing Sweep's, since it
// must see promotions that landed between run start and settle. Like ADR
// 0056's other budgets this is a soft cap, not a hard one: two runs settling
// at the same moment can each read the same total and both spend it. A
// Snapshot or DayTotalsAll error fails closed (0 room, a warning to stderr)
// rather than promoting on a total it could not compute.
func (r *Runner) promotionPolicy(choreName string) PromotionPolicy {
	pp := PromotionPolicy{
		Classes:  r.policy.Classes[choreName],
		MaxFiles: r.policy.PromotionMaxFiles,
		Label:    r.policy.PromotionLabel,
	}
	if r.policy.MaxPromotionsPerDay <= 0 {
		return pp
	}
	perDay := r.policy.MaxPromotionsPerDay
	backend, enabled, zone, now := r.backend, r.policy.Enabled, r.policy.Zone, r.now
	pp.Room = func() int {
		var totals ledger.Totals
		snap, err := ledger.Snapshot(backend)
		if err == nil {
			totals, err = ledger.DayTotalsAll(snap, enabled, now().In(zone))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "butler: promotion room: %s\n", err)
			return 0
		}
		remaining := perDay - totals.Promoted
		if remaining < 0 {
			remaining = 0
		}
		return remaining
	}
	return pp
}
