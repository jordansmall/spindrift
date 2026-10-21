package local

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

func mapLookup(m map[string]string) backendkit.Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestStrategyShape(t *testing.T) {
	if !reflect.DeepEqual(Strategy.Descriptor, backend.Local) {
		t.Errorf("Descriptor = %+v, want backend.Local", Strategy.Descriptor)
	}
	if Strategy.ValidateCodeForge == nil || Strategy.NewIssueTracker == nil ||
		Strategy.NewCodeForge == nil || Strategy.NewLedger == nil || Strategy.HostRepoDir == nil {
		t.Error("local must fill ValidateCodeForge, NewIssueTracker, NewCodeForge, NewLedger, HostRepoDir")
	}
	if Strategy.ValidateTracker != nil || Strategy.NewReadOnlyCodeForge != nil {
		t.Error("local has no tracker validator and no read-only forge")
	}
}

func TestStrategyValidateCodeForgeRequiresImmediate(t *testing.T) {
	err := Strategy.ValidateCodeForge(backendkit.Shared{MergeMode: "manual"}, nil)
	if err == nil || !strings.Contains(err.Error(), "MERGE_MODE=immediate") {
		t.Errorf("manual: err = %v, want MERGE_MODE=immediate complaint", err)
	}
	if err := Strategy.ValidateCodeForge(backendkit.Shared{MergeMode: "immediate"}, nil); err != nil {
		t.Errorf("immediate: err = %v, want nil", err)
	}
}

func TestStrategyLedgerReadsAccumulationRepo(t *testing.T) {
	l := mapLookup(map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "/acc"})
	site, err := Strategy.NewLedger(backendkit.Shared{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if site.Repo != "/acc" || site.URL != "" {
		t.Errorf("site = %+v, want Repo=/acc", site)
	}
}

func TestStrategyHostRepoDirIsAbsolute(t *testing.T) {
	wantRel, err := filepath.Abs("rel/accum.git")
	if err != nil {
		t.Fatal(err)
	}
	wantDefault, err := filepath.Abs(filepath.Join(".spindrift", "accum.git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"relative", map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "rel/accum.git"}, wantRel},
		{"unset defaults under cwd", nil, wantDefault},
		{"absolute passes through", map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "/acc"}, "/acc"},
	} {
		got, err := Strategy.HostRepoDir(backendkit.Shared{}, mapLookup(tc.env))
		if err != nil || got != tc.want {
			t.Errorf("%s: HostRepoDir = %q, %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestStrategyLedgerAbsolutisesAccumulationRepo(t *testing.T) {
	want, err := filepath.Abs("rel/accum.git")
	if err != nil {
		t.Fatal(err)
	}
	l := mapLookup(map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "rel/accum.git"})
	site, err := Strategy.NewLedger(backendkit.Shared{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if site.Repo != want {
		t.Errorf("site.Repo = %q, want %q", site.Repo, want)
	}
}

func TestStrategyCodeForgeAbsolutisesAccumulationRepo(t *testing.T) {
	want, err := filepath.Abs("rel/accum.git")
	if err != nil {
		t.Fatal(err)
	}
	l := mapLookup(map[string]string{"CODE_FORGE_ACCUMULATION_REPO_DIR": "rel/accum.git"})
	cf, err := Strategy.NewCodeForge(backendkit.Shared{}, l, forge.SanitizedParent{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cf.(*localCodeForge).repoPath; got != want {
		t.Errorf("repoPath = %q, want %q", got, want)
	}
}

func TestStrategyCloneURLIsRepoMountDir(t *testing.T) {
	got, err := Strategy.CloneURL(backendkit.Shared{}, nil, "/mnt/repo")
	if err != nil || got != "/mnt/repo" {
		t.Errorf("CloneURL = %q, %v, want /mnt/repo", got, err)
	}
}

func TestStrategyHasNoWebURLOrRemoteMatch(t *testing.T) {
	if Strategy.WebURL != nil || Strategy.RemoteMatch != nil {
		t.Error("local has no web URL and never matches a remote")
	}
}
