package ecosystem

import (
	"encoding/json"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/registryvocab"
)

// npmPackumentMatches is npmRow's RewriteRows Matches func: it accepts the
// packument path (exactly one path segment under base -- the package name)
// and rejects anything deeper, most importantly a tarball path
// (".../-/name-1.0.0.tgz"), which must reach the Forwarder unrewritten and
// untouched by this row. A scoped name reaches Matches already
// percent-decoded (registryproxy.selectRoute hands the row u.Path, not the
// escaped remainder it split routing on), so "%40scope%2fname" on the wire
// is "/@scope/name" here -- two segments, the first starting with "@" -- not
// one segment carrying a literal "%40". base == "/" is JoinBase's own
// "no base segment" sentinel (see JoinBase's doc comment); routing through
// JoinBase here instead of a bespoke prefix check keeps that sentinel
// handling in one place.
func npmPackumentMatches(routeRelativePath, base string) bool {
	rel, ok := strings.CutPrefix(routeRelativePath, registryvocab.JoinBase(base, ""))
	if !ok || !strings.HasPrefix(rel, "/") {
		return false
	}

	segments := strings.Split(rel[1:], "/")
	switch len(segments) {
	case 1:
		// Unscoped package name: exactly one segment, not itself a bare "@..."
		// (npm has no such name shape -- a scoped name is always two segments).
		return isPackageNameSegment(segments[0]) && !strings.HasPrefix(segments[0], "@")
	case 2:
		// Scoped package name ("@scope/name"): the leading "@" segment must
		// carry more than the sigil alone, and the name segment must be a
		// real name. A trailing slash (an empty final segment) fails this.
		return len(segments[0]) > 1 && strings.HasPrefix(segments[0], "@") && isPackageNameSegment(segments[1])
	default:
		// Three or more segments is a tarball path (".../-/name-1.0.0.tgz"),
		// scoped or not -- never the packument itself.
		return false
	}
}

// isPackageNameSegment reports whether one decoded path segment can be an npm
// package name. It rules out the shapes a packument request never carries but
// a path otherwise shaped like one can: the empty segment, npm's own "-"
// tarball-directory marker (so "GET /-" is a no-match rather than a row match
// whose body holds nothing rewritable), and the dot segments, which name a
// directory rather than a package.
func isPackageNameSegment(segment string) bool {
	switch segment {
	case "", "-", ".", "..":
		return false
	}
	return true
}

// rewriteNpmPackument rewrites every same-host dist.tarball URL in an npm
// packument body to the Forwarder, route-relative with the route's prefix
// re-inserted -- so npm install's tarball download (fetched by pacote
// straight off the packument, not derived from any registry setting) stays
// on the credentialed path. The packument embeds an absolute tarball URL
// that pacote fetches verbatim rather than deriving it from the registry
// env-var binding, so without this the download leaves the proxy for the
// real upstream (issue #3401).
//
// A packument can hold both same-host and foreign-host tarballs -- a
// registry that publishes some versions to a CDN -- and must not fail
// closed on the mix: only the shape defects below fail the whole rewrite
// (RewriteNone, body untouched); a foreign-host tarball is instead reported
// as a declined edit (empty To -- see RewriteEdit's doc comment) alongside
// whatever was applied, which the caller logs as a skip and never learns a
// route from.
func rewriteNpmPackument(body []byte, rc registryvocab.RewriteContext) registryvocab.RewriteResult {
	obj, ok := decodeOneJSONObject(body)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	versions, ok := obj["versions"].(map[string]any)
	if !ok {
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}

	// Map iteration order is randomized; sort version names so the edit
	// list -- and the caller's log lines drawn from it -- is deterministic
	// across runs given the same packument.
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
			// Not an absolute URL at all -- npm never emits this shape, so
			// there's nothing to log a skip for; silently leave it be, same
			// as any other malformed field above.
			continue
		}

		edits = append(edits, edit)
		if edit.To == "" {
			// Foreign host (a CDN, a mirror): reported so the caller can
			// log the skip, but nothing in the body changes for it.
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
		// Every declined value was foreign-host, none applied: still a
		// reportable skip, not RewriteNone -- there was something
		// recognizable to rewrite, this row just declined it.
		return registryvocab.RewriteResult{Body: body, Edits: edits, Outcome: registryvocab.RewriteSkippedForeignHost}
	}

	newBody, err := json.Marshal(obj)
	if err != nil {
		// Unreachable in practice: obj came from a successful json.Decode
		// above, so every value in it is already representable as JSON.
		return registryvocab.RewriteResult{Body: body, Outcome: registryvocab.RewriteNone}
	}
	return registryvocab.RewriteResult{Body: newBody, Edits: edits, Outcome: registryvocab.RewriteApplied}
}
