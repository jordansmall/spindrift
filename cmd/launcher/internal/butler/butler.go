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
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/report"
)

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
	// PromotionLabel is the Consumer's configured work dispatch label
	// (LABEL), captured before the butler kind's own (label-less) family
	// swapped it out -- the label a promoted finding must carry for the work
	// path to pick it up (issue #3880).
	PromotionLabel string
	// PatchPaths is BUTLER_PATCH_PATHS, the host allow/deny glob list a patch
	// candidate's diff paths must clear (ADR 0057).
	PatchPaths string
	// PatchMaxFiles is BUTLER_PATCH_MAX_FILES, the host limit on how many
	// files a patch candidate's diff may touch.
	PatchMaxFiles int
	// PatchMaxLines is BUTLER_PATCH_MAX_LINES, the host limit on changed
	// lines (added plus removed) across a patch candidate's whole diff.
	PatchMaxLines int
}

// DefaultPatchPaths is BUTLER_PATCH_PATHS' schema default (lib/env-schema.nix
// butlerPatchPaths), pinned equal to it by the launcher's schema tests.
const DefaultPatchPaths = "docs/**,*.md,!docs/adr/**,!**/CLAUDE.md,!**/CONTEXT.md,!**/CONTRIBUTING.md,!**/AGENTS.md,!skills/**,!templates/**,!fragments/**,!.github/**"

// PatchForge is the host capability the patch rung needs to land a finding
// as a draft PR (ADR 0057, issue #4074, #4112): push plus draft-PR create,
// forge.IssueLabeler to fall a failed landing back to promote, and
// forge.PRForge's OpenPRForBranch plus forge.BranchDeleter to check a
// failed create for a PR the server made anyway before deleting the pushed
// branch. See butlerPatchForge (cmd/launcher/butler.go) for when the rung
// is on. A Runner built with a nil PatchForge never reaches decide's patch
// branch at all -- see WithPatchForge.
type PatchForge interface {
	AgentBranch(num string) string
	forge.BranchPusher
	forge.DraftPRCreator
	forge.IssueLabeler
	forge.BranchDeleter
	OpenPRForBranch(branch string) (forge.PR, bool, error)
}

// PatchGate is the work merge gate a landed patch PR is handed to once its
// finding issue's Done Ledger commit has landed (ADR 0057, issue #4076): the
// same CI-watch/merge machinery a normal work dispatch settles through, but
// entered through its adopt-an-open-PR seam since the finding issue was
// never claimed by a Dispatcher -- d is always nil and gen is always 0 at
// the call site. Production hands it a *settle.Settle built with Unclaimed
// true (settle.Config.Unclaimed), which means no fix passes for an issue
// nothing is dispatched against.
type PatchGate interface {
	SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string)
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
	// commit (a crashed run, or a Ledger write that failed), or the Box was
	// skipped because a live run already holds the Chore (issue #3705): the
	// claim stands for the next run to resume from.
	ClaimLeft
)

// Outcome is Sweep's result.
type Outcome struct {
	Kind  Kind
	Chore string
	// Reasons holds one `chore %q not due: %s` line per candidate, only set
	// when Kind is NotDue.
	Reasons []string
	// Filed, Promoted, Dropped, Patched are only set when Kind is Swept.
	Filed, Promoted, Dropped, Patched int
}

// Runner sweeps one due Chore per Sweep call.
type Runner struct {
	backend    ledger.Backend
	tree       Tree
	it         forge.IssueTracker
	newBox     func(dispatch.Chore) dispatch.Dispatcher
	policy     Policy
	now        func() time.Time
	patchForge PatchForge
	patchGate  PatchGate
}

// New constructs a Runner. newBox builds the Dispatcher for one Chore run (a
// real *dispatch.Factory.NewChore in production, dispatch.Fake in tests).
// The patch rung stays off until a caller opts in with WithPatchForge.
func New(backend ledger.Backend, tree Tree, it forge.IssueTracker, newBox func(dispatch.Chore) dispatch.Dispatcher, policy Policy, now func() time.Time) *Runner {
	return &Runner{backend: backend, tree: tree, it: it, newBox: newBox, policy: policy, now: now}
}

// WithPatchForge opts r into the patch rung (ADR 0057, issue #4074): f backs
// decide's patch branch for every subsequent Sweep call, and gate is the
// work merge gate a landed patch PR is handed to once its Done Ledger commit
// lands (issue #4076). Returns r so a caller can chain it onto New. A non-nil
// f must come with a non-nil gate -- Runner.run calls gate.SettleAdopted
// unguarded once f lands a patch. If WithPatchForge is never called,
// r.patchForge stays nil -- see Runner.run for what that gates.
func (r *Runner) WithPatchForge(f PatchForge, gate PatchGate) *Runner {
	r.patchForge = f
	r.patchGate = gate
	return r
}

// Sweep picks the first due Chore out of chores (in order) and runs it, or
// reports why none is due (ADR 0056). chores is either the single name given
// on --chore, or every Policy.Chores entry in configured order when none was
// given; a name absent from Policy.Chores is an error, since its Every and
// Classes would otherwise silently read as zero. tree.Head is read once up
// front, and the due check (internal/chore.Check) shares a single now()
// reading across every candidate, so the picture of "what's due" is
// consistent across the whole pass rather than drifting chore to chore. The
// due check -- today's totals plus each candidate's Read/History -- reads
// the backend's own view, which for a hosted Ledger is the mirror the
// factory fetched once at run start (issue #3995), so it costs no fetch. A
// rival's commit since then is invisible here but still wins the remote's
// compare-and-swap, so this run's Claim reports Outcome{Kind: LostRace}
// rather than overwriting it.
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

	today, err := ledger.DayTotalsAll(r.backend, r.policy.choreNames(), whenNow.In(r.policy.Zone))
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: total today's ledgers: %w", err)
	}
	room := r.policy.Budgets.Room(today)

	var reasons []string
	type answer struct {
		name string
		next report.NextDue
	}
	var answers []answer
	for _, c := range candidates {
		tip, err := r.backend.Read(c.Name)
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger: %w", c.Name, err)
		}
		recent, err := r.backend.History(c.Name, whenNow.Add(-c.Every))
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: read %s ledger history: %w", c.Name, err)
		}
		cfg := chore.DueConfig{Every: c.Every, ClaimTimeout: r.policy.ClaimTimeout}
		verdict := chore.Check(tip, recent, head, whenNow, room, cfg)
		if verdict != chore.Due {
			reasons = append(reasons, fmt.Sprintf("chore %q not due: %s", c.Name, verdict))
			// NextDue reads the budget day from now's Location, so it gets the
			// policy zone, as the day totals above did.
			at, onTipMove := chore.NextDue(tip, recent, head, whenNow.In(r.policy.Zone), room, cfg)
			// (zero, false): nothing time- or head-based lifts it, so there is
			// nothing to tell the daemon.
			if !at.IsZero() || onTipMove {
				answers = append(answers, answer{name: c.Name, next: report.NextDue{At: at, OnTipMove: onTipMove}})
			}
			continue
		}
		return r.run(c, tip, head, whenNow, room)
	}

	// Reported only on this exit: the daemon reads not_due records when the
	// child ends not-due, and a later candidate that ran leaves it nothing to
	// park.
	for _, a := range answers {
		report.NotDue(dispatchkey.Chore(a.name), a.next)
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
// due decision was made; r.now is called fresh at settle time instead. room
// is Sweep's own chore.Room, handed to the Box and to settle rather than
// re-walked. Once the Box has run, whether its settle step actually wrote
// the done commit (Box success) or left the claim standing (Box crash, ADR
// 0056) is read off the settle step's own return, never by re-reading the
// Ledger -- a crashed run's settle writes nothing at all, so there would be
// nothing new there to read back anyway.
func (r *Runner) run(c chore.Chore, tip ledger.Tip, head string, claimedAt time.Time, room chore.Room) (Outcome, error) {
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
	// A nil patchForge means this Runner never opted into the patch rung
	// (WithPatchForge), so patchesPerDay reads as 0 regardless of the
	// Consumer's own BUTLER_MAX_PATCHES_PER_DAY -- newPromotion's patchEnabled
	// derivation is the only place that decides "on", so settle never needs
	// its own patchForge-nil check (issue #4074).
	patchesPerDay := r.policy.Budgets.MaxPatchesPerDay
	if r.patchForge == nil {
		patchesPerDay = 0
	}
	promo := newPromotion(c.Classes, r.policy.PromotionMaxFiles, r.policy.Budgets.MaxPromotionsPerDay, r.policy.PromotionLabel, patchPolicy{
		classes:  c.PatchClasses,
		perDay:   patchesPerDay,
		paths:    r.policy.PatchPaths,
		maxFiles: r.policy.PatchMaxFiles,
		maxLines: r.policy.PatchMaxLines,
	})

	// The Box only ever sees a class list when promotion is on and today's
	// promotion room is actually > 0: with nothing left to spend this run,
	// there is nothing useful to tell the Box, and settle re-checks a
	// finding's class against promo itself regardless.
	var classes []string
	if promo.enabled && room.Promotions > 0 {
		classes = c.Classes
	}
	// promo.patchEnabled is required here, not just room.Patches > 0: room.Patches
	// derives from the Consumer's BUTLER_MAX_PATCHES_PER_DAY budget and today's
	// ledger alone (chore.Budgets.Room), so it stays positive even when this
	// Runner has no patchForge (issue #4074) -- patchesPerDay above is what
	// actually folds that in, and patchEnabled is the only place that reads
	// patchesPerDay. It also requires len(classes) > 0 -- the promotion class
	// list just above -- because the relay fragment only ever honours
	// CHORE_PATCH_CLASSES for a class also present on CHORE_CLASSES: with
	// promotion off or its room spent, classes is empty and a patch-class
	// list alone would tell the Box about candidates the relay fragment says
	// to omit -class for.
	var patchClasses []string
	if promo.patchEnabled && len(classes) > 0 && room.Patches > 0 {
		patchClasses = c.PatchClasses
	}
	d := r.newBox(dispatch.Chore{Name: choreName, Branch: r.policy.Branch, Scope: scope, Classes: classes, ClassList: c.ClassList, PatchClasses: patchClasses, MaxFindings: room.Findings})
	defer d.Close()
	step := newSettleRun(r.it, r.backend, choreName, claim, scope, r.now, room, promo, patchRung{tree: r.tree, forge: r.patchForge, base: r.policy.Branch, gate: r.patchGate})
	settle := func(result dispatch.Result) settled { return step.settle(d, result) }
	s := dispatch.Route(d.Run(),
		func() settled {
			// A live run already holds this Chore's Box (#562): settling would
			// read the empty result as a crash (#3705). Leave its claim
			// standing and don't retry.
			fmt.Printf("    #%s  status=already-in-flight  note=live run continues\n", dispatchkey.Chore(choreName))
			return settled{}
		},
		settle, settle)
	if !s.done {
		return Outcome{Kind: ClaimLeft, Chore: choreName}, nil
	}
	return Outcome{Kind: Swept, Chore: choreName, Filed: s.filed, Promoted: s.promoted, Dropped: s.dropped, Patched: s.patched}, nil
}
