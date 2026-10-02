package runner

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// issueTextValue returns the value of the LAST "ISSUE_TEXT=" entry in env.
// resolvedRunEnv emits at most one, but ociRunEnv starts from the calling
// process's own os.Environ(), which still carries the blank ISSUE_TEXT each
// subtest sets. ociRunEnv appends the boxEnv-driven entry last, so scanning
// from the end picks it over that inherited one.
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
// of issue #3470: the env each runner hands its Box carries ISSUE_TEXT, and
// feeding that env into promptassembly's box-env read (EnvFromEnviron) yields
// a prompt with the "# ISSUE TEXT" section. Each case blanks every Box env
// var first, then applies only ISSUE_TEXT via t.Setenv; the runner's other
// off-argv keys do not affect the claim.
func TestIssueTextReachesAssembledPromptUnderBothRunners(t *testing.T) {
	const issueText = "issue-3470 title line\n\nA multi-line private issue body,\nwith a second paragraph and a trailing note."

	reg, err := promptassembly.LoadRegistryFile(repopath.RegistryJSON())
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
			for _, name := range promptassembly.BoxEnvVarNames {
				t.Setenv(name, "")
			}
			boxEnv := map[string]string{"ISSUE_TEXT": issueText}
			runEnv := tc.runEnv(boxEnv)

			t.Setenv("ISSUE_TEXT", issueTextValue(t, runEnv))

			env := promptassembly.EnvFromEnviron()
			env.PromptsDir = repopath.PromptsDir()

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
