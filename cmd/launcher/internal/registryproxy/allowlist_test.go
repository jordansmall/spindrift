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
