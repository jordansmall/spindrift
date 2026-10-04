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
	"os"
	"path/filepath"
	"sync"
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

// RegistryJSON is internal/promptassembly/testdata/registry.json.
func RegistryJSON() string { return promptassemblyTestdata("registry.json") }

// ForbiddenMarkersJSON is internal/promptassembly/testdata/forbidden-markers.json.
func ForbiddenMarkersJSON() string { return promptassemblyTestdata("forbidden-markers.json") }

// ValidateMarkersJSON is internal/promptassembly/testdata/validate-markers.json.
func ValidateMarkersJSON() string { return promptassemblyTestdata("validate-markers.json") }

// PromptAssemblyGoldenDir is the repo-root tests/testdata/prompt-assembly-golden
// directory: the committed prompt bytes the promptassembly golden test pins.
func PromptAssemblyGoldenDir() string {
	return filepath.Join(moduleDir(), "..", "..", "tests", "testdata", "prompt-assembly-golden")
}
