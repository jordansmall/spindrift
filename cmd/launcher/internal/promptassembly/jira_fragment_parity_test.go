package promptassembly

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The jira golden must stay byte-identical to the github issue-read step until
// ADR 0068's display-name collapse, so the two shipped copies are pinned.
func TestJiraIssueReadFragmentMatchesGithub(t *testing.T) {
	read := func(backend string) []byte {
		b, err := os.ReadFile(filepath.Join(promptsDir, "fragments", backend, "issue-read.md"))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if !bytes.Equal(read("jira"), read("github")) {
		t.Error("fragments/jira/issue-read.md differs from fragments/github/issue-read.md")
	}
}
