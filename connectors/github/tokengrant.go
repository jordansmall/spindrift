package main

import (
	"fmt"
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

// tokenGrant reports what the token the host attached may do. The host sends
// only the token's prefix up to its first "_", so a fine-grained PAT
// (github_pat_...) arrives as "github_". GitHub signals write capability
// differently per shape: X-OAuth-Scopes for classic and OAuth tokens, and
// permissions.push for an App installation token, which has no ambient user
// role to inflate it. Any other shape is not introspectable.
func tokenGrant(t forge.Target, prefix string) (forge.Grant, error) {
	switch prefix {
	case "ghp_", "gho_":
		resp, err := request("GET", t.API, "/user", nil)
		if err != nil {
			return forge.Grant{}, err
		}
		if !is2xx(resp.Status) {
			return forge.Grant{}, statusError("GET", "/user", resp.Status)
		}
		// Absent or empty scopes grant nothing, so neither is an error.
		value, _ := responseHeader("X-OAuth-Scopes")
		write := false
		for _, s := range strings.Split(value, ",") {
			write = write || writeScopes[strings.TrimSpace(s)]
		}
		return forge.Grant{Introspectable: true, WriteCapable: write}, nil
	case "ghs_":
		var repo struct {
			Permissions *struct {
				Push *bool `json:"push"`
			} `json:"permissions"`
		}
		if err := call("GET", t.API, forgePath(t, ""), nil, &repo); err != nil {
			return forge.Grant{}, err
		}
		if repo.Permissions == nil || repo.Permissions.Push == nil {
			return forge.Grant{}, fmt.Errorf("repo permissions field missing or incomplete for %s; cannot determine write capability", t.Repo)
		}
		return forge.Grant{Introspectable: true, WriteCapable: *repo.Permissions.Push}, nil
	}
	return forge.Grant{Reason: notIntrospectableReason}, nil
}
