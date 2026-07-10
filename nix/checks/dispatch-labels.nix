# Lifecycle-label literals in the agent workflows must not silently drift
# from lib/env-schema.nix (issue #460): the workflows are hand-maintained
# YAML with no other guard tying them back to the schema defaults, so a
# schema rename could orphan a `gh issue edit --add-label`/`--remove-label`
# call with nobody noticing until dispatch or recover misbehaves in
# production.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg concatStringsSep filter hasInfix;
  schema = import ../../lib/env-schema.nix;
  workflowsSrc =
    builtins.readFile ../../.github/workflows/agent-dispatch.yml
    + builtins.readFile ../../.github/workflows/agent-recover.yml;
  lifecycleLabels = [
    schema.label.default
    schema.inProgressLabel.default
    schema.completeLabel.default
    schema.failedLabel.default
  ];
  # agent-trigger (fires agent-dispatch.yml) and agent-recover (fires
  # agent-recover.yml) have no lib/env-schema.nix entry of their own — they
  # are dispatch/recover trigger vocabulary, not user-tunable knobs — so they
  # are anchored here, against the two workflows' own
  # `if: github.event.label.name == '...'` guards, instead of a schema
  # default.
  triggerOnlyLabels = [
    "agent-trigger"
    "agent-recover"
  ];
  missing = filter (l: !hasInfix l workflowsSrc) (lifecycleLabels ++ triggerOnlyLabels);
in
{
  dispatch-labels-pinned-in-workflows =
    assert assertMsg (missing == [ ])
      "agent-dispatch.yml/agent-recover.yml missing lifecycle-label literal(s) — schema rename or trigger-vocab rename not propagated to the workflows: ${concatStringsSep ", " missing}";
    pkgs.runCommand "dispatch-labels-pinned-in-workflows" { } "touch $out";
}
