package github

import (
	"path/filepath"
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

// TestOpenPRForBranch_SingleGHCall verifies OpenPRForBranch resolves a found
// PR with exactly one `gh` invocation. It used to make a second `gh pr view
// --json isDraft` call solely to populate the (now-removed) draft field;
// that call is gone, so a single `gh pr list` must be enough to report the
// PR as found.
func TestOpenPRForBranch_SingleGHCall(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo "https://github.com/owner/repo/pull/42"
  exit 0
fi
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	pr, ok, err := c.OpenPRForBranch("agent/issue-1")
	if err != nil {
		t.Fatalf("OpenPRForBranch: %v", err)
	}
	if !ok {
		t.Fatal("OpenPRForBranch: want found, got not found")
	}
	if pr.URL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("OpenPRForBranch URL = %q, want the listed PR URL", pr.URL)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("OpenPRForBranch made %d gh calls, want exactly 1", len(matches))
	}
}
