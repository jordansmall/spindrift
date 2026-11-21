package main

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

// researchMarker and dedupSeparator mirror core's forge.ResearchMarker and the
// dedupmarker.Append layout; this module cannot import cmd/launcher.
const (
	researchMarker = "<!-- spindrift-research -->"
	dedupSeparator = "\n\n"
)

// transition always answers unmapped: Forgejo has no statuses, so core falls
// back to the label swap through editLabels. Registering it is what makes core
// attempt transitions at all.
func transition(tracker.Target, string, tracker.TransitionRequest) (tracker.TransitionResult, error) {
	return tracker.TransitionResult{Outcome: tracker.TransitionUnmapped}, nil
}

// setLabels replaces the issue's whole label set by name; the empty set is [],
// never null.
func setLabels(t tracker.Target, number string, names []string) error {
	path, err := numberedPath(t, number, "/labels")
	if err != nil {
		return err
	}
	return call("PUT", t.API, path, marshal(map[string][]string{"labels": append([]string{}, names...)}), nil)
}

// editLabels reads the current set and writes it back with the edit applied:
// Forgejo has no additive edit by name, and a DELETE by name would carry the
// escaped "/" of a name like agent/ready, which the Connector host refuses.
// The read-then-write is not atomic.
func editLabels(t tracker.Target, number string, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	cur, err := issue(t, number)
	if err != nil {
		return err
	}
	names := slices.DeleteFunc(cur.Labels, func(n string) bool { return slices.Contains(remove, n) })
	for _, n := range add {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return setLabels(t, number, names)
}

// issueComment posts r.Body, with the research marker line restored atop it
// when core stripped it (Forgejo has no entity property to keep it in).
func issueComment(t tracker.Target, number string, r tracker.CommentRequest) error {
	path, err := numberedPath(t, number, "/comments")
	if err != nil {
		return err
	}
	body := r.Body
	if r.Research {
		body = researchMarker + "\n" + body
	}
	return call("POST", t.API, path, marshal(map[string]string{"body": body}), nil)
}

// closeIssue closes unconditionally; core has already read the issue.
func closeIssue(t tracker.Target, number string) error {
	path, err := numberedPath(t, number, "")
	if err != nil {
		return err
	}
	return call("PATCH", t.API, path, marshal(map[string]string{"state": "closed"}), nil)
}

type rawLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// labelUnion is the repository's labels followed by the owner's org-only ones,
// the repo winning a name clash. orgUnreadable reports that the org lookup was
// refused for lack of auth, so an org-only label may exist unseen.
func labelUnion(t tracker.Target) (labels []rawLabel, orgUnreadable bool, err error) {
	owner, _, _ := strings.Cut(t.Repo, "/")
	if owner == "" {
		return nil, false, fmt.Errorf("repo %q has no owner", t.Repo)
	}
	key := func(l rawLabel) string { return l.Name }
	labels, err = pageUntilEmpty(t.API, issuePath(t, "/labels"), url.Values{}, key)
	if err != nil {
		return nil, false, err
	}
	org, err := pageUntilEmpty(t.API, "/api/v1/orgs/"+url.PathEscape(owner)+"/labels", url.Values{}, key)
	var ce *connector.Error
	switch {
	case errors.As(err, &ce) && ce.Class == connector.NotFound:
		// A user-owned repo has no org.
		org = nil
	case errors.As(err, &ce) && ce.Class == connector.Auth:
		// The token lacks read:organization; the repo read already worked.
		org, orgUnreadable = nil, true
	case err != nil:
		return nil, false, err
	}
	seen := map[string]bool{}
	for _, l := range labels {
		seen[l.Name] = true
	}
	for _, l := range org {
		if !seen[l.Name] {
			seen[l.Name] = true
			labels = append(labels, l)
		}
	}
	return labels, orgUnreadable, nil
}

func listLabels(t tracker.Target) ([]string, error) {
	labels, _, err := labelUnion(t)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names, nil
}

// createLabel takes the colour as bare hex; Forgejo needs the leading #.
func createLabel(t tracker.Target, name, description, color string) error {
	return call("POST", t.API, issuePath(t, "/labels"),
		marshal(map[string]string{"name": name, "description": description, "color": "#" + color}), nil)
}

// fileIssue creates the issue and returns its html_url; r.IssueType is ignored,
// as Forgejo has no native issue types. Forgejo's create takes label IDs, so
// names resolve against the label union first. With the org lookup unreadable,
// an unresolved name files anyway and every name is then applied by name; that
// apply failing still answers the URL, since core reads ("", err) as nothing
// filed and would refile (#4459).
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
	ids := []int64{}
	var applyByName bool
	if len(labels) > 0 {
		defined, orgUnreadable, err := labelUnion(t)
		if err != nil {
			return "", err
		}
		var missing []string
		for _, n := range labels {
			i := slices.IndexFunc(defined, func(l rawLabel) bool { return l.Name == n })
			if i < 0 {
				missing = append(missing, n)
			} else {
				ids = append(ids, defined[i].ID)
			}
		}
		if len(missing) > 0 && !orgUnreadable {
			return "", fmt.Errorf("forgejo: label(s) not defined on %s or its owner: %s", t.Repo, strings.Join(missing, ", "))
		}
		applyByName = len(missing) > 0
	}
	var created struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := call("POST", t.API, issuePath(t, "/issues"),
		marshal(map[string]any{"title": r.Title, "body": body, "labels": ids}), &created); err != nil {
		return "", err
	}
	if created.HTMLURL == "" {
		return "", errors.New("forgejo: create issue response carried no html_url")
	}
	if applyByName {
		_ = setLabels(t, strconv.Itoa(created.Number), labels)
	}
	return created.HTMLURL, nil
}
