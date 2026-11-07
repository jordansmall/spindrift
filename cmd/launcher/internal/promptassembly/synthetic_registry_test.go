package promptassembly

import (
	"os"
	"path/filepath"
	"testing"
)

// syntheticRegistry stages files under a fresh PromptsDir and returns the rows
// as a Registry plus coveredEnv (PromptsDir retargeted at that dir), so a
// mechanics test reads as rows in, rendered bodies out without touching the
// production registry or prose. Every gate state therefore follows coveredEnv's
// flags (skills baked, github tracker and forge); a test needing another state
// sets it on the returned Env. files maps PromptsDir-relative paths (e.g.
// "fragments/x.md") to content and overrides the minimal base templates
// staged by default, which exist because Assemble always reads the kind's
// base prompt and, on the fresh work path, review-prompt.md.
func syntheticRegistry(t *testing.T, rows []FragmentRow, files map[string]string) (Registry, Env) {
	t.Helper()
	dir := t.TempDir()
	staged := map[string]string{
		"issue-prompt.md":  "issue\n",
		"review-prompt.md": "review\n",
	}
	for name, content := range files {
		staged[name] = content
	}
	for name, content := range staged {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	env := coveredEnv()
	env.PromptsDir = dir
	return Registry{Rows: rows}, env
}
