package jira

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// filledFields are the create fields PostIssue sets itself, plus reporter,
// which Jira defaults to the caller. A required field outside this set (and
// without a default) makes every host-side create fail.
var filledFields = map[string]bool{
	"project": true, "summary": true, "issuetype": true,
	"description": true, "labels": true, "reporter": true,
}

// createMetaPageSize and maxCreateMetaPages bound the createmeta walk; the
// page cap stops a server that ignores startAt from looping forever.
const (
	createMetaPageSize = 50
	maxCreateMetaPages = 100
)

// createMetaRow covers both createmeta row kinds: issue types carry id and
// name, create fields carry fieldId, name, required, and hasDefaultValue.
type createMetaRow struct {
	ID              string `json:"id"`
	FieldID         string `json:"fieldId"`
	Name            string `json:"name"`
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
}

// createMetaPage decodes both wire shapes: Cloud nests rows under
// "issueTypes" or "fields", Server/DC 8.4+ under "values".
type createMetaPage struct {
	IssueTypes []createMetaRow `json:"issueTypes"`
	Fields     []createMetaRow `json:"fields"`
	Values     []createMetaRow `json:"values"`
	Total      int             `json:"total"`
	IsLast     bool            `json:"isLast"`
}

func (p createMetaPage) rows() []createMetaRow {
	return append(append(append([]createMetaRow{}, p.IssueTypes...), p.Fields...), p.Values...)
}

// createMetaRows walks every page of a createmeta endpoint.
func (j *jiraClient) createMetaRows(path string) ([]createMetaRow, error) {
	var all []createMetaRow
	for range maxCreateMetaPages {
		var page createMetaPage
		q := fmt.Sprintf("%s?startAt=%d&maxResults=%d", path, len(all), createMetaPageSize)
		if err := j.rest.Do(http.MethodGet, q, nil, &page); err != nil {
			return nil, err
		}
		rows := page.rows()
		all = append(all, rows...)
		if len(rows) == 0 || page.IsLast || (page.Total > 0 && len(all) >= page.Total) {
			break
		}
	}
	return all, nil
}

// CheckIssueType implements forge.IssueTypeChecker from the project's create
// metadata.
func (j *jiraClient) CheckIssueType() ([]string, error) {
	base := "/rest/api/2/issue/createmeta/" + url.PathEscape(j.cfg.ProjectKey) + "/issuetypes"
	types, err := j.createMetaRows(base)
	if err != nil {
		return nil, err
	}
	var offered []string
	typeID := ""
	for _, ty := range types {
		offered = append(offered, ty.Name)
		if ty.Name == j.cfg.IssueType {
			typeID = ty.ID
		}
	}
	if typeID == "" {
		return nil, fmt.Errorf("JIRA_ISSUE_TYPE %q is not an issue type of project %s (offered: %s; it must match an offered name exactly as the instance spells it, e.g. \"Aufgabe\" for Task on a German one): %w",
			j.cfg.IssueType, j.cfg.ProjectKey, strings.Join(offered, ", "), forge.ErrUnknownIssueType)
	}
	fields, err := j.createMetaRows(base + "/" + url.PathEscape(typeID))
	if err != nil {
		return nil, err
	}
	var unfilled []string
	for _, f := range fields {
		if f.Required && !f.HasDefaultValue && !filledFields[f.FieldID] {
			unfilled = append(unfilled, fmt.Sprintf("%q (%s)", f.Name, f.FieldID))
		}
	}
	return unfilled, nil
}

var _ forge.IssueTypeChecker = (*jiraClient)(nil)
