package main

import (
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
		if _, err := send("POST", t.API, base+"/labels", map[string][]string{"labels": names}, nil); err != nil {
			return err
		}
	}
	return removeLabels(t, number, remove)
}

// issueVars is the owner/repo/number variable set every issue-node GraphQL
// query takes.
func issueVars(t tracker.Target, number string) (map[string]any, error) {
	owner, repo, err := splitRepo(t.Repo)
	if err != nil {
		return nil, err
	}
	n, err := parseNumber(number)
	if err != nil {
		return nil, err
	}
	return map[string]any{"owner": owner, "repo": repo, "number": n}, nil
}

func errNoRepository(t tracker.Target) error {
	return connector.Errorf(connector.NotFound, "POST /graphql: no repository %s", t.Repo)
}

func errNoIssue(t tracker.Target, number string) error {
	return connector.Errorf(connector.NotFound, "POST /graphql: no issue %s in %s", number, t.Repo)
}

// removeLabels resolves each label's node id, one fixed lookup per name, then
// removes the labels in one mutation. A label the repository lacks is already
// absent, the state asked for; a missing issue is an error.
func removeLabels(t tracker.Target, number string, remove []string) error {
	names := nonEmpty(remove)
	if len(names) == 0 {
		return nil
	}
	vars, err := issueVars(t, number)
	if err != nil {
		return err
	}
	var issueID string
	var ids []string
	// N labels cost N+1 GraphQL requests: a fixed document cannot take a list
	// of names to look up, so the rate-limit cost grows with the label count.
	for _, name := range names {
		// Names travel as variables, never interpolated into the document.
		vars["name"] = name
		var data struct {
			Repository *struct {
				Issue *struct {
					ID string `json:"id"`
				} `json:"issue"`
				Label *struct {
					ID string `json:"id"`
				} `json:"label"`
			} `json:"repository"`
		}
		if err := graphql(t.API, labelIDQuery, vars, &data); err != nil {
			return err
		}
		if data.Repository == nil {
			return errNoRepository(t)
		}
		if data.Repository.Issue == nil {
			return errNoIssue(t, number)
		}
		issueID = data.Repository.Issue.ID
		if data.Repository.Label != nil {
			ids = append(ids, data.Repository.Label.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return graphql(t.API, removeLabelsMutation, map[string]any{"id": issueID, "ids": ids}, nil)
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
	_, err = send("POST", t.API, base+"/comments", map[string]string{"body": body}, nil)
	return err
}

// fileIssue creates the issue and returns its html_url. GitHub has no entity
// property, so the dedup marker goes back into the body as its last line.
// IssueType is ignored.
func fileIssue(t tracker.Target, r tracker.IssueRequest) (string, error) {
	labels, err := tracker.StampRouteLabels(r.Labels, r.Route)
	if err != nil {
		return "", err
	}
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
	}{r.Title, body, labels}
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	if _, err := send("POST", t.API, repoPath(t), req, &created); err != nil {
		return "", err
	}
	if created.HTMLURL == "" {
		return "", errors.New("github: create issue response carried no html_url")
	}
	return created.HTMLURL, nil
}

// closeIssue closes the issue as completed over GraphQL, as `gh issue close
// --reason completed` does: the id query and the mutation both ride the
// /graphql route already declared.
func closeIssue(t tracker.Target, number string) error {
	vars, err := issueVars(t, number)
	if err != nil {
		return err
	}
	var data struct {
		Repository *struct {
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"repository"`
	}
	if err := graphql(t.API, issueIDQuery, vars, &data); err != nil {
		return err
	}
	if data.Repository == nil {
		return errNoRepository(t)
	}
	if data.Repository.Issue == nil {
		return errNoIssue(t, number)
	}
	return graphql(t.API, closeIssueMutation, map[string]any{"id": data.Repository.Issue.ID}, nil)
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
	_, err := send("POST", t.API, labelsPath(t), map[string]string{
		"name": name, "description": description, "color": color,
	}, nil)
	return err
}
