package registrypathset

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

func TestDeriveFromGitRef_CommittedNpmDerives(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
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
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
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
	bare, src := testutil.BareRepoWithConfigs(t, "main",
		map[string]string{".npmrc": "registry=https://committed.example.com/npm\n"}, nil)
	testutil.WriteFixture(t, src, ".yarnrc.yml", "npmRegistryServer: https://uncommitted.example.com/yarn\n")

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
	testutil.GitRun(t, src, "init", "-b", "main")
	testutil.GitRun(t, src, "config", "user.email", "test@example.com")
	testutil.GitRun(t, src, "config", "user.name", "Test")
	testutil.WriteFixture(t, src, "README", "main\n")
	testutil.GitRun(t, src, "add", "README")
	testutil.GitRun(t, src, "commit", "-m", "main seed")

	testutil.GitRun(t, src, "checkout", "-b", "other")
	if err := os.WriteFile(filepath.Join(src, ".npmrc"), []byte("registry=https://other-branch.example.com/npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.GitRun(t, src, "add", ".npmrc")
	testutil.GitRun(t, src, "commit", "-m", "other branch config")

	bare := filepath.Join(t.TempDir(), "accum.git")
	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, src, "push", bare, "+refs/heads/main:refs/heads/main", "+refs/heads/other:refs/heads/other")

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
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
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
	bare, _ := testutil.BareRepoWithConfigs(t, "main", nil, nil)

	got, err := DeriveFromGitRef(bare, "main")
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeriveFromGitRef = %+v, want no host path sets", got)
	}
}

// A row that is not in ecosystem.Table is walked end to end: materialized from
// the ref, parsed, and derived, proving the rows parameter reaches both stages.
func TestDeriveFromGitRefRows_WalksSuppliedRowNotTable(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		"fake.cfg": "x",
	}, nil)
	row := ecosystem.Row{
		Name:             "fake",
		InTreeConfigPath: "fake.cfg",
		ConfigParser: func(string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Host: "fake.example.com", UpstreamBaseURL: "https://fake.example.com/repo"}}, true, nil
		},
	}

	got, err := DeriveFromGitRefRows(bare, "main", []ecosystem.Row{row})
	if err != nil {
		t.Fatalf("DeriveFromGitRefRows: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "fake.example.com",
		Origin:   "https://fake.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: "fake", Path: "/repo"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveFromGitRefRows = %+v, want %+v", got, want)
	}
}
