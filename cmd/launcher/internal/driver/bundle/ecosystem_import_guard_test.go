package bundle_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// ecosystemImportViolations lists every package directory under moduleRoot,
// test files included, that imports an ecosystem package it has no right to: a
// package directly beneath internal/ecosystem (ecosystemkit aside) is imported
// only by the Bundle at bundleDir and by its own directory. Core, the other
// ecosystem packages, and every other package are all barred, which keeps an
// ecosystem's code out of any binary that does not link the Bundle. It also
// returns how many permitted imports it saw, so a walk that finds nothing at
// all can be told apart from one that found nothing wrong.
func ecosystemImportViolations(t *testing.T, moduleRoot, moduleName, bundleDir string) (violations []string, permitted int) {
	t.Helper()
	prefix := moduleName + "/internal/ecosystem/"
	ecosystemDir := filepath.Join(moduleRoot, "internal", "ecosystem")
	for _, dir := range packageDirs(t, moduleRoot) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", dir, err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			file := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("ParseFile(%s): %v", file, err)
			}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil || !strings.HasPrefix(p, prefix) {
					continue
				}
				sub, _, _ := strings.Cut(strings.TrimPrefix(p, prefix), "/")
				if sub == ecosystemKit {
					continue
				}
				own := filepath.Join(ecosystemDir, sub)
				if dir == bundleDir || dir == own || strings.HasPrefix(dir, own+string(filepath.Separator)) {
					permitted++
					continue
				}
				rel, err := filepath.Rel(moduleRoot, file)
				if err != nil {
					t.Fatal(err)
				}
				violations = append(violations, fmt.Sprintf("%s imports %s", filepath.ToSlash(rel), strings.TrimPrefix(p, moduleName+"/")))
			}
		}
	}
	slices.Sort(violations)
	return violations, permitted
}

// No core package may import an ecosystem package: the Bundle is the one place
// that names them, so a binary links an ecosystem only by linking the Bundle.
func TestNoCorePackageImportsAnEcosystemPackage(t *testing.T) {
	moduleName := readModuleName(t, filepath.Join(realModuleRoot, "go.mod"))
	bundleDir := filepath.Join(realModuleRoot, "internal", "driver", "bundle")
	violations, permitted := ecosystemImportViolations(t, realModuleRoot, moduleName, bundleDir)
	if permitted == 0 {
		t.Fatal("the walk saw no import of an ecosystem package, not even the Bundle's; it no longer covers the tree")
	}
	if len(violations) > 0 {
		t.Errorf("ecosystem package imported outside the Bundle and its own directory:\n%s", strings.Join(violations, "\n"))
	}
}

func TestEcosystemImportViolationsClassifiesPlantedImports(t *testing.T) {
	const mod = "example.com/fixturemodule"
	imp := func(pkg string, paths ...string) string {
		s := "package " + pkg + "\n\nimport (\n"
		for _, p := range paths {
			s += "\t_ \"" + mod + "/" + p + "\"\n"
		}
		return s + ")\n"
	}
	const (
		core = "internal/ecosystem"
		sub  = "internal/ecosystem/sub"
		oth  = "internal/ecosystem/other"
		kit  = "internal/ecosystem/ecosystemkit"
	)
	files := map[string]string{
		"go.mod": "module " + mod + "\n\ngo 1.24\n",
		// Permitted: the Bundle, an ecosystem package and its tests (external
		// test package included) importing its own directory, the kit, and core.
		"internal/driver/bundle/ecosystems.go": imp("bundle", core, sub, oth),
		"internal/ecosystem/sub/sub.go":        imp("sub", core, kit),
		"internal/ecosystem/sub/sub_test.go":   imp("sub_test", sub),
		"internal/ecosystem/sub/deep/deep.go":  imp("deep", sub),
		"internal/ecosystem/other/other.go":    imp("other", core, kit),
		"internal/ecosystem/ecosystem.go":      imp("ecosystem", kit),
		"internal/reader/reader.go":            imp("reader", core, kit),
		// Barred.
		"main.go":                                imp("main", oth),
		"internal/reader/planted.go":             imp("reader", sub),
		"internal/reader/planted_test.go":        imp("reader_test", sub),
		"internal/ecosystem/planted.go":          imp("ecosystem", sub),
		"internal/ecosystem/other/planted.go":    imp("other", sub),
		"internal/ecosystem/ecosystemkit/kit.go": imp("ecosystemkit", sub),
		"internal/driver/bundle/inner/inner.go":  imp("inner", sub),
		"internal/ecosystem/testdata/broken.go":  "this is not go source {{",
	}
	root := t.TempDir()
	for p, body := range files {
		testutil.WriteFixture(t, root, p, body)
	}
	got, permitted := ecosystemImportViolations(t, root, mod, filepath.Join(root, "internal", "driver", "bundle"))
	want := []string{
		"internal/driver/bundle/inner/inner.go imports internal/ecosystem/sub",
		"internal/ecosystem/ecosystemkit/kit.go imports internal/ecosystem/sub",
		"internal/ecosystem/other/planted.go imports internal/ecosystem/sub",
		"internal/ecosystem/planted.go imports internal/ecosystem/sub",
		"internal/reader/planted.go imports internal/ecosystem/sub",
		"internal/reader/planted_test.go imports internal/ecosystem/sub",
		"main.go imports internal/ecosystem/other",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations = %q, want %q", got, want)
	}
	if permitted != 4 {
		t.Fatalf("permitted imports = %d, want 4", permitted)
	}
}
