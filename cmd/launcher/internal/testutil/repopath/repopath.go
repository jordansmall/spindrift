// Package repopath names shared on-disk test fixtures by absolute path.
// Tests anywhere under cmd/launcher call these helpers instead of counting
// "../" for their own package depth.
//
// A fixture read by more than one package gets a function here; a package
// reading only its own testdata/ keeps opening it directly.
//
// The package must stay stdlib-only (pinned by its test): packages that
// internal/testutil cannot serve without an import cycle, such as
// internal/dispatchkey via internal/report, import it too.
package repopath

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// findModuleDir returns the nearest ancestor of start (inclusive) holding a go.mod.
func findModuleDir(start string) (string, error) {
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", start)
		}
		dir = parent
	}
}

// moduleDir walks up from the working directory rather than using
// runtime.Caller, whose paths are module-relative under -trimpath. go test
// runs each package with its own directory as cwd, so this works at any depth.
// Callable from package-level var initializers, hence panic over testing.TB.
//
// The first call's os.Getwd() is cached for the process: a test that chdirs
// out of the module before its first repopath call makes every later call
// panic, so call repopath before any chdir.
var moduleDir = sync.OnceValue(func() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("repopath: getwd: %v", err))
	}
	dir, err := findModuleDir(wd)
	if err != nil {
		panic("repopath: " + err.Error())
	}
	return dir
})

// PromptsDir is the repo-root templates/default/prompts directory.
func PromptsDir() string {
	return filepath.Join(moduleDir(), "..", "..", "templates", "default", "prompts")
}

func promptassemblyTestdata(name string) string {
	return filepath.Join(moduleDir(), "internal", "promptassembly", "testdata", name)
}

// ForbiddenMarkersJSON is internal/promptassembly/testdata/forbidden-markers.json.
func ForbiddenMarkersJSON() string { return promptassemblyTestdata("forbidden-markers.json") }

// ValidateMarkersJSON is internal/promptassembly/testdata/validate-markers.json.
func ValidateMarkersJSON() string { return promptassemblyTestdata("validate-markers.json") }

// PromptAssemblyGoldenDir is the repo-root tests/testdata/prompt-assembly-golden
// directory: the committed prompt bytes the promptassembly golden test pins.
func PromptAssemblyGoldenDir() string {
	return filepath.Join(moduleDir(), "..", "..", "tests", "testdata", "prompt-assembly-golden")
}

// MergedPromptsDir is PromptsDir with every connectors/<name>/fragments tree
// overlaid onto its fragments/ in a fresh temp dir, the layout the Box image
// bakes (issue #6857). The Go assembler reads one root, so a prompt that
// renders a Connector-shipped fragment needs this rather than PromptsDir.
func MergedPromptsDir(tb testing.TB) string {
	tb.Helper()
	dst := tb.TempDir()
	if err := copyTree(PromptsDir(), dst); err != nil {
		tb.Fatalf("repopath: copy prompts: %v", err)
	}
	dirs, err := filepath.Glob(filepath.Join(moduleDir(), "..", "..", "connectors", "*", "fragments"))
	if err != nil {
		tb.Fatalf("repopath: glob connector fragments: %v", err)
	}
	for _, dir := range dirs {
		if err := copyTree(dir, filepath.Join(dst, "fragments")); err != nil {
			tb.Fatalf("repopath: overlay %s: %v", dir, err)
		}
	}
	return dst
}

// copyTree copies the regular files under src into dst, creating directories.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
}
