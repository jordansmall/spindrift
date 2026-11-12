package main

import (
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/inputdoc"
)

// resolveDriver reads DRIVER, the one artifact with an ambient env fallback,
// used only when no document bakes one (ADR 0064).
func resolveDriver() (driver, warning string) {
	return inputdoc.ResolveDriver(docArtifacts().Shared.Driver.String(), os.Getenv("DRIVER"), inputdoc.DriverWarned())
}

// docArtifacts returns launcherInput's artifacts, or the zero value (every key
// absent) when no document is loaded. Artifacts are nix-resolved
// (capability, tracker/forge, agent presence, image and store references), so
// they are read strictly from the document, never from os.Getenv: a stray
// ambient env var must never override what nix baked in (issues #2527, #6611).
func docArtifacts() inputdoc.Artifacts { return launcherInput.Artifacts }

// resolveChoreCatalog reads the CHORE_CATALOG artifact (lib/preambles.nix's
// runArtifacts) for butlerPreflight's prompt-exists check.
func resolveChoreCatalog() choreCatalog {
	raw, ok := docArtifacts().Shared.ChoreCatalog.Lookup()
	if !ok {
		return choreCatalog{}
	}
	return choreCatalog{names: strings.Fields(raw), known: true}
}

// gitConfigLookup resolves a host git config key (e.g. "user.name"), the
// fallback for GIT_USER_NAME/GIT_USER_EMAIL when neither the document, a
// flag, nor env supplies one. It runs in-process so the generated wrapper
// exports no knob env at all (ADR 0020), and it is a package var so tests can
// substitute a fake instead of shelling out.
var gitConfigLookup = func(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// launcherInput is the one inputdoc.Resolve result, set by mainRun after the
// document loads. Its zero value resolves every knob to its schema default,
// which is what a test or direct caller sees before any Resolve.
var launcherInput inputdoc.Resolved

// resolvedKnob reads a schema knob through the flag > document > schema
// default tiers of launcherInput; a knob env var is rejected at startup
// (ADR 0020).
func resolvedKnob(key string) string { return launcherInput.Source().String(key) }

// resolveBoxEnvVar resolves one BOX_ENV_VARS forwarding name from
// launcherInput: a schema knob through its tiers (the wrapper no longer
// pre-populates env with baked defaults, ADR 0020), anything else as a secret
// read live from env, because the --secret-cmd fallback exports secrets after
// Resolve. The host-derived commit identity is not resolved here:
// hostGitConfigResolver answers it from the launcher's startup snapshot, not
// re-read per dispatch, so Box and host commits carry one identity for the
// process's lifetime (issue #5333).
func resolveBoxEnvVar(name string) string {
	return inputdoc.BoxEnv(launcherKnobSource{launcherInput.Source()}, name)
}

// processEnv is os.Environ as a map, the form inputdoc.Resolve takes.
func processEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// ambientKnobEnvError rejects every non-secret schema knob found in the
// process environment (ADR 0020). mainRun runs it ahead of --help, which exits
// before Resolve would repeat the check.
func ambientKnobEnvError() error { return inputdoc.AmbientKnobError(processEnv()) }

// launcherKnobSource layers onto Resolve's tiers the two reads that only
// exist after it: secrets, which the --secret-cmd fallback exports later, and
// the host git config behind GIT_USER_NAME/GIT_USER_EMAIL. The wrapper exports
// no knob env at all (ADR 0020), so that fallback has to run in process; the
// schema row's hostGitConfigKey is the one place the key lives, so the
// launcher's startup snapshot (which the Box env reuses) reads the same keys
// (issues #5329, #5333).
type launcherKnobSource struct{ inputdoc.KnobSource }

func (launcherKnobSource) Secret(env string) string { return os.Getenv(env) }

func (s launcherKnobSource) HostGitConfig(env string) string {
	if v := s.KnobSource.HostGitConfig(env); v != "" {
		return v
	}
	if e, ok := inputdoc.SchemaFlag(env); ok && e.HostGitConfigKey != "" {
		return gitConfigLookup(e.HostGitConfigKey)
	}
	return ""
}

func loadSchemaConfig() inputdoc.Knobs {
	return inputdoc.LoadKnobs(launcherKnobSource{launcherInput.Source()})
}

// backendRowSetting resolves the knob a backend row names as its baseEnv. A
// schema knob resolves through the flag, document and default tiers; a
// Connector's own knob is in no document, so it comes from the environment,
// where its credential lives too. The daemon's backendRowSetting
// (daemon/demand.go) reads the environment first, then the document.
func backendRowSetting(r inputdoc.Resolved, env map[string]string) func(string) string {
	return func(key string) string {
		if _, ok := inputdoc.SchemaFlag(key); ok {
			return r.Source().String(key)
		}
		return env[key]
	}
}
