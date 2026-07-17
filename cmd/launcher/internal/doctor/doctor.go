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
)

// LabelMeta holds the default color and description for a triage label.
type LabelMeta struct {
	Description string
	Color       string // hex without leading #
}

// TriageLabelMeta is the single source of truth for default triage/research
// label colors and descriptions, keyed by the canonical label name. It
// covers both the four operator-configurable work-tier labels and the six
// fixed research-tier labels (ADR 0022, `forge.ResearchDispatchLabels()` /
// `forge.ResearchVerdictLabels()`) so a doctor run creates either kind with a
// real color/description instead of falling back to gray.
var TriageLabelMeta = map[string]LabelMeta{
	"ready-for-agent":   {Description: "Fully specified; ready for an AFK agent", Color: "0075ca"},
	"agent-in-progress": {Description: "An AFK agent is actively working this issue", Color: "e4e669"},
	"agent-failed":      {Description: "Box exited non-zero; needs human triage", Color: "d93f0b"},
	"agent-complete":    {Description: "Agent work merged and green", Color: "0e8a16"},

	"agent-research":             {Description: "Apply to fire a research dispatch", Color: "fbca04"},
	"agent-research-in-progress": {Description: "A Box is reviewing this issue", Color: "bfd4f2"},
	"agent-research-recommend":   {Description: "Relevant and enriched — promote it", Color: "2cbe4e"},
	"agent-research-reject":      {Description: "False positive, not worth it, or a duplicate — close it", Color: "e11d21"},
	"agent-research-unclear":     {Description: "Needs a human answer — answer, then re-apply agent-research", Color: "d4c5f9"},
	"agent-research-failed":      {Description: "Box crashed or produced no verdict; needs human triage", Color: "b60205"},
}

// ResearchLabelNames returns the six fixed research-tier label names (ADR
// 0022), sourced from forge.ResearchDispatchLabels()/ResearchVerdictLabels()
// rather than duplicated as string literals.
func ResearchLabelNames() []string {
	dl := forge.ResearchDispatchLabels()
	vl := forge.ResearchVerdictLabels()
	return []string{dl.Dispatchable, dl.InProgress, dl.Failed, vl.Recommend, vl.Reject, vl.Unclear}
}

// Config is the minimal slice of launcher config Run needs: the Issue
// Tracker kind (for error hints) and the four work-tier label names.
type Config struct {
	IssueTracker    string
	Label           string
	InProgressLabel string
	FailedLabel     string
	CompleteLabel   string
}

// Run probes both seams (IssueTracker + CodeForge), then checks that all
// configured triage labels and the fixed research-tier labels (ADR 0022)
// exist in the repository. When interactive is true and labels are missing,
// it prompts to create them. In non-interactive mode, missing triage labels
// are fatal (non-zero exit); missing research labels are advisory only and
// never affect the exit code. stdin is an already-constructed *bufio.Scanner
// so a caller mid-way through its own scripted stdin flow (Quickstart's
// finish line) can hand over the same scanner instead of double-wrapping the
// underlying reader and losing already-buffered input.
func Run(it forge.IssueTracker, cf forge.CodeForge, c Config, w io.Writer, stdin *bufio.Scanner, interactive bool) error {
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.IssueTracker == "jira" {
		tokenHint, slugHint = "JIRA_TOKEN", "JIRA_BASE_URL / JIRA_PROJECT_KEY"
	}
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
	fmt.Fprintf(w, "ok: issue tracker confirmed — %s is reachable\n", repo)
	cfRepo, err := cf.Probe()
	if err != nil {
		return fmt.Errorf("code forge connectivity check failed: %w", err)
	}
	fmt.Fprintf(w, "ok: code forge confirmed — %s is reachable\n", cfRepo)

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

	// checkLabels reports on both label tiers: work (fatal if missing) and
	// research (advisory — ADR 0022's agent-research family is reported but
	// never fails the check, so CI doctor runs stay green for deployments
	// that don't use research yet).
	checkLabels := func() (workMissing, researchMissing []string, err error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, nil, fmt.Errorf("label check failed: %w", lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		workMissing = checkLabelSet([]string{c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel}, present)
		researchMissing = checkLabelSet(ResearchLabelNames(), present)
		return workMissing, researchMissing, nil
	}

	workMissing, researchMissing, err := checkLabels()
	if err != nil {
		return err
	}
	if len(researchMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d research label(s) missing (ADR 0022) — does not fail this check\n", len(researchMissing))
	}
	missing := append(append([]string{}, workMissing...), researchMissing...)
	if len(missing) == 0 {
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

	for _, name := range missing {
		meta, ok := TriageLabelMeta[name]
		if !ok {
			meta = LabelMeta{Color: "ededed"}
		}
		if cerr := it.CreateLabel(name, meta.Description, meta.Color); cerr != nil {
			return fmt.Errorf("create label %q: %w", name, cerr)
		}
		fmt.Fprintf(w, "created: label %q\n", name)
	}

	// Re-verify after creation.
	workMissing, researchMissing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(workMissing) > 0 {
		return fmt.Errorf("one or more triage labels are still missing after creation")
	}
	// Work labels are fatal (handled above) and research labels are
	// advisory (ADR 0022), so the two tiers get separate wrap-up lines
	// here: an advisory note if research is still short after creation,
	// or a single success line naming both tiers once neither is.
	if len(researchMissing) > 0 {
		fmt.Fprintf(w, "advisory: %d research label(s) still missing after creation (ADR 0022) — does not fail this check\n", len(researchMissing))
		return nil
	}
	fmt.Fprintln(w, "ok: all triage and research labels present")
	return nil
}
