package connectorforgetest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Production code is https-only, so git is pointed at each harness's bare repo
// by a url.<bare>.insteadOf rewrite. Git children inherit the process
// environment, so the rewrite has to be process-visible; a per-test t.Setenv
// would forbid t.Parallel, so every harness shares one global config file
// instead. Backends have unique hosts, so their rules never collide.
var gitEnv struct {
	mu    sync.Mutex
	users int
	dir   string
	rules map[string]string  // https clone URL -> bare repo path
	saved map[string]*string // env var -> prior value, nil when it was unset
}

var gitIdentity = map[string]string{
	"GIT_AUTHOR_NAME":     "Test Bot",
	"GIT_AUTHOR_EMAIL":    "bot@example.com",
	"GIT_COMMITTER_NAME":  "Test Bot",
	"GIT_COMMITTER_EMAIL": "bot@example.com",
}

// useGitEnv gives t's harness the shared git identity and config file for the
// rest of the test; the last user out restores the process environment.
func useGitEnv(t *testing.T) {
	t.Helper()
	gitEnv.mu.Lock()
	defer gitEnv.mu.Unlock()
	// Counted and released before anything that can t.Fatal, so a failed first
	// setup still restores the environment it already changed.
	first := gitEnv.users == 0
	gitEnv.users++
	t.Cleanup(releaseGitEnv)
	if first {
		dir, err := os.MkdirTemp("", "connectorforge-gitenv-")
		if err != nil {
			t.Fatal(err)
		}
		gitEnv.dir, gitEnv.rules, gitEnv.saved = dir, map[string]string{}, map[string]*string{}
		env := map[string]string{"GIT_CONFIG_GLOBAL": filepath.Join(dir, "gitconfig")}
		for k, v := range gitIdentity {
			env[k] = v
		}
		for k, v := range env {
			if prior, ok := os.LookupEnv(k); ok {
				gitEnv.saved[k] = &prior
			} else {
				gitEnv.saved[k] = nil
			}
			os.Setenv(k, v) //nolint:errcheck // cannot fail for these names
		}
		writeGitConfigLocked(t)
	}
}

func releaseGitEnv() {
	gitEnv.mu.Lock()
	defer gitEnv.mu.Unlock()
	if gitEnv.users--; gitEnv.users > 0 {
		return
	}
	for k, prior := range gitEnv.saved {
		if prior == nil {
			os.Unsetenv(k) //nolint:errcheck // best-effort restore
		} else {
			os.Setenv(k, *prior) //nolint:errcheck // best-effort restore
		}
	}
	os.RemoveAll(gitEnv.dir) //nolint:errcheck // best-effort temp cleanup
}

// setGitRewrite makes git clone url from the bare repo at path. It replaces any
// earlier rewrite for url and is dropped when t ends.
func setGitRewrite(t *testing.T, url, path string) {
	t.Helper()
	gitEnv.mu.Lock()
	defer gitEnv.mu.Unlock()
	gitEnv.rules[url] = path
	writeGitConfigLocked(t)
	t.Cleanup(func() {
		gitEnv.mu.Lock()
		defer gitEnv.mu.Unlock()
		delete(gitEnv.rules, url)
		writeGitConfigLocked(t)
	})
}

// writeGitConfigLocked renames a fresh file into place so a git child never
// reads a half-written config.
func writeGitConfigLocked(t *testing.T) {
	t.Helper()
	urls := make([]string, 0, len(gitEnv.rules))
	for u := range gitEnv.rules {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	var b strings.Builder
	for _, u := range urls {
		fmt.Fprintf(&b, "[url %s]\n\tinsteadOf = %s\n", gitQuote(gitEnv.rules[u]), gitQuote(u))
	}
	tmp, err := os.CreateTemp(gitEnv.dir, "gitconfig-*")
	if err != nil {
		t.Fatal(err)
	}
	_, werr := tmp.WriteString(b.String())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		t.Fatal(werr)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(gitEnv.dir, "gitconfig")); err != nil {
		t.Fatal(err)
	}
}

func gitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
