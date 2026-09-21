# The agent workflows hand-maintain their label literals in YAML, so a rename in
# lib/labels.nix could orphan a `gh issue edit --add-label` call unnoticed
# (issue #460). The checks here guard both directions: registry to workflows,
# and registry covers every label the harness writes or creates outside the
# registry-derived path (issue #2528).
{ pkgs, ... }:
let
  inherit (pkgs.lib)
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
  # agent-trigger and agent-recover are dispatch trigger vocabulary, not
  # user-tunable knobs, so they have no lib/env-schema.nix entry and are
  # anchored here against the two workflows' own
  # `if: github.event.label.name == '...'` guards instead of a schema default.
  triggerOnlyLabels = labels.triggerOnly;
  # agent-research.yml is the one workflow that reads and writes the research
  # vocabulary, so it must carry every literal too (issue #2528 AC1).
  researchLabels = map (r: r.name) (labels.research ++ labels.researchVerdicts);
  requiredLabels = lifecycleLabels ++ triggerOnlyLabels ++ researchLabels;
  # The GitHub set and the Forgejo Actions mirror (issue #1967) must anchor the
  # same vocabulary. Each set is checked independently so a label present only
  # in the github set cannot mask its absence from the forgejo set.
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
  # Tokenizes a workflow source into exact label-shaped words by blanking every
  # character outside [a-z0-9-]. A hasInfix substring check would pin
  # `agent-research` vacuously: it is a prefix of five other required tokens, so
  # any longer label still present keeps the check true even after the standalone
  # trigger usage is renamed away. Exact token membership closes that gap.
  tokenize =
    src:
    filter (s: s != "") (
      splitString " " (stringAsChars (c: if builtins.match "[a-z0-9-]" c == null then " " else c) src)
    );
  # Factored out so dispatch-labels-pinned-in-workflows-regression can exercise
  # this assertion path against a doctored workflowSets without touching the
  # real workflow files.
  assertLabelsPinned =
    { requiredLabels, workflowSets }:
    let
      workflowTokens = mapAttrs (_: tokenize) workflowSets;
      missingBySet = mapAttrs (_: toks: filter (l: !(elem l toks)) requiredLabels) workflowTokens;
      offenders = filter (name: missingBySet.${name} != [ ]) (builtins.attrNames missingBySet);
    in
    assert assertMsg (offenders == [ ])
      "agent-dispatch.yml/agent-recover.yml/agent-research.yml missing label literal(s) — schema rename, trigger-vocab rename, or research-label rename not propagated to the workflows: ${
        concatStringsSep "; " (map (n: "${n}: ${concatStringsSep ", " missingBySet.${n}}") offenders)
      }";
    offenders;
  # Only tokens shaped like our labels are worth checking; otherwise every
  # hyphenated comment phrase becomes a false missing-label offender. Used only
  # by triggerGuardLabel, whose guard line never carries prose: the harness-write
  # check uses per-surface extraction instead, because this shape filter fails
  # open on a de-prefixed rename and broadening it false-positives.
  isLabelShaped = s: builtins.match "agent-[a-z-]+" s != null;
  # Pins each workflow's own `if:` label guard (issue #2528 AC3). A file-wide token
  # check cannot catch a rename of the agent-research guard: the literal
  # legitimately reappears dozens of times in the same file, so the token
  # survives the guard pointing elsewhere. This reads the guard's own line.
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
    "github:agent-research.yml" = "agent-research-trigger";
    "forgejo:agent-dispatch.yml" = "agent-trigger";
    "forgejo:agent-recover.yml" = "agent-recover";
    "forgejo:agent-research.yml" = "agent-research-trigger";
    # Not a dispatch trigger: the reject verdict is what fires the close, so
    # this guard is pinned to a *verdict* label. Renaming the verdict without
    # updating the workflow would leave rejected issues silently unclosed.
    "github:agent-research-close.yml" = "agent-research-reject";
    "forgejo:agent-research-close.yml" = "agent-research-reject";
  };
  realTriggerGuardSrcs = {
    "github:agent-dispatch.yml" = builtins.readFile ../../.github/workflows/agent-dispatch.yml;
    "github:agent-recover.yml" = builtins.readFile ../../.github/workflows/agent-recover.yml;
    "github:agent-research.yml" = builtins.readFile ../../.github/workflows/agent-research.yml;
    "forgejo:agent-dispatch.yml" = builtins.readFile ../../.forgejo/workflows/agent-dispatch.yml;
    "forgejo:agent-recover.yml" = builtins.readFile ../../.forgejo/workflows/agent-recover.yml;
    "forgejo:agent-research.yml" = builtins.readFile ../../.forgejo/workflows/agent-research.yml;
    "github:agent-research-close.yml" = builtins.readFile ../../.github/workflows/agent-research-close.yml;
    "forgejo:agent-research-close.yml" = builtins.readFile ../../.forgejo/workflows/agent-research-close.yml;
  };
  # Takes triggerGuardSrcs as a parameter so the regression check below can
  # exercise this assertion path against a doctored source map without touching
  # the real workflow files.
  assertTriggerGuardsPinned =
    triggerGuardSrcs:
    let
      mismatches = filter (
        name: triggerGuardLabel triggerGuardSrcs.${name} != triggerGuardExpectations.${name}
      ) (builtins.attrNames triggerGuardExpectations);
    in
    assert assertMsg (mismatches == [ ])
      "workflow label guard(s) don't match their expected label: ${
        concatStringsSep "; " (
          map (
            name:
            "${name}: guard names ${builtins.toJSON (triggerGuardLabel triggerGuardSrcs.${name})}, want ${
              builtins.toJSON triggerGuardExpectations.${name}
            }"
          ) mismatches
        )
      }";
    mismatches;
  # Every name lib/labels.nix's families carry, plus the trigger-only pair. This
  # is the set label-registry-covers-harness-writes checks membership against.
  allRegistryLabels =
    map (r: r.name) (
      labels.work
      ++ labels.research
      ++ labels.researchVerdicts
      ++ labels.researchFinding
      ++ labels.priority
      ++ labels.ambiguous
      ++ labels.recoverable
      ++ labels.reviewFinding
    )
    ++ labels.triggerOnly;
  # The surfaces that write or create a label literal outside the
  # registry-derived TriageLabelMeta path (issues #2528, #2749). A fifth,
  # settle/research.go's hard-coded "agent-research-finding" argument, is known
  # and still uncovered. Each extract pulls the literal from its own known
  # syntactic position, span-scanned so a reformat cannot hide it.
  harnessSurfaces = {
    "cmd/launcher/internal/settle/gate.go" = {
      src = builtins.readFile ../../cmd/launcher/internal/settle/gate.go;
      extract = extractFileIssueIntentsProvenanceLabel;
    };
    "templates/default/prompts/fragments/filer-label-direct.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct.md;
      extract = extractLabelCreateTokens;
    };
    "templates/default/prompts/fragments/filer-label-direct-forgejo.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct-forgejo.md;
      extract = extractNameFieldTokens;
    };
    "cmd/launcher/internal/doctor/doctor.go" = {
      src = builtins.readFile ../../cmd/launcher/internal/doctor/doctor.go;
      extract = src: extractResearchLabelNamesLiteral src ++ extractAmbiguousLabelNamesLiteral src;
    };
  };
  # The literal is gate.go's fourth argument to fileIssueIntentsDetailed
  # (issues #2590, #3608). Anchored to the call itself rather than to its first
  # three argument names, so renaming gate.go's local num/result variables
  # cannot false-negative this extractor. splitString has no notion of lines,
  # so the whole span between the marker and the next ")" is split on ",",
  # inert to a gofmt reformat.
  extractFileIssueIntentsProvenanceLabel =
    src:
    let
      marker = "fileIssueIntentsDetailed(";
      labelFromCall =
        segment:
        let
          call = builtins.head (splitString ")" segment);
          args = splitString "," call;
        in
        if builtins.length args < 4 then
          [ ]
        else
          let
            quoteParts = splitString "\"" (builtins.elemAt args 3);
          in
          if builtins.length quoteParts < 2 then
            [ ]
          else
            let
              value = builtins.elemAt quoteParts 1;
            in
            if builtins.match "[a-z][a-z0-9-]*" value != null then [ value ] else [ ];
    in
    concatMap labelFromCall (builtins.tail (splitString marker src));
  # The literal is the shell bareword right after `label create `, unquoted
  # unlike the Go and JSON surfaces below.
  extractLabelCreateTokens =
    src:
    let
      marker = "label create ";
      relevantLines = filter (l: hasInfix marker l) (splitString "\n" src);
      tokenAfterMarker =
        line:
        let
          parts = splitString marker line;
        in
        if builtins.length parts < 2 then
          [ ]
        else
          let
            rest = builtins.elemAt parts 1;
            firstTok = builtins.head (filter (s: s != "") (splitString " " rest));
          in
          if builtins.match "[a-z0-9-]+" firstTok != null then [ firstTok ] else [ ];
    in
    concatMap tokenAfterMarker relevantLines;
  # Only the value of the "name":"..." key, not any quoted string on the line:
  # the same line's "description" value carries prose like "non-blocking" that
  # a line-wide filter would flag as a phantom missing label.
  extractNameFieldTokens =
    src:
    let
      marker = ''"name":"'';
      relevantLines = filter (l: hasInfix marker l) (splitString "\n" src);
      tokenAfterMarker =
        line:
        let
          parts = splitString marker line;
        in
        if builtins.length parts < 2 then
          [ ]
        else
          let
            rest = builtins.elemAt parts 1;
            value = builtins.head (splitString "\"" rest);
          in
          if builtins.match "[a-z0-9-]+" value != null then [ value ] else [ ];
    in
    concatMap tokenAfterMarker relevantLines;
  # Shared span-scanned quote-split used by both Go label-literal extractors
  # below: splits the whole source on the marker, not line by line, and takes
  # everything up to the next quote in each following segment.
  labelLiteralAfterMarker =
    marker: src:
    let
      labelFromSegment =
        segment:
        let
          quoteParts = splitString "\"" segment;
        in
        if builtins.length quoteParts < 2 then
          [ ]
        else
          let
            value = builtins.head quoteParts;
          in
          if builtins.match "[a-z0-9-]+" value != null then [ value ] else [ ];
    in
    concatMap labelFromSegment (builtins.tail (splitString marker src));
  # doctor.go's ResearchLabelNames() literal (ADR 0041). The marker is unique in
  # that file: its other append(names, ...) call has no quote after the comma.
  extractResearchLabelNamesLiteral = labelLiteralAfterMarker ''append(names, "'';
  # doctor.go's AmbiguousLabelNames() literal (issue #2817). A separate
  # extractor because the two Go shapes differ and so need distinct markers.
  # This marker is the only `return []string{` span in doctor.go, so it cannot
  # cross-match ResearchLabelNames().
  extractAmbiguousLabelNamesLiteral = labelLiteralAfterMarker ''return []string{"'';
  # extractLabelCreateTokens and extractNameFieldTokens scan line by line, so a
  # shell `\`-continued `gh label create` would leave the marker and its
  # literal on different lines and match neither. Folding backslash-newline
  # into a space first re-joins them however the source wraps.
  joinBackslashNewline = src: replaceStrings [ "\\\n" ] [ " " ] src;
  labelsWrittenBy = { src, extract }: unique (extract (joinBackslashNewline src));
  # Checks emptyOffenders before registryOffenders: every surface writes at
  # least one label today, so an empty extraction means the extractor's marker
  # no longer matches the source, not that the surface stopped writing labels.
  # Checking that first keeps a broken extractor from passing vacuously as a
  # clean membership result. Factored out so the regressions below can call it.
  assertHarnessWritesInRegistry =
    { harnessSurfaces, registryLabels }:
    let
      extractedBySurface = mapAttrs (_: labelsWrittenBy) harnessSurfaces;
      emptyOffenders = filter (name: extractedBySurface.${name} == [ ]) (
        builtins.attrNames extractedBySurface
      );
    in
    assert assertMsg (emptyOffenders == [ ])
      "a harnessSurfaces extractor found zero label literal(s) for: ${
        concatStringsSep ", " emptyOffenders
      } — every known surface writes at least one label today, so an empty extraction means the extractor's marker/shape no longer matches the source (requoted, reformatted, or renamed), not that the surface stopped writing labels; fix the extractor in nix/checks/dispatch-labels.nix before trusting label-registry-covers-harness-writes again";
    let
      missingFromRegistryBySurface = mapAttrs (
        _: labels: filter (l: !(elem l registryLabels)) labels
      ) extractedBySurface;
      registryOffenders = filter (name: missingFromRegistryBySurface.${name} != [ ]) (
        builtins.attrNames missingFromRegistryBySurface
      );
    in
    assert assertMsg (registryOffenders == [ ])
      "a Harness surface writes/creates a label lib/labels.nix doesn't know about — add a row for it to the registry: ${
        concatStringsSep "; " (
          map (n: "${n}: ${concatStringsSep ", " missingFromRegistryBySurface.${n}}") registryOffenders
        )
      }";
    registryOffenders;
in
{
  dispatch-labels-pinned-in-workflows =
    assert (assertLabelsPinned { inherit requiredLabels workflowSets; }) == [ ];
    assert (assertTriggerGuardsPinned realTriggerGuardSrcs) == [ ];
    pkgs.runCommand "dispatch-labels-pinned-in-workflows" { } "touch $out";

  # Proves assertTriggerGuardsPinned rejects a renamed agent-research trigger
  # guard (issue #2528 AC3). A file-wide token check could not show this:
  # renaming only the guard line leaves dozens of bystander `agent-research`
  # occurrences in the same file, so the token stays present regardless.
  dispatch-labels-pinned-in-workflows-regression =
    let
      driftedResearchSrc =
        replaceStrings
          [ "if: github.event.label.name == 'agent-research-trigger'" ]
          [ "if: github.event.label.name == 'agent-study'" ]
          realTriggerGuardSrcs."github:agent-research.yml";
      doctoredTriggerGuardSrcs = realTriggerGuardSrcs // {
        "github:agent-research.yml" = driftedResearchSrc;
      };
      result = builtins.tryEval (assertTriggerGuardsPinned doctoredTriggerGuardSrcs);
    in
    assert assertMsg (!result.success)
      "dispatch-labels-pinned-in-workflows-regression: expected assertTriggerGuardsPinned to reject a synthetic github agent-research.yml with the dispatch-trigger guard renamed from agent-research-trigger to agent-study, but it evaluated successfully";
    pkgs.runCommand "dispatch-labels-pinned-in-workflows-regression" { } "touch $out";

  # Proves assertLabelsPinned itself still rejects a rename (issue #2528 AC3).
  # The trigger-guard regression above doctors one `if:` line, which
  # assertLabelsPinned's whole-file token scan never looks at, leaving it
  # otherwise unexercised. This doctors workflowSets.github instead.
  dispatch-labels-pinned-in-workflows-research-rename-regression =
    let
      driftedGithub =
        replaceStrings [ "agent-research-recommend" ] [ "agent-research-approved" ]
          workflowSets.github;
      doctoredWorkflowSets = workflowSets // {
        github = driftedGithub;
      };
      result = builtins.tryEval (assertLabelsPinned {
        inherit requiredLabels;
        workflowSets = doctoredWorkflowSets;
      });
    in
    assert assertMsg (!result.success)
      "dispatch-labels-pinned-in-workflows-research-rename-regression: expected assertLabelsPinned to reject a synthetic workflowSets.github with agent-research-recommend renamed to agent-research-approved, but it evaluated successfully";
    pkgs.runCommand "dispatch-labels-pinned-in-workflows-research-rename-regression" { } "touch $out";

  # Proves the tokenize plus exact `elem` membership check rejects the
  # substring-prefix drift class (issue #2528). The synthetic set carries
  # several compound agent-research-* labels but never the standalone token, so
  # a hasInfix check would wrongly pass where exact tokenization must reject.
  dispatch-labels-tokenize-exact-match-regression =
    let
      syntheticWorkflowSets = {
        github = ''
          agent-research-recommend
          agent-research-in-progress
          agent-research-failed
        '';
      };
      result = builtins.tryEval (assertLabelsPinned {
        requiredLabels = [ "agent-research" ];
        workflowSets = syntheticWorkflowSets;
      });
    in
    assert assertMsg (!result.success)
      "dispatch-labels-tokenize-exact-match-regression: expected assertLabelsPinned to reject a synthetic workflowSets containing only compound agent-research-* labels and never the standalone agent-research token, but it evaluated successfully";
    pkgs.runCommand "dispatch-labels-tokenize-exact-match-regression" { } "touch $out";

  # Asserts every label literal the harnessSurfaces above write is a name in
  # lib/labels.nix (issues #2528, #2749). This is the check that would have
  # failed before lib/labels.nix grew a reviewFinding row.
  label-registry-covers-harness-writes =
    assert
      (assertHarnessWritesInRegistry {
        inherit harnessSurfaces;
        registryLabels = allRegistryLabels;
      }) == [ ];
    pkgs.runCommand "label-registry-covers-harness-writes" { } "touch $out";

  # Proves assertHarnessWritesInRegistry catches a de-prefixed rename
  # (issue #2528). A shape-only filter would have discarded "review-finding"
  # before the membership check, so the surface would extract [ ] and this
  # regression would have had nothing to reject: the fail-open gap it closes.
  label-registry-covers-harness-writes-regression =
    let
      doctoredGateSrc =
        replaceStrings [ ''"agent-review-finding"'' ] [ ''"review-finding"'' ]
          harnessSurfaces."cmd/launcher/internal/settle/gate.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/gate.go" = harnessSurfaces."cmd/launcher/internal/settle/gate.go" // {
          src = doctoredGateSrc;
        };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-regression: expected assertHarnessWritesInRegistry to reject a synthetic gate.go with agent-review-finding de-prefixed to review-finding, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-regression" { } "touch $out";

  # Proves extractLabelCreateTokens still catches an unregistered label once
  # `gh label create` is line-wrapped with a shell `\` (issue #2528). Before
  # joinBackslashNewline the marker and the literal landed on different lines,
  # so the per-line scan returned [ ] and this had nothing to reject.
  label-registry-covers-harness-writes-continuation-regression =
    let
      doctoredFilerLabelDirectSrc =
        replaceStrings
          [ "label create agent-review-finding" ]
          [
            ''
              label create \
                     agent-unregistered-label''
          ]
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct.md".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "templates/default/prompts/fragments/filer-label-direct.md" =
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct.md"
          // {
            src = doctoredFilerLabelDirectSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-continuation-regression: expected assertHarnessWritesInRegistry to reject a synthetic filer-label-direct.md with the label-create bareword line-continued and renamed to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-continuation-regression" { } "touch $out";

  # Proves the emptyOffenders half fires when extractLabelCreateTokens'
  # unquoted-bareword assumption breaks (issue #2528): a leading `"` fails the
  # label-shape match, the surface extracts [ ], and before emptyOffenders that
  # let the whole check pass while the harness created an unregistered label.
  label-registry-covers-harness-writes-quoted-bareword-regression =
    let
      doctoredFilerLabelDirectSrc =
        replaceStrings [ "label create agent-review-finding" ] [ ''label create "agent-unregistered-label"'' ]
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct.md".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "templates/default/prompts/fragments/filer-label-direct.md" =
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct.md"
          // {
            src = doctoredFilerLabelDirectSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-quoted-bareword-regression: expected assertHarnessWritesInRegistry to reject a synthetic filer-label-direct.md with the label-create bareword double-quoted and renamed to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-quoted-bareword-regression" { } "touch $out";

  # Proves the emptyOffenders half fires when extractNameFieldTokens' no-space
  # `"name":"` marker meets ordinary JSON spacing (issue #2528): the marker
  # never matches, the surface extracts [ ], and before emptyOffenders that let
  # the whole check pass silently.
  label-registry-covers-harness-writes-json-spacing-regression =
    let
      doctoredForgejoSrc =
        replaceStrings [ ''"name":"agent-review-finding"'' ] [ ''"name": "agent-unregistered-label"'' ]
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct-forgejo.md".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "templates/default/prompts/fragments/filer-label-direct-forgejo.md" =
          harnessSurfaces."templates/default/prompts/fragments/filer-label-direct-forgejo.md"
          // {
            src = doctoredForgejoSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-json-spacing-regression: expected assertHarnessWritesInRegistry to reject a synthetic filer-label-direct-forgejo.md with a space after the \"name\" key's colon and the value renamed to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-json-spacing-regression" { } "touch $out";

  # Proves the emptyOffenders half fires when the anchored
  # `fileIssueIntentsDetailed(` marker breaks on a rename of the call (issue
  # #2528): splitString never finds the old marker, the surface extracts [ ],
  # and before emptyOffenders the check passed while the harness kept writing
  # labels unobserved.
  label-registry-covers-harness-writes-call-rename-regression =
    let
      doctoredGateSrc =
        replaceStrings [ "fileIssueIntentsDetailed(" ] [ "fileReviewFindingIntents(" ]
          harnessSurfaces."cmd/launcher/internal/settle/gate.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/gate.go" = harnessSurfaces."cmd/launcher/internal/settle/gate.go" // {
          src = doctoredGateSrc;
        };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-call-rename-regression: expected assertHarnessWritesInRegistry to reject a synthetic gate.go with fileIssueIntentsDetailed renamed to fileReviewFindingIntents, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-call-rename-regression" { } "touch $out";

  # Proves assertHarnessWritesInRegistry catches doctor.go's hand-written
  # ResearchLabelNames() literal drifting from lib/labels.nix's researchFinding
  # row (issue #2749, ADR 0041). Without doctor.go as a surface, a rename on
  # either side went undetected and metaFor() fell through to the gray
  # no-description default instead of erroring. The converse is the check below.
  label-registry-covers-harness-writes-research-finding-drift-regression =
    let
      doctoredDoctorSrc =
        replaceStrings [ ''"agent-research-finding"'' ] [ ''"agent-unregistered-label"'' ]
          harnessSurfaces."cmd/launcher/internal/doctor/doctor.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/doctor/doctor.go" = harnessSurfaces."cmd/launcher/internal/doctor/doctor.go" // {
          src = doctoredDoctorSrc;
        };
      };
      extractedLabels = labelsWrittenBy {
        src = doctoredDoctorSrc;
        extract = extractResearchLabelNamesLiteral;
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (extractedLabels == [ "agent-unregistered-label" ])
      "label-registry-covers-harness-writes-research-finding-drift-regression: expected extractResearchLabelNamesLiteral to find [ \"agent-unregistered-label\" ] on the doctored ResearchLabelNames() literal (not [ ]), but got: ${concatStringsSep ", " extractedLabels}";
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-research-finding-drift-regression: expected assertHarnessWritesInRegistry to reject a synthetic doctor.go with ResearchLabelNames()'s agent-research-finding literal renamed to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-research-finding-drift-regression" { } "touch $out";

  # The converse of the check above (issue #2749 AC3): the registry side is
  # doctored instead, so doctor.go's still-correct literal is no longer a name
  # in allRegistryLabels. Maps over allRegistryLabels rather than lib/labels.nix's
  # raw rows because that list is what the assertion checks membership against.
  label-registry-covers-harness-writes-research-finding-registry-rename-regression =
    let
      doctoredRegistryLabels = map (
        l: if l == "agent-research-finding" then "agent-research-note" else l
      ) allRegistryLabels;
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        inherit harnessSurfaces;
        registryLabels = doctoredRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-research-finding-registry-rename-regression: expected assertHarnessWritesInRegistry to reject a synthetic registry with agent-research-finding renamed to agent-research-note, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-research-finding-registry-rename-regression" { } "touch $out";

  # Mirrors the research-finding drift regression above for doctor.go's other
  # literal, AmbiguousLabelNames()'s "agent-ambiguous-spec" (issue #2817).
  # Guards against extractAmbiguousLabelNamesLiteral going blind as much as
  # against assertHarnessWritesInRegistry itself.
  label-registry-covers-harness-writes-ambiguous-label-drift-regression =
    let
      doctoredDoctorSrc =
        replaceStrings [ ''"agent-ambiguous-spec"'' ] [ ''"agent-unregistered-label"'' ]
          harnessSurfaces."cmd/launcher/internal/doctor/doctor.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/doctor/doctor.go" = harnessSurfaces."cmd/launcher/internal/doctor/doctor.go" // {
          src = doctoredDoctorSrc;
        };
      };
      extractedLabels = labelsWrittenBy {
        src = doctoredDoctorSrc;
        extract = extractAmbiguousLabelNamesLiteral;
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (extractedLabels == [ "agent-unregistered-label" ])
      "label-registry-covers-harness-writes-ambiguous-label-drift-regression: expected extractAmbiguousLabelNamesLiteral to find [ \"agent-unregistered-label\" ] on the doctored AmbiguousLabelNames() literal (not [ ]), but got: ${concatStringsSep ", " extractedLabels}";
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-ambiguous-label-drift-regression: expected assertHarnessWritesInRegistry to reject a synthetic doctor.go with AmbiguousLabelNames()'s agent-ambiguous-spec literal renamed to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-ambiguous-label-drift-regression" { } "touch $out";

  # Proves the span-scanned extraction survives a gofmt multi-line reformat of
  # the fileIssueIntentsDetailed(...) call arguments (issues #2528 AC1, #2590). A
  # per-line scan would return [ ] the moment the literal lands on a different
  # line than the marker, the fails-open direction, so this asserts the
  # extractor finds the label, not merely that the assertion rejects it.
  label-registry-covers-harness-writes-fileissueintents-multiline-regression =
    let
      doctoredGateSrc =
        replaceStrings
          [ ''fileIssueIntentsDetailed(s.it, num, result, "agent-review-finding", "")'' ]
          [
            "fileIssueIntentsDetailed(\n\t\ts.it,\n\t\tnum,\n\t\tresult,\n\t\t\"agent-unregistered-label\",\n\t\t\"\",\n\t)"
          ]
          harnessSurfaces."cmd/launcher/internal/settle/gate.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/gate.go" = harnessSurfaces."cmd/launcher/internal/settle/gate.go" // {
          src = doctoredGateSrc;
        };
      };
      extractedLabels = labelsWrittenBy {
        src = doctoredGateSrc;
        extract = extractFileIssueIntentsProvenanceLabel;
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (extractedLabels == [ "agent-unregistered-label" ])
      "label-registry-covers-harness-writes-fileissueintents-multiline-regression: expected extractFileIssueIntentsProvenanceLabel to find [ \"agent-unregistered-label\" ] on the multi-line-reformatted call (not [ ]), but got: ${concatStringsSep ", " extractedLabels}";
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-fileissueintents-multiline-regression: expected assertHarnessWritesInRegistry to reject a synthetic gate.go with the fileIssueIntentsDetailed(...) call gofmt-reformatted across multiple lines and its label argument swapped to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-fileissueintents-multiline-regression" { } "touch $out";
}
