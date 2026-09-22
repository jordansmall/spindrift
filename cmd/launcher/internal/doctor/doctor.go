// Package doctor implements the forge/label validation shared by the
// `spindrift doctor` subcommand and Quickstart's finish line (ADR 0027).
// Quickstart runs before the CLI exists, so it cannot shell out to the
// subcommand and calls this package directly instead.
package doctor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// ErrConnectivity classifies a Run failure as an auth-or-connectivity problem
// reaching the issue tracker or code forge (exit 3, issue #2569). Every builtin
// probe and label call wraps it with %w whatever the underlying cause, so a
// caller classifies by errors.Is instead of matching message text.
var ErrConnectivity = errors.New("issue tracker or code forge connectivity failure")

// ErrRequiredLabelsMissing classifies a Run failure as required checks failed or
// declined (exit 4, issue #2569): work-tier triage labels are missing and were
// not created.
var ErrRequiredLabelsMissing = errors.New("required triage label(s) missing or declined")

// errRequiredLabelsMissing is shared by the non-interactive and interactive-decline
// paths below so their identical message cannot drift apart.
func errRequiredLabelsMissing(workMissing []string) error {
	return fmt.Errorf("%w: %s missing — create them in the repository", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
}

// LabelMeta holds the default color and description for a triage label.
type LabelMeta struct {
	Description string
	Color       string // hex without leading #
}

// ResearchLabelNames returns the seven fixed research-tier label names (ADR 0022).
// All but "agent-research-finding" (ADR 0041) come from forge rather than local
// literals; that one has no forge declaration to source it from.
func ResearchLabelNames() []string {
	dl := forge.ResearchDispatchLabels()
	vl := forge.ResearchVerdictLabels()
	names := []string{dl.Dispatchable, dl.InProgress, dl.Failed}
	for _, e := range vl.Entries() {
		names = append(names, e.Label)
	}
	names = append(names, "agent-research-finding")
	return names
}

// PriorityLabelNames returns the three fixed priority-tier label names (ADR 0040).
func PriorityLabelNames() []string {
	return forge.PriorityLabelNames()
}

// AmbiguousLabelNames returns the single fixed ambiguous-spec-tier label name.
// The literal mirrors forge.DispatchLabels.Ambiguous, which has no accessor.
func AmbiguousLabelNames() []string {
	return []string{"agent-ambiguous-spec"}
}

// RuntimeCheckName is exported so callers filtering the row out of a larger slice
// match this constant, not the bare literal "runtime". Matching the literal would
// let a rename here silently reintroduce double-reporting of the runtime row.
const RuntimeCheckName = "runtime"

// RuntimeCheck builds the Required-tier "runtime" Check row backing
// launcherchecks.RequiredKnobChecks, which feeds validate()'s fatal startup gate.
// The advisory runtime line doctor and Quickstart print is a separate path
// (Config.Runtime), and both callers strip this row before calling Run so the two
// never both report for one invocation (issue #2559 AC2).
func RuntimeCheck(runtime string) Check {
	return Check{
		Name:   RuntimeCheckName,
		Tier:   Required,
		Remedy: "set RUNTIME to podman, docker, rancher, or bwrap, and ensure the matching CLI is on PATH",
		Probe: func() (any, error) {
			return nil, runner.ValidateRuntime(runtime)
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("runtime %q found on PATH", runtime)
		},
	}
}

// Config is the minimal slice of launcher config Run needs.
type Config struct {
	IssueTracker string

	// TokenHint and SlugHint name the env vars Run points an operator at in its
	// auth-failure and repo-not-found remediation text. The caller resolves them
	// because internal/doctor cannot see package main's backend registry, which
	// owns the backend-to-env-var mapping. Empty means the github-shaped default
	// (GH_TOKEN / --repo-slug REPO_SLUG).
	TokenHint string
	SlugHint  string

	Label           string
	InProgressLabel string
	FailedLabel     string
	CompleteLabel   string

	// Runtime is the configured container runtime (podman|docker|rancher|bwrap),
	// reported as advisory and never fatal: Quickstart's own prompt lets an
	// operator deliberately scaffold with an uninstalled runtime, so doctor must
	// not turn that accepted state into a hard failure.
	Runtime string

	// MergePolicy is the configured post-green merge policy (immediate|auto|manual,
	// MERGE_MODE). The branch-protection row is Required under immediate/auto,
	// which have no human merge gate, and Advisory under manual.
	MergePolicy string

	// BaseBranch is the branch the branch-protection row queries (BASE_BRANCH,
	// default "main").
	BaseBranch string
}

// Run probes the issue tracker and code forge, then checks that every configured
// triage, research, priority, and ambiguous-spec label exists, offering to create
// missing ones when interactive. Only missing work-tier labels fail the run; the
// other tiers and extraChecks are advisory. stdin is the caller's own scanner, so
// Quickstart can hand one over mid-flow without losing already-buffered input.
func Run(it forge.IssueTracker, cf forge.CodeForge, c Config, w io.Writer, stdin *bufio.Scanner, interactive bool, extraChecks []Check) (err error) {
	rep := NewReporter(w)
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.TokenHint != "" {
		tokenHint, slugHint = c.TokenHint, c.SlugHint
	}

	// it and cf are stable for this whole call, so BranchProtectionCheck and the
	// recoverable-issues probe share one resolution instead of each asserting the
	// optional interfaces themselves (issue #2946).
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})

	// These rows fail fast. Each Probe returns the repo slug so SuccessMsg can
	// name it.
	connectivityChecks := []Check{
		{
			Name: "issue-tracker",
			Tier: Required,
			Probe: func() (any, error) {
				repo, err := it.Probe()
				if err != nil {
					if errors.Is(err, forge.ErrAuthFailure) {
						return nil, fmt.Errorf("%w: forge auth check failed (check %s is set and valid): %w", ErrConnectivity, tokenHint, err)
					}
					if errors.Is(err, forge.ErrRepoNotFound) {
						return nil, fmt.Errorf("%w: forge repo not found (check %s is correct): %w", ErrConnectivity, slugHint, err)
					}
					if errors.Is(err, forge.ErrRateLimit) {
						return nil, fmt.Errorf("%w: forge rate limited (wait for the quota window to reset, then retry): %w", ErrConnectivity, err)
					}
					return nil, fmt.Errorf("%w: forge connectivity check failed: %w", ErrConnectivity, err)
				}
				return repo, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("issue tracker confirmed — %s is reachable", output.(string))
			},
		},
		{
			Name: "code-forge",
			Tier: Required,
			Probe: func() (any, error) {
				repo, err := cf.Probe()
				if err != nil {
					return nil, fmt.Errorf("%w: code forge connectivity check failed: %w", ErrConnectivity, err)
				}
				return repo, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("code forge confirmed — %s is reachable", output.(string))
			},
		},
	}

	// Unlike connectivityChecks above, a Required failure here must not block the
	// rest of Run (issue #2798): an unprotected base branch is a one-time repo
	// setup gap the operator fixes from the same run that surfaces it, not a sign
	// every later live call is moot the way an unreachable forge is.
	repoStateChecks := []Check{
		BranchProtectionCheck(caps, c.MergePolicy, c.BaseBranch),
		{
			Name: "recoverable-issues",
			Tier: Required,
			Probe: func() (any, error) {
				// Query only when Recoverable resolves to a real label. GitHub
				// and Forgejo ignore an empty label filter instead of erroring,
				// so an unconditional call would match every open issue on a
				// tracker that leaves Recoverable unmapped. console/adapter.go's
				// countRecoverable guards the same way.
				recoverableCount := 0
				if caps.LabeledTracker == nil || caps.LabeledTracker.StateLabels().Label(forge.Recoverable) != "" {
					recoverable, err := it.ListIssues(forge.Recoverable)
					if err != nil {
						return nil, fmt.Errorf("%w: recoverable issue check failed: %w", ErrConnectivity, err)
					}
					recoverableCount = len(recoverable)
				}
				return recoverableCount, nil
			},
			SuccessMsg: func(output any) string {
				return fmt.Sprintf("%d recoverable issue(s) — run `spindrift recover <issue>` to land each", output.(int))
			},
		},
	}

	results := RunChecksFailFast(connectivityChecks)
	if cerr := FirstRequiredError(results); cerr != nil {
		// RunChecksFailFast stops at the first Required failure, so that result is
		// always the last element. cmdDoctor already prints cerr to stderr, so
		// writing the failing row's MISSING line here too would double-report it.
		// It never prints the Remedy, so write that one line or the remedy reaches
		// the operator nowhere.
		rep.Results(results[:len(results)-1])
		failing := results[len(results)-1]
		if suffix := remedySuffix(failing.Check.Remedy, cerr.Error()); suffix != "" {
			fmt.Fprintf(w, "  remedy: %s\n", suffix)
		}
		return cerr
	}
	rep.Results(results)

	// A blocking repository-state failure is reported inline, unlike the failing
	// connectivity row above: the report continues past it, so suppressing the
	// MISSING line would leave an orphan remedy line under no row at all.
	repoStateResults := RunChecks(repoStateChecks)
	rep.Results(repoStateResults)
	deferredRepoStateErr := FirstRequiredError(repoStateResults)
	defer func() {
		// This error wins the return value, so a later error from the label
		// section below would otherwise be overwritten and reach no stream at
		// all. Print it to w first.
		if deferredRepoStateErr != nil {
			if err != nil {
				fmt.Fprintf(w, "MISSING: %v\n", err)
			}
			err = deferredRepoStateErr
		}
	}()

	// extraChecks are informational: a failing row at either tier never makes Run
	// return an error.
	rep.Results(RunChecks(extraChecks))

	// Runtime row, advisory and never fatal. Rationale on Config.Runtime.
	if c.Runtime == "" {
		fmt.Fprintln(w, "advisory: RUNTIME not set — skipping runtime check")
	} else if rerr := runner.ValidateRuntime(c.Runtime); rerr != nil {
		fmt.Fprintf(w, "advisory: runtime %q not ready: %v — does not fail this check\n", c.Runtime, rerr)
	} else {
		fmt.Fprintf(w, "ok: runtime %q found on PATH\n", c.Runtime)
	}

	// A missing row's prefix mirrors its tier's exit-code weight: MISSING for the
	// fatal work tier, advisory for the three tiers that never fail the check.
	checkLabelSet := func(names []string, present map[string]bool, tier Tier) []string {
		var missing []string
		for _, label := range names {
			if present[label] {
				fmt.Fprintf(w, "ok: label %q present\n", label)
				continue
			}
			fmt.Fprintf(w, "%s: label %q missing\n", rowPrefix(tier), label)
			missing = append(missing, label)
		}
		return missing
	}

	// checkLabels reports on all four tiers, but only the work tier is fatal. The
	// research (ADR 0022), priority (ADR 0040), and ambiguous-spec (#2275)
	// families stay advisory so a CI doctor run stays green on a deployment that
	// does not use them yet.
	checkLabels := func() (workMissing, researchMissing, priorityMissing, ambiguousMissing []string, err error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: label check failed: %w", ErrConnectivity, lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		workMissing = checkLabelSet([]string{c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel}, present, Required)
		researchMissing = checkLabelSet(ResearchLabelNames(), present, Advisory)
		priorityMissing = checkLabelSet(PriorityLabelNames(), present, Advisory)
		ambiguousMissing = checkLabelSet(AmbiguousLabelNames(), present, Advisory)
		return workMissing, researchMissing, priorityMissing, ambiguousMissing, nil
	}

	workMissing, researchMissing, priorityMissing, ambiguousMissing, err := checkLabels()
	if err != nil {
		return err
	}
	if len(researchMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d research label(s) missing (ADR 0022 / ADR 0041) — does not fail this check\n", len(researchMissing))
	}
	if len(priorityMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d priority label(s) missing (ADR 0040) — does not fail this check\n", len(priorityMissing))
	}
	if len(ambiguousMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d ambiguous-spec label(s) missing — does not fail this check\n", len(ambiguousMissing))
	}
	missing := append(append(append(append([]string{}, workMissing...), researchMissing...), priorityMissing...), ambiguousMissing...)
	if len(missing) == 0 {
		fmt.Fprintln(w, "ok: all triage, research, priority, and ambiguous-spec labels present")
		return nil
	}

	if !interactive {
		if len(workMissing) > 0 {
			return errRequiredLabelsMissing(workMissing)
		}
		return nil
	}

	advisoryCount := len(researchMissing) + len(priorityMissing) + len(ambiguousMissing)
	requiredClause := fmt.Sprintf("%d required", len(workMissing))
	if len(workMissing) > 0 {
		requiredClause += " (declining leaves this check failing)"
	}
	advisoryClause := fmt.Sprintf("%d advisory", advisoryCount)
	if advisoryCount > 0 {
		advisoryClause += " (declining is safe, does not fail this check)"
	}
	fmt.Fprintf(w, "Create %d missing label(s) — %s and %s? [y/N] ",
		len(missing), requiredClause, advisoryClause)
	if !stdin.Scan() || strings.ToLower(strings.TrimSpace(stdin.Text())) != "y" {
		fmt.Fprintln(w)
		if len(workMissing) > 0 {
			return errRequiredLabelsMissing(workMissing)
		}
		return nil
	}

	// metaFor resolves the four work-tier labels by role because an operator can
	// rename them: a TriageLabelMeta[name] lookup keyed on the default name would
	// miss a renamed label and fall back to gray (#2528 AC2). The other tiers use
	// fixed literals, so the map lookup stays correct for them.
	metaFor := func(name string) LabelMeta {
		switch name {
		case c.Label:
			return MetaDispatchable
		case c.InProgressLabel:
			return MetaInProgress
		case c.FailedLabel:
			return MetaFailed
		case c.CompleteLabel:
			return MetaComplete
		}
		if meta, ok := TriageLabelMeta[name]; ok {
			return meta
		}
		return LabelMeta{Color: "ededed"}
	}

	// A CreateLabel failure on a required work-tier label is fatal. One on an
	// advisory label is only reported, here and again in the still-missing lines
	// below: accepting the prompt must never leave an operator worse off than
	// declining it, which is safe for an advisory-only run (issue #2569).
	workSet := make(map[string]bool, len(workMissing))
	for _, name := range workMissing {
		workSet[name] = true
	}
	for _, name := range missing {
		meta := metaFor(name)
		if cerr := it.CreateLabel(name, meta.Description, meta.Color); cerr != nil {
			if workSet[name] {
				return fmt.Errorf("%w: create label %q: %w", ErrConnectivity, name, cerr)
			}
			fmt.Fprintf(w, "advisory: create label %q failed: %v — does not fail this check\n", name, cerr)
			continue
		}
		fmt.Fprintf(w, "created: label %q\n", name)
	}

	workMissing, researchMissing, priorityMissing, ambiguousMissing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(workMissing) > 0 {
		return fmt.Errorf("%w: %s still missing after creation", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
	}
	// Work labels are fatal above, so each advisory tier (ADR 0022 / ADR 0040 /
	// ADR 0041 / #2275) gets its own wrap-up line here, or one success line
	// naming all four tiers when none is still short.
	stillMissing := false
	if len(researchMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d research label(s) still missing after creation (ADR 0022 / ADR 0041) — does not fail this check: %s\n", len(researchMissing), strings.Join(researchMissing, ", "))
		stillMissing = true
	}
	if len(priorityMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d priority label(s) still missing after creation (ADR 0040) — does not fail this check: %s\n", len(priorityMissing), strings.Join(priorityMissing, ", "))
		stillMissing = true
	}
	if len(ambiguousMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d ambiguous-spec label(s) still missing after creation — does not fail this check: %s\n", len(ambiguousMissing), strings.Join(ambiguousMissing, ", "))
		stillMissing = true
	}
	if stillMissing {
		return nil
	}
	fmt.Fprintln(w, "ok: all triage, research, priority, and ambiguous-spec labels present")
	return nil
}
