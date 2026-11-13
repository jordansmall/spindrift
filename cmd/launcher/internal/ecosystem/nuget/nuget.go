// Package nuget is NuGet's Ecosystem Strategy: a repo-root packages.lock.json
// classifies the Target as "dotnet", and nuget.config's v3 package sources
// declare the registries the Forwarder fronts.
package nuget

import (
	"encoding/xml"
	"io"
	"net/url"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Strategy has no binding yet (issue #5963 adds it), which is why its config is
// listed in DiscoveryConfigPaths, read by discovery and the path-set but never
// rewritten, rather than in InTreeConfigPath. The spellings are NuGet's own
// probe order on a case-sensitive filesystem. It has no route-declaration hook,
// so a route's ecosystems block accepts only the generic path key. Its
// credentials come from the operator's stores alone: a repo's own
// packageSourceCredentials is never read.
var Strategy = ecosystem.Strategy{
	Name:                 "nuget",
	CredentialStores:     []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:        []string{"packages.lock.json"},
	Classification:       "dotnet",
	DiscoveryConfigPaths: []string{"nuget.config", "NuGet.config", "NuGet.Config"},
	ConfigParser:         parseNuGetConfig,
}

// publicNuGetHost is NuGet's implicit default source. Routing it would point a
// credential-less public feed at the proxy, so an operator who wants it routed
// opts in through a routes file instead.
const publicNuGetHost = "api.nuget.org"

// nugetConfig is the decode shape for the packageSources slice of nuget.config;
// every other element (packageSourceMapping, credentials, ...) is none of this
// package's business. Children decode in document order because <clear/> and
// <remove/> act on what precedes them.
type nugetConfig struct {
	PackageSources struct {
		Items []struct {
			XMLName         xml.Name
			Key             string `xml:"key,attr"`
			Value           string `xml:"value,attr"`
			ProtocolVersion string `xml:"protocolVersion,attr"`
		} `xml:",any"`
	} `xml:"packageSources"`
	DisabledPackageSources struct {
		Items []struct {
			Key   string `xml:"key,attr"`
			Value string `xml:"value,attr"`
		} `xml:"add"`
	} `xml:"disabledPackageSources"`
}

type packageSource struct{ key, value, protocolVersion string }

// effectiveSources applies <clear/>, <remove/> and key-replacing <add/> in
// document order, then drops every source <disabledPackageSources> switches
// off. NuGet compares keys case-insensitively.
func effectiveSources(raw nugetConfig) []packageSource {
	var sources []packageSource
	indexOf := func(key string) int {
		return slices.IndexFunc(sources, func(s packageSource) bool { return strings.EqualFold(s.key, key) })
	}
	for _, item := range raw.PackageSources.Items {
		switch item.XMLName.Local {
		case "clear":
			sources = nil
		case "remove":
			if i := indexOf(item.Key); i >= 0 {
				sources = slices.Delete(sources, i, i+1)
			}
		case "add":
			source := packageSource{item.Key, item.Value, strings.TrimSpace(item.ProtocolVersion)}
			if i := indexOf(item.Key); i >= 0 && item.Key != "" {
				sources[i] = source
			} else {
				sources = append(sources, source)
			}
		}
	}
	for _, d := range raw.DisabledPackageSources.Items {
		if strings.EqualFold(strings.TrimSpace(d.Value), "true") {
			if i := indexOf(d.Key); i >= 0 {
				sources = slices.Delete(sources, i, i+1)
			}
		}
	}
	return sources
}

// parseNuGetConfig is NuGet's ConfigParser. It keeps only absolute http(s) v3
// sources, which NuGet recognises by protocolVersion="3" or a URL ending in
// ".json" (any case); a v2 feed, a local path, or a URL with userinfo is
// skipped, not an error. nuget.org is neither declared nor counted as a named
// registry. The declared URL is the service index's directory for a ".json"
// URL, and the URL itself for a protocolVersion="3" one, since the path-set is
// the declared URL's path. A query or fragment never survives. A source that
// <disabledPackageSources> switches off is dropped before any of this.
func parseNuGetConfig(content string) ([]ecosystem.Declaration, bool, error) {
	var raw nugetConfig
	dec := xml.NewDecoder(strings.NewReader(content))
	// A prolog may declare windows-1252 or iso-8859-1 over ASCII content, which
	// is valid; non-UTF-8 bytes still fail the decode.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := dec.Decode(&raw); err != nil {
		return nil, false, err
	}

	var out []ecosystem.Declaration
	namedAny := false
	for _, source := range effectiveSources(raw) {
		u, err := url.Parse(source.value)
		if err == nil && registryvocab.HostKey(u.Host) == publicNuGetHost {
			continue
		}
		namedAny = true
		if err != nil {
			continue
		}
		if strings.HasSuffix(strings.ToLower(u.Path), ".json") {
			u.Path = u.Path[:strings.LastIndex(u.Path, "/")+1]
			u.RawPath = ""
		} else if source.protocolVersion != "3" {
			continue
		}
		u.RawQuery, u.Fragment = "", ""
		host, upstreamBaseURL, ok := ecosystemkit.HTTPAbsoluteURL(u.String())
		if !ok {
			continue
		}
		out = append(out, ecosystem.Declaration{Host: host, UpstreamBaseURL: upstreamBaseURL})
	}
	return out, namedAny, nil
}
