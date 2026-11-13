// Package nuget is NuGet's Ecosystem Strategy: a repo-root packages.lock.json
// classifies the Target as "dotnet", and nuget.config's v3 package sources
// declare the registries the Forwarder fronts.
package nuget

import (
	"encoding/xml"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registryvocab"
)

// Strategy binds nuget.config in place: ApplyInTreeBinding repoints each routed
// source at the Forwarder, rewriteNuGetConfig then marks it
// allowInsecureConnections (the loopback endpoint is HTTP, which a .NET 9+ SDK
// refuses with NU1302 otherwise) and drops its packageSourceCredentials entry,
// and the run's revert restores the committed file. The binding takes the first
// spelling present in DiscoveryConfigPaths, which are NuGet's own probe order on
// a case-sensitive filesystem. It has no route-declaration hook, so a route's
// ecosystems block accepts only the generic path key. Its credentials come from
// the operator's stores alone: a repo's own packageSourceCredentials is never
// read, only removed from a routed source so NuGet's client-side credential
// handling cannot abort before the request reaches the proxy.
var Strategy = ecosystem.Strategy{
	Name:                 "nuget",
	CredentialStores:     []string{"netrc", "npmrc", "gradle-properties"},
	LockfileNames:        []string{"packages.lock.json"},
	Classification:       "dotnet",
	DiscoveryConfigPaths: []string{"nuget.config", "NuGet.config", "NuGet.Config"},
	ConfigParser:         parseNuGetConfig,
	InTreeRewrite:        rewriteNuGetConfig,
}

// publicNuGetHost is NuGet's implicit default source. It is declared Public so
// discovery can name it, but never routed: routing it would point a
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

func newConfigDecoder(content string) *xml.Decoder {
	dec := xml.NewDecoder(strings.NewReader(content))
	// A prolog may declare windows-1252 or iso-8859-1 over ASCII content, which
	// is valid; non-UTF-8 bytes still fail the decode.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	return dec
}

// parseNuGetConfig is NuGet's ConfigParser. It keeps only absolute http(s) v3
// sources, which NuGet recognises by protocolVersion="3" or a URL ending in
// ".json" (any case); a v2 feed, a local path, or a URL with userinfo is
// skipped, not an error. nuget.org is declared Public (never routed) and not
// counted as a named registry. The declared URL is the service index's
// directory for a ".json" URL, and the URL itself for a protocolVersion="3"
// one, since the path-set is the declared URL's path. A query or fragment
// never survives. A source that <disabledPackageSources> switches off is
// dropped before any of this.
func parseNuGetConfig(content string) ([]ecosystem.Declaration, bool, error) {
	var raw nugetConfig
	if err := newConfigDecoder(content).Decode(&raw); err != nil {
		return nil, false, err
	}

	var out []ecosystem.Declaration
	namedAny := false
	for _, source := range effectiveSources(raw) {
		u, err := url.Parse(source.value)
		public := err == nil && registryvocab.HostKey(u.Host) == publicNuGetHost
		if !public {
			namedAny = true
		}
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
		out = append(out, ecosystem.Declaration{Host: host, UpstreamBaseURL: upstreamBaseURL, Public: public})
	}
	return out, namedAny, nil
}

const allowInsecureAttr = "allowInsecureConnections"

// textEdit replaces content[start:end] with text.
type textEdit struct {
	start, end int
	text       string
}

// removal deletes content[start:end].
func removal(start, end int) textEdit { return textEdit{start: start, end: end} }

// rewriteNuGetConfig is nuget's InTreeRewrite. It edits the text in place
// rather than re-marshalling, so everything outside the edits stays
// byte-identical and the revert's checkout needs no round trip through an XML
// writer. Each <add> under packageSources whose value now points at one of
// localURLs gains allowInsecureConnections="true", and the
// packageSourceCredentials entry of each such source is removed.
func rewriteNuGetConfig(content string, localURLs []string) (string, error) {
	dec := newConfigDecoder(content)
	sourcesPath := []string{"configuration", "packageSources", "add"}

	type credential struct {
		name string
		span textEdit
	}
	var (
		path        []string
		edits       []textEdit
		routedKeys  []string
		credentials []credential
		credStart   int
	)
	for {
		start := int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		end := int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			path = append(path, t.Name.Local)
			switch {
			case slices.Equal(path, sourcesPath):
				if edit, key, ok := routedSource(content[start:end], t, localURLs); ok {
					routedKeys = append(routedKeys, key)
					if edit != nil {
						edit.start, edit.end = edit.start+start, edit.end+start
						edits = append(edits, *edit)
					}
				}
			case isCredentialPath(path):
				credStart = start
			}
		case xml.EndElement:
			if isCredentialPath(path) {
				credentials = append(credentials, credential{path[2], removal(credStart, end)})
			}
			path = path[:len(path)-1]
		}
	}

	// Credentials may precede packageSources, so they are matched only once
	// every routed key is known.
	for _, c := range credentials {
		name := decodeXMLLocalName(c.name)
		if slices.ContainsFunc(routedKeys, func(k string) bool { return strings.EqualFold(k, name) }) {
			edits = append(edits, wholeLines(content, c.span))
		}
	}
	slices.SortFunc(edits, func(a, b textEdit) int { return a.start - b.start })
	var out strings.Builder
	last := 0
	for _, e := range edits {
		out.WriteString(content[last:e.start])
		out.WriteString(e.text)
		last = e.end
	}
	out.WriteString(content[last:])
	return out.String(), nil
}

// isCredentialPath reports whether path is a source's element directly under
// packageSourceCredentials.
func isCredentialPath(path []string) bool {
	return len(path) == 3 && path[0] == "configuration" && path[1] == "packageSourceCredentials"
}

// routedSource reports whether the <add> start tag names a source routed
// through the Forwarder and, if so, its key and the edit marking it (nil when
// the tag already says "true"), with offsets relative to the tag.
func routedSource(tag string, el xml.StartElement, localURLs []string) (*textEdit, string, bool) {
	var key, value string
	for _, a := range el.Attr {
		switch a.Name.Local {
		case "key":
			key = a.Value
		case "value":
			value = a.Value
		}
	}
	if !slices.ContainsFunc(localURLs, func(u string) bool { return atURLBoundary(value, u) }) {
		return nil, "", false
	}
	// An existing attribute's value is replaced where the author wrote it; a
	// new one goes after the last attribute, keeping the tag's own "/>" spacing.
	if m := insecureAttrValue.FindStringSubmatchIndex(tag); m != nil {
		if tag[m[2]:m[3]] == "true" {
			return nil, key, true
		}
		return &textEdit{m[2], m[3], "true"}, key, true
	}
	at := len(strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(tag, ">"), "/"), " \t\r\n"))
	return &textEdit{at, at, " " + allowInsecureAttr + `="true"`}, key, true
}

var insecureAttrValue = regexp.MustCompile(`\s` + allowInsecureAttr + `\s*=\s*["']([^"']*)`)

// atURLBoundary reports whether value is base, or base followed by a path,
// query or fragment, so a longer port or host sharing the prefix never matches.
func atURLBoundary(value, base string) bool {
	rest, ok := strings.CutPrefix(value, base)
	return ok && (rest == "" || strings.ContainsRune("/?#", rune(rest[0])))
}

var xmlNameEscape = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)

// decodeXMLLocalName undoes XmlConvert.EncodeLocalName, which NuGet writes a
// source key through to name its packageSourceCredentials element (a space
// becomes "_x0020_").
func decodeXMLLocalName(name string) string {
	return xmlNameEscape.ReplaceAllStringFunc(name, func(m string) string {
		n, _ := strconv.ParseUint(m[2:6], 16, 16)
		return string(rune(n))
	})
}

// wholeLines widens a removal to its full lines, indentation and newline
// included, when nothing else shares them, so no blank line is left behind.
func wholeLines(content string, span textEdit) textEdit {
	ls := span.start
	for ls > 0 && (content[ls-1] == ' ' || content[ls-1] == '\t') {
		ls--
	}
	le := span.end
	for le < len(content) && (content[le] == ' ' || content[le] == '\t' || content[le] == '\r') {
		le++
	}
	if (ls == 0 || content[ls-1] == '\n') && le < len(content) && content[le] == '\n' {
		return removal(ls, le+1)
	}
	return span
}
