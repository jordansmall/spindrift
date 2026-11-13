package main

import (
	"fmt"
	"strconv"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

// responseHeader reads a header of the latest response; tests swap it because
// connector.ResponseHeader has no host outside wasip1.
var responseHeader = connector.ResponseHeader

// notIntrospectableReason is the read-only gate's explanation for a token whose
// grants GitHub does not expose. Core renders it verbatim, so it matches the
// native adapter's wording.
const notIntrospectableReason = "e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect"

// writeScopes are the classic/OAuth scopes granting repository write access;
// classic tokens have no read-only variant of repo access.
var writeScopes = map[string]bool{"repo": true, "public_repo": true}

// viewerQuery is the cheapest authenticated read; only its X-OAuth-Scopes
// response header is used.
const viewerQuery = `{viewer{login}}`

// tokenGrant reports what the token the host attached may do. The host sends
// only the token's prefix up to its first "_", so a fine-grained PAT
// (github_pat_...) arrives as "github_". GitHub signals write capability
// differently per shape: X-OAuth-Scopes for classic and OAuth tokens, and the
// repository's `permissions.push` for an App installation token. Any other
// shape is not introspectable.
//
// The classic read goes over `POST /graphql`, which the Connector already
// declares. The installation token's push comes from `GET
// /installation/repositories`, a root that lets every load of the Connector
// list the repositories the installation reaches, with their permissions (a
// small widening beside what `POST /graphql` already admits): GraphQL's
// viewerPermission is null for an App, and REST's `GET /user` and `GET
// /repos/{repo}` would each need a subtree-admitting Route that every load of
// the Connector would inherit.
func tokenGrant(t forge.Target, prefix string) (forge.Grant, error) {
	switch prefix {
	case "ghp_", "gho_":
		if err := graphql(t.API, viewerQuery, map[string]any{}, nil); err != nil {
			return forge.Grant{}, err
		}
		// Absent or empty scopes grant nothing, so neither is an error.
		value, _ := responseHeader("X-OAuth-Scopes")
		var scopes []string
		write := false
		for _, s := range strings.Split(value, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			scopes = append(scopes, s)
			write = write || writeScopes[s]
		}
		return forge.Grant{Introspectable: true, WriteCapable: write, Scopes: scopes}, nil
	case "ghs_":
		push, err := installationPush(t)
		if err != nil {
			return forge.Grant{}, err
		}
		return forge.Grant{Introspectable: true, WriteCapable: push}, nil
	}
	return forge.Grant{Reason: notIntrospectableReason}, nil
}

// installationPush walks the installation's repositories for t.Repo and returns
// its `permissions.push`. A repository the installation does not list, or one
// listed without a push permission, is an error rather than a guess; this
// mirrors forge/github.TokenRepoPushPermission, which a wasip1 module cannot
// import.
func installationPush(t forge.Target) (bool, error) {
	type page struct {
		Repositories []struct {
			FullName    string `json:"full_name"`
			Permissions *struct {
				Push *bool `json:"push"`
			} `json:"permissions"`
		} `json:"repositories"`
	}
	var push *bool
	found := false
	err := pagesOf(t.API, "/installation/repositories?per_page="+strconv.Itoa(pageSize),
		func(p page) int { return len(p.Repositories) },
		func(p page) bool {
			for _, r := range p.Repositories {
				if strings.EqualFold(r.FullName, t.Repo) {
					found = true
					if r.Permissions != nil {
						push = r.Permissions.Push
					}
					return true
				}
			}
			return false
		})
	switch {
	case err != nil:
		return false, err
	case !found:
		return false, fmt.Errorf("%s is not among the installation's repositories; cannot determine write capability", t.Repo)
	case push == nil:
		return false, fmt.Errorf("installation repository %s carries no push permission; cannot determine write capability", t.Repo)
	}
	return *push, nil
}
