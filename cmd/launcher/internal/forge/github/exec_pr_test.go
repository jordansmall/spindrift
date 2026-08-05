package github

import (
	"strings"
	"testing"
)

// TestOpenPRForBranch_PRListFailureSurfacesStderr verifies that when `gh pr
// list` fails, the error OpenPRForBranch returns includes gh's actual
// stderr — not just "exit status 1" — so isTransientForgeError has a real
// marker to pattern-match against (issue #2323).
func TestOpenPRForBranch_PRListFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.OpenPRForBranch("agent/issue-1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("error must surface gh's stderr; got: %v", err)
	}
}

// TestOpenPRForBranch_PRViewFailureSurfacesStderr verifies that when `gh pr
// view` (the isDraft lookup) fails, the error OpenPRForBranch returns
// includes gh's actual stderr for the same reason.
func TestOpenPRForBranch_PRViewFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo "https://github.com/owner/repo/pull/42"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.OpenPRForBranch("agent/issue-1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("error must surface gh's stderr; got: %v", err)
	}
}
