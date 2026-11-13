package main

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
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
		scopes         []string
	}{
		"classic repo":          {"ghp_", "read:org, repo", true, true, []string{"read:org", "repo"}},
		"classic public_repo":   {"ghp_", "public_repo", true, true, []string{"public_repo"}},
		"oauth public_repo":     {"gho_", "public_repo", true, true, []string{"public_repo"}},
		"classic read only":     {"ghp_", "read:org", true, false, []string{"read:org"}},
		"no scopes":             {"ghp_", "", true, false, nil},
		"header absent":         {"ghp_", "", false, false, nil},
		"similar scope is not":  {"gho_", "repo:status, repository_hooks", true, false, []string{"repo:status", "repository_hooks"}},
		"scope list is trimmed": {"gho_", "  notifications ,repo  ", true, true, []string{"notifications", "repo"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := stub(t, map[string]connector.Response{graphqlURL: ok(`{"data":{"viewer":{"login":"bot"}}}`)})
			asked := stubScopes(t, tc.header, tc.present)
			g, err := tokenGrant(ftgt, tc.prefix)
			if want := (forge.Grant{Introspectable: true, WriteCapable: tc.write, Scopes: tc.scopes}); err != nil || !reflect.DeepEqual(g, want) {
				t.Fatalf("tokenGrant = %+v, %v; want %+v", g, err, want)
			}
			if len(*got) != 1 || (*got)[0].Method != "POST" || !strings.Contains((*got)[0].Body, "viewer") || len(*asked) != 1 || (*asked)[0] != "X-OAuth-Scopes" {
				t.Errorf("requests %+v, headers asked %v; want one POST /graphql viewer query and X-OAuth-Scopes", *got, *asked)
			}
		})
	}
}

const installationURL = "https://api.github.com/installation/repositories?per_page=100&page="

// installationRepos renders an /installation/repositories answer; each entry is
// a full_name and its raw permissions object ("" leaves the object out).
func installationRepos(entries ...[2]string) string {
	var parts []string
	for _, e := range entries {
		r := `{"full_name":"` + e[0] + `"`
		if e[1] != "" {
			r += `,"permissions":` + e[1]
		}
		parts = append(parts, r+"}")
	}
	return `{"total_count":` + strconv.Itoa(len(parts)) + `,"repositories":[` + strings.Join(parts, ",") + `]}`
}

// fullPage is a page of pageSize other repositories, so the walk goes on.
func fullPage() string {
	var entries [][2]string
	for i := range pageSize {
		entries = append(entries, [2]string{"o/other" + strconv.Itoa(i), `{"push":false}`})
	}
	return installationRepos(entries...)
}

func stubInstallation(t *testing.T, pages ...string) *[]connector.Request {
	resp := map[string]connector.Response{}
	for i, p := range pages {
		resp[installationURL+strconv.Itoa(i+1)] = ok(p)
	}
	return stub(t, resp)
}

func TestTokenGrantInstallationTokenReadsRepositoryPush(t *testing.T) {
	other := [2]string{"o/other", `{"push":false}`}
	for name, tc := range map[string]struct {
		pages []string
		write bool
	}{
		"push":                 {[]string{installationRepos(other, [2]string{"o/r", `{"push":true,"pull":true}`})}, true},
		"read only":            {[]string{installationRepos([2]string{"o/r", `{"push":false,"pull":true}`})}, false},
		"name is case-folded":  {[]string{installationRepos([2]string{"O/R", `{"push":true}`})}, true},
		"repo on a later page": {[]string{fullPage(), installationRepos([2]string{"o/r", `{"push":true}`})}, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := stubInstallation(t, tc.pages...)
			g, err := tokenGrant(ftgt, "ghs_")
			if err != nil || !reflect.DeepEqual(g, forge.Grant{Introspectable: true, WriteCapable: tc.write}) {
				t.Fatalf("tokenGrant = %+v, %v; want write=%v", g, err, tc.write)
			}
			if len(*got) != len(tc.pages) {
				t.Fatalf("requests = %+v, want %d", *got, len(tc.pages))
			}
			for _, r := range *got {
				if r.Method != "GET" || !strings.HasPrefix(r.URL, installationURL) {
					t.Errorf("request %+v, want GET /installation/repositories", r)
				}
			}
		})
	}
}

func TestTokenGrantInstallationRepositoryUnresolvedIsAnError(t *testing.T) {
	for name, pages := range map[string][]string{
		"not listed":       {installationRepos([2]string{"o/other", `{"push":true}`})},
		"empty list":       {installationRepos()},
		"no permissions":   {installationRepos([2]string{"o/r", ""})},
		"permissions null": {installationRepos([2]string{"o/r", "null"})},
		"not json":         {"not json"},
		"push key absent":  {installationRepos([2]string{"o/r", `{"pull":true}`})},
		"push null":        {installationRepos([2]string{"o/r", `{"push":null}`})},
	} {
		t.Run(name, func(t *testing.T) {
			stubInstallation(t, pages...)
			if g, err := tokenGrant(ftgt, "ghs_"); err == nil {
				t.Errorf("tokenGrant = %+v, want an error", g)
			}
		})
	}
}

func TestTokenGrantInstallationWalkIsBounded(t *testing.T) {
	pages := make([]string, maxPages)
	for i := range pages {
		pages[i] = fullPage()
	}
	got := stubInstallation(t, pages...)
	if g, err := tokenGrant(ftgt, "ghs_"); err == nil {
		t.Errorf("tokenGrant = %+v, want an error", g)
	}
	if len(*got) != maxPages {
		t.Errorf("requests = %d, want %d", len(*got), maxPages)
	}
}

func TestTokenGrantNotIntrospectableMakesNoRequest(t *testing.T) {
	for _, prefix := range []string{"github_", "", "xyz_", "ghu_"} {
		got := stub(t, nil)
		g, err := tokenGrant(ftgt, prefix)
		if err != nil || !reflect.DeepEqual(g, forge.Grant{Reason: notIntrospectableReason}) {
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
			graphqlURL:            {Status: 401, Body: []byte("{}")},
			installationURL + "1": {Status: 401, Body: []byte("{}")},
		})
		stubScopes(t, "repo", true)
		_, err := tokenGrant(ftgt, prefix)
		var ce *connector.Error
		if err == nil || !errors.As(err, &ce) || ce.Class != connector.Auth {
			t.Errorf("%q: err = %v, want an auth-class error", prefix, err)
		}
	}
}
