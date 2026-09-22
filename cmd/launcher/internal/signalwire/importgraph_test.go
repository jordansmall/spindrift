package signalwire

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestImportGraphIsStandardLibraryOnly pins the package doc's claim, which
// lib/mkHarness.nix's driver-exec fileset depends on: signalwire imports
// nothing outside the standard library, so a Box binary linking it never
// drags in the launcher-side listener's closure (internal/doctor, and behind
// it internal/runner, internal/forge and the container backend). No transitive
// walk is needed -- a module-local import is itself non-standard-library, so
// the property is visible one level down. registryproxy's importgraph_test.go
// is the precedent for pinning a boundary this way; its walk exists because it
// forbids three specific packages rather than all of them.
func TestImportGraphIsStandardLibraryOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	parsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		parsed++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquoting import %s in %s: %v", imp.Path.Value, name, err)
			}
			// A standard-library import path's first segment names no
			// host, so it holds no dot; every module-local and
			// third-party one does.
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s, outside the standard library", name, path)
			}
		}
	}
	if parsed == 0 {
		t.Fatal("parsed no non-test source files -- a broken walker, not a genuinely import-free package")
	}
}
