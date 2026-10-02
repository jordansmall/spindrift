package repopath

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptsDir(t *testing.T) {
	dir := PromptsDir()
	fi, err := os.Stat(filepath.Join(dir, "fragments"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("PromptsDir() = %q: no fragments dir (err=%v)", dir, err)
	}
}

func TestFixturePaths(t *testing.T) {
	for name, p := range map[string]string{
		"RegistryJSON":         RegistryJSON(),
		"ForbiddenMarkersJSON": ForbiddenMarkersJSON(),
		"ValidateMarkersJSON":  ValidateMarkersJSON(),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s() = %q: %v", name, p, err)
		}
	}
}

func TestFindModuleDir(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findModuleDir(nested)
	if err != nil || got != root {
		t.Errorf("findModuleDir(%q) = %q, %v; want %q", nested, got, err, root)
	}
}

func TestFindModuleDirMissing(t *testing.T) {
	start := t.TempDir()
	_, err := findModuleDir(start)
	if err == nil {
		t.Skip("a go.mod exists above the temp dir")
	}
	if !strings.Contains(err.Error(), start) {
		t.Errorf("findModuleDir(%q) error = %q; want it to name the start dir", start, err)
	}
}

// The package must be importable from every test package, including ones
// reached by internal/report, so it may not depend on any launcher package.
func TestImportsStdlibOnly(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if first, _, _ := strings.Cut(imp, "/"); strings.Contains(first, ".") {
			t.Errorf("non-stdlib import %q", imp)
		}
	}
}
