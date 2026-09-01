package bindregistry

import "fmt"

// GradleInitScript renders the Gradle init-script content dropped into
// $GRADLE_USER_HOME/init.d/. Gradle auto-loads every .gradle/.gradle.kts file
// there, the home-level equivalent of cargo's $CARGO_HOME/config.toml — so
// unlike the in-tree cargo/npm/pnpm/yarn-berry rewrites, no Target repo build
// file is touched. A JVM-level forward-proxy property
// (-Dhttps.proxyHost/JAVA_TOOL_OPTIONS) cannot stand in: Gradle resolves
// against whatever repositories{} its own scripts declare, not a JVM-wide HTTP
// proxy. The redirect URL carries no path beyond the Forwarder's root, leaning
// on REGISTRY_PROXY_UPSTREAM_URL for the operator registry's base path —
// guessing one (e.g. Maven Central's "/maven2") lands wrong on any upstream
// that doesn't share it.
//
// Two redirect forms, empirically verified against Gradle 8.14.4 with a local
// stand-in Forwarder (no JDK in this repo's own test suite):
//
//   - The one-shot form (spindriftRedirect) clears a repository container and
//     adds ours. Only correct where it installs *after* whatever it competes
//     with has finished declaring repositories.
//   - The persistent form (spindriftPersistentRedirect) adds ours once, then
//     installs a repos.all{} listener that removes any OTHER repository the
//     container gains afterward, forever. RepositoryHandler.all(Action) fires
//     for repositories already present AND for every one added later, so the
//     listener keeps winning rather than losing to whichever declaration runs
//     last — needed because a project's own buildscript{repositories{}} block
//     runs after the one-shot clear-then-add and silently appends the real
//     upstream back in. It also covers a settings script's explicit
//     pluginManagement{repositories{gradlePluginPortal()}}, which resolves
//     synchronously during settings evaluation before any later lifecycle
//     callback: the listener, installed from beforeSettings, is already
//     attached when that call runs.
//
// Call sites:
//
//   - buildscript.repositories, persistent form from a top-level
//     allprojects{}: buildscript classpath resolution completes before
//     gradle.projectsEvaluated fires, so deferring it would silently resolve
//     against the real upstream.
//   - settings.pluginManagement.repositories and
//     settings.buildscript.repositories, persistent form from
//     gradle.beforeSettings — the only hook early enough to win before the
//     settings script body runs. A settings-level plugins{} block resolves
//     *during* settings evaluation, before gradle.settingsEvaluated fires.
//     gradle.beforeSettings and allowInsecureProtocol both need Gradle 6.0+,
//     so both are wrapped in try/catch — unguarded they would throw and kill
//     every build in the Box. On that pre-6.0 fallback, settingsEvaluated
//     still covers pluginManagement.repositories, just later than the settings
//     script's own declarations.
//   - settings.pluginManagement.repositories again, and
//     settings.dependencyResolutionManagement.repositories, one-shot form from
//     gradle.settingsEvaluated. The pluginManagement call is guarded on
//     spindriftPluginManagementManaged so it runs only as the pre-6.0
//     fallback: unguarded on 6.0+, its clear-then-add re-adds an unnamed repo
//     the persistent listener immediately strips, leaving
//     pluginManagement.repositories empty and every plugins{} block
//     unresolvable. dependencyResolutionManagement/repositoriesMode need
//     Gradle 6.8+, hence the try/catch; on that path spindriftSettingsManaged
//     stays false and projectsEvaluated takes over.
//   - each project's own repositories, one-shot form from
//     gradle.projectsEvaluated, once every build script's own repositories{}
//     block has run — earlier forms are silently undone when that script
//     re-declares the real repository. Skipped when settingsEvaluated found
//     Gradle 7+ centralized dependency management
//     (FAIL_ON_PROJECT_REPOS/PREFER_SETTINGS) in force: under
//     FAIL_ON_PROJECT_REPOS this allprojects{} override is itself rejected as
//     a forbidden project-level repository, turning a working build into a
//     hard failure.
//
// Kept a pure string-builder so it's unit-testable without a filesystem;
// driver-exec bind-registry resolves $GRADLE_USER_HOME and writes this to
// init.d/spindrift-registry-proxy.init.gradle.
func GradleInitScript(port int) string {
	return fmt.Sprintf(`def spindriftMavenUrl = "http://127.0.0.1:%d/"
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
`, port)
}
