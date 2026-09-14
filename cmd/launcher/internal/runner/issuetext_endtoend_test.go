package runner

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// promptassemblyTestdataRegistry and promptassemblyPromptsDir mirror
// promptassembly's own assemble_test.go package-relative constants
// (loadTestRegistry, promptsDir), unreachable from here since both are
// unexported test-only helpers in a different package. internal/runner
// sits at the same tree depth as internal/promptassembly, so the same
// "../../../../templates/..." relative depth applies unchanged.
const (
	promptassemblyTestdataRegistry = "../promptassembly/testdata/registry.json"
	promptassemblyPromptsDir       = "../../../../templates/default/prompts"
)

// issueTextValue extracts the value of the LAST "ISSUE_TEXT=..." entry in
// env, failing the test if none is found. resolvedRunEnv's output carries
// at most one such entry, but ociRunEnv's starts from the calling process's
// own os.Environ() -- which, running inside a dispatched Box, can itself
// already carry an unrelated ambient ISSUE_TEXT -- before appending the
// boxEnv-driven one this test actually cares about; that append is always
// last, so scanning from the end picks it over any ambient entry.
func issueTextValue(t *testing.T, env []string) string {
	t.Helper()
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "ISSUE_TEXT="); ok {
			return v
		}
	}
	t.Fatal("no ISSUE_TEXT= entry found in run env")
	return ""
}

// TestIssueTextReachesAssembledPromptUnderBothRunners is the end-to-end half
// of issue #3470's acceptance criteria: the env each runner hands its Box
// (ociRunEnv for OCI, resolvedRunEnv for bwrap) carries ISSUE_TEXT=<value>,
// and feeding exactly that env into promptassembly's own box-env read
// (EnvFromEnviron, which os.Getenv("ISSUE_TEXT")s the process environment
// the Box's entrypoint would see -- boxenv_gen.go's Env.IssueText field)
// yields a prompt containing the "# ISSUE TEXT" section (rendered by
// promptassembly's issueTextSection, which assemblePromptBodies appends
// to every body). Each case builds the run env from a realistic
// multi-line ISSUE_TEXT in box.Env, applies only that one entry via
// t.Setenv (mirroring how a Box process actually inherits it -- not the
// runner's other off-argv keys, irrelevant to this claim), then assembles
// and asserts the section and value are present.
func TestIssueTextReachesAssembledPromptUnderBothRunners(t *testing.T) {
	const issueText = "issue-3470 title line\n\nA multi-line private issue body,\nwith a second paragraph and a trailing note."

	reg, err := promptassembly.LoadRegistryFile(promptassemblyTestdataRegistry)
	if err != nil {
		t.Fatalf("LoadRegistryFile: %v", err)
	}

	tests := []struct {
		name   string
		runEnv func(boxEnv map[string]string) []string
	}{
		{"oci", ociRunEnv},
		{"bwrap", resolvedRunEnv},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			boxEnv := map[string]string{"ISSUE_TEXT": issueText}
			runEnv := tc.runEnv(boxEnv)

			t.Setenv("ISSUE_TEXT", issueTextValue(t, runEnv))

			env := promptassembly.EnvFromEnviron()
			env.PromptsDir = promptassemblyPromptsDir

			result, err := promptassembly.Assemble(env, reg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if !strings.Contains(result.Prompt, "# ISSUE TEXT") {
				t.Errorf("%s: assembled prompt missing \"# ISSUE TEXT\" section:\n%s", tc.name, result.Prompt)
			}
			if !strings.Contains(result.Prompt, issueText) {
				t.Errorf("%s: assembled prompt missing the ISSUE_TEXT value", tc.name)
			}
		})
	}
}
