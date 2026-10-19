package runnertest

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// issueTextValue returns the value of the LAST "ISSUE_TEXT=" entry in env.
// A runner's env may start from the calling process's own os.Environ(), which
// still carries the blank ISSUE_TEXT the test sets, while the boxEnv-driven
// entry is appended last, so scanning from the end picks it over that
// inherited one.
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
// a runner hands its Box (runEnv) carries ISSUE_TEXT, and feeding that env
// into promptassembly's box-env read (EnvFromEnviron) yields a prompt with
// the "# ISSUE TEXT" section. It blanks every Box env var first, then applies
// only ISSUE_TEXT via t.Setenv; the runner's other off-argv keys do not
// affect the claim.
func AssertIssueTextReachesPrompt(t *testing.T, name string, runEnv func(boxEnv map[string]string) []string) {
	t.Helper()
	const issueText = "issue-3470 title line\n\nA multi-line private issue body,\nwith a second paragraph and a trailing note."

	reg, err := promptassembly.LoadRegistryFile(repopath.RegistryJSON())
	if err != nil {
		t.Fatalf("LoadRegistryFile: %v", err)
	}

	for _, v := range promptassembly.BoxEnvVarNames {
		t.Setenv(v, "")
	}
	boxEnv := map[string]string{"ISSUE_TEXT": issueText}
	t.Setenv("ISSUE_TEXT", issueTextValue(t, runEnv(boxEnv)))

	env := promptassembly.EnvFromEnviron()
	env.PromptsDir = repopath.PromptsDir()

	result, err := promptassembly.Assemble(env, reg)
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
