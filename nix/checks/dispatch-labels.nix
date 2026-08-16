# Label literals in the agent workflows must not silently drift from
# lib/labels.nix (issue #460, extended by issue #2528): the workflows are
# hand-maintained YAML with no other guard tying them back to the label
# registry, so a rename in the registry (or in schema.*.default, which
# lib/labels.nix's work rows source from) could orphan a `gh issue edit
# --add-label`/`--remove-label` call with nobody noticing until dispatch,
# recover, or research misbehaves in production.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    filter
    hasInfix
    mapAttrs
    replaceStrings
    ;
  labels = import ../../lib/labels.nix;
  lifecycleLabels = map (r: r.name) labels.work;
  # agent-trigger (fires agent-dispatch.yml) and agent-recover (fires
  # agent-recover.yml) have no lib/env-schema.nix entry of their own — they
  # are dispatch/recover trigger vocabulary, not user-tunable knobs — so they
  # are anchored here, against the two workflows' own
  # `if: github.event.label.name == '...'` guards, instead of a schema
  # default. Sourced from lib/labels.nix's triggerOnly list, the one root for
  # this vocabulary.
  triggerOnlyLabels = labels.triggerOnly;
  # Research-tier standing/in-progress/failed names plus the three
  # verdict-terminal names (issue #2528 AC1): agent-research.yml is the one
  # workflow that reads and writes this vocabulary, so it must carry every
  # literal too.
  researchLabels = map (r: r.name) (labels.research ++ labels.researchVerdicts);
  requiredLabels = lifecycleLabels ++ triggerOnlyLabels ++ researchLabels;
  # Both control-plane template sets — GitHub (.github/workflows) and the
  # Forgejo Actions mirror (.forgejo/workflows, issue #1967) — must anchor the
  # same lifecycle + trigger + research vocabulary. Each set is checked
  # independently so a label present only in the github set cannot mask its
  # absence from the forgejo set.
  workflowSets = {
    github =
      builtins.readFile ../../.github/workflows/agent-dispatch.yml
      + builtins.readFile ../../.github/workflows/agent-recover.yml
      + builtins.readFile ../../.github/workflows/agent-research.yml;
    forgejo =
      builtins.readFile ../../.forgejo/workflows/agent-dispatch.yml
      + builtins.readFile ../../.forgejo/workflows/agent-recover.yml
      + builtins.readFile ../../.forgejo/workflows/agent-research.yml;
  };
  # Asserts every entry in requiredLabels appears as a literal substring
  # somewhere in each workflowSets value, else throws naming the offending
  # set(s) and their missing label(s). Factored out (mirroring
  # nix/checks/schema-drift.nix's assertDogfoodDocModelsOk idiom) so
  # dispatch-labels-pinned-in-workflows-regression can exercise this exact
  # assertion path against a synthetic, doctored workflowSets without
  # touching the real workflow files.
  assertLabelsPinned =
    { requiredLabels, workflowSets }:
    let
      missingBySet = mapAttrs (_: src: filter (l: !hasInfix l src) requiredLabels) workflowSets;
      offenders = filter (name: missingBySet.${name} != [ ]) (builtins.attrNames missingBySet);
    in
    assert assertMsg (offenders == [ ])
      "agent-dispatch.yml/agent-recover.yml/agent-research.yml missing label literal(s) — schema rename, trigger-vocab rename, or research-label rename not propagated to the workflows: ${
        concatStringsSep "; " (
          map (n: "${n}: ${concatStringsSep ", " missingBySet.${n}}") offenders
        )
      }";
    offenders;
in
{
  dispatch-labels-pinned-in-workflows =
    assert (assertLabelsPinned { inherit requiredLabels workflowSets; }) == [ ];
    pkgs.runCommand "dispatch-labels-pinned-in-workflows" { } "touch $out";

  # Regression guard (issue #2528 AC3): the parity assertion above must
  # actually detect a research-label rename, not just pass vacuously because
  # the real workflows currently agree with lib/labels.nix. Doctors a copy of
  # workflowSets.github with agent-research-recommend's literal swapped for a
  # plausible drifted rename (as if someone renamed the label in the workflow
  # YAML without updating lib/labels.nix), then runs assertLabelsPinned — the
  # exact function dispatch-labels-pinned-in-workflows calls — against that
  # doctored set via tryEval, so this fails if the hasInfix/assertMsg check is
  # ever dropped from assertLabelsPinned.
  dispatch-labels-pinned-in-workflows-regression =
    let
      driftedGithub = replaceStrings [ "agent-research-recommend" ] [ "agent-research-approved" ]
        workflowSets.github;
      doctoredWorkflowSets = workflowSets // {
        github = driftedGithub;
      };
      result = builtins.tryEval (
        assertLabelsPinned {
          inherit requiredLabels;
          workflowSets = doctoredWorkflowSets;
        }
      );
    in
    assert assertMsg (!result.success)
      "dispatch-labels-pinned-in-workflows-regression: expected assertLabelsPinned to reject a synthetic workflowSets.github with agent-research-recommend renamed to agent-research-approved, but it evaluated successfully";
    pkgs.runCommand "dispatch-labels-pinned-in-workflows-regression" { } "touch $out";
}
