package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// labelMeta holds the default color and description for a triage label.
type labelMeta struct {
	description string
	color       string // hex without leading #
}

// triageLabelMeta is the single source of truth for default triage label
// colors and descriptions, keyed by the canonical label name.
var triageLabelMeta = map[string]labelMeta{
	"ready-for-agent":   {description: "Fully specified; ready for an AFK agent", color: "0075ca"},
	"agent-in-progress": {description: "An AFK agent is actively working this issue", color: "e4e669"},
	"agent-failed":      {description: "Box exited non-zero; needs human triage", color: "d93f0b"},
	"agent-complete":    {description: "Agent work merged and green", color: "0e8a16"},
}

// researchLabelMeta is the single source of truth for default research
// label (ADR 0022) colors and descriptions. Unlike triageLabelMeta, `spindrift
// doctor` never checks or creates these: the research family is a fixed,
// non-configurable vocabulary agent-research.yml and the research prompt key
// off directly, not a user-tunable knob — see docs/reference.md's "Create
// the research labels on the Target repo" for the manual creation path this
// map keeps in sync with.
var researchLabelMeta = map[string]labelMeta{
	"agent-research":             {description: "Apply to fire a research dispatch", color: "fbca04"},
	"agent-research-in-progress": {description: "A Box is reviewing this issue", color: "bfd4f2"},
	"agent-research-recommend":   {description: "Relevant and enriched — promote it", color: "0e8a16"},
	"agent-research-reject":      {description: "False positive, not worth it, or a duplicate — close it", color: "d93f0b"},
	"agent-research-unclear":     {description: "Needs a human answer — answer, then re-apply agent-research", color: "d4c5f9"},
	"agent-research-failed":      {description: "Box crashed or produced no verdict; needs human triage", color: "b60205"},
}

// runDoctor probes both seams (IssueTracker + CodeForge), then checks that
// all configured triage labels exist in the repository. When interactive is
// true and labels are missing, it prompts to create them; otherwise it reports
// and exits non-zero.
func runDoctor(it forge.IssueTracker, cf forge.CodeForge, c config, w io.Writer, stdin io.Reader, interactive bool) error {
	tokenHint, slugHint := "GH_TOKEN", "--repo-slug / REPO_SLUG"
	if c.issueTracker == "jira" {
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

	checkLabels := func() ([]string, error) {
		existing, lerr := it.ListLabels()
		if lerr != nil {
			return nil, fmt.Errorf("label check failed: %w", lerr)
		}
		present := make(map[string]bool, len(existing))
		for _, l := range existing {
			present[l] = true
		}
		expected := []string{c.label, c.inProgressLabel, c.failedLabel, c.completeLabel}
		var missing []string
		for _, label := range expected {
			if present[label] {
				fmt.Fprintf(w, "ok: label %q present\n", label)
			} else {
				fmt.Fprintf(w, "MISSING: label %q missing\n", label)
				missing = append(missing, label)
			}
		}
		return missing, nil
	}

	missing, err := checkLabels()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}

	if !interactive {
		return fmt.Errorf("one or more triage labels are missing — create them in the repository")
	}

	fmt.Fprintf(w, "Create %d missing label(s)? [y/N] ", len(missing))
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() || strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
		fmt.Fprintln(w)
		return fmt.Errorf("one or more triage labels are missing — create them in the repository")
	}

	for _, name := range missing {
		meta, ok := triageLabelMeta[name]
		if !ok {
			meta = labelMeta{color: "ededed"}
		}
		if cerr := it.CreateLabel(name, meta.description, meta.color); cerr != nil {
			return fmt.Errorf("create label %q: %w", name, cerr)
		}
		fmt.Fprintf(w, "created: label %q\n", name)
	}

	// Re-verify after creation.
	missing, err = checkLabels()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		fmt.Fprintln(w, "ok: all triage labels present")
		return nil
	}
	return fmt.Errorf("one or more triage labels are still missing after creation")
}
