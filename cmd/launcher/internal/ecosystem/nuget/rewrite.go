package nuget

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
	"spindrift.dev/launcher/internal/registryvocab"
)

// serviceIndexRow rewrites the v3 service index. dotnet restore follows the
// flat-container and registration resources from the index's absolute @id
// URLs, so left alone it would go straight to the upstream feed without the
// Forwarder's credential.
var serviceIndexRow = registryvocab.RewriteRow{
	Name:      "nuget service index",
	Ecosystem: nameNuGet,
	Method:    http.MethodGet,
	// The bare base is also the index: a protocolVersion="3" source declares the
	// index URL itself, with no index.json to trim.
	Matches: func(routeRelativePath, base string) bool {
		return routeRelativePath == registryvocab.JoinBase(base, "/index.json") || routeRelativePath == base
	},
	Rewrite: rewriteServiceIndex,
}

// uriTemplateMarkers are the placeholder openings a service-index @id may
// carry (ReadmeUriTemplate's {lower_id}, ...), kept literal so the client can
// substitute them.
var uriTemplateMarkers = []string{"{"}

// repositorySignatures reports whether a resource's @type (a string, or rarely
// an array of strings) names the RepositorySignatures resource. NuGet 6.x
// refuses it over the Forwarder's http endpoint (NU1301), and it only serves
// signature verification of an upstream the Box cannot reach, so it is
// dropped rather than repointed. It is dropped from every routed index, even
// one with no match-host resource, so such an index still counts as rewritten.
func repositorySignatures(resourceType any) bool {
	isSignatures := func(s string) bool {
		return s == "RepositorySignatures" || strings.HasPrefix(s, "RepositorySignatures/")
	}
	switch v := resourceType.(type) {
	case string:
		return isSignatures(v)
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && isSignatures(s) {
				return true
			}
		}
	}
	return false
}

func rewriteServiceIndex(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	unchanged := registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	obj, ok := ecosystemkit.DecodeOneJSONObject(body)
	if !ok {
		return unchanged
	}
	resources, ok := obj["resources"].([]any)
	if !ok {
		return unchanged
	}

	var (
		edits   []registryvocab.RewriteEdit
		kept    = make([]any, 0, len(resources))
		dropped bool
		applied bool
	)
	for _, r := range resources {
		res, isObj := r.(map[string]any)
		if isObj && repositorySignatures(res["@type"]) {
			dropped = true
			continue
		}
		kept = append(kept, r)
		if !isObj {
			continue
		}
		id, ok := res["@id"].(string)
		if !ok {
			continue
		}
		edit, ok := ecosystemkit.RepointRegistryURL(id, rc)
		if !ok {
			continue
		}
		edit = ecosystemkit.SpliceTemplateRawPath(id, edit, rc, uriTemplateMarkers)
		edits = append(edits, edit)
		if edit.To != "" {
			res["@id"] = edit.To
			applied = true
		}
	}

	switch {
	case !applied && !dropped && len(edits) == 0:
		return unchanged
	case !applied && !dropped:
		return registryvocab.RewriteResult{Body: body, Edits: edits, Outcome: registryvocab.RewriteSkippedForeignHost}
	}

	obj["resources"] = kept
	// An Encoder, unlike json.Marshal, can leave &, < and > unescaped so URI
	// templates keep their bytes; it appends a newline Marshal would not.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		// Unreachable: obj came from a successful decode.
		return unchanged
	}
	return registryvocab.RewriteResult{Body: bytes.TrimSuffix(buf.Bytes(), []byte("\n")), Edits: edits, Outcome: registryvocab.RewriteApplied}
}

// registrationRow repoints the registry-written links in a registration index
// or page. Restore follows the page @ids and packageContent (the .nupkg download)
// from these documents verbatim (#5961), and a package with many versions
// outgrows the proxy's buffered-body cap, where an over-cap body would be
// relayed unrewritten, so the row streams.
//
// Widening trigger: the predicate leaves the registry's other match-host links
// (parent, registration, catalogEntry.@id) pointing upstream. If restore is
// seen requesting one, widen the predicate to that field alone; a 401 on such
// a URL is the trigger firing. A request for some
// other document (search, the vulnerability index on a routed host) takes its
// own row. A request that escapes the rewrite reaches the upstream with no
// credential and fails visibly; it never succeeds with one attached.
var registrationRow = registryvocab.RewriteRow{
	Name:          "nuget registration",
	Ecosystem:     nameNuGet,
	Method:        http.MethodGet,
	Matches:       registrationMatches,
	RewriteStream: streamRegistration,
}

// registrationMatches accepts "<registration-dir>/<id>/index.json" and
// "<registration-dir>/<id>/page/<lo>/<hi>.json" under base, where the
// directory is one segment starting "registration" (Artifactory's
// "registration-semver2", nuget.org's "registration5-gz-semver2"). The .nupkg
// download "<dir>/Download/<id>/<version>" has four segments and so never
// matches; it must reach the Forwarder unrewritten. An id arrives
// percent-decoded and never contains '/'.
func registrationMatches(routeRelativePath, base string) bool {
	rel, ok := strings.CutPrefix(routeRelativePath, registryvocab.JoinBase(base, ""))
	if !ok || !strings.HasPrefix(rel, "/") {
		return false
	}
	segs := strings.Split(rel[1:], "/")
	if slices.Contains(segs, "") || !strings.HasPrefix(segs[0], "registration") {
		return false
	}
	switch len(segs) {
	case 3:
		return segs[2] == "index.json"
	case 5:
		return segs[2] == "page" && strings.HasSuffix(segs[4], ".json")
	}
	return false
}

// registrationMaxItemsDepth is the deepest items[] nesting a registration
// document has: index -> page -> leaf.
const registrationMaxItemsDepth = 2

// streamRegistration repoints only the registry-written @id and packageContent
// values at the document root and under items[] (the page and leaf links). A
// publisher writes catalogEntry (projectUrl, iconUrl, licenseUrl), and a
// match-host URL there must neither be repointed nor teach the proxy a path
// (ADR 0047).
func streamRegistration(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
	return ecosystemkit.StreamRepointJSON(dst, src, rc, onEdit, isRegistrationLinkPath)
}

// isRegistrationLinkPath reports whether the string value just read is an @id
// or packageContent member of the root object, of an items[] element, or of an
// items[].items[] element, and of no other object. Every step to it is an
// object keyed "items" then an array, so a valid path has odd length: pairs of
// (items, array index) followed by the final key.
func isRegistrationLinkPath(path []ecosystemkit.JSONPathStep) bool {
	n := len(path)
	if n%2 != 1 || n > 1+2*registrationMaxItemsDepth {
		return false
	}
	for i := 0; i < n-1; i += 2 {
		if !path[i].IsObject || path[i].Key != "items" || path[i+1].IsObject {
			return false
		}
	}
	last := path[n-1]
	return last.IsObject && (last.Key == "@id" || last.Key == "packageContent")
}
