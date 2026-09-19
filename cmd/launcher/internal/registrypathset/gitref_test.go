package registrypathset

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// mustRunGit repeats bootstrap_test.go's helper of the same name because the
// two test files live in different packages.
func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
	}
}

// mustBareRepoWithConfigs pushes a source checkout into a second, bare repo so
// the fixture has no working tree at all, the way an Accumulation repo looks
// (ADR 0033) and unlike a plain t.TempDir() Derive fixture. uncommittedFiles
// lands in the source checkout's working tree but never in a commit, so a test
// can pin that only committed content reaches the derived set (issue #3310 AC2).
func mustBareRepoWithConfigs(t *testing.T, branch string, files, uncommittedFiles map[string]string) string {
	t.Helper()
	src := t.TempDir()
	mustRunGit(t, src, "init", "-b", branch)
	mustRunGit(t, src, "config", "user.email", "test@example.com")
	mustRunGit(t, src, "config", "user.name", "Test")

	for rel, body := range files {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRunGit(t, src, "add", rel)
	}
	if len(files) == 0 {
		// A branch with no commit has no resolvable ^{tree}, so give it one
		// harmless commit to keep "carries no config files" distinct from
		// "carries no commits at all".
		if err := os.WriteFile(filepath.Join(src, "README"), []byte("empty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRunGit(t, src, "add", "README")
	}
	mustRunGit(t, src, "commit", "-m", "seed")

	for rel, body := range uncommittedFiles {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bare := filepath.Join(t.TempDir(), "accum.git")
	mustRunGit(t, "", "init", "--bare", bare)
	mustRunGit(t, src, "push", bare, "+refs/heads/"+branch+":refs/heads/"+branch)

	return bare
}

func TestDeriveFromGitRef_CommittedNpmDerives(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	}, nil)

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "host.example.com",
		Origin:   "https://host.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveFromGitRef = %+v, want %+v", got, want)
	}
}

// The nested .cargo/config.toml pins that materialization creates parent
// directories rather than only handling repo-root files.
func TestDeriveFromGitRef_MultipleEcosystemsMaterializeAndDerive(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://npm.example.com/repo/npm\n",
		".cargo/config.toml": `
[registries.mycorp]
index = "sparse+https://cargo.example.com/repo/cargo/index"
`,
	}, nil)

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}

	hosts := make(map[string]HostPathSet, len(got))
	for _, hps := range got {
		hosts[hps.Host] = hps
	}
	if len(hosts) != 2 {
		t.Fatalf("DeriveFromGitRef = %+v, want exactly 2 host path sets", got)
	}
	if hps, ok := hosts["npm.example.com"]; !ok || !hps.admits("/repo/npm/axios") {
		t.Errorf("npm host path set = %+v, want an admitting /repo/npm subtree", hps)
	}
	if hps, ok := hosts["cargo.example.com"]; !ok || !hps.admits("/repo/cargo/index/config.json") {
		t.Errorf("cargo host path set = %+v, want an admitting /repo/cargo/index subtree", hps)
	}
}

// Issue #3310 AC2. If DeriveFromGitRef read the working tree instead of the
// ref, the derived set would admit a registry the Accumulation repo never
// recorded.
func TestDeriveFromGitRef_UncommittedFileNeverEnters(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main",
		map[string]string{".npmrc": "registry=https://committed.example.com/npm\n"},
		map[string]string{".yarnrc.yml": "npmRegistryServer: https://uncommitted.example.com/yarn\n"},
	)

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "committed.example.com",
		Origin:   "https://committed.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: "npm", Path: "/npm"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveFromGitRef = %+v, want only the committed host %+v", got, want)
	}
}

// The fixture needs two branches so a config committed on one cannot leak
// into the derived set for the other.
func TestDeriveFromGitRef_OtherBranchConfigNeverEnters(t *testing.T) {
	src := t.TempDir()
	mustRunGit(t, src, "init", "-b", "main")
	mustRunGit(t, src, "config", "user.email", "test@example.com")
	mustRunGit(t, src, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, src, "add", "README")
	mustRunGit(t, src, "commit", "-m", "main seed")

	mustRunGit(t, src, "checkout", "-b", "other")
	if err := os.WriteFile(filepath.Join(src, ".npmrc"), []byte("registry=https://other-branch.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, src, "add", ".npmrc")
	mustRunGit(t, src, "commit", "-m", "other branch config")

	bare := filepath.Join(t.TempDir(), "accum.git")
	mustRunGit(t, "", "init", "--bare", bare)
	mustRunGit(t, src, "push", bare, "+refs/heads/main:refs/heads/main", "+refs/heads/other:refs/heads/other")

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeriveFromGitRef over main = %+v, want no host path sets: the npm config only exists on other", got)
	}
}

func TestDeriveFromGitRef_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := DeriveFromGitRef(missing, "main")
	if err == nil {
		t.Fatal("DeriveFromGitRef: want error for a missing repo dir, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("DeriveFromGitRef error = %q, want it to name %q", err.Error(), missing)
	}
}

// A plain directory must be rejected rather than read as an empty tree.
func TestDeriveFromGitRef_NotAGitRepoFails(t *testing.T) {
	dir := t.TempDir()
	_, err := DeriveFromGitRef(dir, "main")
	if err == nil {
		t.Fatal("DeriveFromGitRef: want error for a directory that is not a git repo, got nil")
	}
}

// A missing ref is an error, unlike a ref that declares no config files. See
// TestDeriveFromGitRef_NoConfigFilesDerivesEmptySet for the other half.
func TestDeriveFromGitRef_MissingRefFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	}, nil)

	_, err := DeriveFromGitRef(bare, "does-not-exist")
	if err == nil {
		t.Fatal("DeriveFromGitRef: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("DeriveFromGitRef error = %q, want it to name the missing ref", err.Error())
	}
}

// Declaring nothing is not an error here. The caller
// (registryroutesresolve.go) decides whether an empty set fails the route it
// is resolving.
func TestDeriveFromGitRef_NoConfigFilesDerivesEmptySet(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", nil, nil)

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeriveFromGitRef = %+v, want no host path sets", got)
	}
}
