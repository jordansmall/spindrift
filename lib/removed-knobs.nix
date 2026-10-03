# Knobs deleted from lib/env-schema.nix whose old setting must fail loudly
# instead of vanishing into "option does not exist" or an ignored key. Keyed by
# the schema key mkHarness's `defaults` used. lib/flakeModule.nix declares each
# flakePath and legacySection option as a never-set null and throws if a
# Consumer sets it; lib/mkHarness.nix throws on the key in `defaults`. The
# launcher's own env/settings-document check is the hand-kept table in
# cmd/launcher/removedknobs.go, pinned to `env` by nix/checks/equivalence.nix.
{
  orchestratorEnabled = {
    env = "ORCHESTRATOR_ENABLED";
    # The flake-module option path under perSystem.spindrift, and the legacy
    # settings.<legacySection>.<key> alias the knob had.
    flakePath = [
      "dispatch"
      "orchestrator"
      "enable"
    ];
    legacySection = "promptSkillIteration";
    message = "ORCHESTRATOR_ENABLED was removed: the orchestrator is now the only Box path (issue #4291), so there is nothing to switch; delete the setting";
  };
}
