# Eval-level checks for lib/prompt-contract.nix (issue #2245): a pure-data
# registry of the harness-owned shared prompt blocks (outcome contract,
# COMMS, CHECK/COMMIT, research verdict) that lib/mkHarness.nix now
# slices/injects from instead of hand-wiring via lib/prompt-inject.nix
# (issue #2246), plus (below) the registry of markers a Box's own output is
# expected to emit (validateMarkers, consumed by
# cmd/launcher/internal/promptassembly's Validate function, issue #2405).
# This file diffs each injectBlocks row's canonicalText against a
# from-scratch slice of the actual source .md files on disk, and asserts
# cross-row invariants across injectBlocks, validateMarkers,
# forbiddenMarkers, outcomeStatusSets, and sharedObligations (e.g. every
# forbiddenMarkers row's severity/carrier/message shape, every
# buildTimeRejectVerdicts branch, every sharedObligations row's real
# fragment content) so a future consumer can't silently break real behavior
# -- it no longer pins any of these registries' row count, row order, or
# per-row literal field values, since those can only ever fail on a
# deliberate data edit, not a real behavioral bug.
{ pkgs, ... }:
let
  promptContract = import ../../lib/prompt-contract.nix;
  promptInject = import ../../lib/prompt-inject.nix;
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasSuffix
    removeSuffix
    ;
  issuePromptSource = builtins.readFile ../../templates/default/prompts/issue-prompt.md;
  researchPromptSource = builtins.readFile ../../templates/default/prompts/research-prompt.md;
  # Shared fixture for the prompt-contract-shared-obligation-violations-for-*
  # tests below (issue #2699): identical `obligations` list reused by each,
  # only `contentBySource` differs per test.
  fixtureObligations = [
    {
      id = "fold-commits";
      branches = [
        {
          id = "inline";
          source = "fixture-inline.md";
        }
        {
          id = "orchestrator";
          source = "fixture-orchestrator.md";
        }
      ];
      requiredSubstrings = [ "fold your commits" ];
    }
  ];
in
{
  prompt-contract-canonical-text-outcome-matches-live-slice =
    let
      expected = promptInject.sliceFromMarker "# LAND THE CHANGE" issuePromptSource;
      out = promptContract.canonicalText.outcome;
      startMarker = "# LAND THE CHANGE";
    in
    assert assertMsg (
      out == expected
    ) "canonicalText.outcome must equal a from-scratch sliceFromMarker of issue-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0) "canonicalText.outcome must be non-empty";
    assert assertMsg (
      builtins.substring 0 (builtins.stringLength startMarker) out == startMarker
    ) "canonicalText.outcome must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-outcome-matches-live-slice" { } "touch $out";

  prompt-contract-canonical-text-comms-matches-live-slice =
    let
      expected = promptInject.sliceBetween "# COMMS" "# SCOUT" issuePromptSource;
      out = promptContract.canonicalText.comms;
      startMarker = "# COMMS";
    in
    assert assertMsg (
      out == expected
    ) "canonicalText.comms must equal a from-scratch sliceBetween of issue-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0) "canonicalText.comms must be non-empty";
    assert assertMsg (
      builtins.substring 0 (builtins.stringLength startMarker) out == startMarker
    ) "canonicalText.comms must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-comms-matches-live-slice" { } "touch $out";

  prompt-contract-canonical-text-check-matches-live-slice =
    let
      rawSlice = promptInject.sliceBetween "# CHECK" "# REVIEW" issuePromptSource;
      # issue #2462: the CHECK block's own endMarker ("# REVIEW") is now
      # glued directly onto the COMMIT_PUSH_READ_WRITE_STEP/
      # COMMIT_PUSH_READ_ONLY_STEP placeholder pair in issue-prompt.md's raw
      # source (no blank line in between) -- the only way to keep the
      # *rendered* prompt byte-identical, since the fragment loop's own
      # per-row "\n\n" append already supplies the separator (a template-
      # level blank line on top of that would double it up, see
      # lib/prompt-contract.nix's ensureTrailingBlankLine comment). That
      # leaves the raw slice ending exactly at the placeholder token with no
      # trailing blank line, so this reproduces lib/prompt-contract.nix's own
      # ensureTrailingBlankLine normalization here too -- a no-op for every
      # other row (comms/outcome/research-verdict), whose own endMarker is
      # still naturally preceded by a real blank line in source.
      expected = if hasSuffix "\n\n" rawSlice then rawSlice else removeSuffix "\n" rawSlice + "\n\n";
      out = promptContract.canonicalText.check;
      startMarker = "# CHECK";
    in
    assert assertMsg (out == expected)
      "canonicalText.check must equal a from-scratch sliceBetween of issue-prompt.md's own text, normalized to guarantee a trailing blank line (issue #2462's ensureTrailingBlankLine)";
    assert assertMsg (builtins.stringLength out > 0) "canonicalText.check must be non-empty";
    assert assertMsg (
      builtins.substring 0 (builtins.stringLength startMarker) out == startMarker
    ) "canonicalText.check must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-check-matches-live-slice" { } "touch $out";

  prompt-contract-canonical-text-research-verdict-matches-live-slice =
    let
      expected = promptInject.sliceFromMarker "# POST THE VERDICT" researchPromptSource;
      out = promptContract.canonicalText.research-verdict;
      startMarker = "# POST THE VERDICT";
    in
    assert assertMsg (out == expected)
      "canonicalText.research-verdict must equal a from-scratch sliceFromMarker of research-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0) "canonicalText.research-verdict must be non-empty";
    assert assertMsg (
      builtins.substring 0 (builtins.stringLength startMarker) out == startMarker
    ) "canonicalText.research-verdict must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-research-verdict-matches-live-slice" { }
      "touch $out";

  # mkHarness.nix injects each block at `marker` (the byId lookup driving
  # outcomeContractMarker/commsMarker/etc.) but canonicalText slices from
  # `startMarker` -- a real behavioral cross-check, not a value restatement,
  # since the two fields are free to diverge and nothing else pins them equal.
  prompt-contract-inject-blocks-every-row-marker-equals-start-marker =
    let
      bad = builtins.filter (r: r.marker != r.startMarker) promptContract.injectBlocks;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every injectBlocks row's marker must equal its own startMarker, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-inject-blocks-every-row-marker-equals-start-marker" { }
      "touch $out";

  # Pins forbiddenMarkers (issue #2464): the opposite-direction registry from
  # validateMarkers above -- every row here names a write-capable git/gh
  # operation a read-only Box's rendered prompt must never order the Driver
  # to run.
  prompt-contract-forbidden-markers-every-row-carrier-fragment-body =
    let
      bad = builtins.filter (r: r.carrier != "fragment-body") promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's carrier must be 'fragment-body', offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-carrier-fragment-body" { }
      "touch $out";

  prompt-contract-forbidden-markers-every-row-severity-reject =
    let
      bad = builtins.filter (r: r.severity != "reject") promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's severity must be 'reject', offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-severity-reject" { } "touch $out";

  prompt-contract-forbidden-markers-every-row-when-box-access-read-only =
    let
      bad = builtins.filter (r: r.when != "boxAccessReadOnly") promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's when must be 'boxAccessReadOnly', offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-when-box-access-read-only" { }
      "touch $out";

  prompt-contract-forbidden-markers-every-row-message-mentions-own-marker =
    let
      bad = builtins.filter (r: !(pkgs.lib.hasInfix r.marker r.message)) promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's message must contain its own marker substring, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-message-mentions-own-marker" { }
      "touch $out";

  # issue #2499: every row's kind must be a known value -- structural
  # coverage only (does the field hold a value someone typo'd), not
  # behavioral. promptassembly.Validate no longer branches on kind at all
  # (issue #2513 deleted its forbidden-marker loop); the two places that do
  # still branch on kind are each pinned separately:
  #   - lib/prompt-contract.nix's buildTimeForbiddenMarkerViolations filters
  #     to kind == "substring" rows only -- pinned by this file's sibling
  #     build-time-forbidden-marker-fragment-gh-api-mutation-kind-not-scanned
  #     check (nix/checks/prompts.nix).
  #   - readonlyguards.go's command-shim rendering switches on kindGhAPIMutation
  #     for its runtime argument scan -- pinned Go-side by
  #     cmd/launcher/internal/readonlyguards/readonlyguards_test.go's
  #     TestInstall_GhAPIMutationRejectsMutatingMethod.
  prompt-contract-forbidden-markers-every-row-kind-known-value =
    let
      knownKinds = [
        "substring"
        "gh-api-mutation"
      ];
      bad = builtins.filter (r: !(builtins.elem r.kind knownKinds)) promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's kind must be one of [${concatStringsSep ", " knownKinds}], offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-kind-known-value" { } "touch $out";

  # issue #2499: every row's enforce must be a known value naming which
  # runtime layer (if any) backstops the row -- "prompt-only" for rows with
  # no runtime backstop, since a runtime guard would collide with a
  # legitimate in-box use of the same operation.
  prompt-contract-forbidden-markers-every-row-enforce-known-value =
    let
      knownEnforce = [
        "command-shim"
        "git-hook"
        "prompt-only"
      ];
      bad = builtins.filter (r: !(builtins.elem r.enforce knownEnforce)) promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's enforce must be one of [${concatStringsSep ", " knownEnforce}], offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-enforce-known-value" { } "touch $out";

  # issue #2509 (Finding 2): every row whose enforce is "git-hook" or
  # "command-shim" -- the rows readonlyguards.go actually renders into a
  # runtime shim/hook script -- must carry a runtimeMessage distinct from its
  # (prompt-validator-facing) message field. A "prompt-only" row is never
  # runtime-rendered, so it carries no runtimeMessage at all.
  prompt-contract-forbidden-markers-runtime-rendered-rows-have-runtime-message =
    let
      runtimeRendered = builtins.filter (
        r: r.enforce == "git-hook" || r.enforce == "command-shim"
      ) promptContract.forbiddenMarkers;
      bad = builtins.filter (
        r: !(r ? runtimeMessage) || r.runtimeMessage == "" || r.runtimeMessage == r.message
      ) runtimeRendered;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every git-hook/command-shim forbiddenMarkers row must carry a non-empty runtimeMessage distinct from its message, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-runtime-rendered-rows-have-runtime-message" { }
      "touch $out";

  prompt-contract-forbidden-markers-prompt-only-rows-have-no-runtime-message =
    let
      promptOnly = builtins.filter (r: r.enforce == "prompt-only") promptContract.forbiddenMarkers;
      bad = builtins.filter (r: r ? runtimeMessage) promptOnly;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every prompt-only forbiddenMarkers row must carry no runtimeMessage, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-prompt-only-rows-have-no-runtime-message" { }
      "touch $out";

  # Pins buildTimeRejectVerdicts (issue #2250): the build-time reject arm that
  # resolves each validateMarkers "reject" row into one of ok/reject/advise,
  # given whatever static gate/content knowledge is available at build time.
  # A future consumer (lib/mkHarness.nix) supplies the real staticGates/
  # contentByRowId; this check exercises the pure function in isolation with
  # minimal inline fixtures.
  prompt-contract-build-time-reject-verdicts-reject-when-gate-true-and-marker-missing =
    let
      out = promptContract.buildTimeRejectVerdicts {
        staticGates = {
          orchestratorEnabled = true;
        };
        contentByRowId = {
          reviewer-verdict = "no marker here";
        };
      };
      row = builtins.head (builtins.filter (r: r.id == "reviewer-verdict") out);
    in
    assert assertMsg (row.verdict == "reject")
      "buildTimeRejectVerdicts: reviewer-verdict must be 'reject' when orchestratorEnabled=true and its content lacks the marker, got: ${row.verdict}";
    pkgs.runCommand
      "prompt-contract-build-time-reject-verdicts-reject-when-gate-true-and-marker-missing"
      { }
      "touch $out";

  prompt-contract-build-time-reject-verdicts-advise-when-gate-false-and-marker-missing =
    let
      out = promptContract.buildTimeRejectVerdicts {
        staticGates = {
          orchestratorEnabled = false;
        };
        contentByRowId = {
          reviewer-verdict = "no marker here";
        };
      };
      row = builtins.head (builtins.filter (r: r.id == "reviewer-verdict") out);
    in
    assert assertMsg (row.verdict == "advise")
      "buildTimeRejectVerdicts: reviewer-verdict must be 'advise' when orchestratorEnabled=false and its content lacks the marker, got: ${row.verdict}";
    pkgs.runCommand
      "prompt-contract-build-time-reject-verdicts-advise-when-gate-false-and-marker-missing"
      { }
      "touch $out";

  prompt-contract-build-time-reject-verdicts-ok-when-marker-present =
    let
      out = promptContract.buildTimeRejectVerdicts {
        staticGates = {
          orchestratorEnabled = true;
        };
        contentByRowId = {
          reviewer-verdict = "the VERDICT: line is here";
        };
      };
      row = builtins.head (builtins.filter (r: r.id == "reviewer-verdict") out);
    in
    assert assertMsg (row.verdict == "ok")
      "buildTimeRejectVerdicts: reviewer-verdict must be 'ok' when its content contains the marker (regardless of the gate), got: ${row.verdict}";
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-ok-when-marker-present" { }
      "touch $out";

  prompt-contract-build-time-reject-verdicts-advise-when-fully-unresolved =
    let
      out = promptContract.buildTimeRejectVerdicts {
        staticGates = { };
        contentByRowId = { };
      };
      verdicts = map (r: r.verdict) out;
    in
    assert assertMsg (builtins.all (v: v == "advise") verdicts)
      "buildTimeRejectVerdicts: every row must be 'advise' when both staticGates and contentByRowId are entirely unresolved, got: [${concatStringsSep ", " verdicts}]";
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-advise-when-fully-unresolved" { }
      "touch $out";

  prompt-contract-build-time-reject-verdicts-covers-every-reject-row =
    let
      out = promptContract.buildTimeRejectVerdicts {
        staticGates = { };
        contentByRowId = { };
      };
      expectedIds = map (r: r.id) (
        builtins.filter (r: r.severity == "reject") promptContract.validateMarkers
      );
      outIds = map (r: r.id) out;
    in
    assert assertMsg (builtins.length out == builtins.length expectedIds)
      "buildTimeRejectVerdicts must return exactly one entry per severity==\"reject\" validateMarkers row (currently ${toString (builtins.length expectedIds)}), got: ${toString (builtins.length out)}";
    assert assertMsg (outIds == expectedIds)
      "buildTimeRejectVerdicts must iterate validateMarkers' own severity==\"reject\" rows in order rather than a hand-duplicated list, expected ids [${concatStringsSep ", " expectedIds}], got: [${concatStringsSep ", " outIds}]";
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-covers-every-reject-row" { }
      "touch $out";

  # Pins buildTimeResearchDirectFileViolations (issue #2595, ADR 0041): the
  # build-time backstop proving a research prompt (research-prompt.md /
  # research-self-contained-prompt.md) never references one of
  # lib/fragments.nix's FILER_FILE_DIRECT*-gated rows' envsubst placeholder --
  # research filing is host-mediated/relay-only by design, so wiring a direct-
  # file var into a research prompt must fail the build, not silently
  # regress. Exercises the pure function in isolation with minimal inline
  # fixtures first, then against the real registry/prompt content below.
  prompt-contract-build-time-research-direct-file-violations-detects-direct-var-in-content =
    let
      out = promptContract.buildTimeResearchDirectFileViolations {
        directFileFragmentRows = [
          {
            fragment = "filer-file-direct.md";
            var = "SOME_VAR";
          }
        ];
        researchPromptContentByName = {
          "research-prompt.md" = "before \${SOME_VAR} after";
        };
      };
    in
    assert assertMsg (builtins.length out == 1)
      "buildTimeResearchDirectFileViolations must report one violation when a research prompt's content contains a direct-file row's \${VAR} placeholder, got: ${toString (builtins.length out)}";
    pkgs.runCommand
      "prompt-contract-build-time-research-direct-file-violations-detects-direct-var-in-content"
      { }
      "touch $out";

  prompt-contract-build-time-research-direct-file-violations-none-when-var-absent =
    let
      out = promptContract.buildTimeResearchDirectFileViolations {
        directFileFragmentRows = [
          {
            fragment = "filer-file-direct.md";
            var = "SOME_VAR";
          }
        ];
        researchPromptContentByName = {
          "research-prompt.md" = "no direct-file placeholders here";
        };
      };
    in
    assert assertMsg (out == [ ])
      "buildTimeResearchDirectFileViolations must report no violations when the content contains none of the direct-file rows' \${VAR} placeholders, got: ${toString (builtins.length out)}";
    pkgs.runCommand "prompt-contract-build-time-research-direct-file-violations-none-when-var-absent"
      { }
      "touch $out";

  prompt-contract-build-time-research-direct-file-violations-none-when-no-direct-rows =
    let
      out = promptContract.buildTimeResearchDirectFileViolations {
        directFileFragmentRows = [ ];
        researchPromptContentByName = {
          "research-prompt.md" = "\${SOME_VAR} and \${ANYTHING_ELSE}";
        };
      };
    in
    assert assertMsg (out == [ ])
      "buildTimeResearchDirectFileViolations must report no violations when directFileFragmentRows is empty, regardless of content, got: ${toString (builtins.length out)}";
    pkgs.runCommand
      "prompt-contract-build-time-research-direct-file-violations-none-when-no-direct-rows"
      { }
      "touch $out";

  prompt-contract-build-time-research-direct-file-violations-violation-names-fragment-and-var =
    let
      out = promptContract.buildTimeResearchDirectFileViolations {
        directFileFragmentRows = [
          {
            fragment = "filer-file-direct.md";
            var = "FILER_FILE_DIRECT_STEP";
          }
        ];
        researchPromptContentByName = {
          "research-self-contained-prompt.md" = "\${FILER_FILE_DIRECT_STEP}";
        };
      };
      violation = builtins.head out;
    in
    assert assertMsg (builtins.length out == 1)
      "buildTimeResearchDirectFileViolations must report exactly one violation for this fixture, got: ${toString (builtins.length out)}";
    assert assertMsg (violation.fragment == "filer-file-direct.md")
      "buildTimeResearchDirectFileViolations violation record must name the offending row's fragment, got: ${violation.fragment}";
    assert assertMsg (violation.var == "FILER_FILE_DIRECT_STEP")
      "buildTimeResearchDirectFileViolations violation record must name the offending row's var, got: ${violation.var}";
    assert assertMsg (violation.promptName == "research-self-contained-prompt.md")
      "buildTimeResearchDirectFileViolations violation record must name the offending research prompt, got: ${violation.promptName}";
    pkgs.runCommand
      "prompt-contract-build-time-research-direct-file-violations-violation-names-fragment-and-var"
      { }
      "touch $out";

  # The real registry check (issue #2595, ADR 0041): filters lib/fragments.nix's
  # real fragment rows down to the FILER_FILE_DIRECT*-gated ones and feeds the
  # real, on-disk content of both research prompts -- proving today's "holds
  # by construction" claim documented at lib/fragments.nix's
  # research-file-issues-relay.md row (the one row wiring FILER_FILE_RELAY into
  # a research-only fragment) actually holds, and will keep failing the build
  # the moment it stops holding.
  prompt-contract-build-time-research-direct-file-violations-real-registry-passes =
    let
      fragments = import ../../lib/fragments.nix;
      directFileFragmentRows = builtins.filter (
        row: pkgs.lib.hasInfix "FILER_FILE_DIRECT" row.gate
      ) fragments;
      out = promptContract.buildTimeResearchDirectFileViolations {
        inherit directFileFragmentRows;
        researchPromptContentByName = {
          "research-prompt.md" = builtins.readFile ../../templates/default/prompts/research-prompt.md;
          "research-self-contained-prompt.md" =
            builtins.readFile ../../templates/default/prompts/research-self-contained-prompt.md;
        };
      };
    in
    assert assertMsg (directFileFragmentRows != [ ])
      "prompt-contract-build-time-research-direct-file-violations-real-registry-passes: expected at least one FILER_FILE_DIRECT*-gated row in lib/fragments.nix, got none -- fixture is vacuous";
    assert assertMsg (out == [ ])
      "buildTimeResearchDirectFileViolations must return no violations against the real fragments.nix registry and real research prompt content (ADR 0041: research filing is host-mediated/relay-only), got: ${builtins.toJSON out}";
    pkgs.runCommand "prompt-contract-build-time-research-direct-file-violations-real-registry-passes"
      { }
      "touch $out";

  # Pins outcomeStatusSets' research row (issue #2524): the row must be
  # derived from lib/research-verdicts.nix's defaultVerdicts (the single
  # source of truth for the built-in research verdict tokens) plus the
  # "blocked" crash/no-verdict escape hatch, never a hand-typed restatement
  # of that list -- so the research vocabulary is rooted in exactly one
  # place.
  prompt-contract-outcome-status-sets-research-row-derives-from-verdict-registry =
    let
      researchVerdicts = import ../../lib/research-verdicts.nix;
      out = promptContract.outcomeStatusesFor "research";
      expected = (map (v: v.verdict) researchVerdicts.defaultVerdicts) ++ [ "blocked" ];
    in
    assert assertMsg (out == expected)
      "outcomeStatusSets' research row's statuses must equal lib/research-verdicts.nix's defaultVerdicts' verdict tokens (in order) plus \"blocked\", got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-outcome-status-sets-research-row-derives-from-verdict-registry" { }
      "touch $out";

  # Pins markerChannels' row order (issue #2974, parent #2972): the single
  # authoritative statement of the 5 marker channels a Box's output carries.
  # Order matters here -- lib/renderers.nix's renderMarkerChannelsGo walks
  # this list in exactly this order to emit typed constants
  # (cmd/launcher/internal/outcome/markerchannels_gen.go), so a reorder here
  # is a breaking change to that generated code, not a cosmetic one.
  prompt-contract-marker-channels-ids-known =
    let
      out = map (r: r.id) promptContract.markerChannels;
      expected = [
        "outcome"
        "comment"
        "pr-intent"
        "issue-intent"
        "review-verdict"
      ];
    in
    assert assertMsg (out == expected)
      "markerChannels' ids must be exactly [${concatStringsSep ", " expected}] in that order, got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-marker-channels-ids-known" { } "touch $out";

  prompt-contract-marker-channels-every-row-defense-known-value =
    let
      bad = builtins.filter (
        r:
        !(builtins.elem r.defense [
          "structural"
          "nonce"
          "fold"
        ])
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every markerChannels row's defense must be one of \"structural\"/\"nonce\"/\"fold\", offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-every-row-defense-known-value" { } "touch $out";

  prompt-contract-marker-channels-every-row-carrier-known-value =
    let
      bad = builtins.filter (
        r:
        !(builtins.elem r.carrier [
          "final-message"
          "mid-run-log"
          "subagent-first-line"
        ])
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every markerChannels row's carrier must be one of \"final-message\"/\"mid-run-log\"/\"subagent-first-line\", offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-every-row-carrier-known-value" { } "touch $out";

  # Unlike defense/carrier above, fieldShape has no enum to pin -- it's a
  # human-readable grammar (lib/prompt-contract.nix:945-947), not
  # machine-parsed. But it still had zero invariant coverage: a row could
  # land with a blank or missing fieldShape and nothing would catch it. This
  # is deliberately not a grammar validator (that's future work, if ever
  # needed) -- just the minimal non-empty-string assertion that stops the
  # blank/malformed case.
  prompt-contract-marker-channels-every-row-field-shape-non-empty =
    let
      bad = builtins.filter (
        r: !(r ? fieldShape) || !(builtins.isString r.fieldShape) || r.fieldShape == ""
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every markerChannels row's fieldShape must be a non-empty string, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-every-row-field-shape-non-empty" { }
      "touch $out";

  # Cross-registry drift guard: markerChannels' `token` and validateMarkers'
  # `marker` name the same literal for every channel that has a
  # validateMarkers row (every one except "outcome", which validateMarkers
  # never scans for since the outcome contract is validated structurally,
  # ADR 0047, not via this marker-presence registry). Ties the two registries
  # together so a future edit to one marker spelling can't silently diverge
  # from the other.
  prompt-contract-marker-channels-token-matches-validate-markers =
    let
      bad = builtins.filter (
        r: r.id != "outcome" && !(builtins.any (v: v.marker == r.token) promptContract.validateMarkers)
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every non-outcome markerChannels row's token must match some validateMarkers row's marker, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-token-matches-validate-markers" { } "touch $out";

  # Pins sharedObligationViolationsFor (issue #2699): sharedObligations'
  # consumer function, which checks that BOTH branches of a paired prompt
  # fork carry every literal substring a shared obligation declares.
  # Exercised here with fixtureObligations (a hand-built obligations list)
  # and synthetic contentBySource -- the point of taking contentBySource as
  # an explicit argument is that this stays testable with fixture content
  # proving the check can actually fail, independent of the real
  # sharedObligations registry (exercised separately below, against the
  # real on-disk fragments).
  prompt-contract-shared-obligation-violations-for-detects-missing-substring =
    let
      contentBySource = {
        "fixture-inline.md" = "Before you finish, fold your commits into one.";
        "fixture-orchestrator.md" = "Before you finish, tidy up your work.";
      };
      out = promptContract.sharedObligationViolationsFor fixtureObligations contentBySource;
    in
    assert assertMsg (builtins.length out == 1)
      "sharedObligationViolationsFor must report exactly one violation when exactly one branch's content is missing the declared substring, got ${toString (builtins.length out)}";
    assert assertMsg ((builtins.head out).branchId == "orchestrator")
      "sharedObligationViolationsFor must name the offending branch's id ('orchestrator'), got '${(builtins.head out).branchId}'";
    pkgs.runCommand "prompt-contract-shared-obligation-violations-for-detects-missing-substring" { }
      "touch $out";

  prompt-contract-shared-obligation-violations-for-empty-when-all-branches-satisfy =
    let
      contentBySource = {
        "fixture-inline.md" = "Before you finish, fold your commits into one.";
        "fixture-orchestrator.md" = "Before you finish, fold your commits into one too.";
      };
      out = promptContract.sharedObligationViolationsFor fixtureObligations contentBySource;
    in
    assert assertMsg (out == [ ])
      "sharedObligationViolationsFor must return no violations when every branch's content satisfies every declared substring, got: [${
        concatStringsSep ", " (map (v: v.branchId) out)
      }]";
    pkgs.runCommand "prompt-contract-shared-obligation-violations-for-empty-when-all-branches-satisfy"
      { }
      "touch $out";

  # Acceptance criterion (issue #2699): a violation's pre-rendered `message`
  # must name BOTH the offending fork branch and the obligation it's missing
  # -- not just carry those ids in separate structured fields no caller reads
  # before rendering. The other tests in this group only assert on
  # `.branchId`/list length, so a future edit that silently drops
  # `${obligation.id}` (or `${branch.id}`) from the message template would
  # stay green everywhere else.
  prompt-contract-shared-obligation-violations-for-message-names-branch-and-obligation =
    let
      contentBySource = {
        "fixture-inline.md" = "Before you finish, fold your commits into one.";
        "fixture-orchestrator.md" = "Before you finish, tidy up your work.";
      };
      out = promptContract.sharedObligationViolationsFor fixtureObligations contentBySource;
      message = (builtins.head out).message;
    in
    assert assertMsg
      (pkgs.lib.hasInfix "orchestrator" message && pkgs.lib.hasInfix "fold-commits" message)
      "sharedObligationViolationsFor's message must name both the at-fault branch ('orchestrator') and the missing obligation ('fold-commits'), got: '${message}'";
    pkgs.runCommand
      "prompt-contract-shared-obligation-violations-for-message-names-branch-and-obligation"
      { }
      "touch $out";

  # Proves the real sharedObligations registry's own "commit-folding" row
  # (issue #2699) can actually go red: exercises sharedObligationViolationsFor
  # directly against that real row, but with the inline branch's content
  # swapped for a synthetic string carrying none of the row's declared
  # substrings while the orchestrator branch keeps its real, on-disk content.
  # If a future edit ever drops the row (or the row's own `requiredSubstrings`
  # list), this degrades to an empty result and fails loudly instead of
  # silently passing. Filters `out` down to the commit-folding obligation's
  # own violations before asserting count/branchId, so adding a second,
  # unrelated obligation to the registry later can't fail this test on an
  # incidental extra violation it was never about.
  prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-folding =
    let
      brokenInlineContent = "no folding instruction of any kind in this fragment";
      realOrchestratorContent = builtins.readFile ../../templates/default/prompts/fragments/commit-rework-orchestrator.md;
      out = promptContract.sharedObligationViolationsFor promptContract.sharedObligations {
        "fragments/review-loop-inline.md" = brokenInlineContent;
        "fragments/commit-rework-orchestrator.md" = realOrchestratorContent;
      };
      commitFoldingViolations = builtins.filter (v: v.obligationId == "commit-folding") out;
    in
    assert assertMsg (builtins.length commitFoldingViolations == 1)
      "sharedObligationViolationsFor must report exactly one commit-folding violation when the real sharedObligations registry's inline branch content is swapped for content missing the declared obligation, got ${toString (builtins.length commitFoldingViolations)}";
    assert assertMsg ((builtins.head commitFoldingViolations).branchId == "review-loop-inline")
      "sharedObligationViolationsFor must name the offending branch's id ('review-loop-inline'), got '${(builtins.head commitFoldingViolations).branchId}'";
    pkgs.runCommand "prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-folding"
      { }
      "touch $out";

  # Enforcing check (issue #2699): the real sharedObligations registry's rows
  # must hold against the real, on-disk fragment content every branch
  # declares -- fails loudly, naming the offending branch and missing
  # substring(s), the moment a future edit to either fragment file silently
  # drops a shared obligation the other branch still carries.
  prompt-contract-shared-obligations-satisfied =
    let
      violations = promptContract.sharedObligationViolations;
      messages = map (v: v.message) violations;
    in
    assert assertMsg (
      violations == [ ]
    ) "shared prompt-fork obligations violated:\n${concatStringsSep "\n" messages}";
    pkgs.runCommand "prompt-contract-shared-obligations-satisfied" { } "touch $out";
}
