package gradle

import (
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryvocab"
)

// TestInitScript_ExactContent pins the full rendered script for a
// route declaring a gradle path.
func TestInitScript_ExactContent(t *testing.T) {
	got := initScript(27182, "r0", gradleDeclaredRoutes())
	want := `def spindriftMavenUrl = "http://127.0.0.1:27182/r0/maven/"
def spindriftSettingsManaged = false
def spindriftPluginManagementManaged = false

def spindriftConfigureRepo = { repo ->
  repo.url = uri(spindriftMavenUrl)
  try {
    repo.allowInsecureProtocol = true
  } catch (MissingPropertyException ignored) {
    // allowInsecureProtocol requires Gradle 6.0+ -- it exists specifically
    // to add friction to insecure-protocol repositories, and older Gradle
    // never restricted http:// repositories in the first place, so there is
    // nothing to opt into and nothing lost by skipping it here.
  }
}

// One-shot: only correct where this fires after the thing it competes with
// has already finished declaring repositories (see the call sites below).
def spindriftRedirect = { repos ->
  repos.clear()
  repos.maven { spindriftConfigureRepo(it) }
}

// Persistent: installs ours once, then removes any OTHER repository this
// container gains afterward, forever -- see GradleInitScript's own doc
// comment above for why this is needed: a competing declaration (e.g. a
// project's own buildscript{repositories{}} block) can still run after the
// one-shot form's clear-then-add and silently append the real upstream back
// in.
def spindriftPersistentRedirect = { repos ->
  repos.maven { repo ->
    repo.name = 'spindrift'
    spindriftConfigureRepo(repo)
  }
  repos.all { repo ->
    if (repo.name != 'spindrift') {
      repos.remove(repo)
    }
  }
}

allprojects {
  spindriftPersistentRedirect(buildscript.repositories)
}

try {
  gradle.beforeSettings { settings ->
    spindriftPersistentRedirect(settings.pluginManagement.repositories)
    spindriftPersistentRedirect(settings.buildscript.repositories)
    spindriftPluginManagementManaged = true
  }
} catch (MissingMethodException ignored) {
  // gradle.beforeSettings requires Gradle 6.0+ -- on older Gradle this
  // throws at registration time above, so the hook is simply never
  // installed. settingsEvaluated below still covers
  // pluginManagement.repositories, just after the settings script's own
  // declarations run instead of before -- a settings-level plugins{} block,
  // or a settings.buildscript{} block written directly in settings.gradle,
  // can still resolve against the real upstream on pre-6.0 Gradle
  // specifically.
}

gradle.settingsEvaluated { settings ->
  if (!spindriftPluginManagementManaged) {
    spindriftRedirect(settings.pluginManagement.repositories)
  }
  try {
    spindriftRedirect(settings.dependencyResolutionManagement.repositories)
    spindriftSettingsManaged = settings.dependencyResolutionManagement.repositoriesMode.get().name() != "PREFER_PROJECT"
  } catch (Exception ignored) {
    // dependencyResolutionManagement/repositoriesMode require Gradle 6.8+ --
    // on older Gradle this throws (MissingPropertyException/
    // MissingMethodException), so fall back to the per-project
    // projectsEvaluated override below instead of crashing every build.
    spindriftSettingsManaged = false
  }
}

gradle.projectsEvaluated {
  if (!spindriftSettingsManaged) {
    allprojects {
      spindriftRedirect(repositories)
    }
  }
}
`
	if got != want {
		t.Errorf("initScript(27182) = %q, want %q", got, want)
	}
}

// TestInitScript_PrefixInterpolated proves the route prefix (issue
// #3142), not just the port, reaches the rendered spindriftMavenUrl.
func TestInitScript_PrefixInterpolated(t *testing.T) {
	base := initScript(27182, "r0", gradleDeclaredRoutes())
	for _, prefix := range []string{"artifactory-gradle", "r1"} {
		got := initScript(27182, prefix, gradleDeclaredRoutes())
		want := strings.Replace(base, "127.0.0.1:27182/r0/", "127.0.0.1:27182/"+prefix+"/", 1)
		if got != want {
			t.Errorf("initScript(27182, %q) = %q, want %q", prefix, got, want)
		}
	}
}

// TestInitScript_NoDeclaredPathIsInert pins the fallback (issue #3259).
// Gradle has no InTreeConfigPath and registrypathset.Derive never tags
// "gradle", so a route with no declared gradle path renders an inert script
// rather than a bare-route-root redirect, which would 404 against the
// Forwarder's path-set enforcement.
//
// No test feeds an unknown ecosystem's block: Parse refuses one before it
// reaches the renderer, and a real neighbour's block is a corpus case.
func TestInitScript_NoDeclaredPathIsInert(t *testing.T) {
	got := initScript(27182, "r0", []registrymanifest.Route{{Prefix: "r0"}})
	for _, marker := range []string{"allprojects", "gradle.beforeSettings", "gradle.settingsEvaluated", "gradle.projectsEvaluated", "spindriftMavenUrl", "repos.clear", "repos.maven"} {
		if strings.Contains(got, marker) {
			t.Errorf("path-less initScript = %q, must not contain %q", got, marker)
		}
	}
	if strings.Contains(got, ecosystem.GradleRetiredRouteKey) {
		t.Errorf("inert initScript = %q, must not name the retired %q key (ADR 0048)", got, ecosystem.GradleRetiredRouteKey)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("path-less initScript is empty, want a minimal valid (but inert) init script")
	}
}

// gradleDeclaredRoutes is the only route shape that renders the real redirect
// script: an ecosystems block with an operator-supplied gradle path.
func gradleDeclaredRoutes() []registrymanifest.Route {
	return []registrymanifest.Route{{
		Prefix:     "r0",
		Ecosystems: registryvocab.RouteEcosystems{"gradle": registryvocab.RouteDeclaration{"path": "/maven"}},
	}}
}

// Strategy's Render must be initScript itself, not a reimplementation of it.
func TestStrategy_HomeConfig(t *testing.T) {
	hc := Strategy.HomeConfig
	if hc == nil {
		t.Fatal("gradle Strategy has nil HomeConfig")
	}
	if hc.HomeEnvVar != "GRADLE_USER_HOME" {
		t.Errorf("HomeEnvVar = %q, want %q", hc.HomeEnvVar, "GRADLE_USER_HOME")
	}
	if hc.HomeRelativeDefault != ".gradle" {
		t.Errorf("HomeRelativeDefault = %q, want %q", hc.HomeRelativeDefault, ".gradle")
	}
	if hc.ConfigPath != "init.d/spindrift-registry-proxy.init.gradle" {
		t.Errorf("ConfigPath = %q, want %q", hc.ConfigPath, "init.d/spindrift-registry-proxy.init.gradle")
	}
	if hc.Render == nil {
		t.Fatal("gradle Strategy HomeConfig has nil Render")
	}
	got := hc.Render(27182, "r0", nil)
	want := initScript(27182, "r0", nil)
	if got != want {
		t.Errorf("Render(27182, %q) = %q, want %q", "r0", got, want)
	}
}

// Gradle binds only through its home-level init script, reads no in-tree
// config, and has nothing a repo-aware re-render or env export could add.
func TestStrategy_Shape(t *testing.T) {
	s := Strategy
	if s.Name != "gradle" || s.Classification != "gradle" {
		t.Errorf("Name, Classification = %q, %q, want gradle, gradle", s.Name, s.Classification)
	}
	if s.EnvExports != nil || s.BindingEnvVar != "" || s.RepoAwareHomeConfig != nil || s.InTreeConfigPath != "" {
		t.Errorf("gradle binds only via HomeConfig, got EnvExports=%v BindingEnvVar=%q RepoAwareHomeConfig=%v InTreeConfigPath=%q",
			s.EnvExports != nil, s.BindingEnvVar, s.RepoAwareHomeConfig != nil, s.InTreeConfigPath)
	}
	if want := []string{"netrc", "npmrc", "gradle-properties"}; !slices.Equal(s.CredentialStores, want) {
		t.Errorf("CredentialStores = %v, want %v", s.CredentialStores, want)
	}
	if s.RetiredRouteKey != "gradle-path" {
		t.Errorf("RetiredRouteKey = %q, want gradle-path", s.RetiredRouteKey)
	}
	wantLockfiles := []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradle.lockfile"}
	if !slices.Equal(s.LockfileNames, wantLockfiles) {
		t.Errorf("LockfileNames = %v, want %v", s.LockfileNames, wantLockfiles)
	}
}

// registryroutes resolves the retired gradle-path key through the registry.
func TestStrategy_ResolvesByRetiredRouteKey(t *testing.T) {
	row, ok := ecosystem.RowByRetiredRouteKey("gradle-path", []ecosystem.Strategy{Strategy})
	if !ok || row.Name != "gradle" {
		t.Errorf(`RowByRetiredRouteKey("gradle-path") = (%q, %v), want (gradle, true)`, row.Name, ok)
	}
}
