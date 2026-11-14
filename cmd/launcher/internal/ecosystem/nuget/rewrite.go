package nuget

import (
	"bytes"
	"encoding/json"
	"net/http"
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
