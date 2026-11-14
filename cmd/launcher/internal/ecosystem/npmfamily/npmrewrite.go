package npmfamily

import (
	"io"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem/ecosystemkit"
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

// npmTarballDepth is the container depth of a dist object: the packument,
// versions, one version, dist.
const npmTarballDepth = 4

// streamNpmPackument repoints every versions.*.dist.tarball string at the
// Forwarder as it tokenizes and passes every other byte through verbatim.
// Pacote fetches the packument's embedded absolute URL verbatim instead of
// deriving it from the registry env-var binding, so without this the download
// leaves the proxy (issue #3401). The window, abandonment and edit-ordering
// contract is ecosystemkit.StreamRepointJSON's.
func streamNpmPackument(dst io.Writer, src io.Reader, rc registryvocab.RewriteContext, onEdit func(registryvocab.RewriteEdit)) error {
	return ecosystemkit.StreamRepointJSON(dst, src, rc, onEdit, isNpmTarballPath)
}

// isNpmTarballPath reports whether the string value just read sits at
// versions.<any>.dist.tarball, every ancestor an object.
func isNpmTarballPath(path []ecosystemkit.JSONPathStep) bool {
	if len(path) != npmTarballDepth {
		return false
	}
	for _, f := range path {
		if !f.IsObject {
			return false
		}
	}
	return path[0].Key == "versions" && path[2].Key == "dist" && path[3].Key == "tarball"
}
