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
# cmd/launcher/internal/settle/gate.go's fileIssueIntents(...) call,
# created directly by
# templates/default/prompts/fragments/filer-label-direct{,-forgejo}.md,
# neither going through doctor.Run()) escaped the registry until this check
# was added. label-registry-covers-harness-writes below closes that gap for
# those four known label-writing surfaces. (Issue #2590 moved the Go literal
# out of issue_intent.go's own issueIntentLabels var — since removed — and
# into this call site's own fourth argument, and turned the call itself from
# a method (s.fileIssueIntents(...)) into the package-level function
# fileIssueIntents(s.it, ...); see harnessSurfaces below.)
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
  # lib/documented-fact-checker.nix's assertMarkedBlockOk idiom) so
  # dispatch-labels-pinned-in-workflows-regression can exercise this exact
  # assertion path against a synthetic, doctored workflowSets without
  # touching the real workflow files.
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
  # A token is worth checking against the registry only if it's shaped like
  # one of our labels (agent-<word>[-<word>...]) — otherwise every hyphenated
  # comment phrase in the same file (e.g. "the do-not-trust-the-agent-target
  # invariant") would tokenize into a false "missing label" offender. Used by
  # triggerGuardLabel, which filters tokens on a workflow's own `if:
  # github.event.label.name == '...'` guard line and has no false-positive
  # risk from that (the guard line never carries unrelated prose). NOT used
  # by label-registry-covers-harness-writes below any more — see
  # harnessSurfaces for why a shape-only filter both fails open (on a
  # de-prefixed rename) and, if broadened, false-positives (on prose like
  # "non-blocking").
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
            "${name}: guard names ${builtins.toJSON (triggerGuardLabel triggerGuardSrcs.${name})}, want ${
              builtins.toJSON triggerGuardExpectations.${name}
            }"
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
      ++ labels.researchFinding
      ++ labels.priority
      ++ labels.ambiguous
      ++ labels.recoverable
      ++ labels.reviewFinding
    )
    ++ labels.triggerOnly;
  # The four known surfaces that write or create a label literal outside
  # the registry-derived TriageLabelMeta path (issue #2528 AC1, extended to a
  # fourth surface by issue #2749). ResearchLabelNames() is called from
  # inside doctor.Run() (doctor.go:317) — it's the literal itself, not the
  # call site, that sits outside the registry-derived codegen path. A fifth
  # surface, cmd/launcher/internal/settle/research.go:121's hard-coded
  # "agent-research-finding" argument to fileIssueIntentsDetailed(...), is
  # known and currently uncovered — out of scope for issue #2749.
  # Each is paired with a dedicated `extract` function — not a generic
  # markers-line-select + whole-line-tokenize + isLabelShaped-filter
  # pipeline — that pulls the label literal from its own known syntactic
  # position, scanned across a marker-to-terminator SPAN rather than
  # restricted to one line (a per-line restriction is exactly what let a
  # gofmt-clean reformat or a shell line-continuation go unextracted before —
  # see label-registry-covers-harness-writes-fileissueintents-multiline-regression
  # (gofmt reformat) and label-registry-covers-harness-writes-continuation-regression
  # (shell line-continuation) below):
  #   - gate.go: the string literal inside the fourth argument of the
  #     fileIssueIntents(s.it, num, result, "agent-review-finding") call
  #     gate.go's work-path settle makes (issue #2590 moved this literal out
  #     of issue_intent.go's own issueIntentLabels var — since removed — and
  #     into this call site's own literal argument, and turned the call from
  #     a method (s.fileIssueIntents(...)) into the package-level function
  #     fileIssueIntents(s.it, ...)). Anchored to the call itself
  #     ("fileIssueIntents(") and scanned span-wise, so a gofmt multi-line
  #     reformat of the call's arguments is inert to extraction — see
  #     extractFileIssueIntentsProvenanceLabel below.
  #   - filer-label-direct.md: the shell bareword immediately following the
  #     literal words `label create ` (unquoted, unlike the other two), after
  #     joinBackslashNewline (defined by labelsWrittenBy below) has folded
  #     every `\`-continued line into its continuation first, so a
  #     line-wrapped `gh label create \` still yields the literal that
  #     follows on the next line — see
  #     label-registry-covers-harness-writes-continuation-regression below.
  #   - filer-label-direct-forgejo.md: specifically the VALUE of the
  #     "name":"..." JSON key on that line.
  #   - doctor.go: the string literal inside ResearchLabelNames()'s `names =
  #     append(names, "agent-research-finding")` call (ADR 0041) — the one
  #     hand-written label literal in doctor.go that has no
  #     forge.ResearchFindingLabel() counterpart to source from instead (see
  #     the doc comment on ResearchLabelNames() itself). Anchored to the
  #     marker "append(names, \"" — confirmed unique in the file: the file's
  #     other `append(names, ...)` call, `append(names, e.Label)`, has no
  #     quote immediately after the comma, so it never matches — and scanned
  #     span-wise to the literal's closing quote, mirroring
  #     extractNameFieldTokens' simple quote-split shape — see
  #     extractResearchLabelNamesLiteral below.
  # A shape-only filter (isLabelShaped, which requires an agent- prefix)
  # fails open on a de-prefixed rename: if the fileIssueIntents call's
  # "agent-review-finding" argument were renamed to "review-finding",
  # isLabelShaped would filter it out of the token stream entirely, so
  # labelsWrittenBy would return [] for that surface and
  # label-registry-covers-harness-writes would pass silently even though the
  # harness now writes an unregistered label —
  # label-registry-covers-harness-writes-regression below reproduces and
  # closes that gap. Conversely, simply broadening isLabelShaped to accept
  # any hyphenated lowercase word (instead of per-surface extraction) would
  # reintroduce a false positive: the forgejo fragment's
  # "name":"agent-review-finding" line also carries a "description" value
  # containing the lowercase hyphenated phrase "non-blocking", which a
  # whole-line tokenize + broadened-shape-filter would wrongly flag as a
  # phantom missing-from-registry label on the CURRENT, correct source.
  # Per-surface, span-scanned extraction avoids all three failure modes by
  # only ever looking where the literal is actually syntactically expected to
  # be, regardless of how the surrounding source is line-wrapped.
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
      extract = extractResearchLabelNamesLiteral;
    };
  };
  # The literal sits inside the fourth argument of the
  # fileIssueIntents(s.it, num, result, "agent-review-finding") call gate.go's
  # work-path settle makes (issue #2590 parameterized fileIssueIntents by
  # provenance label, moving the literal out of issue_intent.go's own
  # now-removed issueIntentLabels var and into this call site's own literal
  # argument instead, and turned the call from a method
  # (s.fileIssueIntents(...)) into the package-level function
  # fileIssueIntents(s.it, ...)). Anchored to the call itself
  # ("fileIssueIntents(") rather than to the call's first three argument
  # names, so a future rename of gate.go's own local num/result variables (or
  # the s.it receiver expression) doesn't false-negative this extractor --
  # only a rename of the fileIssueIntents call itself (or the label argument
  # ceasing to be a literal) can break the marker. Splits on "," to reach the
  # fourth (0-indexed 3rd) positional argument, then on the first pair of
  # quotes within it -- splitString has no notion of lines, so a gofmt
  # multi-line reformat of the call's arguments is inert to this span-scanned
  # extraction: the whole call segment between one "fileIssueIntents(" marker
  # and the next ")" is collected first, then split on "," across that whole
  # span regardless of embedded newlines, rather than scanned one source line
  # at a time -- see
  # label-registry-covers-harness-writes-fileissueintents-multiline-regression
  # below, which proves this claim rather than merely asserting it.
  extractFileIssueIntentsProvenanceLabel =
    src:
    let
      marker = "fileIssueIntents(";
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
  # The literal is the shell bareword immediately following the literal
  # words `label create ` (unquoted, unlike the Go and JSON surfaces below) —
  # split the line on that marker and take the first whitespace-delimited
  # token of what follows.
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
  # The literal is specifically the VALUE of the "name":"..." JSON key on
  # that line, not any quoted string on it — the same line's "description"
  # value is free-form prose (e.g. "non-blocking") that would false-positive
  # as a phantom missing label under a line-wide/shape-only filter. Split on
  # the `"name":"` marker itself and take everything up to the next quote.
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
  # The literal sits inside ResearchLabelNames()'s `names = append(names,
  # "agent-research-finding")` call (doctor.go, ADR 0041). Anchored to the
  # marker "append(names, \"" — marker uniqueness in doctor.go is argued in
  # the harnessSurfaces bullet above. Splits the whole source on the marker
  # (span-scanned, not per-line, though this literal is realistically always
  # on one line) and, for each segment after the first, takes everything up
  # to the next quote as the literal — the same shape as extractNameFieldTokens
  # above.
  extractResearchLabelNamesLiteral =
    src:
    let
      marker = ''append(names, "'';
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
  # extractLabelCreateTokens and extractNameFieldTokens both scan line by
  # line, so a shell `\`-continued line (e.g. `gh label create \` with the
  # actual label bareword on the following line) would otherwise put the
  # marker on one line and the literal it's supposed to precede on the next,
  # yielding no match on either — the same per-line fail-open class
  # extractFileIssueIntentsProvenanceLabel closes above for Go's multi-line
  # call-argument layout.
  # Folding every backslash-newline into a single space before extraction
  # runs re-joins each continued line into one, so the marker and the
  # literal that follows it end up on the same line regardless of how the
  # source wraps — see
  # label-registry-covers-harness-writes-continuation-regression below.
  joinBackslashNewline = src: replaceStrings [ "\\\n" ] [ " " ] src;
  labelsWrittenBy = { src, extract }: unique (extract (joinBackslashNewline src));
  # Core assertion for the Registry ⊇ Harness direction, factored out
  # (mirroring assertLabelsPinned's own factoring) so
  # label-registry-covers-harness-writes-regression can exercise this exact
  # assertion path against a doctored harnessSurfaces without touching real
  # files beyond one doctored string.
  #
  # Guards two distinct failure modes, in order:
  #   1. emptyOffenders — every harnessSurfaces entry writes at least one real
  #      label today, so an extractor returning [] never means "this surface
  #      stopped writing labels"; it means the extractor's marker/shape no
  #      longer matches the source (a requote, a reformat, a call rename) and
  #      the check below would otherwise pass vacuously instead of validating
  #      anything for that surface — exactly the class of gap the four
  #      *-regression checks below reproduce (a quoted bareword an unquoted
  #      extractor can't see, `"name": "..."` spacing a no-space marker can't
  #      see, a renamed Go call an anchored marker can't see, and a
  #      gofmt-reformatted multi-line call a per-line scan can't see).
  #   2. registryOffenders — the pre-existing Registry ⊇ Harness membership
  #      check: every label an extractor DID find is a name in
  #      lib/labels.nix.
  # Checked in this order so a broken extractor fails loudly on its own
  # (case 1) rather than silently masquerading as case 2's clean pass.
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
      driftedResearchSrc =
        replaceStrings
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

  # Regression guard (issue #2528 AC3), distinct from the trigger-guard
  # regression above: assertLabelsPinned itself — the file-wide "does this
  # token appear ANYWHERE in the file" check, extended by issue #2528 to
  # fold in research labels — needs its OWN doctored fixture to prove it
  # still rejects a rename. The trigger-guard regression above only doctors
  # a single `if: ...` guard line; assertLabelsPinned's whole-file token
  # scan never even looks at that line specifically, so that regression
  # alone leaves assertLabelsPinned completely unexercised. This instead
  # doctors workflowSets.github with agent-research-recommend's literal
  # swapped for a plausible drifted rename (as if someone renamed the label
  # in the workflow YAML without updating lib/labels.nix), then runs
  # assertLabelsPinned — the exact function
  # dispatch-labels-pinned-in-workflows calls first — against that doctored
  # set via tryEval, so this fails if the tokenize/exact-membership check is
  # ever dropped from assertLabelsPinned. (This recreates the original
  # regression issue #2528's f57ef25f commit added under the
  # dispatch-labels-pinned-in-workflows-regression name, which a later
  # commit's wholesale body replacement of that same-named check displaced.)
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

  # Regression guard (issue #2528): proves the tokenize + exact `elem`
  # membership fix in assertLabelsPinned actually rejects the
  # substring-prefix drift class the comment on tokenize above describes,
  # rather than merely documenting it. Builds a small synthetic
  # workflowSets — a literal string, not a doctored real file, kept minimal
  # and self-contained — that contains several compound research labels
  # (agent-research-recommend, agent-research-in-progress,
  # agent-research-failed) but never the standalone token agent-research
  # anywhere. A plain hasInfix "agent-research" substring check would
  # wrongly PASS this fixture, since "agent-research" is a literal substring
  # of all three compound labels present; exact tokenization must REJECT it,
  # since none of those compound labels tokenizes to the bare word
  # "agent-research".
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

  # Registry ⊇ Harness direction (issue #2528, extended to a fourth surface by
  # issue #2749): asserts every label literal the four harnessSurfaces above
  # write/create is a name somewhere in lib/labels.nix. This is the check that
  # would have failed before lib/labels.nix grew a reviewFinding row for
  # agent-review-finding.
  label-registry-covers-harness-writes =
    assert
      (assertHarnessWritesInRegistry {
        inherit harnessSurfaces;
        registryLabels = allRegistryLabels;
      }) == [ ];
    pkgs.runCommand "label-registry-covers-harness-writes" { } "touch $out";

  # Regression guard (issue #2528): proves assertHarnessWritesInRegistry
  # actually catches a de-prefixed rename of a harness-written label. Doctors
  # the real gate.go source (the file itself, via replaceStrings on its own
  # builtins.readFile content, not a synthetic fixture) so the
  # fileIssueIntents(...) call's "agent-review-finding" argument loses its
  # agent- prefix down to "review-finding", builds a doctored harnessSurfaces
  # with only that one surface's src swapped in, and asserts via tryEval that
  # assertHarnessWritesInRegistry now correctly REJECTS it — "review-finding"
  # is not a name in lib/labels.nix's registry. Before the per-surface
  # extraction fix, a shape-only filter (isLabelShaped, which requires an
  # agent- prefix) would have discarded "review-finding" before it ever
  # reached the registry-membership check, so labelsWrittenBy would have
  # returned [] for that surface and this regression would have found
  # nothing to reject — exactly the fail-open gap this check closes.
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

  # Regression guard (issue #2528): proves extractLabelCreateTokens still
  # catches an unregistered label once the `gh label create` invocation is
  # line-wrapped with a shell `\` continuation — the same per-line fail-open
  # class extractFileIssueIntentsProvenanceLabel's span-scanned (not per-line)
  # extraction avoids above for gate.go's call-argument layout, but for the
  # shell-continuation shape instead of gofmt's multi-line call arguments.
  # Doctors the real filer-label-direct.md source so the bareword right
  # after `label create` moves onto its own continuation line AND is renamed
  # to "agent-unregistered-label" (a name lib/labels.nix does not carry),
  # then asserts via tryEval that assertHarnessWritesInRegistry rejects it.
  # Before joinBackslashNewline, the marker `label create ` and the literal
  # that follows it would land on two different lines, so
  # extractLabelCreateTokens' per-line scan would find `\` immediately after
  # the marker, fail its label-shape match, and return [] — this regression
  # would have found nothing to reject.
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

  # Regression guard (issue #2528): proves the emptyOffenders half of
  # assertHarnessWritesInRegistry actually fires when
  # extractLabelCreateTokens' unquoted-bareword assumption breaks. Doctors
  # filer-label-direct.md so the label-create argument is double-quoted
  # (`gh label create "agent-unregistered-label"`) — extractLabelCreateTokens
  # splits on whitespace and label-shape-matches the raw token, so a leading
  # `"` fails builtins.match and the whole surface silently extracts [ ],
  # which — before the emptyOffenders check existed — let
  # label-registry-covers-harness-writes pass even though the harness now
  # creates an unregistered label. Confirms via tryEval that
  # assertHarnessWritesInRegistry now rejects this instead.
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

  # Regression guard (issue #2528): proves the emptyOffenders half of
  # assertHarnessWritesInRegistry actually fires when
  # extractNameFieldTokens' no-space `"name":"` marker breaks on ordinary
  # JSON formatting. Doctors filer-label-direct-forgejo.md so the `"name"`
  # key gets a space after its colon (`"name": "agent-unregistered-label"`,
  # gofmt/jq's normal style) — the marker never matches, so the surface
  # silently extracts [ ], which — before the emptyOffenders check existed —
  # let label-registry-covers-harness-writes pass even though the harness now
  # creates an unregistered label. Confirms via tryEval that
  # assertHarnessWritesInRegistry now rejects this instead.
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

  # Regression guard (issue #2528): proves the emptyOffenders half of
  # assertHarnessWritesInRegistry actually fires when
  # extractFileIssueIntentsProvenanceLabel's anchored `fileIssueIntents(`
  # marker breaks on a rename of the call itself. Doctors gate.go so the call
  # reads `fileReviewFindingIntents(...)` instead — splitString never finds
  # the old marker, so the surface silently extracts [ ], which — before the
  # emptyOffenders check existed — let label-registry-covers-harness-writes
  # pass even though the harness still writes agent-review-finding (now
  # unobserved) and could just as easily have started writing something
  # unregistered alongside it. Confirms via tryEval that
  # assertHarnessWritesInRegistry now rejects this instead.
  label-registry-covers-harness-writes-call-rename-regression =
    let
      doctoredGateSrc =
        replaceStrings [ "fileIssueIntents(" ] [ "fileReviewFindingIntents(" ]
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
      "label-registry-covers-harness-writes-call-rename-regression: expected assertHarnessWritesInRegistry to reject a synthetic gate.go with fileIssueIntents renamed to fileReviewFindingIntents, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-call-rename-regression" { } "touch $out";

  # Regression guard (issue #2749): proves assertHarnessWritesInRegistry
  # actually catches a drift between doctor.go's hand-written
  # ResearchLabelNames() literal ("agent-research-finding", ADR 0041, at
  # `names = append(names, "agent-research-finding")`) and lib/labels.nix's
  # researchFinding row — the fourth harnessSurfaces entry added by this
  # commit. Doctors the real doctor.go source (via replaceStrings on its own
  # builtins.readFile content, not a synthetic fixture) so that literal
  # becomes "agent-unregistered-label", builds a doctored harnessSurfaces with
  # only that one surface's src swapped in, and asserts via tryEval that
  # assertHarnessWritesInRegistry now correctly REJECTS it —
  # "agent-unregistered-label" is not a name in lib/labels.nix's registry.
  # Before this commit, doctor.go wasn't a harnessSurfaces entry at all, so a
  # future rename of lib/labels.nix's researchFinding.name without updating
  # doctor.go's literal (or vice versa) would have gone completely undetected
  # by this file — metaFor() would silently fall through to the gray
  # "ededed" no-description default instead of erroring. Named for the
  # failure mode it guards (doctor.go's literal drifting from the registry),
  # not the file it doctors, matching the naming convention of its siblings
  # (-continuation-regression, -quoted-bareword-regression,
  # -json-spacing-regression, -call-rename-regression, -multiline-regression
  # below). See label-registry-covers-harness-writes-research-finding-registry-rename-regression
  # below for the converse direction: a rename on lib/labels.nix's side
  # instead of doctor.go's.
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

  # Regression guard (issue #2749 AC3), converse direction from the check
  # above: that check doctors doctor.go's literal and proves
  # assertHarnessWritesInRegistry catches the harness side drifting away from
  # the registry. This instead doctors the REGISTRY side — simulating a
  # rename of lib/labels.nix's researchFinding row from "agent-research-finding"
  # to a plausible new name, "agent-research-note" — and proves
  # assertHarnessWritesInRegistry catches that direction too: doctor.go's
  # ResearchLabelNames() literal still says "agent-research-finding", which
  # after the doctored rename is no longer a name anywhere in
  # allRegistryLabels. Builds doctoredRegistryLabels by mapping the
  # replacement over allRegistryLabels (not over lib/labels.nix's raw rows —
  # allRegistryLabels is the exact list assertHarnessWritesInRegistry checks
  # membership against), then calls assertHarnessWritesInRegistry with the
  # real (undoctored) harnessSurfaces against that doctored registry via
  # tryEval, asserting it now correctly REJECTS it. This is the check issue
  # #2749's third acceptance criterion asked to be confirmed manually; pinning
  # it here as a permanent check keeps it from silently regressing later.
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

  # Regression guard (issue #2528 AC1, issue #2590): proves the new
  # extractor's span-scanned (marker-to-")", split on multi-line source)
  # extraction survives a gofmt multi-line reformat of the fileIssueIntents(
  # ...) call's arguments — a property nothing previously exercised, since
  # this same commit deletes label-registry-covers-harness-writes-reformat-
  # regression (proved the *old* extractor survived a reformatted
  # `[]string{...}` var block, a different risk: argument count, not
  # multi-line span-splitting). A naive per-line scan would silently return
  # [ ] the moment the label literal lands on a different line than the
  # `fileIssueIntents(` marker, the dangerous fails-open direction. Doctors
  # gate.go's single-line call into a gofmt-plausible five-line reformat with
  # the label argument swapped to "agent-unregistered-label" (unregistered),
  # then asserts two things: the extractor itself still finds exactly
  # [ "agent-unregistered-label" ] (not [ ], which would hide "found but
  # rejected" behind "found nothing"), and assertHarnessWritesInRegistry as a
  # whole still rejects the doctored input end to end.
  label-registry-covers-harness-writes-fileissueintents-multiline-regression =
    let
      doctoredGateSrc =
        replaceStrings
          [ ''fileIssueIntents(s.it, num, result, "agent-review-finding")'' ]
          [
            "fileIssueIntents(\n\t\ts.it,\n\t\tnum,\n\t\tresult,\n\t\t\"agent-unregistered-label\",\n\t)"
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
      "label-registry-covers-harness-writes-fileissueintents-multiline-regression: expected assertHarnessWritesInRegistry to reject a synthetic gate.go with the fileIssueIntents(...) call gofmt-reformatted across multiple lines and its label argument swapped to agent-unregistered-label, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-fileissueintents-multiline-regression" { } "touch $out";
}
