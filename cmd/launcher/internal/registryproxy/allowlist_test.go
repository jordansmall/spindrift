package registryproxy

import "testing"

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
// cargo's sparse-index shape are not allowed.
func TestIsAllowedPath_UnrelatedRejected(t *testing.T) {
	cases := []string{"/../etc/passwd", "/evil"}
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
// relation to the go module proxy protocol's shape is not allowed.
func TestIsAllowedPath_GoModuleUnrelatedRejected(t *testing.T) {
	if isAllowedPath("/evil") {
		t.Errorf("isAllowedPath(%q) = true, want false", "/evil")
	}
}
