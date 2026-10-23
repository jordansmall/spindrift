// Package hostprofile holds advisory knowledge about upstream registry host
// products. Only Route discovery and doctor read it; the proxy, route parsing
// and route resolution never import it (ADR 0071).
package hostprofile

import (
	"net/http"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/glob"
)

// Row describes one host product. It is plain data: no funcs, so a profile
// edit can never change enforcement.
type Row struct {
	Name string
	// Hosts are glob patterns matched against the lowercased hostname of the
	// declared registry URL.
	Hosts []string
	// PathPrefixes match the path of the declared registry URL, for
	// self-hosted installs a hostname cannot identify.
	PathPrefixes []string
	// ProbeHeaders are response header names the discovery probe receives.
	ProbeHeaders []string
	// AuthScheme uses the routes-file grammar (bearer, basic, header:<Name>);
	// empty for a host that needs no credential.
	AuthScheme string
	// BasicUsername is display text describing the username convention.
	BasicUsername string
	// Siblings maps an ecosystem row name to path patterns, relative to the
	// registry path the Target repo declares.
	Siblings         map[string][]string
	CredentialHelper string
	Note             string
}

const publicNote = "known host, unauthenticated, nothing to configure"

// table is ordered; Match returns the first row any signal matches. It stays
// unexported so no importer can reorder or edit it under Match.
var table = []Row{
	{
		Name:         "artifactory",
		Hosts:        []string{"*.jfrog.io"},
		PathPrefixes: []string{"/artifactory/api/"},
		ProbeHeaders: []string{"X-Artifactory-Id"},
		// AuthScheme and Siblings stay provisional until the discovery corpus
		// cases establish them (ADR 0071 decision 3).
		AuthScheme:       "bearer",
		BasicUsername:    "the Artifactory username (or email) that owns the token",
		CredentialHelper: "an access token from the JFrog Platform UI or `jf rt access-token-create`",
		Note:             "auth scheme and sibling paths are provisional, not yet established by discovery cases",
	},
	{Name: "crates.io", Hosts: []string{"crates.io", "*.crates.io"}, Note: publicNote},
	{Name: "registry.npmjs.org", Hosts: []string{"registry.npmjs.org"}, Note: publicNote},
	{Name: "proxy.golang.org", Hosts: []string{"proxy.golang.org"}, Note: publicNote},
}

// Match returns the first table row whose host pattern, path prefix or probe
// header matches. An unparseable URL can still match on a header.
func Match(declaredURL string, header http.Header) (Row, bool) {
	var host, path string
	if u, err := url.Parse(declaredURL); err == nil {
		host, path = strings.ToLower(u.Hostname()), u.Path
	}
	for _, row := range table {
		if matches(row, host, path, header) {
			return row, true
		}
	}
	return Row{}, false
}

func matches(row Row, host, path string, header http.Header) bool {
	if host != "" {
		for _, p := range row.Hosts {
			if glob.Match(p, host) {
				return true
			}
		}
	}
	for _, p := range row.PathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, name := range row.ProbeHeaders {
		if len(header.Values(name)) > 0 {
			return true
		}
	}
	return false
}
