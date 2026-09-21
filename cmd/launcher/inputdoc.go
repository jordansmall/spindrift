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
// every lookup then falls through to os.Getenv or schemaFlags.
var loadedDoc *inputdoc.Document

// getenvArtifact reads key from the environment, then the loaded document's
// artifacts section, then def. Artifacts are nix-computed plumbing, never
// operator knobs, so an ambient artifact env var draws no deprecation warning.
func getenvArtifact(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if loadedDoc != nil {
		if v, ok := loadedDoc.Artifacts[key]; ok && v != "" {
			return v
		}
	}
	return def
}

// docArtifact reads key strictly from the loaded document's Artifacts, never
// from os.Getenv. The keys it backs are nix-resolved policy signals
// (capability, tracker/forge, agent presence), so a stray ambient env var must
// never override what nix baked into the document (issue #2527). Its only
// callers are the matching-document trust branches, not dispatchConfig (#2533).
func docArtifact(key string) string {
	if loadedDoc == nil {
		return ""
	}
	return loadedDoc.Artifacts[key]
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
// document's settings or artifacts, then the schema default, because the
// wrapper no longer pre-populates env with baked defaults (ADR 0020).
// Secrets such as GH_TOKEN have no entry, so this reduces to os.Getenv (#625).
func resolveBoxEnvVar(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if loadedDoc != nil {
		if v, ok := loadedDoc.Settings[name]; ok && v != "" {
			return v
		}
		if v, ok := loadedDoc.Artifacts[name]; ok && v != "" {
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
		v := os.Getenv(e.env)
		if v == "" {
			continue
		}
		equiv := "--" + e.flag
		if e.settingsPath != "" {
			equiv += " or " + e.settingsPath
		}
		fmt.Fprintf(w, "%s=%s set in environment — knob env overrides are deprecated; use %s\n", e.env, v, equiv)
	}
}
