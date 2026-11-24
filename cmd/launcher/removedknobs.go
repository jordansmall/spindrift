package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/inputdoc"
)

// removedKnobs lists the env knobs lib/env-schema.nix no longer declares and
// whose old setting would otherwise silently do nothing. lib/removed-knobs.nix
// holds the Nix-side counterpart (a flake option, unless the knob was env-only,
// and the mkHarness defaults key).
var removedKnobs = []struct{ env, reason string }{
	{"ORCHESTRATOR_ENABLED", "the orchestrator is now the only Box path (issue #4291), so there is nothing to switch"},
	{"BOX_GH_TOKEN", "no Box holds a forge credential (issues #6900 and #6901), so nothing reads it; see MIGRATING.md"},
	{"BOX_FORGEJO_TOKEN", "no Box holds a forge credential (issues #6900 and #6901), so nothing reads it; see MIGRATING.md"},
	{"BOX_FORGE_CREDENTIAL", "every Box already holds no forge credential (issue #6900), so there is nothing to opt in or out of; see MIGRATING.md"},
	{"CONNECTOR_SANDBOX", "the Connector sandbox is always on (issue #7022), so there is nothing to switch"},
	{"SPINDRIFT_GITHUB_CONNECTOR_TOKEN", "the github Connector takes GH_TOKEN as its credential instead (issue #7499; see MIGRATING.md)"},
	{"SPINDRIFT_JIRA_CONNECTOR_TOKEN", "the jira Connector authorizes with JIRA_TOKEN instead (issue #7501); see MIGRATING.md"},
	{"SPINDRIFT_JIRA_CONNECTOR_TOKEN_CMD", "the jira Connector authorizes with JIRA_TOKEN_CMD instead (issue #7501); see MIGRATING.md"},
	{"SPINDRIFT_JIRA_CONNECTOR_EMAIL", "the jira Connector takes its Basic identity from JIRA_EMAIL instead (issue #7501); see MIGRATING.md"},
	{"SPINDRIFT_FORGEJO_CONNECTOR_TOKEN", "FORGEJO_TOKEN now authorizes forgejo Connector requests and git alike (issue #7500); set FORGEJO_TOKEN instead; see MIGRATING.md"},
	{"SPINDRIFT_FORGEJO_CONNECTOR_TOKEN_CMD", "FORGEJO_TOKEN_CMD now supplies forgejo's one credential (issue #7500); set FORGEJO_TOKEN_CMD instead; see MIGRATING.md"},
}

// removedKnobsCheckName names the removed-knobs row, so validateConfigChecks
// can tell whether its caller's checks already cover removed choices.
const removedKnobsCheckName = "removed-knobs"

// removedKnobsCheck fails when any removed knob or removed choice is still
// set, in the environment (set-but-empty counts, for a knob) or a loaded input
// document's settings.
// It reads both directly rather than through resolvedKnob: the knob is no
// longer in the schema, and a silent default would hide the stale setting.
func removedKnobsCheck() doctor.Check {
	return doctor.Check{
		Name:   removedKnobsCheckName,
		Tier:   doctor.Required,
		Remedy: "delete the setting from the Consumer config, env file, or --input document",
		Probe: func() (any, error) {
			var errs []string
			for _, k := range removedKnobs {
				_, inEnv := os.LookupEnv(k.env)
				_, inDoc := launcherInput.Setting(k.env)
				if inEnv || inDoc {
					errs = append(errs, fmt.Sprintf("%s was removed: %s; delete the setting", k.env, k.reason))
				}
			}
			// A removed choice is value-level: the knob stays declared, so only
			// the removed value is a stale setting. A flag is caught by the
			// choice walk, which skips only what this row already reported.
			for _, f := range inputdoc.SchemaFlags {
				for _, value := range slices.Sorted(maps.Keys(f.RemovedChoices)) {
					if sourceHolds(f.Env, value) {
						errs = append(errs, f.RemovedChoices[value])
					}
				}
			}
			if len(errs) > 0 {
				return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
			}
			return nil, nil
		},
	}
}

// setsRemovedChoice reports whether value is one of env's removed choices and
// the environment or a loaded input document's settings holds it: the sources
// the removed-knobs row reads, and so the removed choices it already reports.
func setsRemovedChoice(env, value string) bool {
	if _, removed := inputdoc.RemovedChoice(env, value); !removed {
		return false
	}
	return sourceHolds(env, value)
}

// sourceHolds reports whether the environment or a loaded input document's
// settings sets env to value.
func sourceHolds(env, value string) bool {
	envValue, inEnv := os.LookupEnv(env)
	docValue, inDoc := launcherInput.Setting(env)
	return (inEnv && envValue == value) || (inDoc && docValue == value)
}
