package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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

// butlerPreflight holds the guards cmdButler checks before it claims anything.
// The Filer gate matters because a butler Box relays findings only through the
// Filer: without one it would still report ready, and settling would advance
// lastSwept and the cursor past findings nobody filed.
func butlerPreflight(codeForge, butlerChores, chore string, filerEnabled bool) error {
	row, ok := backendByName(codeForge)
	if !ok || row.newLedger == nil {
		return fmt.Errorf("butler: CODE_FORGE=%q cannot host a butler Ledger (supported: %s)", codeForge, strings.Join(ledgerCapableNames(), ", "))
	}
	if !choreEnabled(butlerChores, chore) {
		return fmt.Errorf("butler: chore %q is not enabled (BUTLER_CHORES=%q)", chore, butlerChores)
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
	if err := butlerPreflight(lc.config.codeForge, lc.config.butlerChores, chore, filerEnabled); err != nil {
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
