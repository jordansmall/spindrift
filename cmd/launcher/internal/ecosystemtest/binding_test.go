package ecosystemtest

import (
	"os"
	"path/filepath"
	"testing"
)

// gitCopy must return its error and remove the temp dir on every failure path,
// not panic on a nil cleanup.
func TestGitCopyFailureCleansUp(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, c *replay){
		"repo is a regular file": func(t *testing.T, c *replay) {
			if err := os.WriteFile(filepath.Join(c.dir, "repo"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		// Stat fails with ENOTDIR, not ErrNotExist: the non-ENOENT stat branch.
		"case dir beneath a regular file": func(t *testing.T, c *replay) {
			f := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			c.dir = filepath.Join(f, "sub")
		},
		"git missing": func(t *testing.T, _ *replay) { t.Setenv("PATH", "") },
	} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			c := &replay{dir: t.TempDir()}
			setup(t, c)

			work, cleanup, err := c.gitCopy()
			if err == nil {
				cleanup()
				t.Fatalf("gitCopy = %q, nil error; want failure", work)
			}
			entries, rerr := os.ReadDir(tmp)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Errorf("temp dir leaked after failure: %v", entries)
			}
		})
	}
}
