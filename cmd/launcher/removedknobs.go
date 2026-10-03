package main

import (
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
)

// removedKnobs lists the env knobs lib/env-schema.nix no longer declares and
// whose old setting would otherwise silently do nothing. lib/removed-knobs.nix
// holds the Nix-side counterpart (flake option and mkHarness defaults key).
var removedKnobs = []struct{ env, reason string }{
	{"ORCHESTRATOR_ENABLED", "the orchestrator is now the only Box path (issue #4291), so there is nothing to switch"},
}

// removedKnobsCheck fails when any removed knob is still set, in the
// environment (set-but-empty counts) or a loaded input document's settings.
// It reads both directly rather than through getenvSchema: the knob is no
// longer in the schema, and a silent default would hide the stale setting.
func removedKnobsCheck() doctor.Check {
	return doctor.Check{
		Name:   "removed-knobs",
		Tier:   doctor.Required,
		Remedy: "delete the setting from the Consumer config, env file, or --input document",
		Probe: func() (any, error) {
			var errs []string
			for _, k := range removedKnobs {
				_, inEnv := os.LookupEnv(k.env)
				inDoc := false
				if loadedDoc != nil {
					_, inDoc = loadedDoc.Settings[k.env]
				}
				if inEnv || inDoc {
					errs = append(errs, fmt.Sprintf("%s was removed: %s; delete the setting", k.env, k.reason))
				}
			}
			if len(errs) > 0 {
				return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
			}
			return nil, nil
		},
	}
}
