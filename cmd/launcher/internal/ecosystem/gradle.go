package ecosystem

import (
	"fmt"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// gradleRow is the gradle ecosystem's Table entry. Its binding lands
// entirely through HomeConfig.Render (wired to GradleInitScript): Gradle
// auto-loads the rendered init-script rather than reading an env var, so
// this row needs neither EnvExports nor BindingEnvVar.
var gradleRow = Row{
	Name: "gradle",
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

// GradleInitScript renders the Gradle init-script content dropped into
// $GRADLE_USER_HOME/init.d/, mirroring the heredoc from the deleted
// entrypoint.sh phase_gradle_binding (see git history) verbatim. Gradle
// auto-loads every .gradle/.gradle.kts file under init.d/, the home-level
// equivalent of cargo's $CARGO_HOME/config.toml (see CargoConfigTOML) -- so
// unlike the in-tree cargo/npm/pnpm/yarn-berry config rewrites, no rewrite
// of a Target repo's own build files is needed here. A JVM-level
// forward-proxy property (-Dhttps.proxyHost/JAVA_TOOL_OPTIONS) cannot stand
// in for this: Gradle resolves dependencies against whatever repositories{}
// its own build/settings scripts declare, not through a JVM-wide HTTP
// proxy. The redirect URL carries no path beyond the Forwarder's own root --
// it leans on the route's own upstream-base-url (ADR 0045) already carrying
// whatever base path the operator's registry needs; guessing a path shape
// here (e.g. Maven Central's own "/maven2") would land at the wrong path on
// any upstream that doesn't happen to share it, and Table's own gradle row
// already documents artifact-base paths as registry-specific, not
// derivable.
//
// Two redirect forms, and three call sites for the persistent one, all
// empirically verified against a real Gradle 8.14.4 (a local stand-in HTTP
// server standing in for the Forwarder; not exercised by this repo's own
// toolchain or test suite, which has no JDK/gradle dependency) -- covering
// both an explicit `maven { url ... }` block and the bare
// `mavenCentral()`/`gradlePluginPortal()` shorthand a real build/settings
// script is more likely to use:
//
//   - The one-shot form (spindriftRedirect below) clears whatever a
//     repository container already holds and adds ours. Only correct where
//     the override installs *after* the thing it competes with has already
//     finished declaring repositories, so nothing can still append behind
//     it once the override runs.
//   - The persistent form (spindriftPersistentRedirect below) adds ours
//     once, then installs a repos.all{} listener that removes any OTHER
//     repository the container gains afterward, forever.
//     RepositoryHandler.all(Action) fires its action immediately for every
//     repository already present AND again for every repository added
//     later, so the listener keeps winning instead of losing to whatever
//     declaration runs last. This matters because a project's own
//     buildscript{repositories{mavenCentral()}} block, or a
//     settings.gradle's own buildscript{} block, runs *after* the one-shot
//     form's clear-then-add and silently appends the real upstream back in,
//     so buildscript classpath resolution falls through to it on any
//     Forwarder 404.
//     This also closes the one case an earlier form of this reasoning could
//     not: a settings script's own explicit
//     pluginManagement{repositories{gradlePluginPortal()}} block resolves
//     its plugins{} synchronously during settings-script evaluation, before
//     any later lifecycle callback fires -- but the listener, once
//     installed from beforeSettings, is already attached to the container
//     by the time that block's own gradlePluginPortal() call runs, and
//     removes it on the spot.
//
// Call sites:
//
//   - buildscript.repositories, given the persistent form from a plain
//     top-level allprojects{} (runs immediately as each project is
//     configured, before that project's own build script body runs):
//     buildscript classpath resolution completes before
//     gradle.projectsEvaluated ever fires, so deferring this one like the
//     settings/project repositories below would leave it silently
//     resolving against the real upstream.
//   - settings.pluginManagement.repositories and
//     settings.buildscript.repositories, given the persistent form from
//     gradle.beforeSettings -- the only hook early enough to win before the
//     settings script body itself runs. A settings-level plugins{} block
//     (e.g. the "org.gradle.toolchains.foojay-resolver-convention" id
//     `gradle init`-generated settings.gradle.kts ships by default)
//     resolves its plugins *during* settings evaluation, using whatever
//     pluginManagement.repositories are declared at that point -- this
//     completes before gradle.settingsEvaluated ever fires.
//     settings.buildscript{} is likewise only reachable this early.
//     gradle.beforeSettings and allowInsecureProtocol both require Gradle
//     6.0+; on older Gradle the unguarded forms would throw and kill every
//     build in the Box, so both are wrapped in try/catch. When the
//     beforeSettings catch triggers, settingsEvaluated below still covers
//     pluginManagement.repositories, just after the settings script's own
//     declarations run instead of before -- a settings-level plugins{}
//     block, or a settings.buildscript{} block, can still resolve against
//     the real upstream on pre-6.0 Gradle specifically. When the
//     allowInsecureProtocol catch triggers there is nothing lost: it exists
//     to add friction to insecure-protocol repositories, and older Gradle
//     never restricted http:// repositories in the first place.
//   - settings.pluginManagement.repositories again, and
//     settings.dependencyResolutionManagement.repositories, both given the
//     one-shot form from gradle.settingsEvaluated. The
//     pluginManagement.repositories call is guarded on
//     spindriftPluginManagementManaged (set true only once beforeSettings'
//     persistent redirects have installed) and runs *only* when that guard
//     is false -- i.e. only as the Gradle <6.0 fallback, where the
//     beforeSettings try/catch never installed the listener at all.
//     Without the guard this one-shot form would fire on Gradle 6.0+ too,
//     on the very container the persistent listener is already attached
//     to: clear-then-add re-adds an unnamed repo the listener immediately
//     strips (its name never became 'spindrift'), leaving
//     pluginManagement.repositories empty and every plugins{} block
//     unresolvable. dependencyResolutionManagement/repositoriesMode
//     require Gradle 6.8+; wrapped in try/catch since a Target repo
//     pinning an older Gradle would otherwise throw here and kill every
//     build in the Box -- on that fallback path, spindriftSettingsManaged
//     stays false and the per-project projectsEvaluated override below
//     takes over instead. pluginManagement.repositories needs no such
//     guard, it's much older Gradle API.
//   - each project's own repositories, given the one-shot form from
//     gradle.projectsEvaluated once every project's own build script (and
//     its own repositories{} block) has already run -- the naive top-level
//     allprojects{}/gradle.beforeSettings forms run too early here and are
//     silently undone once that script re-declares the real repository.
//     Only applied when settingsEvaluated found the project is not already
//     using Gradle 7+ centralized dependency management
//     (RepositoriesMode.FAIL_ON_PROJECT_REPOS/PREFER_SETTINGS), or never
//     reached that determination at all (the pre-6.8 try/catch fallback
//     above): under FAIL_ON_PROJECT_REPOS this same allprojects{} override
//     is itself rejected as a forbidden project-level repository, turning
//     a working build into a hard failure.
//
// This function stays a pure string-builder (see CargoConfigTOML for the
// same rationale) so it's unit-testable without touching a filesystem;
// driver-exec bind-registry's bindings mode resolves $GRADLE_USER_HOME and
// writes this content to init.d/spindrift-registry-proxy.init.gradle. prefix
// is the manifest route this script binds to -- see runBindRegistryBindings
// in cmd/launcher/driver-exec/bindregistry_cmd.go for why it's always the
// first manifest route's prefix.
//
// routes is the manifest's full route list (issue #3259). gradle has no
// InTreeConfigPath in ecosystem.Table (Maven/Gradle repo layout lives in
// build.gradle/settings.gradle, not a stable-format config file -- see
// Table's own gradle row comment), so registrypathset.Derive never tags
// anything "gradle" on its own -- unlike npm/yarn/pnpm/cargo, gradle has no
// in-tree declaration to discover a path from. But an operator can declare
// one directly in the routes file (gradle-path, ADR 0045/issue #3259),
// which reaches here tagged "gradle" in routes[0].EnforcedPaths the same
// way a discovered npm/yarn/pnpm path would be. When such an entry is
// found, this renders the redirect script with spindriftMavenUrl carrying
// the full declared path. When none is found, this renders an inert script -- no
// repository interception at all -- mirroring the "absence of declaration =
// absence of binding" fallback AC3 already normalizes for npm/yarn/pnpm's
// own missing-config case: gradle's build falls through to whatever
// repositories it declares itself, unreachable from the network-less Box,
// exactly like an npm project with no committed .npmrc.
func GradleInitScript(port int, prefix string, routes []registrymanifest.Route) string {
	route := firstRoute(routes)
	// gradle-path is a single operator-declared string, not a discovery
	// scan that could produce duplicates (unlike npm's 0/1/>1
	// EnforcedPaths case in NpmFamilyBindings) -- at most one
	// "gradle"-tagged entry can ever appear here, so no ambiguity handling
	// is needed.
	for _, p := range route.EnforcedPaths {
		if p.Ecosystem == "gradle" {
			return gradleRedirectScript(fmt.Sprintf("http://127.0.0.1:%d/%s%s/", port, prefix, p.Path))
		}
	}
	return "// spindrift: gradle has no discoverable per-registry path to redirect\n" +
		"// onto (no in-tree config file to derive one from, and no gradle-path\n" +
		"// declared in the routes file) -- this init script intentionally\n" +
		"// installs no repository redirection, so the build falls through to\n" +
		"// whatever repositories it declares itself.\n"
}

// gradleRedirectScript renders the real repository-redirection init script
// (see GradleInitScript's own doc comment above for the full call-site and
// Gradle-version-compatibility reasoning), pointing every intercepted
// repository at mavenURL -- a route's full declared gradle-path is the only
// value GradleInitScript ever passes here.
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
