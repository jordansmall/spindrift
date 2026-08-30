package registryproxy

import (
	"reflect"
	"testing"
)

// TestIsAllowedPath_ConfigJSON verifies cargo's sparse-index config path is
// allowed.
func TestIsAllowedPath_ConfigJSON(t *testing.T) {
	if !isAllowedPath("/config.json") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/config.json")
	}
}

// TestIsAllowedPath_OneCharCrate verifies cargo's 1-char crate name index
// path shape is allowed.
func TestIsAllowedPath_OneCharCrate(t *testing.T) {
	if !isAllowedPath("/1/a") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/1/a")
	}
}

// TestIsAllowedPath_TwoCharCrate verifies cargo's 2-char crate name index
// path shape is allowed.
func TestIsAllowedPath_TwoCharCrate(t *testing.T) {
	if !isAllowedPath("/2/ab") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/2/ab")
	}
}

// TestIsAllowedPath_ThreeCharCrate verifies cargo's 3-char crate name index
// path shape (keyed by the name's first char) is allowed.
func TestIsAllowedPath_ThreeCharCrate(t *testing.T) {
	if !isAllowedPath("/3/a/abc") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/3/a/abc")
	}
}

// TestIsAllowedPath_FourPlusCharCrate verifies cargo's 4+-char crate name
// index path shape (keyed by the name's first two and next two chars) is
// allowed.
func TestIsAllowedPath_FourPlusCharCrate(t *testing.T) {
	if !isAllowedPath("/ab/cd/abcde") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/ab/cd/abcde")
	}
}

// TestIsAllowedPath_DownloadPathRejected verifies a download/artifact path
// is not allowed: it's registry-specific (the "dl" field in config.json),
// not a statically derivable index path shape.
func TestIsAllowedPath_DownloadPathRejected(t *testing.T) {
	if isAllowedPath("/api/v1/crates/foo/1.0.0/download") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/api/v1/crates/foo/1.0.0/download")
	}
}

// TestIsAllowedPath_RootRejected verifies the bare root path is not allowed.
func TestIsAllowedPath_RootRejected(t *testing.T) {
	if isAllowedPath("/") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/")
	}
}

// TestIsAllowedPath_UnrelatedRejected verifies paths with no relation to
// any bound ecosystem's path shape are not allowed. "/evil" is deliberately
// excluded here: npm's unscoped package metadata shape is a bare name
// segment (any package name looks like this), so a single lowercase word is
// no longer "unrelated" once npm is bound -- see
// TestIsAllowedPath_NpmUnrelatedRejected for npm-specific over-match cases.
func TestIsAllowedPath_UnrelatedRejected(t *testing.T) {
	cases := []string{"/../etc/passwd", "/foo/bar/baz"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = true, want false", path)
			}
		})
	}
}

// TestIsAllowedPath_GoModuleShapesAllowed verifies each of the go module
// proxy protocol's five path shapes is allowed, plus a "~" in a module-path
// segment (a legal module.CheckPath character, e.g.
// module.CheckPath("example.com/a~b/d") returns nil) and a "!"-escaped
// version segment (goproxy case-encodes uppercase letters, e.g.
// module.EscapeVersion("v1.0.0-RC1") returns "v1.0.0-!r!c1").
func TestIsAllowedPath_GoModuleShapesAllowed(t *testing.T) {
	cases := []string{
		"/github.com/foo/bar/@v/list",
		"/github.com/foo/bar/@latest",
		"/github.com/foo/bar/@v/v1.2.3.info",
		"/github.com/foo/bar/@v/v1.2.3.mod",
		"/github.com/foo/bar/@v/v1.2.3.zip",
		"/example.com/a~b/d/@v/list",
		"/github.com/foo/bar/@v/v1.0.0-!r!c1.info",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if !isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = false, want true", path)
			}
		})
	}
}

// TestIsAllowedPath_GoModuleUppercaseEscapeAllowed verifies module paths
// with a "!"-escaped originally-uppercase segment (per the goproxy-protocol
// spec's case-encoding rule) are allowed across all five path shapes.
func TestIsAllowedPath_GoModuleUppercaseEscapeAllowed(t *testing.T) {
	cases := []string{
		"/github.com/!google-cloud/foo/@v/list",
		"/github.com/!google-cloud/foo/@latest",
		"/github.com/!google-cloud/foo/@v/v1.2.3.info",
		"/github.com/!google-cloud/foo/@v/v1.2.3.mod",
		"/github.com/!google-cloud/foo/@v/v1.2.3.zip",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if !isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = false, want true", path)
			}
		})
	}
}

// TestIsAllowedPath_GoModuleMissingMarkerRejected verifies a module path
// without the @v/@latest marker is not allowed.
func TestIsAllowedPath_GoModuleMissingMarkerRejected(t *testing.T) {
	if isAllowedPath("/github.com/foo/bar") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/github.com/foo/bar")
	}
}

// TestIsAllowedPath_GoModulePathTraversalRejected verifies a path attempting
// to escape upward is not allowed, including a traversal segment placed
// ahead of an otherwise-valid @v/list suffix (a dots-only segment must not
// satisfy the module-path segment class).
func TestIsAllowedPath_GoModulePathTraversalRejected(t *testing.T) {
	cases := []string{
		"/../../etc/passwd",
		"/foo/../../../@v/list",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = true, want false", path)
			}
		})
	}
}

// TestIsAllowedPath_GoModuleWriteShapedRejected verifies a write-shaped path
// under @v/ (not one of the protocol's read-only list/latest/info/mod/zip
// shapes) is not allowed.
func TestIsAllowedPath_GoModuleWriteShapedRejected(t *testing.T) {
	if isAllowedPath("/github.com/foo/bar/@v/publish") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/github.com/foo/bar/@v/publish")
	}
}

// TestIsAllowedPath_GoModuleUnrelatedRejected verifies a path with no
// relation to the go module proxy protocol's shape is not allowed. Three
// plain segments, no "@": also unrelated to npm's shapes, which need either
// a leading "@scope" segment or a trailing "-/name-version.tgz" pair to
// match past two segments.
func TestIsAllowedPath_GoModuleUnrelatedRejected(t *testing.T) {
	if isAllowedPath("/evil/module/path") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/evil/module/path")
	}
}

// TestIsAllowedPath_NpmUnscopedPackage verifies npm's unscoped package
// metadata path shape is allowed.
func TestIsAllowedPath_NpmUnscopedPackage(t *testing.T) {
	if !isAllowedPath("/lodash") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/lodash")
	}
}

// TestIsAllowedPath_NpmUnscopedPackageVersion verifies npm's unscoped
// version-specific metadata path shape is allowed.
func TestIsAllowedPath_NpmUnscopedPackageVersion(t *testing.T) {
	if !isAllowedPath("/lodash/4.17.21") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/lodash/4.17.21")
	}
}

// TestIsAllowedPath_NpmUnscopedTarball verifies npm's unscoped tarball path
// shape is allowed.
func TestIsAllowedPath_NpmUnscopedTarball(t *testing.T) {
	if !isAllowedPath("/lodash/-/lodash-4.17.21.tgz") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/lodash/-/lodash-4.17.21.tgz")
	}
}

// TestIsAllowedPath_NpmUnscopedTarballWrongExtensionRejected verifies an
// unscoped tarball-shaped path with an extension other than .tgz is not
// allowed.
func TestIsAllowedPath_NpmUnscopedTarballWrongExtensionRejected(t *testing.T) {
	if isAllowedPath("/lodash/-/lodash-4.17.21.zip") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/lodash/-/lodash-4.17.21.zip")
	}
}

// TestIsAllowedPath_NpmUnscopedTarballSemverBuildMetadata verifies a
// tarball filename carrying legal semver build metadata (a "+" segment,
// e.g. "1.0.0+build") is allowed.
func TestIsAllowedPath_NpmUnscopedTarballSemverBuildMetadata(t *testing.T) {
	if !isAllowedPath("/foo/-/foo-1.0.0+build.tgz") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/foo/-/foo-1.0.0+build.tgz")
	}
}

// TestIsAllowedPath_NpmScopedPackage verifies npm's scoped package metadata
// path shape is allowed.
func TestIsAllowedPath_NpmScopedPackage(t *testing.T) {
	if !isAllowedPath("/@types/node") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/@types/node")
	}
}

// TestIsAllowedPath_NpmScopedPackageVersion verifies npm's scoped
// version-specific metadata path shape is allowed.
func TestIsAllowedPath_NpmScopedPackageVersion(t *testing.T) {
	if !isAllowedPath("/@types/node/20.0.0") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/@types/node/20.0.0")
	}
}

// TestIsAllowedPath_NpmScopedTarball verifies npm's scoped tarball path
// shape is allowed.
func TestIsAllowedPath_NpmScopedTarball(t *testing.T) {
	if !isAllowedPath("/@types/node/-/node-20.0.0.tgz") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/@types/node/-/node-20.0.0.tgz")
	}
}

// TestIsAllowedPath_NpmScopedTarballWrongExtensionRejected verifies a
// scoped tarball-shaped path with an extension other than .tgz is not
// allowed.
func TestIsAllowedPath_NpmScopedTarballWrongExtensionRejected(t *testing.T) {
	if isAllowedPath("/@types/node/-/node-20.0.0.zip") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/@types/node/-/node-20.0.0.zip")
	}
}

// TestIsAllowedPath_NpmUnrelatedRejected verifies npm-shaped-but-malformed
// paths -- a scope with no package name segment, and an empty scope name --
// are not allowed. This does not cover every way npm's patterns could
// over-match; see TestIsAllowedPath_NpmDotLeadingSegmentRejected for the
// dot-leading-segment/traversal-shaped cases.
func TestIsAllowedPath_NpmUnrelatedRejected(t *testing.T) {
	cases := []string{"/@types", "/@/pkg"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = true, want false", path)
			}
		})
	}
}

// TestIsAllowedPath_NpmDotLeadingSegmentRejected verifies path segments that
// start with a dot -- including traversal-shaped ones -- are not allowed.
// Real npm package/scope names can never start with "." or "_"
// (https://docs.npmjs.com/cli/v10/configuring-npm/package-json#name), so a
// path segment starting with "." never matches a real npm request; matching
// it anyway would let a traversal-shaped path pass the allowlist under
// npm's bare-name and unscoped name/version shapes.
func TestIsAllowedPath_NpmDotLeadingSegmentRejected(t *testing.T) {
	cases := []string{"/..", "/../etc", "/.env", "/.hidden"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if isAllowedPath(path) {
				t.Errorf("isAllowedPath(%q) = true, want false", path)
			}
		})
	}
}

// TestIsAllowedPath_NpmSearch verifies npm's search endpoint path is
// allowed.
func TestIsAllowedPath_NpmSearch(t *testing.T) {
	if !isAllowedPath("/-/v1/search") {
		t.Errorf("isAllowedPath(%q) = false, want true", "/-/v1/search")
	}
}

// TestEcosystems verifies Ecosystems() projects the shared binding table's
// rows, in table order, with each row's ecosystem name and lockfile names --
// the shape a later bindregistry package walks for nudge classification.
func TestEcosystems(t *testing.T) {
	want := []EcosystemBinding{
		{Ecosystem: "cargo", LockfileNames: []string{"Cargo.lock"}},
		{Ecosystem: "npm", LockfileNames: []string{"package-lock.json"}},
		{Ecosystem: "yarn", LockfileNames: []string{"yarn.lock"}},
		{Ecosystem: "pnpm", LockfileNames: []string{"pnpm-lock.yaml"}},
		{Ecosystem: "go", LockfileNames: []string{"go.sum"}},
		{
			Ecosystem: "gradle",
			LockfileNames: []string{
				"build.gradle",
				"build.gradle.kts",
				"settings.gradle",
				"settings.gradle.kts",
				"gradle.lockfile",
			},
		},
	}

	got := Ecosystems()

	if len(got) != len(want) {
		t.Fatalf("Ecosystems() returned %d rows, want %d", len(got), len(want))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ecosystems() = %#v, want %#v", got, want)
	}
}

// TestEcosystems_LockfileNamesNotAliased verifies each returned row's
// LockfileNames is an independent copy, not a slice aliasing the shared
// bindings table's own backing array -- a caller mutating its copy must
// never corrupt the table every other caller reads.
func TestEcosystems_LockfileNamesNotAliased(t *testing.T) {
	got := Ecosystems()
	for i := range got {
		if len(got[i].LockfileNames) == 0 {
			continue
		}
		got[i].LockfileNames[0] = "corrupted"
	}

	again := Ecosystems()
	for i, row := range again {
		for _, name := range row.LockfileNames {
			if name == "corrupted" {
				t.Fatalf("Ecosystems()[%d] = %#v after a prior caller mutated its copy; LockfileNames is aliased to the shared table", i, row)
			}
		}
	}
}
