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
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/settle"
)

// butlerClaimTimeout is how long a Ledger claim goes unheld before the next
// run treats it as a crashed worker's leftover and takes it over, rather than
// treating the Chore as busy (ADR 0056). A package const, not an operator
// knob: the due check and budgets that would make this tunable are
// deliberately out of scope for the one-shot command (issue #3875).
const butlerClaimTimeout = 6 * time.Hour

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

// butlerRun groups the adjacent string params runButlerChore takes -- the
// scan target (repo/branch), the Chore, and the claimant host -- easy to
// mis-order as bare positionals.
type butlerRun struct {
	repo, branch, chore, host string
}

// choreEnabled reports whether chore appears in list, BUTLER_CHORES's
// space-separated value (schema key butlerChores). A Consumer opts a Chore in
// by naming it there; the default "" enables none (spec #3870).
func choreEnabled(list, chore string) bool {
	for _, name := range strings.Fields(list) {
		if name == chore {
			return true
		}
	}
	return false
}

// parseButlerArgs parses `butler`'s own args: a required "--chore <name>",
// plus the --no-build flag every dispatch-family verb shares. Unlike
// parseIssuePositionals's callers, butler takes no issue positionals, so any
// other token is a usage error.
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
	if chore == "" {
		return "", false, fmt.Errorf("usage: spindrift butler --chore <name> [--no-build]")
	}
	return chore, noBuild, nil
}

// runButlerChore claims chore's Ledger, computes this run's scan Scope
// (internal/butler.NextScope), dispatches one Box through newDispatcher, and
// settles the result (ADR 0056). Every collaborator is injected so this is
// testable without a real Box or repo seam: newDispatcher builds the
// Dispatcher for one Chore run (a real *dispatch.Factory.NewChore in
// production, dispatch.Fake in tests).
//
// Returns errQueueEmpty when chore's claim is already live -- another run
// holds it and it is not yet stale -- the same "nothing to do right now"
// signal dispatch's queue-empty path returns, so exitCodeFor(2) applies
// unchanged. Any other non-nil error means the run never got as far as
// dispatching a Box (a git, ledger, or lost-race claim failure). Once the Box
// has run, whether Settle actually wrote the done commit (Box success) or
// left the claim standing (Box crash, ADR 0056) is read back off the Ledger
// tip rather than threaded out of Settle, since a crashed run's Settle writes
// nothing at all.
func runButlerChore(backend ledger.Backend, it forge.IssueTracker, id butlerRun, newDispatcher func(dispatch.Chore) dispatch.Dispatcher, now func() time.Time) error {
	tip, err := backend.Read(id.chore)
	if err != nil {
		return fmt.Errorf("butler: read %s ledger: %w", id.chore, err)
	}
	if tip.State.Phase == ledger.Claimed && !tip.State.StaleClaim(now(), butlerClaimTimeout) {
		return errQueueEmpty
	}

	head, err := butler.Head(id.repo, id.branch)
	if err != nil {
		return err
	}
	files, err := butler.TrackedFiles(id.repo, head)
	if err != nil {
		return err
	}

	claim, err := ledger.Claim(backend, id.chore, tip, ledger.ClaimedBy{Host: id.host, Slot: butlerSlot, Start: now()})
	if err != nil {
		if errors.Is(err, ledger.ErrLostRace) {
			// Another worker claimed it between our Read and our Claim:
			// exactly the "nothing to do right now" case, not a real error.
			return errQueueEmpty
		}
		return fmt.Errorf("butler: claim %s: %w", id.chore, err)
	}

	scope := butler.NextScope(claim.State, head, files, butler.DefaultSliceSize)

	d := newDispatcher(dispatch.Chore{Name: id.chore, Branch: id.branch, Scope: scope})
	defer d.Close()
	result := d.Run()

	s := settle.NewButlerSettle(it, backend, id.chore, claim, scope, now)
	s.Settle(d, dispatch.ChoreKey(id.chore), butlerSlot, result)

	final, err := backend.Read(id.chore)
	if err != nil {
		return fmt.Errorf("butler: read %s ledger after settle: %w", id.chore, err)
	}
	if final.State.Phase != ledger.Done {
		return fmt.Errorf("butler: chore %q run did not complete (claim left standing)", id.chore)
	}
	return nil
}

// butlerPreflight holds the guards cmdButler checks before it claims
// anything. A malformed BUTLER_CHORE_CLASSES fails here, before any claim; a
// Chore with no entry is fine, its allow-list is just empty. The Filer gate
// matters because a butler Box relays findings only through the Filer:
// without one it would still report ready, and settling would advance
// lastSwept and the cursor past findings nobody filed.
func butlerPreflight(cfg config, chore string, filerEnabled bool) error {
	row, ok := backendByName(cfg.codeForge)
	if !ok || row.newLedger == nil {
		return fmt.Errorf("butler: CODE_FORGE=%q cannot host a butler Ledger (supported: %s)", cfg.codeForge, strings.Join(ledgerCapableNames(), ", "))
	}
	if !choreEnabled(cfg.butlerChores, chore) {
		return fmt.Errorf("butler: chore %q is not enabled (BUTLER_CHORES=%q)", chore, cfg.butlerChores)
	}
	if _, err := butler.ParseClasses(cfg.butlerChoreClasses); err != nil {
		return fmt.Errorf("butler: BUTLER_CHORE_CLASSES: %w", err)
	}
	if !filerEnabled {
		return fmt.Errorf("butler: chore %q needs a provisioned Filer to relay findings (set FILER_MODEL; DRIVER=opencode never provisions one)", chore)
	}
	return nil
}

// cmdButler is the `butler --chore <name>` subcommand: a one-shot sweep of
// one opted-in Chore (ADR 0056, issue #3875). Deliberately out of scope here:
// picking a Chore itself (the due check), budgets, and promotion -- the
// command always runs the exact Chore named, whatever its due state.
func cmdButler(lc *launchContext, chore string) int {
	defer lc.cleanup()

	filerEnabled := resolveAgentPresenceSignals(lc.config.driver).filerEnabled
	if err := butlerPreflight(lc.config, chore, filerEnabled); err != nil {
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

	id := butlerRun{repo: repo, branch: lc.config.baseBranch, chore: chore, host: host}
	err = runButlerChore(backend, lc.issueTracker, id, newDispatcher, time.Now)
	code := exitCodeFor(err)
	switch {
	case code == 1 && err != nil:
		fmt.Fprintf(os.Stderr, "%s\n", err)
	case code == 2:
		fmt.Fprintf(os.Stderr, "butler: chore %q is already claimed; nothing to do\n", chore)
	}
	return code
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
