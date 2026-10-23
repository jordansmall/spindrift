//go:build integration

package bwrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerationRootsProtectStoreArtifactsFromGC drives the production
// register and reclaim paths against a real nix store and asks nix itself which
// paths are collectable. The store is a throwaway chroot store, so the test
// needs no daemon and never touches the host store.
func TestGenerationRootsProtectStoreArtifactsFromGC(t *testing.T) {
	for _, bin := range []string{"nix", "nix-store"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	t.Setenv("NIX_REMOTE", "local?root="+t.TempDir())
	t.Setenv("NIX_CONFIG", "experimental-features = nix-command")

	src := t.TempDir()
	var storePaths []string
	for _, name := range []string{"artifact-a", "artifact-b"} {
		f := filepath.Join(src, name)
		if err := os.WriteFile(f, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("nix-store", "--add", f).Output()
		if err != nil {
			t.Fatalf("nix-store --add %s: %v", f, err)
		}
		storePaths = append(storePaths, strings.TrimSpace(string(out)))
	}

	dead := func() map[string]bool {
		t.Helper()
		out, err := exec.Command("nix-store", "--gc", "--print-dead").Output()
		if err != nil {
			t.Fatalf("nix-store --gc --print-dead: %v", err)
		}
		set := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			set[strings.TrimSpace(line)] = true
		}
		return set
	}
	assertDead := func(want bool, when string) {
		t.Helper()
		got := dead()
		for _, p := range storePaths {
			if got[p] != want {
				t.Errorf("%s: %s listed dead = %t, want %t", when, p, got[p], want)
			}
		}
	}

	assertDead(true, "before any root is registered")

	pwd := t.TempDir()
	const gen, other = "gen-old", "gen-new"
	registerGenerationRoot(NixGCRootsRoot(pwd), gen, storePaths)
	assertDead(false, "after registerGenerationRoot")

	snapRoot := NixVarSnapshotRoot(pwd)
	if err := os.MkdirAll(NixVarSnapshotDir(pwd, gen), 0o755); err != nil {
		t.Fatal(err)
	}
	lf, err := LockSnapshotShared(NixVarSnapshotDir(pwd, gen))
	if err != nil {
		t.Fatal(err)
	}
	if err := reclaimStaleSnapshots(snapRoot, NixGCRootsRoot(pwd), other); err != nil {
		t.Fatal(err)
	}
	assertDead(false, "while a Box holds the generation's shared lock")

	UnlockSnapshot(lf)
	if err := reclaimStaleSnapshots(snapRoot, NixGCRootsRoot(pwd), other); err != nil {
		t.Fatal(err)
	}
	assertDead(true, "after reclaim dropped the generation")
}
