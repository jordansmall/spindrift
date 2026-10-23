package registrypathset

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
	"spindrift.dev/launcher/internal/registryvocab"
	"spindrift.dev/launcher/internal/testutil"
)

func TestDeriveFromGitRef_CommittedConfigDerives(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		synthetic.Strategy.InTreeConfigPath: "a = https://host.example.com/repo\n",
	}, nil)

	got, err := DeriveFromGitRefRows(bare, "main", syntheticRows)
	if err != nil {
		t.Fatalf("DeriveFromGitRefRows: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "host.example.com",
		Origin:   "https://host.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/repo", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveFromGitRefRows = %+v, want %+v", got, want)
	}
}

// Both Strategies' config paths are nested, pinning that materialization creates
// parent directories rather than only handling repo-root files.
func TestDeriveFromGitRef_MultipleRowsMaterializeAndDerive(t *testing.T) {
	twin := synthetic.Twin("synthetic-twin")
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		synthetic.Strategy.InTreeConfigPath: "a = https://first.example.com/repo/first\n",
		twin.InTreeConfigPath:               "a = https://second.example.com/repo/second\n",
	}, nil)

	got, err := DeriveFromGitRefRows(bare, "main", []ecosystem.Strategy{synthetic.Strategy, twin})
	if err != nil {
		t.Fatalf("DeriveFromGitRefRows: unexpected error: %v", err)
	}

	hosts := make(map[string]HostPathSet, len(got))
	for _, hps := range got {
		hosts[hps.Host] = hps
	}
	if len(hosts) != 2 {
		t.Fatalf("DeriveFromGitRefRows = %+v, want exactly 2 host path sets", got)
	}
	if hps, ok := hosts["first.example.com"]; !ok || !hps.admits("/repo/first/pkg") {
		t.Errorf("first host path set = %+v, want an admitting /repo/first subtree", hps)
	}
	if hps, ok := hosts["second.example.com"]; !ok || !hps.admits("/repo/second/config.json") {
		t.Errorf("second host path set = %+v, want an admitting /repo/second subtree", hps)
	}
}

// Issue #3310 AC2. If DeriveFromGitRef read the working tree instead of the
// ref, the derived set would admit a registry the Accumulation repo never
// recorded.
func TestDeriveFromGitRef_UncommittedFileNeverEnters(t *testing.T) {
	twin := synthetic.Twin("synthetic-twin")
	bare, src := testutil.BareRepoWithConfigs(t, "main",
		map[string]string{synthetic.Strategy.InTreeConfigPath: "a = https://committed.example.com/repo\n"}, nil)
	testutil.WriteFixture(t, src, twin.InTreeConfigPath, "a = https://uncommitted.example.com/repo\n")

	got, err := DeriveFromGitRefRows(bare, "main", []ecosystem.Strategy{synthetic.Strategy, twin})
	if err != nil {
		t.Fatalf("DeriveFromGitRefRows: unexpected error: %v", err)
	}
	want := []HostPathSet{{
		Host:     "committed.example.com",
		Origin:   "https://committed.example.com",
		Subtrees: []registryvocab.Subtree{{Ecosystem: synthetic.Strategy.Name, Path: "/repo", RegistryName: "a"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveFromGitRefRows = %+v, want only the committed host %+v", got, want)
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
	testutil.WriteFixture(t, src, synthetic.Strategy.InTreeConfigPath, "a = https://other-branch.example.com/repo\n")
	testutil.GitRun(t, src, "add", synthetic.Strategy.InTreeConfigPath)
	testutil.GitRun(t, src, "commit", "-m", "other branch config")

	bare := filepath.Join(t.TempDir(), "accum.git")
	testutil.GitRun(t, "", "init", "--bare", bare)
	testutil.GitRun(t, src, "push", bare, "+refs/heads/main:refs/heads/main", "+refs/heads/other:refs/heads/other")

	got, err := DeriveFromGitRefRows(bare, "main", syntheticRows)
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeriveFromGitRefRows over main = %+v, want no host path sets: the config only exists on other", got)
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
		".synthetic/registries": "a = https://host.example.com/repo\n",
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

	got, err := DeriveFromGitRefRows(bare, "main", syntheticRows)
	if err != nil {
		t.Fatalf("DeriveFromGitRef: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeriveFromGitRef = %+v, want no host path sets", got)
	}
}

// A Strategy that is not in ecosystem.Table is walked end to end: materialized from
// the ref, parsed, and derived, proving the rows parameter reaches both stages.
func TestDeriveFromGitRefRows_WalksSuppliedRowNotTable(t *testing.T) {
	bare, _ := testutil.BareRepoWithConfigs(t, "main", map[string]string{
		"fake.cfg": "x",
	}, nil)
	row := ecosystem.Strategy{
		Name:             "fake",
		InTreeConfigPath: "fake.cfg",
		ConfigParser: func(string) ([]ecosystem.Declaration, bool, error) {
			return []ecosystem.Declaration{{Host: "fake.example.com", UpstreamBaseURL: "https://fake.example.com/repo"}}, true, nil
		},
	}

	got, err := DeriveFromGitRefRows(bare, "main", []ecosystem.Strategy{row})
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
