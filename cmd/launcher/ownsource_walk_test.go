package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWalkOwnSourceSkipsModuleRootVendorOnly(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"vendor/a.go", "internal/vendor/b.go", "x.go"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	err := walkOwnSource(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"internal/vendor/b.go", "x.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("visited %v, want %v", got, want)
	}
}

// walkOwnSource is filepath.WalkDir that never descends into root/vendor.
// The Nix check sandbox copies vendored modules into the module root, and
// third-party code is outside every source guard's scope; only the top-level
// vendor is pruned, so a nested dir named vendor is still walked.
func walkOwnSource(root string, fn fs.WalkDirFunc) error {
	skip := filepath.Join(root, "vendor")
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path == skip {
			return filepath.SkipDir
		}
		return fn(path, d, err)
	})
}
