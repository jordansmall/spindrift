# Label literals in the agent workflows must not silently drift from
# lib/labels.nix (issue #460, extended by issue #2528): the workflows are
# hand-maintained YAML with no other guard tying them back to the label
# registry, so a rename in the registry (or in schema.*.default, which
# lib/labels.nix's work rows source from) could orphan a `gh issue edit
# --add-label`/`--remove-label` call with nobody noticing until dispatch,
# recover, or research misbehaves in production.
#
# This file also guards the opposite direction (issue #2528): nix/regen.nix's
# label-registry-gen check (nix/checks/schema-drift.nix) guards registry ->
# Go, and dispatch-labels-pinned-in-workflows above guards registry ->
# workflows, but neither stops a Go source file or a Filer prompt fragment
# from writing/creating a brand-new label literal that lib/labels.nix never
# heard of — exactly how agent-review-finding (written by
# cmd/launcher/internal/settle/issue_intent.go, created directly by
# templates/default/prompts/fragments/filer-label-direct{,-forgejo}.md,
# neither going through doctor.Run()) escaped the registry until this check
# was added. label-registry-covers-harness-writes below closes that gap for
# those three known label-writing surfaces.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    any
    assertMsg
    concatMap
    concatStringsSep
    elem
    filter
    hasInfix
    mapAttrs
    replaceStrings
    splitString
    stringAsChars
    unique
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
  # Tokenizes a workflow source into exact label-shaped words (runs of
  # [a-z0-9-]) by blanking every other character first. A plain hasInfix
  # substring check would pin `agent-research` vacuously: it's a literal
  # prefix of five other required tokens (agent-research-in-progress,
  # agent-research-recommend, ...), so as long as ANY of those longer labels
  # is still present in the file, hasInfix "agent-research" src stays true
  # even if the standalone `agent-research` trigger usage was renamed out
  # from under it. Tokenizing first and then requiring exact membership
  # closes that gap: "agent-research-recommend" and "agent-research" tokenize
  # to two distinct words, never satisfying each other.
  tokenize =
    src:
    filter (s: s != "") (
      splitString " " (stringAsChars (c: if builtins.match "[a-z0-9-]" c == null then " " else c) src)
    );
  # Asserts every entry in requiredLabels appears as an exact token
  # somewhere in each workflowSets value, else throws naming the offending
  # set(s) and their missing label(s). Factored out (mirroring
  # nix/checks/schema-drift.nix's assertDogfoodDocModelsOk idiom) so
  # dispatch-labels-pinned-in-workflows-regression can exercise this exact
  # assertion path against a synthetic, doctored workflowSets without
  # touching the real workflow files.
  assertLabelsPinned =
    { requiredLabels, workflowSets }:
    let
      workflowTokens = mapAttrs (_: tokenize) workflowSets;
      missingBySet = mapAttrs (
        _: toks: filter (l: !(elem l toks)) requiredLabels
      ) workflowTokens;
      offenders = filter (name: missingBySet.${name} != [ ]) (builtins.attrNames missingBySet);
    in
    assert assertMsg (offenders == [ ])
      "agent-dispatch.yml/agent-recover.yml/agent-research.yml missing label literal(s) — schema rename, trigger-vocab rename, or research-label rename not propagated to the workflows: ${
        concatStringsSep "; " (
          map (n: "${n}: ${concatStringsSep ", " missingBySet.${n}}") offenders
        )
      }";
    offenders;
  # A token is worth checking against the registry only if it's shaped like
  # one of our labels (agent-<word>[-<word>...]) — otherwise every hyphenated
  # comment phrase in the same file (e.g. "the do-not-trust-the-agent-target
  # invariant") would tokenize into a false "missing label" offender. Used by
  # both label-registry-covers-harness-writes below and triggerGuardLabel.
  isLabelShaped = s: builtins.match "agent-[a-z-]+" s != null;
  # Dedicated pin for the three dispatch-trigger guards (issue #2528 AC3):
  # each of agent-trigger/agent-recover/agent-research is the literal a
  # workflow's own `if: github.event.label.name == '...'` step-gate compares
  # against. assertLabelsPinned's "does this token appear ANYWHERE in the
  # file" check cannot catch a rename of that guard for agent-research: the
  # literal legitimately reappears dozens of times elsewhere in the same
  # file (doc comments, the claim-remove-labels list), so the token stays
  # present in the file even after the guard itself points somewhere else.
  # triggerGuardLabel instead reads that guard's own line specifically, so a
  # rename is caught regardless of how many bystander occurrences of the old
  # literal remain in prose nearby — the exact gap
  # dispatch-labels-pinned-in-workflows-regression below reproduces and
  # confirms is caught.
  triggerGuardLabel =
    src:
    let
      guardLines = filter (l: hasInfix "if: github.event.label.name ==" l) (splitString "\n" src);
    in
    if guardLines == [ ] then
      null
    else
      let
        toks = filter isLabelShaped (tokenize (builtins.head guardLines));
      in
      if toks == [ ] then null else builtins.head toks;
  triggerGuardExpectations = {
    "github:agent-dispatch.yml" = "agent-trigger";
    "github:agent-recover.yml" = "agent-recover";
    "github:agent-research.yml" = "agent-research";
    "forgejo:agent-dispatch.yml" = "agent-trigger";
    "forgejo:agent-recover.yml" = "agent-recover";
    "forgejo:agent-research.yml" = "agent-research";
  };
  realTriggerGuardSrcs = {
    "github:agent-dispatch.yml" = builtins.readFile ../../.github/workflows/agent-dispatch.yml;
    "github:agent-recover.yml" = builtins.readFile ../../.github/workflows/agent-recover.yml;
    "github:agent-research.yml" = builtins.readFile ../../.github/workflows/agent-research.yml;
    "forgejo:agent-dispatch.yml" = builtins.readFile ../../.forgejo/workflows/agent-dispatch.yml;
    "forgejo:agent-recover.yml" = builtins.readFile ../../.forgejo/workflows/agent-recover.yml;
    "forgejo:agent-research.yml" = builtins.readFile ../../.forgejo/workflows/agent-research.yml;
  };
  # Asserts every workflow's own trigger guard names its expected label,
  # else throws naming the offending workflow(s) and what the guard actually
  # said. Factored to take triggerGuardSrcs as a parameter (mirroring
  # assertLabelsPinned's own factoring) so the regression check below can
  # exercise this exact assertion path against a synthetic, doctored source
  # map without touching the real workflow files.
  assertTriggerGuardsPinned =
    triggerGuardSrcs:
    let
      mismatches = filter (
        name: triggerGuardLabel triggerGuardSrcs.${name} != triggerGuardExpectations.${name}
      ) (builtins.attrNames triggerGuardExpectations);
    in
    assert assertMsg (mismatches == [ ])
      "workflow dispatch-trigger guard(s) don't match their expected label: ${
        concatStringsSep "; " (
          map (
            name:
            "${name}: guard names ${builtins.toJSON (triggerGuardLabel triggerGuardSrcs.${name})}, want ${builtins.toJSON triggerGuardExpectations.${name}}"
          ) mismatches
        )
      }";
    mismatches;
  # Every name lib/labels.nix's families carry, plus the trigger-only pair —
  # the full set label-registry-covers-harness-writes checks membership
  # against.
  allRegistryLabels =
    map (r: r.name) (
      labels.work
      ++ labels.research
      ++ labels.researchVerdicts
      ++ labels.priority
      ++ labels.ambiguous
      ++ labels.recoverable
      ++ labels.reviewFinding
    )
    ++ labels.triggerOnly;
  # The three known surfaces that write or create a label literal outside
  # doctor.Run()'s registry-derived TriageLabelMeta path (issue #2528 AC1).
  # Each is paired with a marker substring so only the line(s) that actually
  # carry the literal are tokenized — scanning a whole file's prose would
  # reintroduce the same false-positive risk isLabelShaped alone doesn't
  # fully close (e.g. issue_intent.go's unrelated "agent-target" comment).
  harnessSurfaces = {
    "cmd/launcher/internal/settle/issue_intent.go" = {
      src = builtins.readFile ../../cmd/launcher/internal/settle/issue_intent.go;
      markers = [ "issueIntentLabels" ];
    };
    "templates/default/prompts/fragments/filer-label-direct.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct.md;
      markers = [ "label create" ];
    };
    "templates/default/prompts/fragments/filer-label-direct-forgejo.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct-forgejo.md;
      markers = [ ''"name":'' ];
    };
  };
  labelsWrittenBy =
    { src, markers }:
    let
      relevantLines = filter (line: any (m: hasInfix m line) markers) (splitString "\n" src);
    in
    unique (filter isLabelShaped (concatMap tokenize relevantLines));
  missingFromRegistryBySurface = mapAttrs (
    _: surface: filter (l: !(elem l allRegistryLabels)) (labelsWrittenBy surface)
  ) harnessSurfaces;
  registryOffenders = filter (name: missingFromRegistryBySurface.${name} != [ ]) (
    builtins.attrNames missingFromRegistryBySurface
  );
in
{
  dispatch-labels-pinned-in-workflows =
    assert (assertLabelsPinned { inherit requiredLabels workflowSets; }) == [ ];
    assert (assertTriggerGuardsPinned realTriggerGuardSrcs) == [ ];
    pkgs.runCommand "dispatch-labels-pinned-in-workflows" { } "touch $out";

  # Regression guard (issue #2528 AC3): the parity assertion above must
  # actually detect a rename of the dual-role agent-research trigger+standing
  # label, not just pass vacuously. A file-wide "does this token appear
  # anywhere" check (assertLabelsPinned) cannot demonstrate that: renaming
  # only the trigger guard's own `if: github.event.label.name ==
  # 'agent-research'` line to `'agent-study'` leaves dozens of bystander
  # `agent-research` occurrences elsewhere in the same file (doc comments,
  # the claim-remove-labels list), so the bare token stays present in the
  # file regardless of the guard rename — a regression built on
  # assertLabelsPinned alone would report success even though the guard
  # really did drift. assertTriggerGuardsPinned reads that guard's own line
  # specifically, so this doctors only that one line in the github
  # agent-research.yml source and confirms assertTriggerGuardsPinned — the
  # exact function dispatch-labels-pinned-in-workflows calls — rejects it via
  # tryEval.
  dispatch-labels-pinned-in-workflows-regression =
    let
      driftedResearchSrc = replaceStrings
        [ "if: github.event.label.name == 'agent-research'" ]
        [ "if: github.event.label.name == 'agent-study'" ]
        realTriggerGuardSrcs."github:agent-research.yml";
      doctoredTriggerGuardSrcs = realTriggerGuardSrcs // {
        "github:agent-research.yml" = driftedResearchSrc;
      };
      result = builtins.tryEval (assertTriggerGuardsPinned doctoredTriggerGuardSrcs);
    in
    assert assertMsg (!result.success)
      "dispatch-labels-pinned-in-workflows-regression: expected assertTriggerGuardsPinned to reject a synthetic github agent-research.yml with the dispatch-trigger guard renamed from agent-research to agent-study, but it evaluated successfully";
    pkgs.runCommand "dispatch-labels-pinned-in-workflows-regression" { } "touch $out";

  # Registry ⊇ Harness direction (issue #2528): asserts every label-shaped
  # literal the three harnessSurfaces above write/create is a name somewhere
  # in lib/labels.nix. This is the check that would have failed before
  # lib/labels.nix grew a reviewFinding row for agent-review-finding.
  label-registry-covers-harness-writes =
    assert assertMsg (registryOffenders == [ ])
      "a Harness surface writes/creates a label lib/labels.nix doesn't know about — add a row for it to the registry: ${
        concatStringsSep "; " (
          map (n: "${n}: ${concatStringsSep ", " missingFromRegistryBySurface.${n}}") registryOffenders
        )
      }";
    pkgs.runCommand "label-registry-covers-harness-writes" { } "touch $out";
}
