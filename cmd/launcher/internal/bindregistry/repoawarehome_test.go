package bindregistry

import (
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// A manifest with no usable route prefix leaves the repo-aware Strategies on the
// public registry; the warning names only those Strategies, not every row passed in.
func TestApplyRepoAwareHomeConfigsWarnsWhenManifestHasNoRoutePrefix(t *testing.T) {
	manifests := map[string]registrymanifest.Manifest{
		"no routes":    {},
		"empty prefix": {Routes: []registrymanifest.Route{{Prefix: ""}}},
	}
	for name, manifest := range manifests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("STUB_REPO_AWARE_HOME", home)
			t.Setenv("HOME", home)

			repoAware := ecosystem.Strategy{
				Name:             "repo-aware",
				InTreeConfigPath: ".repo-aware/registries",
				HomeConfig:       &ecosystem.HomeConfig{HomeEnvVar: "STUB_REPO_AWARE_HOME", ConfigPath: "config.toml"},
				RepoAwareHomeConfig: func(int, string, []registrymanifest.Route, string) (string, []ecosystem.EnvExport, []string) {
					return "rendered", nil, nil
				},
			}
			plain := ecosystem.Strategy{
				Name:       "plain",
				HomeConfig: &ecosystem.HomeConfig{HomeEnvVar: "STUB_REPO_AWARE_HOME", ConfigPath: "plain.toml"},
			}
			published := false
			publish := func([]ecosystem.EnvExport) bool { published = true; return true }

			var stdout strings.Builder
			if !applyRepoAwareHomeConfigs(&stdout, t.TempDir(), []ecosystem.Strategy{plain, repoAware}, ReadyGate(manifest), publish) {
				t.Fatalf("applyRepoAwareHomeConfigs = false, want true (stdout=%q)", stdout.String())
			}

			want := "==> WARNING: registry proxy manifest carries no route prefix — repo-aware will fall back to the public registry\n"
			if stdout.String() != want {
				t.Errorf("stdout = %q, want %q", stdout.String(), want)
			}
			if published {
				t.Error("publish was called, want it skipped")
			}
			if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
				t.Errorf("home dir %s = %v (err %v), want no files written", home, entries, err)
			}
		})
	}
}
