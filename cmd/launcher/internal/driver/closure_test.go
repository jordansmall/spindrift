package driver_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// registry is read off a type the package defines so the module path is not
// restated here.
var registry = reflect.TypeOf(driver.Descriptor{}).PkgPath()

// goList runs `go list args...` and returns its stdout, failing the test with
// go's stderr when it exits non-zero.
func goList(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list %s: %v\n%s", strings.Join(args, " "), err, ee.Stderr)
		}
		t.Fatalf("go list %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// forbiddenFor reports the import paths a Driver's test closure must not
// reach: the registry itself, and any other Driver's package or subpackage.
// The Driver's own subpackages stay allowed.
func forbiddenFor(name string, names []string) func(path string) bool {
	return func(path string) bool {
		if path == registry {
			return true
		}
		for _, other := range names {
			if other == name {
				continue
			}
			pkg := registry + "/" + other
			if path == pkg || strings.HasPrefix(path, pkg+"/") {
				return true
			}
		}
		return false
	}
}

// forbiddenInClosure returns the forbidden packages named in `go list -deps
// -test -f {{.ImportPath}}` output.
func forbiddenInClosure(out string, forbidden func(path string) bool) []string {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		// Test variants print as "path [pkg.test]".
		path, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if forbidden(path) {
			found = append(found, path)
		}
	}
	return found
}

// TestDriverTestClosuresStandAlone pins issue #5506: a Driver package's tests
// stand alone. Its test import closure, subpackages included, must not reach
// the registry or another Driver's package, so a Driver can be built and
// tested without the others. The Driver list comes from the registry, so a
// new Driver is covered the moment it registers.
func TestDriverTestClosuresStandAlone(t *testing.T) {
	names := driver.DescriptorNames()
	if len(names) == 0 {
		t.Fatal("driver.DescriptorNames() is empty; nothing to guard")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pkg := registry + "/" + name
			out := goList(t, "-deps", "-test", "-f", "{{.ImportPath}}", pkg+"/...")
			for _, path := range forbiddenInClosure(out, forbiddenFor(name, names)) {
				t.Errorf("%s's test closure contains %s", pkg, path)
			}
		})
	}
}

func TestForbiddenInClosureReportsPlantedImports(t *testing.T) {
	forbidden := forbiddenFor("claude", []string{"claude", "codex"})
	m := registry
	out := "fmt\n" + m + "/claude\n" + m + "/claude/sub\n" + m + "/driverkit\n" +
		m + " [" + m + "/claude.test]\n" + m + "/codex\n" + m + "/codex/x\n" + m + "/codexfoo\n"
	got := forbiddenInClosure(out, forbidden)
	if want := []string{m, m + "/codex", m + "/codex/x"}; !slices.Equal(got, want) {
		t.Errorf("forbiddenInClosure = %v, want %v", got, want)
	}
	if got := forbiddenInClosure("fmt\n"+m+"/claude\n"+m+"/claude/sub\n", forbidden); len(got) != 0 {
		t.Errorf("clean closure reported %v", got)
	}
}

// callsConformanceRun reports whether the Go file at path declares TestCorpus
// with a conformance.Run call in its body; mentions in comments or other
// functions do not count.
func callsConformanceRun(path string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return false, err
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestCorpus" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Run" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "conformance" {
				found = true
			}
			return !found
		})
	}
	return found, nil
}

// corpusProblems lists how the Driver packages under root fail to carry a
// corpus: a registered Driver with no case under testdata/corpus or no
// TestCorpus replaying it, or a corpus directory belonging to no registered
// Driver. It is a static scan, so it imports no Driver and stays clear of
// the closure rule above. A filesystem or parse error is reported as itself
// rather than as the missing piece it hides.
func corpusProblems(root string, names []string) []string {
	var problems []string
	for _, name := range names {
		cases, err := os.ReadDir(filepath.Join(root, name, "testdata", "corpus"))
		switch {
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			problems = append(problems, name+": "+err.Error())
		case !slices.ContainsFunc(cases, func(e os.DirEntry) bool { return e.IsDir() }):
			problems = append(problems, name+": testdata/corpus holds no case directory")
		}
		tests, err := filepath.Glob(filepath.Join(root, name, "*_test.go"))
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		defined := false
		for _, f := range tests {
			ok, err := callsConformanceRun(f)
			if err != nil {
				problems = append(problems, name+": "+err.Error())
			}
			defined = defined || ok
		}
		if !defined {
			problems = append(problems, name+": no _test.go file defines TestCorpus calling conformance.Run")
		}
	}
	dirs, err := filepath.Glob(filepath.Join(root, "*", "testdata", "corpus"))
	if err != nil {
		problems = append(problems, err.Error())
	}
	for _, dir := range dirs {
		owner := filepath.Base(filepath.Dir(filepath.Dir(dir)))
		if !slices.Contains(names, owner) {
			problems = append(problems, owner+": corpus directory belongs to no registered Driver")
		}
	}
	return problems
}

// TestEveryDriverRunsItsCorpus pins issue #5506: registering a Driver without
// a corpus and the test that replays it must fail, and so must a corpus no
// registered Driver owns.
func TestEveryDriverRunsItsCorpus(t *testing.T) {
	names := driver.DescriptorNames()
	if len(names) == 0 {
		t.Fatal("driver.DescriptorNames() is empty; nothing to guard")
	}
	for _, p := range corpusProblems(".", names) {
		t.Error(p)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorpusProblemsReportsMissingPieces(t *testing.T) {
	root := t.TempDir()
	const corpusTest = "package x\nfunc TestCorpus(t *testing.T) { conformance.Run(t, s, d) }"
	writeTree(t, root, map[string]string{
		"good/testdata/corpus/case1/log":   "x",
		"good/corpus_test.go":              corpusTest,
		"nocorpus/corpus_test.go":          corpusTest,
		"notest/testdata/corpus/case1/log": "x",
		"notest/other_test.go":             "package x\nfunc TestOther(t *testing.T) {}",
		// Both strings appear, but only in comments and a different function.
		"commented/testdata/corpus/case1/log": "x",
		"commented/corpus_test.go": "package x\n// func TestCorpus(t *testing.T) { conformance.Run(t, s, d) }\n" +
			"func TestCorpus(t *testing.T) {}\nfunc TestOther(t *testing.T) { conformance.Run(t, s, d) }",
		"stray/testdata/corpus/case1/log": "x",
	})

	got := corpusProblems(root, []string{"good", "nocorpus", "notest", "commented"})
	want := []string{
		"nocorpus: testdata/corpus holds no case directory",
		"notest: no _test.go file defines TestCorpus calling conformance.Run",
		"commented: no _test.go file defines TestCorpus calling conformance.Run",
		"stray: corpus directory belongs to no registered Driver",
	}
	if !slices.Equal(got, want) {
		t.Errorf("corpusProblems = %q, want %q", got, want)
	}
}

func TestCorpusProblemsSurfacesReadErrors(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// A file where the corpus directory belongs: not a missing corpus.
		"badcorpus/testdata/corpus":          "x",
		"badcorpus/corpus_test.go":           "package x\nfunc TestCorpus(t *testing.T) { conformance.Run(t, s, d) }",
		"badparse/testdata/corpus/case1/log": "x",
		"badparse/corpus_test.go":            "func (",
	})

	got := corpusProblems(root, []string{"badcorpus", "badparse"})
	var sawRead, sawParse bool
	for _, p := range got {
		switch {
		case strings.HasPrefix(p, "badcorpus: "):
			sawRead = true
			if strings.Contains(p, "holds no case directory") {
				t.Errorf("read error masked as missing corpus: %q", p)
			}
		case strings.HasPrefix(p, "badparse: ") && strings.Contains(p, "corpus_test.go"):
			sawParse = true
		}
	}
	if !sawRead || !sawParse {
		t.Errorf("corpusProblems = %q, want a read error for badcorpus and a parse error for badparse", got)
	}
}
