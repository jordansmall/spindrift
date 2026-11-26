package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

// repoQuery reads the repository facts the forge primitives need. It goes over
// the /graphql route the Connector already declares; REST's GET /repos/{repo}
// would need a route admitting every GET under the repository.
const repoQuery = `query($owner:String!,$repo:String!){repository(owner:$owner,name:$repo){nameWithOwner autoMergeAllowed}}`

type rawRepo struct {
	NameWithOwner    string `json:"nameWithOwner"`
	AutoMergeAllowed bool   `json:"autoMergeAllowed"`
}

func repository(t forge.Target) (rawRepo, error) {
	owner, name, err := splitRepo(t.Repo)
	if err != nil {
		return rawRepo{}, err
	}
	var data struct {
		Repository *rawRepo `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "repo": name}
	if err := graphql(t.API, repoQuery, vars, &data); err != nil {
		return rawRepo{}, err
	}
	if data.Repository == nil {
		return rawRepo{}, connector.Errorf(connector.NotFound, "POST /graphql: no repository %s", t.Repo)
	}
	return *data.Repository, nil
}

// probe is the repository's canonical owner/name.
func probe(t forge.Target) (string, error) {
	r, err := repository(t)
	return r.NameWithOwner, err
}

func autoMergeAllowed(t forge.Target) (bool, error) {
	r, err := repository(t)
	return r.AutoMergeAllowed, err
}

// branchPath escapes branch for the URL path, keeping "/" a separator
// (agent/issue-N). An empty, "." or ".." segment is refused: it would
// resolve outside the route subtree the host allows.
func branchPath(branch string) (string, error) {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("branch %q has an empty, \".\" or \"..\" path segment", branch)
		}
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/"), nil
}

func branchExists(t forge.Target, branch string) (bool, error) {
	p, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	// matching-refs is a prefix match: agent/issue-1 also lists agent/issue-10.
	var refs []struct {
		Ref string `json:"ref"`
	}
	if _, err := send("GET", t.API, forgePath(t, "/git/matching-refs/heads/"+p), nil, &refs); err != nil {
		return false, err
	}
	for _, r := range refs {
		if r.Ref == "refs/heads/"+branch {
			return true, nil
		}
	}
	return false, nil
}

func deleteBranch(t forge.Target, branch string) error {
	p, err := branchPath(branch)
	if err != nil {
		return err
	}
	path := forgePath(t, "/git/refs/heads/"+p)
	resp, err := send("DELETE", t.API, path, nil, nil)
	if err != nil && resp.Status == 422 {
		// GitHub answers 422 "Reference does not exist" for a missing ref; core
		// counts a not-found delete as done.
		return connector.Errorf(connector.NotFound, "DELETE %s: status 422", path)
	}
	return err
}

// graphql posts one document and fails on a GraphQL errors array, which rides
// a 200. data, when non-nil, receives the response's data object.
func graphql(api, query string, vars map[string]any, data any) error {
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data json.RawMessage `json:"data"`
	}
	if _, err := send("POST", api, "/graphql", map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("POST /graphql: %s", resp.Errors[0].Message)
	}
	if data == nil {
		return nil
	}
	return json.Unmarshal(resp.Data, data)
}

func open(t forge.Target, p forge.NewPR) (forge.PR, error) {
	var raw rawPR
	if _, err := send("POST", t.API, forgePath(t, "/pulls"), p, &raw); err != nil {
		return forge.PR{}, err
	}
	return raw.pr(), nil
}

func behind(t forge.Target, number string) (int, error) {
	raw, err := readPR(t, number)
	if err != nil {
		return 0, err
	}
	// base...head counts commits reachable from base but not head. The slash in
	// agent/issue-N stays literal: the host refuses an escaped path, and GitHub
	// resolves the wildcard basehead without one.
	base, err := branchPath(raw.Base.Ref)
	if err != nil {
		return 0, err
	}
	head, err := branchPath(raw.Head.Ref)
	if err != nil {
		return 0, err
	}
	basehead := base + "..." + head
	var cmp struct {
		BehindBy int `json:"behind_by"`
	}
	_, err = send("GET", t.API, forgePath(t, "/compare/"+basehead), nil, &cmp)
	return cmp.BehindBy, err
}

// prFiles lists every path the PR changes, a deleted file under its old path.
func prFiles(t forge.Target, number string) ([]string, error) {
	if _, err := parseNumber(number); err != nil {
		return nil, err
	}
	files := []string{}
	path := forgePath(t, fmt.Sprintf("/pulls/%s/files?per_page=%d", number, pageSize))
	err := pages(t.API, path, func(raw []struct {
		Filename string `json:"filename"`
	}) bool {
		for _, f := range raw {
			files = append(files, f.Filename)
		}
		return false
	})
	return files, err
}

// setDraft is idempotent, like `gh pr ready`: the mutations error on a PR
// already in the requested state.
func setDraft(t forge.Target, number string, draft bool) error {
	raw, err := readPR(t, number)
	if err != nil || raw.Draft == draft {
		return err
	}
	doc := markReadyForReviewMutation
	if draft {
		doc = convertToDraftMutation
	}
	return graphql(t.API, doc, map[string]any{"id": raw.NodeID}, nil)
}

func comment(t forge.Target, number, body string) error {
	if _, err := parseNumber(number); err != nil {
		return err
	}
	_, err := send("POST", t.API, forgePath(t, "/issues/"+number+"/comments"), map[string]string{"body": body}, nil)
	return err
}

// merge always rebases: the protocol carries no method (core refuses any other
// MERGE_METHOD for a routed row). GitHub answers 405 both for a genuine refusal
// (failing checks, missing review, merge conflicts) and for a disallowed
// method, so only a 405 whose message names the former is NotMergeable, the
// class core re-reads the PR for; a repo with rebase disabled must fail fast
// instead.
func merge(t forge.Target, number string) error {
	if _, err := parseNumber(number); err != nil {
		return err
	}
	path := forgePath(t, "/pulls/"+number+"/merge")
	resp, err := send("PUT", t.API, path, map[string]string{"merge_method": "rebase"}, nil)
	if err == nil {
		return nil
	}
	if resp.Status == 405 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resp.Body, &m)
		switch {
		case refusesPRState(m.Message):
			return connector.Errorf(connector.NotMergeable, "PUT %s: status 405: %s", path, m.Message)
		case m.Message != "":
			return connector.Errorf(connector.Permanent, "PUT %s: status 405: %s", path, m.Message)
		}
	}
	return err
}

// refusesPRState reports whether a 405 message blames the PR's own state
// (checks, review, conflicts) rather than the repository's merge settings.
func refusesPRState(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "not mergeable") ||
		strings.Contains(message, "status check") ||
		strings.Contains(message, "merge conflicts") ||
		strings.Contains(message, "review is required")
}

func enqueueAutoMerge(t forge.Target, number string) error {
	raw, err := readPR(t, number)
	if err != nil {
		return err
	}
	return graphql(t.API, enableAutoMergeMutation, map[string]any{"id": raw.NodeID}, nil)
}

// branchProtected reports classic protection or a repository ruleset. A 404
// rules out a classic rule, so the ruleset count decides; a 403 (a fine-grained
// PAT without Administration: read) leaves the classic rule unread, so only a
// ruleset is definitive and none is an error, never "unprotected".
func branchProtected(t forge.Target, branch string) (bool, error) {
	p, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	path := forgePath(t, "/branches/"+p+"/protection")
	resp, err := send("GET", t.API, path, nil, nil)
	switch {
	case err == nil:
		return true, nil
	case resp.Status != 404 && resp.Status != 403:
		return false, err
	}
	// The rules endpoint answers 200 with [] when none applies, never 404.
	var rules []json.RawMessage
	if _, err := send("GET", t.API, forgePath(t, "/rules/branches/"+p), nil, &rules); err != nil {
		return false, err
	}
	if len(rules) > 0 || resp.Status == 404 {
		return len(rules) > 0, nil
	}
	return false, fmt.Errorf("GET %s: status 403: classic protection unreadable and no ruleset applies", path)
}
