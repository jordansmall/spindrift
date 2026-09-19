package ecosystem

import (
	"fmt"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// GradleInitScript compares against this const rather than gradleRow.Name to
// avoid a package initialization cycle.
const nameGradle = "gradle"

// GradleRetiredRouteKey is gradle's Row.RetiredRouteKey, exported because
// registryroutes never spells the key itself.
const GradleRetiredRouteKey = "gradle-path"

// gradleRow binds entirely through HomeConfig.Render: Gradle auto-loads the
// rendered init-script rather than reading an env var, so this row needs
// neither EnvExports nor BindingEnvVar.
var gradleRow = Row{
	Name:            nameGradle,
	RetiredRouteKey: GradleRetiredRouteKey,
	LockfileNames: []string{
		"build.gradle",
		"build.gradle.kts",
		"settings.gradle",
		"settings.gradle.kts",
		"gradle.lockfile",
	},
	Classification: "gradle",
	HomeConfig: &HomeConfig{
		HomeEnvVar:          "GRADLE_USER_HOME",
		HomeRelativeDefault: ".gradle",
		ConfigPath:          "init.d/spindrift-registry-proxy.init.gradle",
		Render:              GradleInitScript,
	},
}

// GradleInitScript renders the Gradle init-script dropped into
// $GRADLE_USER_HOME/init.d/. A JVM-wide proxy property cannot replace it:
// Gradle resolves against whatever repositories{} its own scripts declare.
// The path comes from routes[0]'s declared gradle-path (ADR 0045, #3259,
// #3404); with none declared, this renders an inert script.
func GradleInitScript(port int, prefix string, routes []registrymanifest.Route) string {
	if path := declaredPath(routes, nameGradle); path != "" {
		return gradleRedirectScript(fmt.Sprintf("http://127.0.0.1:%d/%s%s/", port, prefix, path))
	}
	return "// spindrift: gradle has no discoverable per-registry path to redirect\n" +
		"// onto (no in-tree config file to derive one from, and no gradle-path\n" +
		"// declared in the routes file) -- this init script intentionally\n" +
		"// installs no repository redirection, so the build falls through to\n" +
		"// whatever repositories it declares itself.\n"
}

// gradleRedirectScript points every intercepted repository at mavenURL,
// verified against Gradle 8.14.4 (this repo's tests have no JDK). Both
// !spindrift*Managed guards in the script are load-bearing: dropping the
// first empties pluginManagement.repositories on Gradle 6.0+; dropping the
// second makes builds using FAIL_ON_PROJECT_REPOS fail.
func gradleRedirectScript(mavenURL string) string {
	return fmt.Sprintf(`def spindriftMavenUrl = %q
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
`, mavenURL)
}
