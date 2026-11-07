package promptassembly

import (
	"os"
	"path/filepath"
	"strings"
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

// stageAgentRoster provisions a one-agent roster whose prompt template is
// staged as <agent>-prompt.md.
func stageAgentRoster(env *Env, agent string) {
	env.AgentsJSONTemplate = `{"` + agent + `":{"model":"x"}}`
	env.AgentsPromptFiles = `{"` + agent + `":"` + agent + `-prompt.md"}`
}

// agentPrompt assembles env against reg and returns the named roster agent's
// rendered prompt, whose template is staged as <agent>-prompt.md.
func agentPrompt(t *testing.T, env Env, reg Registry, agent string) string {
	t.Helper()
	stageAgentRoster(&env, agent)
	result, err := Assemble(env, reg)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return agentPromptFromJSON(t, result.AgentsJSON, agent)
}

// unseparated drops the blank line Assemble puts between rendered fragments,
// so a test can compare the spliced markers as one string.
func unseparated(prompt string) string { return strings.ReplaceAll(prompt, "\n\n", "") }

// assertNoUnsubstituted fails when prompt still holds a ${...} token, which
// means a variable reached the model unrendered. label names the case.
func assertNoUnsubstituted(t *testing.T, label, prompt string) {
	t.Helper()
	if strings.Contains(prompt, "${") {
		t.Errorf("%s: prompt = %q, want no unsubstituted ${...} token", label, prompt)
	}
}
