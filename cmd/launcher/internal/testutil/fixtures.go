package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteFixture writes body to rel under dir, creating any missing parent
// directories of rel.
func WriteFixture(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", full, err)
	}
}

// BareRepoWithConfigs builds the fixture the way an Accumulation repo actually
// looks (ADR 0033): a source checkout commits files on branch, then pushes
// them to a second, bare repo with no working tree at all. It returns the bare
// repo and the source checkout, so a test can compare a ref snapshot against a
// real checkout of the same tree; symlinks maps a link path to its target.
func BareRepoWithConfigs(t *testing.T, branch string, files, symlinks map[string]string) (bare, src string) {
	t.Helper()
	src = t.TempDir()
	GitRun(t, src, "init", "-b", branch)
	GitRun(t, src, "config", "user.email", "test@example.com")
	GitRun(t, src, "config", "user.name", "Test")

	for rel, body := range files {
		WriteFixture(t, src, rel, body)
		GitRun(t, src, "add", rel)
	}
	for link, target := range symlinks {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(src, link)); err != nil {
			t.Fatal(err)
		}
		GitRun(t, src, "add", link)
	}
	if len(files)+len(symlinks) == 0 {
		// A branch with no commit has no resolvable ^{tree}, so give it one
		// harmless commit to keep "carries no config files" distinct from
		// "carries no commits at all".
		WriteFixture(t, src, "README", "empty\n")
		GitRun(t, src, "add", "README")
	}
	GitRun(t, src, "commit", "-m", "seed")

	bare = filepath.Join(t.TempDir(), "accum.git")
	GitRun(t, "", "init", "--bare", bare)
	GitRun(t, src, "push", bare, "+refs/heads/"+branch+":refs/heads/"+branch)

	return bare, src
}
