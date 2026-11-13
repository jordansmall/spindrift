package main

import (
	"encoding/json"
	"errors"
	"strconv"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

// researchMarker and dedupSeparator mirror core's forge.ResearchMarker and the
// dedupmarker.Append layout; this module cannot import cmd/launcher.
const (
	researchMarker = "<!-- spindrift-research -->"
	dedupSeparator = "\n\n"
)

// trackerTransition always answers unmapped: GitHub has no statuses, and a
// Connector has no config channel to name the lifecycle labels, so core falls
// back to the label swap through editLabels. Registering it is what makes core
// attempt transitions at all; unmapped is right because a GitHub Mapping can
// name no status.
func trackerTransition(tracker.Target, string, tracker.TransitionRequest) (tracker.TransitionResult, error) {
	return tracker.TransitionResult{Outcome: tracker.TransitionUnmapped}, nil
}

func postJSON(t tracker.Target, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if out == nil {
		_, err = fetch("POST", t.API, path, b)
		return err
	}
	return call("POST", t.API, path, b, out)
}

func nonEmpty(names []string) []string {
	var out []string
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// editLabels adds then removes labels, skipping empty names. GitHub has no
// single combined edit, so this is three calls: a REST POST for the adds, then
// a GraphQL query for the ids and a mutation for the removals. A failure after
// the add leaves the add applied.
//
// Removal rides GraphQL (as `gh issue edit --remove-label` does) because REST
// removes by `DELETE .../labels/{name}`, and an escaped "/" in a name such as
// agent/ready fails the host's canonical-path check.
func editLabels(t tracker.Target, number string, add, remove []string) error {
	base, err := numberPath(t, number)
	if err != nil {
		return err
	}
	names := nonEmpty(add)
	if len(names) > 0 {
		if err := postJSON(t, base+"/labels", map[string][]string{"labels": names}, nil); err != nil {
			return err
		}
	}
	return removeLabels(t, number, remove)
}

// removeLabels resolves the issue and label node ids in one query, then removes
// the labels in one mutation. A label the repository lacks is already absent,
// the state asked for; a missing issue is an error.
func removeLabels(t tracker.Target, number string, remove []string) error {
	names := nonEmpty(remove)
	if len(names) == 0 {
		return nil
	}
	owner, repo, err := splitRepo(t.Repo)
	if err != nil {
		return err
	}
	n, err := parseNumber(number)
	if err != nil {
		return err
	}
	// Names travel as variables, never interpolated into the document.
	decls := "$owner:String!,$repo:String!,$number:Int!"
	sel := "issue(number:$number){id}"
	vars := map[string]any{"owner": owner, "repo": repo, "number": n}
	for i, name := range names {
		v := "n" + strconv.Itoa(i)
		decls += ",$" + v + ":String!"
		sel += " l" + strconv.Itoa(i) + ":label(name:$" + v + "){id}"
		vars[v] = name
	}
	var data struct {
		Repository map[string]json.RawMessage `json:"repository"`
	}
	if err := graphql(t.API, "query("+decls+"){repository(owner:$owner,name:$repo){"+sel+"}}", vars, &data); err != nil {
		return err
	}
	if data.Repository == nil {
		return connector.Errorf(connector.NotFound, "POST /graphql: no repository %s", t.Repo)
	}
	var issue *struct {
		ID string `json:"id"`
	}
	if raw := data.Repository["issue"]; raw != nil {
		if err := json.Unmarshal(raw, &issue); err != nil {
			return err
		}
	}
	if issue == nil {
		return connector.Errorf(connector.NotFound, "POST /graphql: no issue %s in %s", number, t.Repo)
	}
	var ids []string
	for i := range names {
		var label *struct {
			ID string `json:"id"`
		}
		if raw := data.Repository["l"+strconv.Itoa(i)]; raw != nil {
			if err := json.Unmarshal(raw, &label); err != nil {
				return err
			}
		}
		if label != nil {
			ids = append(ids, label.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return graphql(t.API,
		"mutation($id:ID!,$ids:[ID!]!){removeLabelsFromLabelable(input:{labelableId:$id,labelIds:$ids}){clientMutationId}}",
		map[string]any{"id": issue.ID, "ids": ids}, nil)
}

// issueComment posts r.Body, with the research marker line restored atop it
// when core stripped it (GitHub has no entity property to keep it in).
func issueComment(t tracker.Target, number string, r tracker.CommentRequest) error {
	base, err := numberPath(t, number)
	if err != nil {
		return err
	}
	body := r.Body
	if r.Research {
		body = researchMarker + "\n" + body
	}
	return postJSON(t, base+"/comments", map[string]string{"body": body}, nil)
}

// fileIssue creates the issue and returns its html_url. GitHub has no entity
// property, so the dedup marker goes back into the body as its last line.
// IssueType is ignored, as the native adapter ignores it.
func fileIssue(t tracker.Target, r tracker.IssueRequest) (string, error) {
	body := r.Body
	if r.DedupMarker != "" {
		if body == "" {
			body = r.DedupMarker
		} else {
			body += dedupSeparator + r.DedupMarker
		}
	}
	req := struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}{r.Title, body, append([]string{}, r.Labels...)}
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	if err := postJSON(t, repoPath(t), req, &created); err != nil {
		return "", err
	}
	if created.HTMLURL == "" {
		return "", errors.New("github: create issue response carried no html_url")
	}
	return created.HTMLURL, nil
}

func labelsPath(t tracker.Target) string { return "/repos/" + t.Repo + "/labels" }

// listLabels returns every label name in the repository.
func listLabels(t tracker.Target) ([]string, error) {
	names := []string{}
	path := labelsPath(t) + "?per_page=" + strconv.Itoa(pageSize)
	err := pages(t.API, path, func(raw []struct {
		Name string `json:"name"`
	}) bool {
		for _, l := range raw {
			names = append(names, l.Name)
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

// createLabel fails on an existing label, as `gh label create` does without
// --force; core lists first and creates only the missing ones.
func createLabel(t tracker.Target, name, description, color string) error {
	return postJSON(t, labelsPath(t), map[string]string{
		"name": name, "description": description, "color": color,
	}, nil)
}
