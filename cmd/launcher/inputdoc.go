package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/inputdoc"
)

// loadedDoc is populated once by inputdoc.Load, before loadConfig runs. It
// stays nil when the binary runs without --input (tests, manual debugging), and
// every artifact then reads as empty (DRIVER alone falls back to the env, see
// resolveDriver).
var loadedDoc *inputdoc.Document

// resolveDriver reads DRIVER, the one artifact with an ambient env fallback,
// used only when no document bakes one (ADR 0064).
func resolveDriver() (driver, warning string) {
	return inputdoc.ResolveDriver(docArtifacts().Shared.Driver.String(), os.Getenv("DRIVER"), inputdoc.DriverWarned())
}

// docArtifacts returns the loaded document's artifacts, or the zero value
// (every key absent) when no document is loaded. Artifacts are nix-resolved
// (capability, tracker/forge, agent presence, image and store references), so
// they are read strictly from the document, never from os.Getenv: a stray
// ambient env var must never override what nix baked in (issues #2527, #6611).
func docArtifacts() inputdoc.Artifacts {
	if loadedDoc == nil {
		return inputdoc.Artifacts{}
	}
	return loadedDoc.Artifacts
}

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

// resolveBoxEnvVar resolves one BOX_ENV_VARS forwarding name: ambient env
// first (parseFlags lands an explicit flag there via os.Setenv), then the
// document's settings, then the schema default, because the
// wrapper no longer pre-populates env with baked defaults (ADR 0020). The
// host-derived commit identity is not resolved here: hostGitConfigResolver
// answers it from the launcher's startup snapshot, not re-read per dispatch,
// so Box and host commits carry one identity for the process's lifetime
// (issue #5333).
// Secrets such as GH_TOKEN have no entry, so this reduces to os.Getenv (#625).
// An empty value wins over the document and schema default only per
// ambientSetting, like getenvSchema.
func resolveBoxEnvVar(name string) string {
	if v, ok := ambientSetting(name); ok {
		return v
	}
	if loadedDoc != nil {
		if v, ok := loadedDoc.Settings[name]; ok && v != "" {
			return v
		}
	}
	return schemaDefault(name)
}

// warnAmbientKnobEnv prints one deprecation warning per non-secret schema knob
// found in the environment (ADR 0020 staging: warn this release, error the
// next). It must run before parseFlags mutates the environment via os.Setenv,
// or a flag-set value looks like an ambient one.
func warnAmbientKnobEnv(w io.Writer) {
	for _, e := range schemaFlags {
		v, ok := ambientSetting(e.env)
		if !ok {
			continue
		}
		// A launcherIgnores knob's flag is inert (see flagEntry), so its
		// settings path is the only real migration target;
		// nix/checks/schema-drift.nix pins flakeOption = true on every such
		// knob, which lib/renderers.nix turns into a settings path (#3698).
		equiv := e.settingsPath
		if !e.launcherIgnores {
			equiv = "--" + e.flag
			if e.settingsPath != "" {
				equiv += " or " + e.settingsPath
			}
		}
		if v == "" {
			v = `""`
		}
		fmt.Fprintf(w, "%s=%s set in environment — knob env overrides are deprecated; use %s\n", e.env, v, equiv)
	}
}
