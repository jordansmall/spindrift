package inputdoc

import (
	"errors"
	"fmt"
	"strings"
)

// ErrConfigInvalid marks an error the launcher reports with exitcode.ConfigInvalid.
var ErrConfigInvalid = errors.New("config-invalid")

// configInvalidError keeps its own message while matching ErrConfigInvalid.
type configInvalidError struct{ msg string }

func (e configInvalidError) Error() string { return e.msg }
func (e configInvalidError) Unwrap() error { return ErrConfigInvalid }

// AmbientKnobError rejects every non-secret schema knob present in env (ADR
// 0020: the knob env var is gone, the document and flags carry the value).
// Secrets and non-knob plumbing are not SchemaFlags rows, so they pass.
// The error matches ErrConfigInvalid.
func AmbientKnobError(env map[string]string) error {
	var lines []string
	for _, e := range SchemaFlags {
		v, ok := env[e.Env]
		if !ok || (v == "" && !e.EmptyIsSetting()) {
			continue
		}
		// A launcherIgnores knob's flag is inert (see FlagEntry), so its
		// settings path is the only real migration target;
		// nix/checks/schema-drift.nix pins flakeOption = true on every such
		// knob, which lib/renderers.nix turns into a settings path (#3698).
		equiv := e.SettingsPath
		if !e.LauncherIgnores {
			equiv = "--" + e.Flag
			if e.SettingsPath != "" {
				equiv += " or " + e.SettingsPath
			}
		}
		if v == "" {
			v = `""`
		}
		lines = append(lines, fmt.Sprintf("%s=%s set in environment — knob env vars are not read; use %s", e.Env, v, equiv))
	}
	if len(lines) == 0 {
		return nil
	}
	return configInvalidError{strings.Join(lines, "\n")}
}
