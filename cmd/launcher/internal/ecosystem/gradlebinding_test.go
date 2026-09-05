package ecosystem

import (
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// TestGradleInitScript_ExactContent pins the full rendered script for a
// route declaring a gradle-path.
func TestGradleInitScript_ExactContent(t *testing.T) {
	got := GradleInitScript(27182, "r0", gradleTaggedRoutes())
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
		t.Errorf("GradleInitScript(27182) = %q, want %q", got, want)
	}
}

// TestGradleInitScript_PortInterpolated proves only the port digits vary
// across renders, without restating the ~85-line golden a second time (see
// TestGradleInitScript_ExactContent for the full-content check): it derives
// each port's expected output from a known-good render by substituting the
// port substring, so a broken/misplaced/mistyped %d verb would surface as a
// mismatch here even though a defect in the surrounding template (wrong
// redirect, wrong lifecycle hook, ...) is already caught above.
func TestGradleInitScript_PortInterpolated(t *testing.T) {
	base := GradleInitScript(27182, "r0", gradleTaggedRoutes())
	for _, port := range []int{9999, 12345} {
		got := GradleInitScript(port, "r0", gradleTaggedRoutes())
		want := strings.Replace(base, "27182", strconv.Itoa(port), 1)
		if got != want {
			t.Errorf("GradleInitScript(%d, %q) = %q, want %q", port, "r0", got, want)
		}
	}
}

// TestGradleInitScript_PrefixInterpolated mirrors
// TestGradleInitScript_PortInterpolated but varies the route prefix instead
// of the port (issue #3142), proving the prefix -- not just the port --
// lands in the rendered spindriftMavenUrl without restating the full golden
// a third time.
func TestGradleInitScript_PrefixInterpolated(t *testing.T) {
	base := GradleInitScript(27182, "r0", gradleTaggedRoutes())
	for _, prefix := range []string{"artifactory-gradle", "r1"} {
		got := GradleInitScript(27182, prefix, gradleTaggedRoutes())
		want := strings.Replace(base, "127.0.0.1:27182/r0/", "127.0.0.1:27182/"+prefix+"/", 1)
		if got != want {
			t.Errorf("GradleInitScript(27182, %q) = %q, want %q", prefix, got, want)
		}
	}
}

// TestGradleInitScript_NoGradlePathIsInert pins the fallback (issue #3259):
// gradle can never derive a real per-registry path (no InTreeConfigPath,
// registrypathset.Derive never tags "gradle"), so a route with no declared
// gradle-path renders an inert script -- no repository redirection at all --
// rather than a bare-route-root redirect, which would 404 every request
// against the Forwarder's path-set enforcement.
func TestGradleInitScript_NoGradlePathIsInert(t *testing.T) {
	got := GradleInitScript(27182, "r0", []registrymanifest.Route{{Prefix: "r0"}})
	for _, marker := range []string{"allprojects", "gradle.beforeSettings", "gradle.settingsEvaluated", "gradle.projectsEvaluated", "spindriftMavenUrl", "repos.clear", "repos.maven"} {
		if strings.Contains(got, marker) {
			t.Errorf("gradle-path-less GradleInitScript = %q, must not contain %q", got, marker)
		}
	}
	if strings.TrimSpace(got) == "" {
		t.Error("gradle-path-less GradleInitScript is empty, want a minimal valid (but inert) init script")
	}
}

// TestGradleInitScript_GradlePathIsFullRedirect proves
// that a route whose manifest EnforcedPaths carries a
// "gradle"-tagged entry (issue #3259, an operator-declared gradle-path)
// renders the real redirect script -- not the inert fallback -- with
// spindriftMavenUrl carrying the full declared path, not the bare route
// root.
func TestGradleInitScript_GradlePathIsFullRedirect(t *testing.T) {
	got := GradleInitScript(27182, "r0", []registrymanifest.Route{{
		Prefix: "r0",
		EnforcedPaths: []registrymanifest.EcosystemPath{
			{Ecosystem: "npm", Path: "/npm"},
			{Ecosystem: "gradle", Path: "/some/path"},
		},
	}})
	const wantURL = `def spindriftMavenUrl = "http://127.0.0.1:27182/r0/some/path/"`
	if !strings.Contains(got, wantURL) {
		t.Errorf("GradleInitScript with a gradle-tagged EnforcedPaths entry = %q, want it to contain %q", got, wantURL)
	}
	if !strings.Contains(got, "def spindriftPersistentRedirect = { repos ->") {
		t.Error("GradleInitScript with a gradle-tagged EnforcedPaths entry must still contain the real redirect logic (spindriftPersistentRedirect)")
	}
	for _, marker := range []string{"allprojects", "gradle.beforeSettings", "gradle.settingsEvaluated", "gradle.projectsEvaluated", "repos.clear", "repos.maven"} {
		if !strings.Contains(got, marker) {
			t.Errorf("GradleInitScript with a gradle-tagged EnforcedPaths entry must still contain %q", marker)
		}
	}
}

// gradleTaggedRoutes is one route declaring an operator-supplied
// gradle-path, the only shape that renders the real redirect script.
func gradleTaggedRoutes() []registrymanifest.Route {
	return []registrymanifest.Route{{
		Prefix:        "r0",
		EnforcedPaths: []registrymanifest.EcosystemPath{{Ecosystem: "gradle", Path: "/maven"}},
	}}
}
