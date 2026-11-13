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
const repoQuery = `query($owner:String!,$repo:String!){repository(owner:$owner,name:$repo){url nameWithOwner autoMergeAllowed}}`

type rawRepo struct {
	URL              string `json:"url"`
	NameWithOwner    string `json:"nameWithOwner"`
	AutoMergeAllowed bool   `json:"autoMergeAllowed"`
}

func repository(t forge.Target) (rawRepo, error) {
	owner, name, err := splitRepo(t.Repo)
	if err != nil {
		return rawRepo{}, err
	}
	body, err := json.Marshal(map[string]any{
		"query":     repoQuery,
		"variables": map[string]any{"owner": owner, "repo": name},
	})
	if err != nil {
		return rawRepo{}, err
	}
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			Repository *rawRepo `json:"repository"`
		} `json:"data"`
	}
	if err := call("POST", t.API, "/graphql", body, &resp); err != nil {
		return rawRepo{}, err
	}
	if len(resp.Errors) > 0 {
		return rawRepo{}, fmt.Errorf("POST /graphql: %s", resp.Errors[0].Message)
	}
	if resp.Data.Repository == nil {
		return rawRepo{}, connector.Errorf(connector.NotFound, "POST /graphql: no repository %s", t.Repo)
	}
	return *resp.Data.Repository, nil
}

func remote(t forge.Target) (forge.Remote, error) {
	r, err := repository(t)
	if err != nil {
		return forge.Remote{}, err
	}
	return forge.Remote{CloneURL: r.URL + ".git", WebURL: r.URL}, nil
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
	if err := call("GET", t.API, "/repos/"+t.Repo+"/git/matching-refs/heads/"+p, nil, &refs); err != nil {
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
	path := "/repos/" + t.Repo + "/git/refs/heads/" + p
	resp, err := request("DELETE", t.API, path, nil)
	if err != nil {
		return err
	}
	switch {
	case resp.Status >= 200 && resp.Status <= 299:
		return nil
	case resp.Status == 422:
		// GitHub answers 422 "Reference does not exist" for a missing ref; core
		// counts a not-found delete as done.
		return connector.Errorf(connector.NotFound, "DELETE %s: status 422", path)
	}
	return statusError("DELETE", path, resp.Status)
}
