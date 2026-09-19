package registrydiscover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

// mustBareRepoWithConfigs builds the fixture the way an Accumulation repo
// actually looks (ADR 0033): a source checkout commits the files, then pushes
// them to a second, bare repo, so the fixture has no working tree at all.
func mustBareRepoWithConfigs(t *testing.T, branch string, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	testutil.GitRun(t, src, "init", "-b", branch)
	testutil.GitRun(t, src, "config", "user.email", "test@example.com")
	testutil.GitRun(t, src, "config", "user.name", "Test")

	for rel, body := range files {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		testutil.GitRun(t, src, "add", rel)
	}
	if len(files) == 0 {
		// A branch with no commit has no resolvable ^{tree}, so give it one
		// harmless commit to keep "carries no config files" distinct from
		// "carries no commits at all".
		if err := os.WriteFile(filepath.Join(src, "README"), []byte("empty\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		testutil.GitRun(t, src, "add", "README")
	}
	testutil.GitRun(t, src, "commit", "-m", "seed")

	bare := filepath.Join(t.TempDir(), "accum.git")
	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, src, "push", bare, "+refs/heads/"+branch+":refs/heads/"+branch)

	return bare
}

// Pins ResolveRef's fail-closed contract directly, not only through its
// MaterializeRef and UncoveredHostsFromGitRef callers.
func TestResolveRef_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := ResolveRef(missing, "main"); err == nil {
		t.Fatal("ResolveRef: want error for a missing repo dir, got nil")
	}
}

// An empty repoDir must error rather than fall through to `git -C ""`, which
// resolves the ref against the process cwd's own repo. Run from inside any
// checkout, a ref like "main" would otherwise resolve and report drift for the
// wrong repo. The t.Chdir into the fixture is what makes that failure visible.
func TestResolveRef_EmptyRepoDirFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})
	t.Chdir(bare)

	if err := ResolveRef("", "main"); err == nil {
		t.Fatal("ResolveRef: want error for an empty repo dir, got nil")
	}
}

// The error must name the ref: registrypathset asserts on that same wording
// through MaterializeRef.
func TestResolveRef_MissingRefFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	err := ResolveRef(bare, "does-not-exist")
	if err == nil {
		t.Fatal("ResolveRef: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("ResolveRef error = %q, want it to name the missing ref", err.Error())
	}
}

// registryRouteDriftCheckForRef gates the drift row's existence on this
// success path.
func TestResolveRef_ResolvableRefSucceeds(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	if err := ResolveRef(bare, "main"); err != nil {
		t.Errorf("ResolveRef: unexpected error for a resolvable ref: %v", err)
	}
}

func TestUncoveredHostsFromGitRef_UncoveredHostReported(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	got, err := UncoveredHostsFromGitRef(bare, "main", nil)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	want := []string{"host.example.com"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("UncoveredHostsFromGitRef = %v, want %v", got, want)
	}
}

func TestUncoveredHostsFromGitRef_FullyCoveredReturnsNone(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	got, err := UncoveredHostsFromGitRef(bare, "main", []string{"host.example.com"})
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRef = %v, want none", got)
	}
}

func TestUncoveredHostsFromGitRef_MissingRepoDirFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := UncoveredHostsFromGitRef(missing, "main", nil)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRef: want error for a missing repo dir, got nil")
	}
}

func TestUncoveredHostsFromGitRef_MissingRefFails(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", map[string]string{
		".npmrc": "registry=https://host.example.com/npm\n",
	})

	_, err := UncoveredHostsFromGitRef(bare, "does-not-exist", nil)
	if err == nil {
		t.Fatal("UncoveredHostsFromGitRef: want error for a ref that does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("UncoveredHostsFromGitRef error = %q, want it to name the missing ref", err.Error())
	}
}

// A ref declaring no config files is not an error, unlike a broken repo or
// ref. Both yield no hosts, so only the error distinguishes them.
func TestUncoveredHostsFromGitRef_NoConfigFilesReturnsNone(t *testing.T) {
	bare := mustBareRepoWithConfigs(t, "main", nil)

	got, err := UncoveredHostsFromGitRef(bare, "main", nil)
	if err != nil {
		t.Fatalf("UncoveredHostsFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("UncoveredHostsFromGitRef = %v, want none", got)
	}
}
