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
  # The three known surfaces that write or create a label literal outside
  # doctor.Run()'s registry-derived TriageLabelMeta path (issue #2528 AC1).
  # Each is paired with a dedicated `extract` function — not a generic
  # markers-line-select + whole-line-tokenize + isLabelShaped-filter
  # pipeline — that pulls the label literal from its own known syntactic
  # position, scanned across a marker-to-terminator SPAN rather than
  # restricted to one line (a per-line restriction is exactly what let a
  # gofmt-clean reformat or a shell line-continuation go unextracted before —
  # see the two regression checks named below):
  #   - issue_intent.go: every quoted Go string literal between the
  #     issueIntentLabels = []string{ marker and that declaration's closing
  #     }, wherever the source wraps it onto multiple lines — see
  #     label-registry-covers-harness-writes-reformat-regression below, which
  #     reproduces the gofmt-clean multi-line var block a second label makes
  #     inevitable.
  #   - filer-label-direct.md: the shell bareword immediately following the
  #     literal words `label create ` (unquoted, unlike the other two), after
  #     joinBackslashNewline (defined by labelsWrittenBy below) has folded
  #     every `\`-continued line into its continuation first, so a
  #     line-wrapped `gh label create \` still yields the literal that
  #     follows on the next line — see
  #     label-registry-covers-harness-writes-continuation-regression below.
  #   - filer-label-direct-forgejo.md: specifically the VALUE of the
  #     "name":"..." JSON key on that line.
  # A shape-only filter (isLabelShaped, which requires an agent- prefix)
  # fails open on a de-prefixed rename: if issueIntentLabels'
  # "agent-review-finding" were renamed to "review-finding", isLabelShaped
  # would filter it out of the token stream entirely, so labelsWrittenBy
  # would return [] for that surface and label-registry-covers-harness-writes
  # would pass silently even though the harness now writes an unregistered
  # label — label-registry-covers-harness-writes-regression below reproduces
  # and closes that gap. Conversely, simply broadening isLabelShaped to
  # accept any hyphenated lowercase word (instead of per-surface extraction)
  # would reintroduce a false positive: the forgejo fragment's
  # "name":"agent-review-finding" line also carries a "description" value
  # containing the lowercase hyphenated phrase "non-blocking", which a
  # whole-line tokenize + broadened-shape-filter would wrongly flag as a
  # phantom missing-from-registry label on the CURRENT, correct source.
  # Per-surface, span-scanned extraction avoids all three failure modes by
  # only ever looking where the literal is actually syntactically expected to
  # be, regardless of how the surrounding source is line-wrapped.
  harnessSurfaces = {
    "cmd/launcher/internal/settle/issue_intent.go" = {
      src = builtins.readFile ../../cmd/launcher/internal/settle/issue_intent.go;
      extract = extractIssueIntentLabels;
    };
    "templates/default/prompts/fragments/filer-label-direct.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct.md;
      extract = extractLabelCreateTokens;
    };
    "templates/default/prompts/fragments/filer-label-direct-forgejo.md" = {
      src = builtins.readFile ../../templates/default/prompts/fragments/filer-label-direct-forgejo.md;
      extract = extractNameFieldTokens;
    };
  };
  # The literal(s) sit inside double-quoted Go string literals between the
  # issueIntentLabels = []string{ marker and that declaration's closing }.
  # Restricting extraction to a single line (as an earlier version of this
  # check did, scanning only lines containing the bare substring
  # "issueIntentLabels") fails open the moment a second label makes gofmt's
  # multi-line slice-literal layout the natural shape:
  #   var issueIntentLabels = []string{
  #     "agent-review-finding",
  #     "agent-something-else",
  #   }
  # None of those continuation lines contain "issueIntentLabels" themselves,
  # so a per-line scan would silently extract nothing. Splitting the whole
  # source on the marker instead, then again on the first "}" that follows,
  # isolates exactly the declaration's body regardless of how it's
  # line-wrapped — splitString has no notion of lines, so newlines inside
  # that span are inert. Matching the anchor to "issueIntentLabels = []string{"
  # rather than the bare identifier also skips the doc comment above the var
  # (which mentions "issueIntentLabels" in prose but never followed by
  # " = []string{"). Match-filtering the quote-split segments to the
  # label-literal shape (lowercase, digits, hyphens) then discards the
  # surrounding Go syntax (whitespace, commas, tabs) without requiring an
  # agent- prefix, so a de-prefixed rename still extracts correctly.
  extractIssueIntentLabels =
    src:
    let
      marker = "issueIntentLabels = []string{";
      parts = splitString marker src;
    in
    if builtins.length parts < 2 then
      [ ]
    else
      let
        rest = builtins.elemAt parts 1;
        declBody = builtins.head (splitString "}" rest);
      in
      filter (s: builtins.match "[a-z][a-z0-9-]*" s != null) (splitString "\"" declBody);
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
  # extractLabelCreateTokens and extractNameFieldTokens both scan line by
  # line, so a shell `\`-continued line (e.g. `gh label create \` with the
  # actual label bareword on the following line) would otherwise put the
  # marker on one line and the literal it's supposed to precede on the next,
  # yielding no match on either — the same per-line fail-open class
  # extractIssueIntentLabels closes above for Go's multi-line slice layout.
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
  #      longer matches the source (a requote, a reformat, a var rename) and
  #      the check below would otherwise pass vacuously instead of validating
  #      anything for that surface — exactly the class of gap the three
  #      *-regression checks below reproduce (a quoted bareword an unquoted
  #      extractor can't see, `"name": "..."` spacing a no-space marker can't
  #      see, and a renamed Go var an anchored marker can't see).
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

  # Registry ⊇ Harness direction (issue #2528): asserts every label literal
  # the three harnessSurfaces above write/create is a name somewhere in
  # lib/labels.nix. This is the check that would have failed before
  # lib/labels.nix grew a reviewFinding row for agent-review-finding.
  label-registry-covers-harness-writes =
    assert
      (assertHarnessWritesInRegistry {
        inherit harnessSurfaces;
        registryLabels = allRegistryLabels;
      }) == [ ];
    pkgs.runCommand "label-registry-covers-harness-writes" { } "touch $out";

  # Regression guard (issue #2528): proves assertHarnessWritesInRegistry
  # actually catches a de-prefixed rename of a harness-written label. Doctors
  # the real issue_intent.go source (the file itself, via replaceStrings on
  # its own builtins.readFile content, not a synthetic fixture) so
  # issueIntentLabels' "agent-review-finding" loses its agent- prefix down to
  # "review-finding", builds a doctored harnessSurfaces with only that one
  # surface's src swapped in, and asserts via tryEval that
  # assertHarnessWritesInRegistry now correctly REJECTS it — "review-finding"
  # is not a name in lib/labels.nix's registry. Before the per-surface
  # extraction fix, a shape-only filter (isLabelShaped, which requires an
  # agent- prefix) would have discarded "review-finding" before it ever
  # reached the registry-membership check, so labelsWrittenBy would have
  # returned [] for that surface and this regression would have found
  # nothing to reject — exactly the fail-open gap this check closes.
  label-registry-covers-harness-writes-regression =
    let
      doctoredIssueIntentSrc =
        replaceStrings [ ''"agent-review-finding"'' ] [ ''"review-finding"'' ]
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/issue_intent.go" =
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go"
          // {
            src = doctoredIssueIntentSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-regression: expected assertHarnessWritesInRegistry to reject a synthetic issue_intent.go with agent-review-finding de-prefixed to review-finding, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-regression" { } "touch $out";

  # Regression guard (issue #2528): proves extractIssueIntentLabels still
  # catches an unregistered label once issueIntentLabels grows a second entry
  # and gofmt reformats the var block onto multiple lines — the gofmt-clean,
  # multi-line shape a second label makes inevitable, and the exact layout a
  # per-line scan (an earlier version of this check) silently returned []
  # for. Doctors the real issue_intent.go source by replacing its single-line
  # declaration with that multi-line form, adding
  # "agent-unregistered-label" (a name lib/labels.nix does not carry)
  # alongside the real "agent-review-finding", and asserts via tryEval that
  # assertHarnessWritesInRegistry rejects it. Before the marker-to-brace span
  # fix, no line in the doctored source would contain the bare substring
  # "issueIntentLabels" the old per-line filter looked for, so
  # labelsWrittenBy would have returned [] and this regression would have
  # found nothing to reject.
  label-registry-covers-harness-writes-reformat-regression =
    let
      doctoredIssueIntentSrc =
        replaceStrings
          [ ''var issueIntentLabels = []string{"agent-review-finding"}'' ]
          [
            ''
              var issueIntentLabels = []string{
              	"agent-review-finding",
              	"agent-unregistered-label",
              }''
          ]
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/issue_intent.go" =
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go"
          // {
            src = doctoredIssueIntentSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-reformat-regression: expected assertHarnessWritesInRegistry to reject a synthetic issue_intent.go with issueIntentLabels reformatted onto multiple lines and an unregistered second label added, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-reformat-regression" { } "touch $out";

  # Regression guard (issue #2528): proves extractLabelCreateTokens still
  # catches an unregistered label once the `gh label create` invocation is
  # line-wrapped with a shell `\` continuation — the same per-line fail-open
  # class the reformat regression above closes for issue_intent.go, but for
  # the shell-continuation shape instead of gofmt's multi-line braces.
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
  # extractIssueIntentLabels' anchored `issueIntentLabels = []string{` marker
  # breaks on a var rename. Doctors issue_intent.go so the declaration reads
  # `issueIntentReviewLabels = []string{...}` instead — splitString never
  # finds the old marker, so the surface silently extracts [ ], which —
  # before the emptyOffenders check existed — let
  # label-registry-covers-harness-writes pass even though the harness still
  # writes agent-review-finding (now unobserved) and could just as easily
  # have started writing something unregistered alongside it. Confirms via
  # tryEval that assertHarnessWritesInRegistry now rejects this instead.
  label-registry-covers-harness-writes-var-rename-regression =
    let
      doctoredIssueIntentSrc =
        replaceStrings [ "issueIntentLabels = []string{" ] [ "issueIntentReviewLabels = []string{" ]
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go".src;
      doctoredHarnessSurfaces = harnessSurfaces // {
        "cmd/launcher/internal/settle/issue_intent.go" =
          harnessSurfaces."cmd/launcher/internal/settle/issue_intent.go"
          // {
            src = doctoredIssueIntentSrc;
          };
      };
      result = builtins.tryEval (assertHarnessWritesInRegistry {
        harnessSurfaces = doctoredHarnessSurfaces;
        registryLabels = allRegistryLabels;
      });
    in
    assert assertMsg (!result.success)
      "label-registry-covers-harness-writes-var-rename-regression: expected assertHarnessWritesInRegistry to reject a synthetic issue_intent.go with issueIntentLabels renamed to issueIntentReviewLabels, but it evaluated successfully";
    pkgs.runCommand "label-registry-covers-harness-writes-var-rename-regression" { } "touch $out";
}
