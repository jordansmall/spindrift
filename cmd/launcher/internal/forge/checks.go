package forge

import (
	"fmt"
	"slices"
	"strings"
)

// MissingStatuses returns the mapped statuses absent from have, sorted.
// Statuses compare case-insensitively, as the Jira transition match does.
func MissingStatuses(mapped, have []string) []string {
	known := map[string]bool{}
	for _, s := range have {
		known[strings.ToLower(s)] = true
	}
	mapped = slices.Sorted(slices.Values(mapped)) // sorting first keeps the spelling kept on a case-only dedup stable
	var missing []string
	for _, status := range mapped {
		if key := strings.ToLower(status); !known[key] {
			known[key] = true // report a case-only duplicate once
			missing = append(missing, status)
		}
	}
	return missing
}

// IssueTypeNotOffered returns an error wrapping ErrUnknownIssueType unless
// issueType is one of offered.
func IssueTypeNotOffered(project, issueType string, offered []string) error {
	if slices.Contains(offered, issueType) {
		return nil
	}
	return fmt.Errorf("JIRA_ISSUE_TYPE %q is not an issue type of project %s (offered: %s; it must match an offered name exactly as the instance spells it, e.g. \"Aufgabe\" for Task on a German one): %w",
		issueType, project, strings.Join(offered, ", "), ErrUnknownIssueType)
}

// CreateField is one create field of an issue type, as Jira's createmeta
// reports it.
type CreateField struct {
	ID, Name   string
	Required   bool
	HasDefault bool
}

// filledFields are the create fields PostIssue sets itself, plus reporter,
// which Jira defaults to the caller. A required field outside this set (and
// without a default) makes every host-side create fail.
var filledFields = map[string]bool{
	"project": true, "summary": true, "issuetype": true,
	"description": true, "labels": true, "reporter": true,
}

// UnfilledCreateFields returns the required fields without a default that
// PostIssue does not fill, formatted `"name" (id)`.
func UnfilledCreateFields(fields []CreateField) []string {
	var unfilled []string
	for _, f := range fields {
		if f.Required && !f.HasDefault && !filledFields[f.ID] {
			unfilled = append(unfilled, fmt.Sprintf("%q (%s)", f.Name, f.ID))
		}
	}
	return unfilled
}
