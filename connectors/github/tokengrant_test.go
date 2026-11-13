package main

import (
	"errors"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

// stubScopes replaces responseHeader with a host that served the named
// header's value, recording the names asked for.
func stubScopes(t *testing.T, value string, present bool) *[]string {
	t.Helper()
	var asked []string
	old := responseHeader
	responseHeader = func(name string) (string, bool) {
		asked = append(asked, name)
		return value, present
	}
	t.Cleanup(func() { responseHeader = old })
	return &asked
}

func TestTokenGrantOAuthScopes(t *testing.T) {
	for name, tc := range map[string]struct {
		prefix, header string
		present, write bool
	}{
		"classic repo":          {"ghp_", "read:org, repo", true, true},
		"classic public_repo":   {"ghp_", "public_repo", true, true},
		"oauth public_repo":     {"gho_", "public_repo", true, true},
		"classic read only":     {"ghp_", "read:org", true, false},
		"no scopes":             {"ghp_", "", true, false},
		"header absent":         {"ghp_", "", false, false},
		"similar scope is not":  {"gho_", "repo:status, repository_hooks", true, false},
		"scope list is trimmed": {"gho_", "  notifications ,repo  ", true, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := stub(t, map[string]connector.Response{"https://api.github.com/user": ok("{}")})
			asked := stubScopes(t, tc.header, tc.present)
			g, err := tokenGrant(ftgt, tc.prefix)
			if err != nil || g != (forge.Grant{Introspectable: true, WriteCapable: tc.write}) {
				t.Fatalf("tokenGrant = %+v, %v; want introspectable, write=%v", g, err, tc.write)
			}
			if len(*got) != 1 || (*got)[0].Method != "GET" || len(*asked) != 1 || (*asked)[0] != "X-OAuth-Scopes" {
				t.Errorf("requests %+v, headers asked %v; want one GET /user and X-OAuth-Scopes", *got, *asked)
			}
		})
	}
}

func TestTokenGrantInstallationTokenReadsRepoPush(t *testing.T) {
	for body, want := range map[string]bool{
		`{"permissions":{"push":true}}`:  true,
		`{"permissions":{"push":false}}`: false,
	} {
		got := stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r": ok(body)})
		g, err := tokenGrant(ftgt, "ghs_")
		if err != nil || g != (forge.Grant{Introspectable: true, WriteCapable: want}) {
			t.Fatalf("%s: tokenGrant = %+v, %v", body, g, err)
		}
		if len(*got) != 1 || (*got)[0].Method != "GET" {
			t.Errorf("requests = %+v, want one GET", *got)
		}
	}
}

func TestTokenGrantRepoPermissionsMissingIsAnError(t *testing.T) {
	for _, body := range []string{`{}`, `{"permissions":{}}`, `{"permissions":null}`, `not json`} {
		stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r": ok(body)})
		if g, err := tokenGrant(ftgt, "ghs_"); err == nil {
			t.Errorf("%s: tokenGrant = %+v, want an error", body, g)
		}
	}
}

func TestTokenGrantNotIntrospectableMakesNoRequest(t *testing.T) {
	for _, prefix := range []string{"github_", "", "xyz_", "ghu_"} {
		got := stub(t, nil)
		g, err := tokenGrant(ftgt, prefix)
		if err != nil || g != (forge.Grant{Reason: notIntrospectableReason}) {
			t.Errorf("%q: tokenGrant = %+v, %v; want not introspectable with the reason", prefix, g, err)
		}
		if len(*got) != 0 {
			t.Errorf("%q: sent %+v, want no request", prefix, *got)
		}
	}
}

func TestTokenGrantNon2xxIsAnError(t *testing.T) {
	for _, prefix := range []string{"ghp_", "gho_", "ghs_"} {
		stub(t, map[string]connector.Response{
			"https://api.github.com/user":      {Status: 401, Body: []byte("{}")},
			"https://api.github.com/repos/o/r": {Status: 401, Body: []byte("{}")},
		})
		stubScopes(t, "repo", true)
		_, err := tokenGrant(ftgt, prefix)
		var ce *connector.Error
		if err == nil || !errors.As(err, &ce) || ce.Class != connector.Auth {
			t.Errorf("%q: err = %v, want an auth-class error", prefix, err)
		}
	}
}
