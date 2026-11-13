package jira

import (
	"fmt"
	"net/http"
	"net/url"

	"spindrift.dev/launcher/internal/forge"
)

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
	if err := forge.IssueTypeNotOffered(j.cfg.ProjectKey, j.cfg.IssueType, offered); err != nil {
		return nil, err
	}
	rows, err := j.createMetaRows(base + "/" + url.PathEscape(typeID))
	if err != nil {
		return nil, err
	}
	fields := make([]forge.CreateField, len(rows))
	for i, r := range rows {
		fields[i] = forge.CreateField{ID: r.FieldID, Name: r.Name, Required: r.Required, HasDefault: r.HasDefaultValue}
	}
	return forge.UnfilledCreateFields(fields), nil
}

var _ forge.IssueTypeChecker = (*jiraClient)(nil)
