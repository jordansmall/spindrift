package ecosystem

import (
	"encoding/json"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/registryvocab"
)

// npmPackumentMatches accepts the packument path (one segment under base) and
// rejects anything deeper, most importantly a tarball path
// (".../-/name-1.0.0.tgz"), which must reach the Forwarder unrewritten. A
// scoped name arrives already percent-decoded, so "%40scope%2fname" on the
// wire is "/@scope/name" here: two segments, not one literal "%40".
func npmPackumentMatches(routeRelativePath, base string) bool {
	// JoinBase rather than a bare prefix check on base: base == "/" is
	// JoinBase's "no base segment" sentinel, and that handling stays in one
	// place.
	rel, ok := strings.CutPrefix(routeRelativePath, registryvocab.JoinBase(base, ""))
	if !ok || !strings.HasPrefix(rel, "/") {
		return false
	}

	segments := strings.Split(rel[1:], "/")
	switch len(segments) {
	case 1:
		// npm has no one-segment "@..." name: a scoped name is always two.
		return isPackageNameSegment(segments[0]) && !strings.HasPrefix(segments[0], "@")
	case 2:
		// The first segment must carry a scope name, not the "@" alone; a
		// trailing slash leaves an empty final segment, which
		// isPackageNameSegment rejects.
		return len(segments[0]) > 1 && strings.HasPrefix(segments[0], "@") && isPackageNameSegment(segments[1])
	default:
		// Three or more segments is a tarball path, never the packument.
		return false
	}
}

// isPackageNameSegment rules out the shapes a packument request never carries:
// the empty segment, npm's own "-" tarball-directory marker, and the dot
// segments, which name a directory rather than a package.
func isPackageNameSegment(segment string) bool {
	switch segment {
	case "", "-", ".", "..":
		return false
	}
	return true
}

// rewriteNpmPackument repoints every same-host dist.tarball URL at the
// Forwarder: pacote fetches the packument's embedded absolute URL verbatim
// instead of deriving it from the registry env-var binding, so without this
// the download leaves the proxy (issue #3401). A foreign-host tarball is a
// declined edit (empty To), not a failure of the whole rewrite.
func rewriteNpmPackument(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	obj, ok := decodeOneJSONObject(body)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	versions, ok := obj["versions"].(map[string]any)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	// Map iteration order is randomized; sort so the edit list, and the
	// caller's log lines drawn from it, stay deterministic.
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	sort.Strings(names)

	var edits []registryvocab.RewriteEdit

	for _, name := range names {
		version, ok := versions[name].(map[string]any)
		if !ok {
			continue
		}
		dist, ok := version["dist"].(map[string]any)
		if !ok {
			continue
		}
		tarballStr, ok := dist["tarball"].(string)
		if !ok {
			continue
		}

		edit, ok := repointRegistryURL(tarballStr, rc)
		if !ok {
			// Not an absolute URL: npm never emits this shape, so there is
			// nothing worth logging a skip for.
			continue
		}

		edits = append(edits, edit)
		if edit.To == "" {
			// Foreign host (a CDN, a mirror): reported so the caller can log
			// the skip, but nothing in the body changes for it.
			continue
		}

		dist["tarball"] = edit.To
	}

	applied := false
	for _, edit := range edits {
		if edit.To != "" {
			applied = true
			break
		}
	}

	if !applied {
		if len(edits) == 0 {
			return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
		}
		// Every edit was declined as foreign-host: still a reportable skip,
		// not RewriteNone, because there was something recognizable here.
		return registryvocab.RewriteResult{Body: body, Edits: edits, Outcome: registryvocab.RewriteSkippedForeignHost}
	}

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable: obj came from a successful decode, so every value in
		// it is already representable as JSON.
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	return registryvocab.RewriteResult{Body: newBody, Edits: edits, Outcome: registryvocab.RewriteApplied}
}
