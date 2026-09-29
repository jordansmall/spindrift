package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/runner"
)

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

// parseButlerArgs parses `butler`'s own args: an optional "--chore <name>",
// plus the --no-build flag every dispatch-family verb shares. Unlike
// parseIssuePositionals's callers, butler takes no issue positionals, so any
// other token is a usage error. An empty chore return means "pick a due
// Chore" rather than sweep one named explicitly.
func parseButlerArgs(args []string) (choreName string, noBuild bool, err error) {
	noBuild, remaining := dispatchNoBuildArgs(args)
	for i := 0; i < len(remaining); i++ {
		if remaining[i] != "--chore" {
			return "", false, fmt.Errorf("unrecognized argument: %s", remaining[i])
		}
		if i+1 >= len(remaining) {
			return "", false, fmt.Errorf("flag --chore requires a value")
		}
		choreName = remaining[i+1]
		i++
	}
	return choreName, noBuild, nil
}

// butlerOutcomeErr translates a Sweep Outcome into the error cmdButler prints
// and feeds to exitCodeFor. Swept yields nil: the Runner's settle step
// already printed the per-chore status line.
func butlerOutcomeErr(o butler.Outcome) error {
	switch o.Kind {
	case butler.NotDue:
		// The same "nothing to do right now" signal dispatch's queue-empty
		// path returns, so exitCodeFor(2) applies unchanged.
		return fmt.Errorf("butler: %s: %w", strings.Join(o.Reasons, "; "), errQueueEmpty)
	case butler.LostRace:
		// Another worker claimed it between the due check's Read and the
		// Claim: exactly the "nothing to do right now" case, not a real error.
		return fmt.Errorf("butler: chore %q claimed by another run first: %w", o.Chore, errQueueEmpty)
	case butler.ClaimLeft:
		return fmt.Errorf("butler: chore %q run did not complete (claim left standing)", o.Chore)
	default:
		return nil
	}
}

// choreCatalog is the baked CHORE_CATALOG artifact (rendered by
// lib/preambles.nix's runArtifacts), parsed for butlerPreflight's
// prompt-exists check. known is false when the artifact key is absent
// entirely (no --input, or a document baked before it existed) -- skip the
// check rather than fail on missing information. Present-but-empty (known
// true, names nil) means every enabled chore's prompt is provably missing.
type choreCatalog struct {
	names []string
	known bool
}

// butlerPreflight holds the guards cmdButler checks before it claims
// anything. A malformed BUTLER_CHORE_CLASSES fails here, before any claim; a
// Chore with no entry is fine, its allow-list is just empty. The Filer gate
// matters because a butler Box relays findings only through the Filer:
// without one it would still report ready, and settling would advance
// lastSwept and the cursor past findings nobody filed. An empty chore (no
// --chore) needs BUTLER_CHORES to name at least one candidate instead. A
// per-sweep cap above the day cap is also rejected here: left alone, every
// run would trip internal/chore.Check's SweepFindingsExceedHeadroom at
// Filed=0 and no run could ever start.
//
// The chore name format and prompt-file-exists checks below catch a
// malformed BUTLER_CHORES entry or missing chores/<name>.md here, before a
// claim -- otherwise they'd only surface once the Runner's Box fails
// in promptassembly.choreSection, leaving the claim standing for the full
// claim timeout (issue #3905).
func butlerPreflight(cfg config, choreName string, filerEnabled bool, catalog choreCatalog) ([]chore.Chore, error) {
	row, ok := backendByName(cfg.codeForge)
	if !ok || row.newLedger == nil {
		return nil, fmt.Errorf("butler: CODE_FORGE=%q cannot host a butler Ledger (supported: %s)", cfg.codeForge, strings.Join(ledgerCapableNames(), ", "))
	}
	if choreName != "" && !promptassembly.ValidChoreName(choreName) {
		return nil, fmt.Errorf("butler: chore %q: invalid name format: %s", choreName, promptassembly.ChoreNameRule)
	}
	chores, err := chore.Load(chore.Knobs{Chores: cfg.butlerChores, Every: cfg.butlerEvery, Classes: cfg.butlerChoreClasses})
	if err != nil {
		return nil, fmt.Errorf("butler: %w", err)
	}
	if choreName != "" {
		if !slices.ContainsFunc(chores, func(c chore.Chore) bool { return c.Name == choreName }) {
			return nil, fmt.Errorf("butler: chore %q is not enabled (BUTLER_CHORES=%q)", choreName, cfg.butlerChores)
		}
	} else if len(chores) == 0 {
		return nil, fmt.Errorf("butler: %w", chore.ErrNoChores)
	}
	// An override only takes effect when it names an existing directory
	// (runner.IsDir), so an unset or bogus SPINDRIFT_PROMPT_DIR falls through
	// to the baked catalog instead of silently skipping both.
	var missingPrompt func(c string) error
	switch {
	case runner.IsDir(cfg.spindriftPromptDir):
		missingPrompt = func(c string) error {
			path := filepath.Join(cfg.spindriftPromptDir, "chores", c+".md")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return nil
			}
			return fmt.Errorf("%s not found (SPINDRIFT_PROMPT_DIR)", path)
		}
	case catalog.known:
		missingPrompt = func(c string) error {
			if slices.Contains(catalog.names, c) {
				return nil
			}
			return fmt.Errorf("chores/%s.md not in the image's baked choresDir (CHORE_CATALOG=%q)", c, strings.Join(catalog.names, " "))
		}
	}
	// nil when there is neither an override dir nor a known catalog to check.
	if missingPrompt != nil {
		for _, c := range chores {
			if err := missingPrompt(c.Name); err != nil {
				return nil, fmt.Errorf("butler: chore %q: prompt file missing: %w", c.Name, err)
			}
		}
	}
	if !filerEnabled {
		return nil, fmt.Errorf("butler: needs a provisioned Filer to relay findings (set FILER_MODEL; DRIVER=opencode never provisions one)")
	}
	if err := butlerBudgets(cfg).Validate(); err != nil {
		return nil, err
	}
	return chores, nil
}

// butlerBudgets builds a chore.Budgets from cfg's BUTLER_MAX_* knobs, the
// single spot both butlerPreflight's Validate check and cmdButler's Policy
// build from, so the two never drift.
func butlerBudgets(cfg config) chore.Budgets {
	return chore.Budgets{
		MaxSweepsPerDay:     cfg.butlerMaxSweepsPerDay,
		MaxFindingsPerDay:   cfg.butlerMaxFindingsPerDay,
		MaxFindingsPerSweep: cfg.butlerMaxFindingsPerSweep,
		DailyTokenCeiling:   cfg.butlerDailyTokenCeiling,
		MaxPromotionsPerDay: cfg.butlerMaxPromotionsPerDay,
	}
}

// butlerSettings is resolveButlerSettings's parsed result.
type butlerSettings struct {
	chores       []chore.Chore
	claimTimeout time.Duration
	window       *daemon.Window
}

// resolveButlerSettings runs every butler config check -- butlerPreflight's
// guards (which also resolve BUTLER_CHORES, BUTLER_EVERY and
// BUTLER_CHORE_CLASSES via chore.Load), then BUTLER_CLAIM_TIMEOUT and
// DAEMON_AWAKE_WINDOW -- before any Ledger claim.
func resolveButlerSettings(cfg config, choreName string) (butlerSettings, error) {
	filerEnabled := resolveAgentPresenceSignals(cfg.driver).filerEnabled
	chores, err := butlerPreflight(cfg, choreName, filerEnabled, resolveChoreCatalog())
	if err != nil {
		return butlerSettings{}, err
	}
	claimTimeout, err := parseButlerClaimTimeout(cfg.butlerClaimTimeout)
	if err != nil {
		return butlerSettings{}, err
	}
	window, err := daemon.ParseWindow(cfg.daemonAwakeWindow)
	if err != nil {
		return butlerSettings{}, err
	}
	return butlerSettings{chores: chores, claimTimeout: claimTimeout, window: window}, nil
}

// cmdButler is the `butler [--chore <name>]` subcommand (ADR 0056, issue
// #3875, #3877, #3880): it sweeps the first due Chore among the one named on
// --chore, or else every BUTLER_CHORES entry in order, and reports "no work"
// with each candidate's reason if none is due. Each swept Chore's finding is
// auto-promoted to the configured work label (lc.config.workLabel) when it
// clears every host-side gate (allow-listed class, file limit, reviewer
// concurrence, and daily promotion room); BUTLER_MAX_PROMOTIONS_PER_DAY
// defaults to 0, off. A resolveButlerSettings failure exits
// exitConfigInvalid (6) rather than 1, so the daemon's shared breaker
// (internal/daemon/outcome.go) never treats a config problem as an
// unclassified error (issue #3920).
func cmdButler(lc *launchContext, choreName string) int {
	defer lc.cleanup()

	settings, err := resolveButlerSettings(lc.config, choreName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitConfigInvalid
	}
	budgets := butlerBudgets(lc.config)

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
	backend, tree, ledgerCleanup, err := row.newLedger(lc.config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "butler: %v\n", err)
		return 1
	}
	defer ledgerCleanup()

	newDispatcher := func(c dispatch.Chore) dispatch.Dispatcher { return lc.factory.NewChore(c) }

	// candidates is the single --chore name, else every settings.chores
	// entry in BUTLER_CHORES order; butlerPreflight already checked a named
	// --chore is present among them.
	candidates := []string{choreName}
	if choreName == "" {
		candidates = make([]string, len(settings.chores))
		for i, c := range settings.chores {
			candidates[i] = c.Name
		}
	}

	policy := butler.Policy{
		Branch:            lc.config.baseBranch,
		Host:              host,
		Chores:            settings.chores,
		ClaimTimeout:      settings.claimTimeout,
		Budgets:           budgets,
		Zone:              settings.window.Location(),
		PromotionMaxFiles: lc.config.butlerPromotionMaxFiles,
		PromotionLabel:    lc.config.workLabel,
	}

	sweeper := butler.New(backend, tree, lc.issueTracker, newDispatcher, policy, time.Now)
	o, err := sweeper.Sweep(candidates)
	if err == nil {
		err = butlerOutcomeErr(o)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
	}
	return exitCodeFor(err)
}

// butlerVerbHandler is verbHandlers["butler"]'s body, split out so its own
// flag-parsing errors are testable without going through bootstrap.
func butlerVerbHandler(args []string, stderr io.Writer) int {
	choreName, noBuild, err := parseButlerArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return 1
	}
	lc, err := bootstrap(!noBuild, dispatchkind.Butler, false)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return bootstrapExitCode(err)
	}
	return cmdButler(lc, choreName)
}
