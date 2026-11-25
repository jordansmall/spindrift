package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/exitcode"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/recordsdigest"
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
// given at most once and non-empty, plus the shared --no-build flag. butler
// takes no positionals, so any other token is a usage error. An empty chore
// return means "pick a due Chore", which is why an explicit empty --chore
// value must fail here.
func parseButlerArgs(args []string) (choreName string, noBuild bool, err error) {
	seen := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--no-build" {
			noBuild = true
			continue
		}
		if args[i] != "--chore" {
			return "", false, fmt.Errorf("unrecognized argument: %s", args[i])
		}
		// --no-build is handled above, not pre-stripped, so a --chore
		// followed by it reports the bad token instead of a missing value.
		if i+1 >= len(args) || args[i+1] == "" {
			return "", false, fmt.Errorf("flag --chore requires a non-empty value")
		}
		if strings.HasPrefix(args[i+1], "-") {
			return "", false, fmt.Errorf("flag --chore requires a chore name, got %q", args[i+1])
		}
		if seen {
			return "", false, fmt.Errorf("flag --chore given more than once")
		}
		seen = true
		choreName = args[i+1]
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

// choreCatalog is the baked CHORE_CATALOG artifact (lib/preambles.nix's
// runArtifacts), parsed for butlerPreflight's prompt-exists check. known is
// false when the key is absent (no --input, or an older document), and the
// check is skipped. Present-but-empty means every enabled prompt is missing.
type choreCatalog struct {
	names []string
	known bool
}

// butlerPreflight holds the guards cmdButler checks before it claims
// anything. It rejects a malformed BUTLER_CHORE_CLASSES, or
// BUTLER_PATCH_CLASSES when BUTLER_MAX_PATCHES_PER_DAY > 0 (a Chore with no
// entry just has an empty allow-list). It needs a Filer and host-side filing
// (hostFiler, issue #6343): a butler Box relays findings only through the
// Filer, so without either, settling would advance lastSwept and the cursor
// past findings nobody filed. An empty chore needs a BUTLER_CHORES candidate.
// It rejects a per-sweep cap above the day cap, which would trip chore.Check's
// SweepFindingsExceedHeadroom at Filed=0 on every run. A malformed BUTLER_CHORES name or missing
// chores/<name>.md would otherwise surface only in the Runner's Box, leaving
// the claim standing for the full claim timeout (issue #3905).
func butlerPreflight(cfg config, choreName string, filerEnabled bool, hostFiler forge.HostPostedIssueFiler, catalog choreCatalog) ([]chore.Chore, error) {
	row, ok := backendkit.ByName(cfg.CodeForge)
	if !ok || row.NewLedger == nil {
		return nil, fmt.Errorf("butler: CODE_FORGE=%q cannot host a butler Ledger (supported: %s)", cfg.CodeForge, strings.Join(ledgerCapableNames(), ", "))
	}
	if choreName != "" && !promptassembly.ValidChoreName(choreName) {
		return nil, fmt.Errorf("butler: chore %q: invalid name format: %s", choreName, promptassembly.ChoreNameRule)
	}
	chores, err := chore.Load(chore.Knobs{
		Chores:           cfg.ButlerChores,
		Every:            cfg.ButlerEvery,
		Classes:          cfg.ButlerChoreClasses,
		PatchClasses:     cfg.ButlerPatchClasses,
		MaxPatchesPerDay: cfg.ButlerMaxPatchesPerDay,
	})
	if err != nil {
		return nil, fmt.Errorf("butler: %w", err)
	}
	if err := checkRecordsDigests(chore.Rows()); err != nil {
		return nil, err
	}
	if choreName != "" {
		if !slices.ContainsFunc(chores, func(c chore.Chore) bool { return c.Name == choreName }) {
			return nil, fmt.Errorf("butler: chore %q is not enabled (BUTLER_CHORES=%q)", choreName, cfg.ButlerChores)
		}
	} else if len(chores) == 0 {
		return nil, fmt.Errorf("butler: %w", chore.ErrNoChores)
	}
	// An override only takes effect when it names an existing directory
	// (runner.IsDir), so an unset or bogus SPINDRIFT_PROMPT_DIR falls through
	// to the baked catalog instead of silently skipping both.
	var missingPrompt func(c string) error
	switch {
	case runner.IsDir(cfg.SpindriftPromptDir):
		missingPrompt = func(c string) error {
			path := filepath.Join(cfg.SpindriftPromptDir, "chores", c+".md")
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
	if hostFiler == nil {
		return nil, fmt.Errorf("butler: ISSUE_TRACKER=%q has no host-side issue filing, so the Filer's relayed findings would be lost", cfg.IssueTracker)
	}
	if !filerEnabled {
		return nil, fmt.Errorf("butler: needs a provisioned Filer to relay findings (set FILER_MODEL)")
	}
	if err := butlerBudgets(cfg).Validate(); err != nil {
		return nil, err
	}
	return chores, nil
}

// checkRecordsDigests fails when a records-scoped catalog row, enabled or not,
// has no registered Records digest: records Chores are built in, so a missing
// Digest is a build wiring gap (ADR 0070 decision 5), caught at startup rather
// than as a failed sweep.
func checkRecordsDigests(rows []chore.Row) error {
	for _, r := range rows {
		if !r.Records {
			continue
		}
		if _, ok := recordsdigest.ByName(r.Name); !ok {
			return recordsdigest.NotRegistered(r.Name)
		}
	}
	return nil
}

// butlerBudgets builds a chore.Budgets from cfg's BUTLER_MAX_* knobs, the
// single spot both butlerPreflight's Validate check and cmdButler's Policy
// build from, so the two never drift.
func butlerBudgets(cfg config) chore.Budgets {
	return chore.Budgets{
		MaxSweepsPerDay:     cfg.ButlerMaxSweepsPerDay,
		MaxFindingsPerDay:   cfg.ButlerMaxFindingsPerDay,
		MaxFindingsPerSweep: cfg.ButlerMaxFindingsPerSweep,
		DailyTokenCeiling:   cfg.ButlerDailyTokenCeiling,
		MaxPromotionsPerDay: cfg.ButlerMaxPromotionsPerDay,
		MaxPatchesPerDay:    cfg.ButlerMaxPatchesPerDay,
	}
}

// butlerSettings is resolveButlerSettings's parsed result.
type butlerSettings struct {
	chores       []chore.Chore
	claimTimeout time.Duration
	window       *daemon.Window
}

// resolveButlerSettings runs every butler config check -- butlerPreflight's
// guards (which also resolve BUTLER_CHORES, BUTLER_EVERY, BUTLER_CHORE_CLASSES
// and, when the patch rung is on, BUTLER_PATCH_CLASSES, via chore.Load), then
// BUTLER_CLAIM_TIMEOUT and DAEMON_AWAKE_WINDOW -- before any Ledger claim.
func resolveButlerSettings(cfg config, caps forge.Capabilities, choreName string) (butlerSettings, error) {
	filerEnabled := resolveAgentPresenceSignals(cfg.driver).filerEnabled
	chores, err := butlerPreflight(cfg, choreName, filerEnabled, caps.HostPostedIssueFiler, resolveChoreCatalog())
	if err != nil {
		return butlerSettings{}, err
	}
	claimTimeout, err := parseButlerClaimTimeout(cfg.ButlerClaimTimeout)
	if err != nil {
		return butlerSettings{}, err
	}
	// A sweep reads only the enabled records Chores' row-owned knobs, but every
	// registered Digest's are validated here, before any Ledger claim.
	for _, s := range recordsdigest.All() {
		if _, err := s.Knobs(cfg.knobLookup); err != nil {
			return butlerSettings{}, fmt.Errorf("butler: %w", err)
		}
	}
	window, err := daemon.ParseWindow(cfg.DaemonAwakeWindow)
	if err != nil {
		return butlerSettings{}, err
	}
	return butlerSettings{chores: chores, claimTimeout: claimTimeout, window: window}, nil
}

// hostPatchForge adapts already-resolved Capabilities into butler.PatchForge:
// every method comes from the fields forge.ResolveCapabilities populated only
// when a write-capable adapter backs the forge (issue #4074, #4112).
type hostPatchForge struct {
	pr forge.PRForge
	forge.BranchPusher
	forge.DraftPRCreator
	forge.IssueLabeler
	forge.BranchDeleter
}

func (h hostPatchForge) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	return h.pr.OpenPRForBranch(branch)
}

// butlerPatchForge builds cmdButler's butler.PatchForge, opting the patch
// rung on only when caps proves the forge can push a branch, open a draft PR
// host-side, look that PR up and delete the branch (landPatch's
// ambiguous-create recovery, issue #4112); the tracker can label an
// already-filed issue; and tracker and forge are one backend.
// Without a labeler (ISSUE_TRACKER=local posts "local:"+slug) a failed push or
// PR-create could not fall the finding back to promote. A split backend
// (forgejo tracker, github forge) would let "Closes #N" and agent/issue-N
// close an unrelated issue, the hazard closeOnlyOwnIssue guards (#2341). Any
// missing leg yields nil, which keeps the rung off (issue #4074).
func butlerPatchForge(caps forge.Capabilities) butler.PatchForge {
	if !caps.HostCanOpenPR() || caps.IssueLabeler == nil ||
		caps.PRForge == nil || caps.BranchDeleter == nil {
		return nil
	}
	if caps.TrackerDescriptor.Name == "" || caps.TrackerDescriptor.Name != caps.ForgeDescriptor.Name {
		return nil
	}
	return hostPatchForge{pr: caps.PRForge, BranchPusher: caps.BranchPusher, DraftPRCreator: caps.DraftPRCreator, IssueLabeler: caps.IssueLabeler, BranchDeleter: caps.BranchDeleter}
}

// cmdButler is the `butler [--chore <name>]` subcommand (ADR 0056, issues
// #3875, #3877, #3880): it sweeps the first due Chore among the --chore one or
// every BUTLER_CHORES entry in order, else reports "no work" with each
// candidate's reason. A swept finding is auto-promoted to lc.config.workLabel
// when it clears every host-side gate (allow-listed class, file limit, reviewer
// concurrence, daily promotion room; BUTLER_MAX_PROMOTIONS_PER_DAY defaults
// to 0, off). A resolveButlerSettings failure exits ConfigInvalid, not Failed,
// so the daemon's breaker never counts a config problem as unclassified (#3920).
func cmdButler(lc *launchContext, choreName string, stderr io.Writer) int {
	defer lc.cleanup()

	settings, err := resolveButlerSettings(lc.config, lc.capabilities, choreName)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return int(exitcode.ConfigInvalid)
	}
	// config.kind() would silently resolve a nil kind to work's, filing findings
	// under the wrong label.
	if lc.config.dispatchKind == nil {
		fmt.Fprintln(stderr, "butler: no dispatch kind set; refusing to file findings under another kind's label")
		return int(exitcode.ConfigInvalid)
	}
	budgets := butlerBudgets(lc.config)

	// Unlike internal/daemon/lock.go and status.go, which fall back to
	// "unknown" on a Hostname failure, Host here is the claim's owner
	// identity recorded in the Ledger: an "unknown" shared across hosts would
	// make that identity ambiguous.
	host, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(stderr, "butler: hostname: %v\n", err)
		return int(exitcode.Failed)
	}
	// butlerPreflight already checked row.NewLedger != nil for this CODE_FORGE.
	row, _ := backendkit.ByName(lc.config.CodeForge)
	backend, tree, ledgerCleanup, err := ledgerFor(lc.config, row)
	if err != nil {
		fmt.Fprintf(stderr, "butler: %v\n", err)
		return int(exitcode.Failed)
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
		Branch:            lc.config.BaseBranch,
		Host:              host,
		Chores:            settings.chores,
		ClaimTimeout:      settings.claimTimeout,
		Budgets:           budgets,
		Zone:              settings.window.Location(),
		PromotionMaxFiles: lc.config.ButlerPromotionMaxFiles,
		PromotionLabel:    lc.config.workLabel,
		FindingLabel:      lc.config.dispatchKind.FindingLabel,
		PatchLabel:        lc.config.dispatchKind.PatchLabel,
		PatchPaths:        lc.config.ButlerPatchPaths,
		PatchMaxFiles:     lc.config.ButlerPatchMaxFiles,
		PatchMaxLines:     lc.config.ButlerPatchMaxLines,
		BranchPrefix:      lc.config.BranchPrefix,
		RecordsRoot:       lc.pwd,
		Knobs:             lc.config.knobLookup,
	}

	sweeper := butler.New(backend, tree, lc.issueTracker, newDispatcher, policy, time.Now).WithPatchForge(butlerPatchForge(lc.capabilities), lc.merge).WithMergeReader(butlerMergeReader(lc.capabilities))
	o, err := sweeper.Sweep(candidates)
	if err == nil {
		err = butlerOutcomeErr(o)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
	}
	return exitCodeFor(err)
}

// butlerVerbHandler is verbHandlers["butler"]'s body, split out so its own
// flag-parsing errors are testable without going through bootstrap.
func butlerVerbHandler(connectors *connectorruntime.Runtime, args []string, stdout, stderr io.Writer) int {
	choreName, noBuild, err := parseButlerArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return int(exitcode.Failed)
	}
	lc, err := bootstrap(connectors, stdout, !noBuild, dispatchkind.Butler, false)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", err)
		return bootstrapExitCode(err)
	}
	return cmdButler(lc, choreName, stderr)
}

// butlerMergeReader is cmdButler's butler.WithMergeReader pair: the PR reader
// and the optional merge-commit reader, each nil when caps lacks it. A nil
// PRForge leaves the tuning digest to the Records alone.
func butlerMergeReader(caps forge.Capabilities) (butler.MergeReader, forge.MergeCommitReader) {
	if caps.PRForge == nil {
		return nil, nil
	}
	return caps.PRForge, caps.MergeCommitReader
}
