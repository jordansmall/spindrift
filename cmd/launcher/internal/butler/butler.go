// Package butler runs one Butler Chore sweep end to end -- claim, scope,
// Box, file, finish -- behind a single Sweep call (issue #3990). The pure
// due/scope logic stays in internal/chore and finding filing in
// internal/settle; promotion, budget room and the settle step are this
// package's own unexported seams.
package butler

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/settle"
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
	// PromotionClasses.
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
	// PromotionLabel is the Consumer's work dispatch label (LABEL), captured
	// before the butler kind's own (label-less) family swapped it out: the
	// label a promoted finding must carry for the work path to pick it up
	// (issue #3880).
	PromotionLabel string
	// FindingLabel is the butler kind row's provenance label, worn by every
	// filed finding.
	FindingLabel string
	// PatchLabel is the butler kind row's patch provenance label, worn beside
	// FindingLabel by a finding landed as a patch PR (ADR 0057).
	PatchLabel string
	// PatchPaths is BUTLER_PATCH_PATHS, the host allow/deny glob list a patch
	// candidate's diff paths must clear (ADR 0057).
	PatchPaths string
	// BranchPrefix is BRANCH_PREFIX, the input to forge.AgentBranchName for a
	// patch PR's head branch.
	BranchPrefix string
	// PatchMaxFiles is BUTLER_PATCH_MAX_FILES, the host limit on how many
	// files a patch candidate's diff may touch.
	PatchMaxFiles int
	// PatchMaxLines is BUTLER_PATCH_MAX_LINES, the host limit on changed
	// lines (added plus removed) across a patch candidate's whole diff.
	PatchMaxLines int
	// RecordsRoot is the checkout root whose Dispatch Records store a
	// records-scoped Chore reads (ADR 0062); empty means no store, so such a
	// Chore is never due.
	RecordsRoot string
	// Drivers is the Driver set the store ingests logs through.
	Drivers *driver.Set
	// Knobs resolves a row-owned knob's env name (BUTLER_TUNING_MIN_RECORDS and
	// the like) to its value (ADR 0068). The butler holds no per-Chore knob
	// field: the records source reads the min-records knob the Chore's row
	// names, and a Digest reads its own through Strategy.Knobs. Nil resolves
	// none, so every row knob takes its row default.
	Knobs recordsdigest.Lookup
}

// DefaultPatchPaths is BUTLER_PATCH_PATHS' schema default (lib/env-schema.nix
// butlerPatchPaths), pinned equal to it by the launcher's schema tests.
const DefaultPatchPaths = "docs/**,*.md,!docs/adr/**,!**/CLAUDE.md,!**/CONTEXT.md,!**/CONTRIBUTING.md,!**/AGENTS.md,!skills/**,!templates/**,!fragments/**,!.github/**"

// PatchForge is the host capability the patch rung needs to land a finding
// as a draft PR (ADR 0057, issue #4074, #4112): IssueLabeler falls a failed
// landing back to promote, and OpenPRForBranch plus BranchDeleter check a
// failed create for a PR the server made anyway before deleting the pushed
// branch.
type PatchForge interface {
	forge.BranchPusher
	forge.DraftPRCreator
	forge.IssueLabeler
	forge.BranchDeleter
	OpenPRForBranch(branch string) (forge.PR, bool, error)
}

// PatchGate is the work merge gate a landed patch PR is handed to once its
// Chore's Done Ledger commit has landed (ADR 0057, issue #4076), entered
// through its adopt-an-open-PR seam since the finding issue was never
// claimed. owner is the Chore's key, the one the daemon parses a butler
// child's records under (issue #4966). Production hands it a *settle.Settle
// built with settle.Config.Unclaimed: no fix passes for an issue nothing is
// dispatched against.
type PatchGate interface {
	SettlePatch(owner dispatchkey.Key, num, prURL string) settle.Settlement
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
	backend     ledger.Backend
	tree        Tree
	it          forge.IssueTracker
	newBox      func(dispatch.Chore) dispatch.Dispatcher
	policy      Policy
	now         func() time.Time
	patchForge  PatchForge
	patchGate   PatchGate
	mergeRead   MergeReader
	mergeCommit forge.MergeCommitReader
	warn        io.Writer
	out         io.Writer
	progress    settle.Progress
}

// New constructs a Runner. newBox builds the Dispatcher for one Chore run (a
// real *dispatch.Factory.NewChore in production, dispatch.Fake in tests).
// The patch rung stays off until a caller opts in with WithPatchForge.
func New(backend ledger.Backend, tree Tree, it forge.IssueTracker, newBox func(dispatch.Chore) dispatch.Dispatcher, policy Policy, now func() time.Time) *Runner {
	return &Runner{backend: backend, tree: tree, it: it, newBox: newBox, policy: policy, now: now, warn: os.Stderr, out: os.Stdout, progress: settle.StdoutProgress{}}
}

// WithPatchForge opts r into the patch rung (ADR 0057, issue #4074) and
// returns r. A non-nil f must come with a non-nil gate: settle calls
// gate.SettlePatch unguarded once f lands a patch.
func (r *Runner) WithPatchForge(f PatchForge, gate PatchGate) *Runner {
	r.patchForge = f
	r.patchGate = gate
	return r
}

// WithProgress sends the Runner's settle progress steps to p instead of stdout,
// and returns r.
func (r *Runner) WithProgress(p settle.Progress) *Runner {
	r.progress = p
	return r
}

// MergeReader reads whether a PR has merged, so a Records digest can show a
// finding's PR as merged after a human merged it (settle holds that PR, so no
// Record says so).
type MergeReader interface {
	PRState(url string) (forge.PRState, error)
}

// WithMergeReader opts r into reading PR merges for a Records digest's Prior
// findings, and returns r. A nil m leaves them to the Records alone; a nil
// commits leaves the merge commit unnamed.
func (r *Runner) WithMergeReader(m MergeReader, commits forge.MergeCommitReader) *Runner {
	r.mergeRead, r.mergeCommit = m, commits
	return r
}

// mergeLookup is r.mergeRead as the digest's recordsdigest.MergeLookup, nil without a
// reader. A failed read counts as not merged and is warned about; a rate limit
// also ends the lookups for the rest of the digest. A URL the forge client
// could read as a flag is never queried.
func (r *Runner) mergeLookup(choreName string) recordsdigest.MergeLookup {
	if r.mergeRead == nil {
		return nil
	}
	limited := false
	return func(url string) (string, bool) {
		if limited || !settle.QueryablePRURL(url) {
			return "", false
		}
		state, err := r.mergeRead.PRState(url)
		if err != nil {
			limited = errors.Is(err, forge.ErrRateLimit)
			fmt.Fprintf(r.warn, "    ?? %s: prior finding PR %s: %v\n", choreName, url, err)
			return "", false
		}
		if state != forge.PRMerged {
			return "", false
		}
		if r.mergeCommit != nil {
			commit, err := r.mergeCommit.MergeCommit(url)
			if err == nil {
				return commit, true
			}
			fmt.Fprintf(r.warn, "    ?? %s: could not read merge commit of %s: %v\n", choreName, url, err)
		}
		return "", true
	}
}

// Sweep picks the first due Chore out of chores (in order) and runs it, or
// reports why none is due (ADR 0056). A name absent from Policy.Chores is an
// error, since its Every and PromotionClasses would otherwise silently read
// as zero. tree.Head and now() are read once so "what's due" is consistent
// across the whole pass. The due check reads the backend's own view, which
// for a hosted Ledger is the mirror fetched once at run start (issue #3995):
// a rival's commit since then is invisible here but still wins the remote's
// compare-and-swap, so this run's Claim reports LostRace.
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
		// The source is built once here and handed to run, so a records-scoped
		// Chore's digest renders the very Records it was judged due on.
		src, err := r.scopeSourceFor(c, head, whenNow)
		if err != nil {
			return Outcome{}, fmt.Errorf("butler: %s: %w", c.Name, err)
		}
		verdict := src.Due(tip, recent, whenNow, room, cfg)
		if verdict != chore.Due {
			reasons = append(reasons, fmt.Sprintf("chore %q not due: %s", c.Name, verdict))
			// NextDue reads the budget day from now's Location, so it gets the
			// policy zone, as the day totals above did.
			at, onTipMove := src.NextDue(tip, recent, whenNow.In(r.policy.Zone), room, cfg)
			// (zero, false): nothing time- or head-based lifts it, so there is
			// nothing to tell the daemon.
			if !at.IsZero() || onTipMove {
				answers = append(answers, answer{name: c.Name, next: report.NextDue{At: at, OnTipMove: onTipMove}})
			}
			continue
		}
		return r.run(c, src, tip, whenNow, room)
	}

	// Reported only on this exit: the daemon reads not_due records when the
	// child ends not-due, and a later candidate that ran leaves it nothing to
	// park.
	for _, a := range answers {
		report.NotDue(dispatchkey.Chore(a.name), a.next)
	}

	return Outcome{Kind: NotDue, Reasons: reasons}, nil
}

// choreNames returns every Policy.Chores name: budgets sum across every
// enabled Chore, not just this Sweep's candidates.
func (p Policy) choreNames() []string {
	names := make([]string, len(p.Chores))
	for i, c := range p.Chores {
		names[i] = c.Name
	}
	return names
}

// run claims c's Ledger (already read as tip), has src Prepare this run's
// scan Scope and Box input, dispatches one Box, and settles the result (ADR
// 0056). claimedAt stamps the claim at the instant its due decision was made.
// Whether settle wrote the done commit is read off its return, never by
// re-reading the Ledger.
func (r *Runner) run(c chore.Chore, src scopeSource, tip ledger.Tip, claimedAt time.Time, room chore.Room) (Outcome, error) {
	choreName := c.Name

	// Staged before the claim, so a failure leaves the Ledger untouched and the
	// next sweep retries.
	if err := src.Stage(); err != nil {
		return Outcome{}, fmt.Errorf("butler: %s: %w", choreName, err)
	}

	claim, err := ledger.Claim(r.backend, choreName, tip, ledger.ClaimedBy{Host: r.policy.Host, Slot: 0, Start: claimedAt})
	if err != nil {
		if errors.Is(err, ledger.ErrLostRace) {
			return Outcome{Kind: LostRace, Chore: choreName}, nil
		}
		return Outcome{}, fmt.Errorf("butler: claim %s: %w", choreName, err)
	}

	p, err := src.Prepare(claim)
	if err != nil {
		return Outcome{}, fmt.Errorf("butler: %s: %w", choreName, err)
	}
	// A nil patchForge zeroes patchesPerDay regardless of
	// BUTLER_MAX_PATCHES_PER_DAY: newPromotion's patchEnabled is the only
	// place that decides "on", so settle needs no nil check (issue #4074).
	patchesPerDay := r.policy.Budgets.MaxPatchesPerDay
	if r.patchForge == nil {
		patchesPerDay = 0
	}
	promo := newPromotion(c.PromotionClasses, r.policy.PromotionMaxFiles, r.policy.Budgets.MaxPromotionsPerDay, r.policy.PromotionLabel, patchPolicy{
		classes:  c.PatchClasses,
		perDay:   patchesPerDay,
		paths:    r.policy.PatchPaths,
		maxFiles: r.policy.PatchMaxFiles,
		maxLines: r.policy.PatchMaxLines,
	})

	// Settle re-checks a finding's class against promo regardless; the Box
	// only sees a list when there is room to spend.
	var promotionClasses []string
	if promo.enabled && room.Promotions > 0 {
		promotionClasses = c.PromotionClasses
	}
	// room.Patches alone stays positive with no patchForge (issue #4074), so
	// patchEnabled is required too. promotionClasses must be non-empty
	// because the relay fragment only honours CHORE_PATCH_CLASSES for a class
	// also on CHORE_CLASSES.
	var patchClasses []string
	if promo.patchEnabled && len(promotionClasses) > 0 && room.Patches > 0 {
		patchClasses = c.PatchClasses
	}
	d := r.newBox(dispatch.Chore{Name: choreName, Branch: r.policy.Branch, Scope: p.scope, PromotionClasses: promotionClasses, ClassList: c.ClassList, PatchClasses: patchClasses, MaxFindings: room.Findings, Input: p.input, ClaimTime: p.claimTime})
	defer d.Close()
	step := newSettleRun(r.it, r.backend, choreName, claim, p.scope, r.now, room, promo, patchRung{tree: r.tree, forge: r.patchForge, branchPrefix: r.policy.BranchPrefix, base: r.policy.Branch, gate: r.patchGate}, kindLabels{finding: r.policy.FindingLabel, patch: r.policy.PatchLabel}, r.out)
	step.progress = r.progress
	if p.arm != nil {
		if err := p.arm(d, step); err != nil {
			return Outcome{}, err
		}
	}
	if c.FindingLabel != "" {
		step.labels = []string{c.FindingLabel}
	}
	settle := func(result dispatch.Result) settled { return step.settle(d, result) }
	s := dispatch.Route(d.Run(),
		func() settled {
			// A live run already holds this Chore's Box (#562): settling would
			// read the empty result as a crash (#3705).
			fmt.Fprintf(r.out, "    #%s  status=already-in-flight  note=live run continues\n", dispatchkey.Chore(choreName))
			return settled{}
		},
		settle, settle)
	if !s.done {
		return Outcome{Kind: ClaimLeft, Chore: choreName}, nil
	}
	return Outcome{Kind: Swept, Chore: choreName, Filed: s.filed, Promoted: s.promoted, Dropped: s.dropped, Patched: s.patched}, nil
}
