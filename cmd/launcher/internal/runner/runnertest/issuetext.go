package runnertest

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// issueTextValue returns the value of the LAST "ISSUE_TEXT=" entry in env: a
// runner's env may start from os.Environ(), whose blank ISSUE_TEXT the test
// set, and the boxEnv entry is appended after it.
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

// AssertIssueTextReachesPrompt is the end-to-end half of issue #3470: the env
// a runner hands its Box (runEnv) carries ISSUE_TEXT through
// promptassembly.EnvFromEnviron into the assembled prompt's "# ISSUE TEXT"
// section. Every other Box env var is blanked first, so only ISSUE_TEXT can
// affect the claim.
func AssertIssueTextReachesPrompt(t *testing.T, name string, runEnv func(boxEnv map[string]string) []string) {
	t.Helper()
	const issueText = "issue-3470 title line\n\nA multi-line private issue body,\nwith a second paragraph and a trailing note."

	for _, v := range promptassembly.BoxEnvVarNames {
		t.Setenv(v, "")
	}
	boxEnv := map[string]string{"ISSUE_TEXT": issueText}
	t.Setenv("ISSUE_TEXT", issueTextValue(t, runEnv(boxEnv)))

	env := promptassembly.EnvFromEnviron()
	env.PromptsDir = repopath.PromptsDir()

	result, err := promptassembly.Assemble(env, promptassembly.Registry{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(result.Prompt, "# ISSUE TEXT") {
		t.Errorf("%s: assembled prompt missing \"# ISSUE TEXT\" section:\n%s", name, result.Prompt)
	}
	if !strings.Contains(result.Prompt, issueText) {
		t.Errorf("%s: assembled prompt missing the ISSUE_TEXT value", name)
	}
}
