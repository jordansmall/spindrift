package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/settle"
)

// butlerEveryConfig is BUTLER_EVERY parsed (schema key butlerEvery, ADR
// 0056): a bare default interval plus per-Chore overrides. With no bare
// token, the default is butlerEveryDefault; a bare "0" token, unlike an
// absent one, still means "no interval".
type butlerEveryConfig struct {
	dflt      time.Duration
	overrides map[string]time.Duration
}

// For returns chore's interval: its override, else the bare default.
func (c butlerEveryConfig) For(chore string) time.Duration {
	if d, ok := c.overrides[chore]; ok {
		return d
	}
	return c.dflt
}

// butlerEveryDefault is the interval a Chore gets when BUTLER_EVERY carries
// no bare default token: without this fallback, every enabled Chore but the
// ones named in an override token would be due on every poll.
const butlerEveryDefault = 6 * time.Hour

// parseButlerEvery parses BUTLER_EVERY's grammar: space-separated tokens,
// each either a bare Go time.ParseDuration string (the default interval for
// every enabled Chore not otherwise overridden) or `<chore>=<duration>` (a
// per-Chore override), e.g. "6h docs-drift=168h". A value with no bare token
// falls back to butlerEveryDefault. Rejects an unparseable or negative
// duration, more than one bare default token, a duplicate override for the
// same Chore, and an empty chore name in a `=<duration>` token.
func parseButlerEvery(value string) (butlerEveryConfig, error) {
	cfg := butlerEveryConfig{overrides: make(map[string]time.Duration)}
	haveDefault := false
	for _, tok := range strings.Fields(value) {
		chore, durStr, isOverride := strings.Cut(tok, "=")
		if isOverride {
			if chore == "" {
				return butlerEveryConfig{}, fmt.Errorf("butler: BUTLER_EVERY: empty chore name in %q", tok)
			}
			if _, exists := cfg.overrides[chore]; exists {
				return butlerEveryConfig{}, fmt.Errorf("butler: BUTLER_EVERY: duplicate override for chore %q", chore)
			}
			d, err := parseNonNegativeDuration(durStr)
			if err != nil {
				return butlerEveryConfig{}, fmt.Errorf("butler: BUTLER_EVERY: chore %q: %w", chore, err)
			}
			cfg.overrides[chore] = d
			continue
		}
		if haveDefault {
			return butlerEveryConfig{}, fmt.Errorf("butler: BUTLER_EVERY: more than one bare default token in %q", value)
		}
		d, err := parseNonNegativeDuration(tok)
		if err != nil {
			return butlerEveryConfig{}, fmt.Errorf("butler: BUTLER_EVERY: %w", err)
		}
		cfg.dflt = d
		haveDefault = true
	}
	if !haveDefault {
		cfg.dflt = butlerEveryDefault
	}
	return cfg, nil
}

// checkOverrides rejects any override in c naming a Chore absent from
// butlerChores -- otherwise a typo in the override key silently no-ops
// instead of ever firing. Errors on the first offender in sorted key order,
// for a deterministic message across runs.
func (c butlerEveryConfig) checkOverrides(butlerChores string) error {
	keys := make([]string, 0, len(c.overrides))
	for chore := range c.overrides {
		keys = append(keys, chore)
	}
	sort.Strings(keys)
	for _, chore := range keys {
		if !choreEnabled(butlerChores, chore) {
			return fmt.Errorf("butler: BUTLER_EVERY: override for chore %q, which is not enabled (BUTLER_CHORES=%q)", chore, butlerChores)
		}
	}
	return nil
}

// parseNonNegativeDuration parses s as a Go duration, rejecting a negative
// result; parseButlerEvery's shared validation for both its bare-default and
// per-Chore-override tokens.
func parseNonNegativeDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return d, nil
}

// parseButlerClaimTimeout parses BUTLER_CLAIM_TIMEOUT (schema key
// butlerClaimTimeout, ADR 0056): a Go duration, required to be strictly
// positive since a zero or negative timeout would make every claim
// immediately stale.
func parseButlerClaimTimeout(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("butler: BUTLER_CLAIM_TIMEOUT: invalid duration %q: %w", value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("butler: BUTLER_CLAIM_TIMEOUT: must be > 0, got %q", value)
	}
	return d, nil
}

// butlerSlot is the Ledger claim's Slot (and generation) for the one-shot
// butler command: it runs outside the Daemon pool entirely, so there is no
// real slot or generation to record.
const butlerSlot = 0

// butlerRun groups the adjacent string params runButler takes -- the scan
// target (repo/branch) and the claimant host -- easy to mis-order as bare
// positionals.
type butlerRun struct {
	repo, branch, host string
}

// butlerPolicy is runButler's resolved butler knobs (ADR 0056).
type butlerPolicy struct {
	every        butlerEveryConfig
	claimTimeout time.Duration
	budgets      butler.Budgets
	// zone is DAEMON_AWAKE_WINDOW's zone, where the budgets' day starts.
	zone *time.Location
	// enabled is every BUTLER_CHORES entry, not just this pass's candidates:
	// budgets are summed across all of them, even under --chore.
	enabled []string
}

// choreEnabled reports whether chore appears in list, BUTLER_CHORES's
// space-separated value (schema key butlerChores). A Consumer opts a Chore in
// by naming it there; the default "" enables none (spec #3870).
func choreEnabled(list, chore string) bool {
	for _, name := range butler.Chores(list) {
		if name == chore {
			return true
		}
	}
	return false
}

// parseButlerArgs parses `butler`'s own args: an optional "--chore <name>",
// plus the --no-build flag every dispatch-family verb shares. Unlike
// parseIssuePositionals's callers, butler takes no issue positionals, so any
// other token is a usage error. An empty chore return means "pick a due
// Chore" rather than sweep one named explicitly.
func parseButlerArgs(args []string) (chore string, noBuild bool, err error) {
	noBuild, remaining := dispatchNoBuildArgs(args)
	for i := 0; i < len(remaining); i++ {
		if remaining[i] != "--chore" {
			return "", false, fmt.Errorf("unrecognized argument: %s", remaining[i])
		}
		if i+1 >= len(remaining) {
			return "", false, fmt.Errorf("flag --chore requires a value")
		}
		chore = remaining[i+1]
		i++
	}
	return chore, noBuild, nil
}

// runButler picks the first due Chore out of chores (in order) and sweeps
// it, or reports why none is due (ADR 0056). chores is either the single
// name given on --chore, or every BUTLER_CHORES entry in configured order
// when none was given. HEAD is read once up front, and the due check
// (internal/butler.Check) takes a single now() reading shared across every
// candidate, so the picture of "what's due" is consistent across the whole
// pass rather than drifting chore to chore.
//
// Returns errQueueEmpty, wrapped with the reason(s) each candidate was not
// due, when nothing is due -- the same "nothing to do right now" signal
// dispatch's queue-empty path returns, so exitCodeFor(2) applies unchanged
// and its text is what cmdButler prints. A lost claim race also returns
// errQueueEmpty. Any other non-nil error means a due candidate's run never
// got as far as dispatching a Box (a git, ledger, or claim failure);
// runButler does not fall through to the next candidate in that case.
func runButler(backend ledger.Backend, it forge.IssueTracker, id butlerRun, chores []string, policy butlerPolicy, newDispatcher func(dispatch.Chore) dispatch.Dispatcher, now func() time.Time) error {
	if len(chores) == 0 {
		return errors.New("butler: no chores to check")
	}

	head, err := butler.Head(id.repo, id.branch)
	if err != nil {
		return err
	}
	whenNow := now()

	today, err := ledger.DayTotalsAll(backend, policy.enabled, whenNow.In(policy.zone))
	if err != nil {
		return fmt.Errorf("butler: total today's ledgers: %w", err)
	}

	var reasons []string
	for _, chore := range chores {
		tip, err := backend.Read(chore)
		if err != nil {
			return fmt.Errorf("butler: read %s ledger: %w", chore, err)
		}
		interval := policy.every.For(chore)
		recent, err := backend.History(chore, whenNow.Add(-interval))
		if err != nil {
			return fmt.Errorf("butler: read %s ledger history: %w", chore, err)
		}
		verdict := butler.Check(tip, recent, head, whenNow, today, butler.DueConfig{Every: interval, ClaimTimeout: policy.claimTimeout, Budgets: policy.budgets})
		if verdict != butler.Due {
			reasons = append(reasons, fmt.Sprintf("chore %q not due: %s", chore, verdict))
			continue
		}
		return runOneButlerChore(backend, it, id, chore, tip, head, policy.budgets.MaxFindingsPerSweep, newDispatcher, whenNow, now)
	}

	return fmt.Errorf("butler: %s: %w", strings.Join(reasons, "; "), errQueueEmpty)
}

// runOneButlerChore claims chore's Ledger (already read as tip, at head),
// computes this run's scan Scope (internal/butler.NextScope), dispatches one
// Box through newDispatcher, and settles the result (ADR 0056). Every
// collaborator is injected so this is testable without a real Box or repo
// seam: newDispatcher builds the Dispatcher for one Chore run (a real
// *dispatch.Factory.NewChore in production, dispatch.Fake in tests).
// claimedAt is runButler's whenNow, reused for ClaimedBy.Start so the claim
// is stamped at the same instant its due decision was made; now is called
// fresh at settle time instead.
//
// Returns errQueueEmpty if Claim loses the race -- another worker claimed
// chore between runButler's Read and this Claim -- the same "nothing to do
// right now" signal a live claim reports. Any other non-nil error means the
// run never got as far as dispatching a Box. Once the Box has run, whether
// Settle actually wrote the done commit (Box success) or left the claim
// standing (Box crash, ADR 0056) is read back off the Ledger tip rather than
// threaded out of Settle, since a crashed run's Settle writes nothing at all.
func runOneButlerChore(backend ledger.Backend, it forge.IssueTracker, id butlerRun, chore string, tip ledger.Tip, head string, maxFindingsPerSweep int, newDispatcher func(dispatch.Chore) dispatch.Dispatcher, claimedAt time.Time, now func() time.Time) error {
	files, err := butler.TrackedFiles(id.repo, head)
	if err != nil {
		return err
	}

	claim, err := ledger.Claim(backend, chore, tip, ledger.ClaimedBy{Host: id.host, Slot: butlerSlot, Start: claimedAt})
	if err != nil {
		if errors.Is(err, ledger.ErrLostRace) {
			// Another worker claimed it between our Read and our Claim:
			// exactly the "nothing to do right now" case, not a real error.
			return fmt.Errorf("butler: chore %q claimed by another run first: %w", chore, errQueueEmpty)
		}
		return fmt.Errorf("butler: claim %s: %w", chore, err)
	}

	scope := butler.NextScope(claim.State, head, files, butler.DefaultSliceSize)

	d := newDispatcher(dispatch.Chore{Name: chore, Branch: id.branch, Scope: scope})
	defer d.Close()
	result := d.Run()

	s := settle.NewButlerSettle(it, backend, chore, claim, scope, now, maxFindingsPerSweep)
	s.Settle(d, dispatch.ChoreKey(chore), butlerSlot, result)

	final, err := backend.Read(chore)
	if err != nil {
		return fmt.Errorf("butler: read %s ledger after settle: %w", chore, err)
	}
	if final.State.Phase != ledger.Done {
		return fmt.Errorf("butler: chore %q run did not complete (claim left standing)", chore)
	}
	return nil
}

// butlerPreflight holds the guards cmdButler checks before it claims
// anything. A malformed BUTLER_CHORE_CLASSES fails here, before any claim; a
// Chore with no entry is fine, its allow-list is just empty. The Filer gate
// matters because a butler Box relays findings only through the Filer:
// without one it would still report ready, and settling would advance
// lastSwept and the cursor past findings nobody filed. An empty chore (no
// --chore) needs BUTLER_CHORES to name at least one candidate instead. A
// per-sweep cap above the day cap is also rejected here: left alone, every
// run would trip internal/butler.Check's SweepFindingsExceedHeadroom at
// Filed=0 and no run could ever start.
func butlerPreflight(cfg config, chore string, filerEnabled bool) error {
	row, ok := backendByName(cfg.codeForge)
	if !ok || row.newLedger == nil {
		return fmt.Errorf("butler: CODE_FORGE=%q cannot host a butler Ledger (supported: %s)", cfg.codeForge, strings.Join(ledgerCapableNames(), ", "))
	}
	if chore != "" {
		if !choreEnabled(cfg.butlerChores, chore) {
			return fmt.Errorf("butler: chore %q is not enabled (BUTLER_CHORES=%q)", chore, cfg.butlerChores)
		}
	} else if len(butler.Chores(cfg.butlerChores)) == 0 {
		return fmt.Errorf("butler: %w", butler.ErrNoChores)
	}
	if _, err := butler.ParseClasses(cfg.butlerChoreClasses); err != nil {
		return fmt.Errorf("butler: BUTLER_CHORE_CLASSES: %w", err)
	}
	if !filerEnabled {
		return fmt.Errorf("butler: needs a provisioned Filer to relay findings (set FILER_MODEL; DRIVER=opencode never provisions one)")
	}
	if cfg.butlerMaxFindingsPerSweep > 0 && cfg.butlerMaxFindingsPerDay > 0 && cfg.butlerMaxFindingsPerSweep > cfg.butlerMaxFindingsPerDay {
		return fmt.Errorf("butler: BUTLER_MAX_FINDINGS_PER_SWEEP (%d) exceeds BUTLER_MAX_FINDINGS_PER_DAY (%d); no run could ever start", cfg.butlerMaxFindingsPerSweep, cfg.butlerMaxFindingsPerDay)
	}
	return nil
}

// cmdButler is the `butler [--chore <name>]` subcommand (ADR 0056, issue
// #3875, #3877): it sweeps the first due Chore among the one named on
// --chore, or else every BUTLER_CHORES entry in order, and reports "no work"
// with each candidate's reason if none is due. Deliberately out of scope
// here: promotion.
func cmdButler(lc *launchContext, chore string) int {
	defer lc.cleanup()

	filerEnabled := resolveAgentPresenceSignals(lc.config.driver).filerEnabled
	if err := butlerPreflight(lc.config, chore, filerEnabled); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	every, err := parseButlerEvery(lc.config.butlerEvery)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := every.checkOverrides(lc.config.butlerChores); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	claimTimeout, err := parseButlerClaimTimeout(lc.config.butlerClaimTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	budgets := butler.Budgets{
		MaxSweepsPerDay:     lc.config.butlerMaxSweepsPerDay,
		MaxFindingsPerDay:   lc.config.butlerMaxFindingsPerDay,
		MaxFindingsPerSweep: lc.config.butlerMaxFindingsPerSweep,
		DailyTokenCeiling:   lc.config.butlerDailyTokenCeiling,
	}
	window, err := daemon.ParseWindow(lc.config.daemonAwakeWindow)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Unlike internal/daemon/lock.go and status.go, which fall back to
	// "unknown" on a Hostname failure, Host here is the claim's owner
	// identity recorded in the Ledger: an "unknown" shared across hosts would
	// make that identity ambiguous.
	host, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(os.Stderr, "butler: hostname: %v\n", err)
		return 1
	}
	// butlerPreflight already checked row.newLedger != nil for this CODE_FORGE.
	row, _ := backendByName(lc.config.codeForge)
	backend, repo, ledgerCleanup, err := row.newLedger(lc.config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "butler: %v\n", err)
		return 1
	}
	defer ledgerCleanup()

	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return lc.factory.NewChore(c) }

	enabled := butler.Chores(lc.config.butlerChores)
	chores := []string{chore}
	if chore == "" {
		chores = enabled
	}

	policy := butlerPolicy{
		every:        every,
		claimTimeout: claimTimeout,
		budgets:      budgets,
		zone:         window.Location(),
		enabled:      enabled,
	}

	id := butlerRun{repo: repo, branch: lc.config.baseBranch, host: host}
	err = runButler(backend, lc.issueTracker, id, chores, policy, newDispatcher, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	return exitCodeFor(err)
}

// butlerVerbHandler is verbHandlers["butler"]'s body, split out so its own
// flag-parsing errors are testable without going through bootstrap.
func butlerVerbHandler(args []string, stderr io.Writer) int {
	chore, noBuild, err := parseButlerArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	lc, err := bootstrap(!noBuild, dispatchkind.Butler, false)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return bootstrapExitCode(err)
	}
	return cmdButler(lc, chore)
}
