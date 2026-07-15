package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// inputDocument is the Go mirror of the nix-rendered Launcher input document
// (ADR 0020): settings holds resolved knob values (schema default < flake
// settings), keyed by env var name; artifacts holds the nix-computed
// plumbing values (image refs, agent files, driver name, ...) that
// goRunPreamble/goBuildPreamble used to export as env before this issue.
type inputDocument struct {
	Settings  map[string]string `json:"settings"`
	Artifacts map[string]string `json:"artifacts"`
}

// loadedDoc is populated once by loadInputDocument, before loadConfig() runs,
// from the --input flag's document path. Left nil for a direct binary
// invocation with no --input flag (tests, manual debugging), in which case
// every lookup falls through to os.Getenv/schemaFlags as before this issue.
var loadedDoc *inputDocument

// loadInputDocument reads and parses the Launcher input document at path.
func loadInputDocument(path string) (*inputDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input document %s: %w", path, err)
	}
	var doc inputDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse input document %s: %w", path, err)
	}
	return &doc, nil
}

// getenvArtifact reads key from the environment (an escape hatch for manual
// override and tests that predate --input), falling back to the loaded input
// document's artifacts section, then def. Artifacts are nix-computed
// plumbing (image refs, agent files, driver name, ...), never operator
// knobs, so unlike getenvSchema an ambient artifact env var draws no
// deprecation warning.
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

// gitConfigLookup resolves a host git config key (e.g. "user.name"), the
// fallback for GIT_USER_NAME/GIT_USER_EMAIL when neither the document, a
// flag, nor env supplies one. Previously a bash line in the generated
// wrapper (`GIT_USER_NAME="${GIT_USER_NAME:-$(git config ...)}"`); moved
// in-process so the wrapper exports no knob env at all (ADR 0020). A
// package var so tests can substitute a fake instead of shelling out.
var gitConfigLookup = func(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveBoxEnvVar resolves one BOX_ENV_VARS forwarding name (dispatch.Config
// ResolveEnv): ambient env first (an explicit flag also lands here via
// parseFlags's os.Setenv), then the loaded document's settings or artifacts
// section, then the knob's schema default. A boxEnv knob the operator never
// sets anywhere (the common case for e.g. MODEL) still reaches the Box with
// its document-resolved value instead of an empty string (ADR 0020: the
// wrapper no longer pre-populates env with baked defaults). Secret names
// (GH_TOKEN, etc.) have no document/schema entry, so this reduces to plain
// os.Getenv for them, unchanged from before #625.
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

// warnAmbientKnobEnv prints one deprecation warning per non-secret schema
// knob found in the environment, naming the variable, its value, and its
// flag/settings equivalent (ADR 0020 staging: warn this release, error the
// next). Must run before parseFlags mutates the environment via os.Setenv,
// so a flag-set value is never mistaken for an ambient one — an operator
// passing both --base-branch and an ambient BASE_BRANCH still gets warned
// about the ambient variable, even though the flag wins the resolved value.
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
