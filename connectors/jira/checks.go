package main

import (
	"fmt"
	"net/url"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const (
	// createMetaPageSize and maxCreateMetaPages bound the createmeta walk; the
	// page cap stops a server that ignores startAt from looping forever.
	createMetaPageSize = 50
	maxCreateMetaPages = 100
)

// probe is one authenticated read. A non-2xx is reported as its status, not an
// error, so core decides what an auth failure means.
func probe(t tracker.Target) (int, error) {
	resp, err := send(connector.Request{
		Method:  "GET",
		URL:     t.API + "/rest/api/2/myself",
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return 0, err
	}
	return resp.Status, nil
}

// statuses names every status in the project's workflows, across issue types.
func statuses(t tracker.Target) ([]string, error) {
	var perType []struct {
		Statuses []struct {
			Name string `json:"name"`
		} `json:"statuses"`
	}
	if err := get(t, "/rest/api/2/project/"+url.PathEscape(t.Repo)+"/statuses", &perType); err != nil {
		return nil, err
	}
	names := []string{}
	for _, it := range perType {
		for _, s := range it.Statuses {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

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
func createMetaRows(t tracker.Target, path string) ([]createMetaRow, error) {
	var all []createMetaRow
	for range maxCreateMetaPages {
		var page createMetaPage
		q := fmt.Sprintf("%s?startAt=%d&maxResults=%d", path, len(all), createMetaPageSize)
		if err := get(t, q, &page); err != nil {
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

// createMeta reports the issue types the project offers and the create fields
// of issueType (exact name match), none when it is not offered.
func createMeta(t tracker.Target, issueType string) (tracker.CreateMeta, error) {
	base := "/rest/api/2/issue/createmeta/" + url.PathEscape(t.Repo) + "/issuetypes"
	types, err := createMetaRows(t, base)
	if err != nil {
		return tracker.CreateMeta{}, err
	}
	cm := tracker.CreateMeta{IssueTypes: []string{}, Fields: []tracker.CreateField{}}
	typeID := ""
	for _, ty := range types {
		cm.IssueTypes = append(cm.IssueTypes, ty.Name)
		if ty.Name == issueType {
			typeID = ty.ID
		}
	}
	if typeID == "" {
		return cm, nil
	}
	rows, err := createMetaRows(t, base+"/"+url.PathEscape(typeID))
	if err != nil {
		return tracker.CreateMeta{}, err
	}
	for _, r := range rows {
		cm.Fields = append(cm.Fields, tracker.CreateField{ID: r.FieldID, Name: r.Name, Required: r.Required, HasDefaultValue: r.HasDefaultValue})
	}
	return cm, nil
}

// count sizes the set listIssues(q) returns without walking it: Cloud's
// approximate count, Server's search total from a zero-row search.
func count(t tracker.Target, q tracker.Query) (int, error) {
	query := openJQL(t.Repo, q)
	switch t.Dialect {
	case "cloud":
		var out struct {
			Count int `json:"count"`
		}
		if err := do(t, "POST", "/rest/api/3/search/approximate-count", map[string]string{"jql": query}, &out); err != nil {
			return 0, err
		}
		return out.Count, nil
	case "server":
		var p searchPayload
		v := url.Values{"jql": {query}, "maxResults": {"0"}}
		if err := get(t, "/rest/api/2/search?"+v.Encode(), &p); err != nil {
			return 0, err
		}
		return p.Total, nil
	}
	return 0, fmt.Errorf("count: unknown Jira dialect %q (want \"cloud\" or \"server\")", t.Dialect)
}
