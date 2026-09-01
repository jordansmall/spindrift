// Package doctor implements the forge/label validation shared by the
// `spindrift doctor` subcommand and Quickstart's finish line (ADR 0027).
// Quickstart cannot shell out to the subcommand — at its pre-CLI stage the
// binary doesn't exist yet — so the logic lives here as a library.
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

// ErrConnectivity classifies a Run failure as an auth-or-connectivity
// problem reaching the issue tracker or code forge (doctor exit 3). Every
// probe and label call wraps it via %w regardless of the underlying cause,
// so callers classify with errors.Is rather than matching message text.
var ErrConnectivity = errors.New("issue tracker or code forge connectivity failure")

// ErrRequiredLabelsMissing classifies a Run failure as required-checks-
// failed-or-declined (doctor exit 4): work-tier triage labels are missing
// and were not created — declined, non-interactive, or a create attempt that
// still left one missing.
var ErrRequiredLabelsMissing = errors.New("required triage label(s) missing or declined")

// errRequiredLabelsMissing is shared by the non-interactive and
// interactive-decline paths so their identical message can't drift apart.
func errRequiredLabelsMissing(workMissing []string) error {
	return fmt.Errorf("%w: %s missing — create them in the repository", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
}

// LabelMeta holds the default color and description for a triage label.
type LabelMeta struct {
	Description string
	Color       string // hex without leading #
}

// ResearchLabelNames returns the seven fixed research-tier label names (ADR
// 0022), sourced from forge rather than duplicated as string literals, plus
// the literal "agent-research-finding" (ADR 0041), which has no forge helper
// to source from.
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

// PriorityLabelNames returns the three fixed priority-tier label names (ADR
// 0040), sourced from forge rather than duplicated as string literals.
func PriorityLabelNames() []string {
	return forge.PriorityLabelNames()
}

// AmbiguousLabelNames returns the single fixed ambiguous-spec-tier label
// name, a literal mirroring forge.DispatchLabels.Ambiguous — there is no
// forge helper to source it from.
func AmbiguousLabelNames() []string {
	return []string{"agent-ambiguous-spec"}
}

// RuntimeCheckName is exported so a caller filtering the row out of a larger
// slice matches this constant instead of the bare literal "runtime" — a
// rename here would otherwise silently reintroduce double-reporting.
const RuntimeCheckName = "runtime"

// RuntimeCheck builds the Required-tier "runtime" Check row backing
// validate()'s fatal fail-fast startup gate — not the advisory runtime line
// doctor and Quickstart print for a human operator, which is a separate path
// (Config.Runtime plus Run's own advisory block). The two are deliberately
// kept apart so they never both report for one invocation: doctorExtraChecks
// strips this row before handing extraChecks to Run, and Quickstart passes
// nil extraChecks and relies on Config.Runtime.
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

// Config is the minimal slice of launcher config Run needs. The auth/repo
// hints are caller-resolved: internal/doctor can't see package main's
// backend registry, which owns the backend-to-env-var mapping.
type Config struct {
	IssueTracker string

	// TokenHint/SlugHint name the env var(s) Run points an operator at in
	// its auth-failure/repo-not-found remediation text. Empty means "use
	// the github-shaped default" (GH_TOKEN / --repo-slug REPO_SLUG).
	TokenHint string
	SlugHint  string

	Label           string
	InProgressLabel string
	FailedLabel     string
	CompleteLabel   string

	// Runtime is the configured container runtime. Reported as an advisory
	// row, never fatal: Quickstart's prompt-time confirmation already lets an
	// operator deliberately scaffold with an uninstalled runtime, and doctor
	// must not turn that accepted state into a hard failure.
	Runtime string

	// MergePolicy makes the branch-protection row Required under
	// immediate/auto (no human merge gate) and Advisory under manual (a human
	// already reviews before merge).
	MergePolicy string

	// BaseBranch is the branch the branch-protection row queries.
	BaseBranch string
}

// Run probes both seams (IssueTracker + CodeForge), then checks that the
// configured triage labels and the fixed research-tier (ADR 0022) and
// priority-tier (ADR 0040) labels exist. When interactive, it prompts to
// create missing ones. Missing triage labels are fatal; missing research and
// priority labels never affect the exit code.
//
// stdin is an already-constructed *bufio.Scanner so a caller mid-way through
// its own scripted stdin flow (Quickstart's finish line) can hand over the
// same scanner instead of double-wrapping the reader and losing buffered
// input. extraChecks rows are reported but purely informational — a failure
// of either tier never makes Run return an error; pass nil when there are
// none.
func Run(it forge.IssueTracker, cf forge.CodeForge, c Config, w io.Writer, stdin *bufio.Scanner, interactive bool, extraChecks []Check) error {
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.TokenHint != "" {
		tokenHint, slugHint = c.TokenHint, c.SlugHint
	}

	// Resolved once: it and cf are stable for the whole Run call, so
	// BranchProtectionCheck and the recoverable-issues probe share this
	// rather than each asserting its own optional interface.
	caps := forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})

	// builtinChecks are the always-run doctor rows. Each Probe returns its
	// fetched detail as Output so SuccessMsg can render a dynamic line;
	// adding a built-in check means adding a row, not editing Run's flow.
	builtinChecks := []Check{
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
		BranchProtectionCheck(caps, c.MergePolicy, c.BaseBranch),
		{
			Name: "recoverable-issues",
			Tier: Required,
			Probe: func() (any, error) {
				// Only query when Recoverable resolves to a real label: GitHub
				// and Forgejo ignore an empty label filter instead of
				// erroring, so an unconditional call would false-match every
				// open issue on a tracker that leaves Recoverable unmapped.
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

	results := RunChecksFailFast(builtinChecks)
	if err := FirstRequiredError(results); err != nil {
		// The failing result is always the last element (fail-fast). Report
		// only the results before it: cmdDoctor already prints err to stderr,
		// so writing the failing row's MISSING line here would double-report.
		// Its Remedy line is not part of that duplication and would otherwise
		// never reach the operator, so write that one line.
		ReportResults(w, results[:len(results)-1])
		failing := results[len(results)-1]
		if failing.Check.Remedy != "" && failing.Check.Remedy != err.Error() {
			fmt.Fprintf(w, "  remedy: %s\n", failing.Check.Remedy)
		}
		return err
	}
	ReportResults(w, results)

	// Informational only: a caller's launcher-startup validation rows are
	// surfaced for visibility, never fatal here regardless of tier.
	ReportResults(w, RunChecks(extraChecks))

	// Runtime row (advisory, never fatal) — rationale on Config.Runtime.
	if c.Runtime == "" {
		fmt.Fprintln(w, "advisory: RUNTIME not set — skipping runtime check")
	} else if rerr := runner.ValidateRuntime(c.Runtime); rerr != nil {
		fmt.Fprintf(w, "advisory: runtime %q not ready: %v — does not fail this check\n", c.Runtime, rerr)
	} else {
		fmt.Fprintf(w, "ok: runtime %q found on PATH\n", c.Runtime)
	}

	checkLabelSet := func(names []string, present map[string]bool) []string {
		var missing []string
		for _, label := range names {
			if present[label] {
				fmt.Fprintf(w, "ok: label %q present\n", label)
			} else {
				fmt.Fprintf(w, "MISSING: label %q missing\n", label)
				missing = append(missing, label)
			}
		}
		return missing
	}

	// checkLabels reports on all four label tiers. Only work is fatal;
	// research (ADR 0022), priority (ADR 0040), and ambiguous-spec are
	// advisory, so CI doctor runs stay green for deployments not using them.
	checkLabels := func() (workMissing, researchMissing, priorityMissing, ambiguousMissing []string, err error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: label check failed: %w", ErrConnectivity, lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		workMissing = checkLabelSet([]string{c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel}, present)
		researchMissing = checkLabelSet(ResearchLabelNames(), present)
		priorityMissing = checkLabelSet(PriorityLabelNames(), present)
		ambiguousMissing = checkLabelSet(AmbiguousLabelNames(), present)
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

	// metaFor resolves the four work-tier labels by role, not by name: they
	// are operator-renameable, so a TriageLabelMeta[name] lookup keyed on the
	// default name would miss a renamed label and fall back to gray. The
	// other tiers have fixed names, so the map lookup stays correct there.
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

	// A CreateLabel failure on a work-tier label is fatal; on an advisory
	// tier it is only reported, because accepting the prompt must never be
	// worse than declining it, and declining is safe for an advisory run.
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

	// Re-verify after creation.
	workMissing, researchMissing, priorityMissing, ambiguousMissing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(workMissing) > 0 {
		return fmt.Errorf("%w: %s still missing after creation", ErrRequiredLabelsMissing, strings.Join(workMissing, ", "))
	}
	// Each advisory tier gets its own wrap-up line if still short after
	// creation; a single success line names all four tiers once none is.
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
