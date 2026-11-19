package boxclone

import (
	"io"
	"strings"
	"testing"
)

// A git argument can carry a clone URL's userinfo (an operator base URL), so
// run's error text must not echo it.
func TestRunErrorRedactsURLCredentials(t *testing.T) {
	err := run(io.Discard, io.Discard, t.TempDir(), "git", "no-such-subcommand", "https://user:hunter2@example.invalid/o/r")
	if err == nil {
		t.Fatal("run of an unknown git subcommand succeeded")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks the credential: %v", err)
	}
}
