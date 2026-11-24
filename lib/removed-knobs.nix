# Knobs deleted from lib/env-schema.nix whose old setting must fail loudly
# instead of vanishing into "option does not exist" or an ignored key. Keyed by
# the schema key mkHarness's `defaults` used. lib/flakeModule.nix declares each
# flakePath and legacySection option as a never-set null and throws if a
# Consumer sets it; lib/mkHarness.nix throws on the key in `defaults`. A row for
# an env-only knob (a Connector module declared it, with no flake option) omits
# flakePath and legacySection: the launcher's env/input-document check and the
# `defaults` key catch it, and `defaults` was the only Nix-side setting path
# such a knob had. The launcher's own env/settings-document check is the
# hand-kept table in cmd/launcher/removedknobs.go, pinned to `env` by
# nix/checks/equivalence.nix.
{
  orchestratorEnabled = {
    env = "ORCHESTRATOR_ENABLED";
    # The flake-module option path under perSystem.spindrift, and the legacy
    # settings.<legacySection>.<key> alias the knob had. A knob that never had a
    # legacy alias omits legacySection.
    flakePath = [
      "dispatch"
      "orchestrator"
      "enable"
    ];
    legacySection = "promptSkillIteration";
    message = "ORCHESTRATOR_ENABLED was removed: the orchestrator is now the only Box path (issue #4291), so there is nothing to switch; delete the setting";
  };
  boxGhToken = {
    env = "BOX_GH_TOKEN";
    flakePath = [
      "forge"
      "boxGhToken"
    ];
    legacySection = "repository";
    message = "BOX_GH_TOKEN was removed: no Box holds a forge credential (issues #6900 and #6901), so nothing reads it; see MIGRATING.md; delete the setting";
  };
  boxForgejoToken = {
    env = "BOX_FORGEJO_TOKEN";
    flakePath = [
      "forge"
      "boxForgejoToken"
    ];
    legacySection = "repository";
    message = "BOX_FORGEJO_TOKEN was removed: no Box holds a forge credential (issues #6900 and #6901), so nothing reads it; see MIGRATING.md; delete the setting";
  };
  boxForgeCredential = {
    env = "BOX_FORGE_CREDENTIAL";
    flakePath = [
      "forge"
      "boxForgeCredential"
    ];
    legacySection = "repository";
    message = "BOX_FORGE_CREDENTIAL was removed: every Box already holds no forge credential (issue #6900), so there is nothing to opt in or out of; see MIGRATING.md; delete the setting";
  };
  connectorSandbox = {
    env = "CONNECTOR_SANDBOX";
    flakePath = [
      "infra"
      "connector"
      "sandbox"
    ];
    message = "CONNECTOR_SANDBOX was removed: the Connector sandbox is always on (issue #7022), so there is nothing to switch; delete the setting";
  };
  githubConnectorToken = {
    env = "SPINDRIFT_GITHUB_CONNECTOR_TOKEN";
    message = "SPINDRIFT_GITHUB_CONNECTOR_TOKEN was removed: the github Connector takes GH_TOKEN as its credential instead (issue #7499; see MIGRATING.md); delete the setting";
  };
}
