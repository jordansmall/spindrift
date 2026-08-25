// Package doctor implements the forge/label validation shared by the
// `spindrift doctor` subcommand and Quickstart's finish line (ADR 0027):
// both need to probe an IssueTracker/CodeForge and offer to create missing
// triage labels, so the logic lives here once instead of being duplicated
// or shelled out to as a subprocess that doesn't exist yet at Quickstart's
// pre-CLI stage.
package doctor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// LabelMeta holds the default color and description for a triage label.
type LabelMeta struct {
	Description string
	Color       string // hex without leading #
}

// ResearchLabelNames returns the seven fixed research-tier label names (ADR
// 0022), sourced from forge.ResearchDispatchLabels()/ResearchVerdictLabels()
// rather than duplicated as string literals, plus the fixed literal
// "agent-research-finding" (ADR 0041). There's no
// forge.ResearchFindingLabel() helper for that last name — unlike
// AmbiguousLabelNames() below, whose fixed literal mirrors a real
// forge.DispatchLabels.Ambiguous declaration, this one has no Go
// counterpart to mirror at all.
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
// 0040), sourced from forge.PriorityLabelNames() rather than duplicated as
// string literals.
func PriorityLabelNames() []string {
	return forge.PriorityLabelNames()
}

// AmbiguousLabelNames returns the single fixed ambiguous-spec-tier label
// name. There's no forge.AmbiguousDispatchLabels() helper — the fixed
// literal "agent-ambiguous-spec" mirrors forge.DispatchLabels.Ambiguous's
// own fixed-literal doc comment.
func AmbiguousLabelNames() []string {
	return []string{"agent-ambiguous-spec"}
}

// RuntimeCheckName is RuntimeCheck's Name field, exported so a caller
// filtering the row out of a larger slice (checks.go's doctorExtraChecks)
// matches on this constant instead of the bare string literal "runtime" —
// a future rename here would otherwise silently reintroduce
// double-reporting of the runtime row.
const RuntimeCheckName = "runtime"

// RuntimeCheck builds the Required-tier "runtime" Check row (Probe:
// runner.ValidateRuntime(runtime), Remedy naming the four valid runtime
// values). It backs launcherRequiredKnobChecks (cmd/launcher/checks.go),
// the Required-tier row that feeds validate()'s fatal fail-fast startup
// gate (main.go) — not the informational/advisory runtime line doctor and
// Quickstart print for a human operator, which is a separate code path
// (Config.Runtime below plus Run's own hand-rolled advisory block). The two
// are deliberately kept apart so they never both report for one invocation
// (issue #2559 AC2): doctorExtraChecks strips this row out of
// launcherChecks(c) before handing it to Run as extraChecks, and
// Quickstart's own doctor.Run call passes nil for extraChecks and relies on
// Config.Runtime instead.
func RuntimeCheck(runtime string) Check {
	return Check{
		Name:   RuntimeCheckName,
		Tier:   Required,
		Remedy: "set RUNTIME to podman, docker, rancher, or bwrap, and ensure the matching CLI is on PATH",
		Probe: func() error {
			return runner.ValidateRuntime(runtime)
		},
		SuccessMsg: func() string {
			return fmt.Sprintf("runtime %q found on PATH", runtime)
		},
	}
}

// Config is the minimal slice of launcher config Run needs: the Issue
// Tracker kind, the caller-resolved auth/repo hint strings for that tracker
// (TokenHint/SlugHint — internal/doctor can't see package main's backend
// registry that owns the "which backend names which env var" mapping, so
// the caller resolves it and hands the strings in), and the four work-tier
// label names.
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

	// Runtime is the operator's configured container runtime (podman|docker|
	// rancher|bwrap). Checked via runner.ValidateRuntime and reported as an
	// advisory row — never fatal — since Quickstart's own prompt-time
	// confirmation already lets an operator deliberately scaffold with an
	// uninstalled runtime, and doctor must not turn that already-accepted
	// state into a hard failure.
	Runtime string

	// MergePolicy is the operator's configured post-green merge policy
	// (immediate|auto|manual, MERGE_MODE) -- the branch-protection row's Tier
	// is Required under immediate/auto (no human merge gate) and Advisory
	// under manual (a human already reviews before merge).
	MergePolicy string

	// BaseBranch is the repository's base/target branch (BASE_BRANCH,
	// default "main") -- the branch the branch-protection row queries.
	BaseBranch string
}

// Run probes both seams (IssueTracker + CodeForge), then checks that all
// configured triage labels and the fixed research-tier (ADR 0022) and
// priority-tier (ADR 0040) labels exist in the repository. When interactive
// is true and labels are missing, it prompts to create them. In
// non-interactive mode, missing triage labels are fatal (non-zero exit);
// missing research and priority labels are advisory only and never affect
// the exit code. stdin is an already-constructed *bufio.Scanner
// so a caller mid-way through its own scripted stdin flow (Quickstart's
// finish line) can hand over the same scanner instead of double-wrapping the
// underlying reader and losing already-buffered input. extraChecks is a
// caller-supplied slice of additional Check rows (Required or Advisory) —
// run through RunChecks and reported via ReportResults after the three
// probes below, but purely informational: unlike the three probes, a
// failing extraChecks row (of either tier) never makes Run return an
// error, the same treatment as the research/priority/ambiguous-spec label
// tiers already get; pass nil when there are none.
func Run(it forge.IssueTracker, cf forge.CodeForge, c Config, w io.Writer, stdin *bufio.Scanner, interactive bool, extraChecks []Check) error {
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.TokenHint != "" {
		tokenHint, slugHint = c.TokenHint, c.SlugHint
	}

	var itRepo, cfRepo string
	recoverableCount := 0

	// builtinChecks are the always-run doctor rows: issue-tracker
	// connectivity, code-forge connectivity, branch protection, and the
	// recoverable-issue count. Each Probe closure captures its fetched
	// detail (repo slug or count) into the local vars above so its
	// SuccessMsg closure can report the exact same dynamic success line the
	// old hand-rolled fmt.Fprintf calls printed — registry-driven, so
	// adding another built-in doctor check means adding a row here, not
	// editing Run's control flow.
	builtinChecks := []Check{
		{
			Name: "issue-tracker",
			Tier: Required,
			Probe: func() error {
				repo, err := it.Probe()
				if err != nil {
					if errors.Is(err, forge.ErrAuthFailure) {
						return fmt.Errorf("forge auth check failed (check %s is set and valid): %w", tokenHint, err)
					}
					if errors.Is(err, forge.ErrRepoNotFound) {
						return fmt.Errorf("forge repo not found (check %s is correct): %w", slugHint, err)
					}
					return fmt.Errorf("forge connectivity check failed: %w", err)
				}
				itRepo = repo
				return nil
			},
			SuccessMsg: func() string {
				return fmt.Sprintf("issue tracker confirmed — %s is reachable", itRepo)
			},
		},
		{
			Name: "code-forge",
			Tier: Required,
			Probe: func() error {
				repo, err := cf.Probe()
				if err != nil {
					return fmt.Errorf("code forge connectivity check failed: %w", err)
				}
				cfRepo = repo
				return nil
			},
			SuccessMsg: func() string {
				return fmt.Sprintf("code forge confirmed — %s is reachable", cfRepo)
			},
		},
		BranchProtectionCheck(cf, c.MergePolicy, c.BaseBranch),
		{
			Name: "recoverable-issues",
			Tier: Required,
			Probe: func() error {
				// Only query when Recoverable resolves to a real label: an
				// unconditional ListIssues(Recoverable) call would
				// false-match every open issue on a tracker (GitHub,
				// Forgejo) that leaves Recoverable unmapped, since both
				// ignore an empty label filter instead of erroring
				// (forge.LabeledTracker's doc comment) — mirroring
				// console/adapter.go's countRecoverable guard for the same
				// reason.
				if lt, ok := it.(forge.LabeledTracker); !ok || lt.StateLabels().Label(forge.Recoverable) != "" {
					recoverable, err := it.ListIssues(forge.Recoverable)
					if err != nil {
						return fmt.Errorf("recoverable issue check failed: %w", err)
					}
					recoverableCount = len(recoverable)
				}
				return nil
			},
			SuccessMsg: func() string {
				return fmt.Sprintf("%d recoverable issue(s) — run `spindrift recover <issue>` to land each", recoverableCount)
			},
		},
	}

	results := RunChecksFailFast(builtinChecks)
	if err := FirstRequiredError(results); err != nil {
		// RunChecksFailFast stops at the first Required failure, so that
		// failing result is always the last element here. Report only the
		// results before it — the caller (cmdDoctor) already prints err to
		// stderr, so writing the failing row's MISSING line to w too would
		// double-report it (origin/main's pre-refactor Run never wrote
		// anything to w on this path). Its Remedy line is not part of that
		// duplication (cmdDoctor never prints it), so still write that one
		// line — otherwise the failing row's remedy is silently dropped and
		// never reaches the operator anywhere.
		ReportResults(w, results[:len(results)-1])
		failing := results[len(results)-1]
		if failing.Check.Remedy != "" && failing.Check.Remedy != err.Error() {
			fmt.Fprintf(w, "  remedy: %s\n", failing.Check.Remedy)
		}
		return err
	}
	ReportResults(w, results)

	// extraChecks are informational only: report each row's outcome via
	// ReportResults, but never let a failure (Required or Advisory) make
	// Run return an error — a caller's launcher-startup validation rows
	// are surfaced for visibility, not treated as fatal here.
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

	// checkLabels reports on all four label tiers: work (fatal if missing),
	// research (advisory — ADR 0022's agent-research family is reported but
	// never fails the check, so CI doctor runs stay green for deployments
	// that don't use research yet), priority (advisory — ADR 0040's
	// agent-priority-* family, same treatment), and ambiguous-spec (advisory
	// — issue #2275's single agent-ambiguous-spec label, same treatment).
	checkLabels := func() (workMissing, researchMissing, priorityMissing, ambiguousMissing []string, err error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, nil, nil, nil, fmt.Errorf("label check failed: %w", lerr)
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
			return fmt.Errorf("one or more triage labels are missing — create them in the repository")
		}
		return nil
	}

	fmt.Fprintf(w, "Create %d missing label(s)? [y/N] ", len(missing))
	if !stdin.Scan() || strings.ToLower(strings.TrimSpace(stdin.Text())) != "y" {
		fmt.Fprintln(w)
		if len(workMissing) > 0 {
			return fmt.Errorf("one or more triage labels are missing — create them in the repository")
		}
		return nil
	}

	// metaFor resolves a missing label's color/description by role for the
	// four operator-configurable work-tier labels — c.Label et al. may be
	// renamed away from their defaults (LABEL/IN_PROGRESS_LABEL/FAILED_LABEL/
	// COMPLETE_LABEL), so a literal TriageLabelMeta[name] lookup keyed on the
	// default name would miss for a renamed label and fall back to gray
	// (#2528 AC2). Research/priority/ambiguous-spec label names are fixed
	// literals (never operator-configurable), so TriageLabelMeta's
	// literal-name lookup stays correct for those tiers.
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

	for _, name := range missing {
		meta := metaFor(name)
		if cerr := it.CreateLabel(name, meta.Description, meta.Color); cerr != nil {
			return fmt.Errorf("create label %q: %w", name, cerr)
		}
		fmt.Fprintf(w, "created: label %q\n", name)
	}

	// Re-verify after creation.
	workMissing, researchMissing, priorityMissing, ambiguousMissing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(workMissing) > 0 {
		return fmt.Errorf("one or more triage labels are still missing after creation")
	}
	// Work labels are fatal (handled above) and research/priority/
	// ambiguous-spec labels are advisory (ADR 0022 / ADR 0040 / ADR 0041 /
	// #2275), so each advisory tier gets its own wrap-up line here: an
	// advisory note if that tier is still short after creation, or a single
	// success line naming all four tiers once none is.
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
