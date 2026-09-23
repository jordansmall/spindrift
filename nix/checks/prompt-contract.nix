# Eval-level checks for lib/prompt-contract.nix (issues #2245, #2246, #2405).
# Each check diffs a registry row against the real on-disk source, or asserts
# a cross-row invariant. It deliberately pins no registry's row count, row
# order, or per-row literal field values: those can only fail on a deliberate
# data edit, never on a real behavioral bug.
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
  # tests below (issue #2699): only `contentBySource` differs per test.
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
  # Shared helper for the prompt-contract-shared-obligations-detects-drift-*
  # checks below (issues #2699, #3610): each proves a real sharedObligations
  # registry row can go red by swapping the inline branch's content for one
  # carrying none of the declared substrings, while the named orchestrator-side
  # branch keeps its real on-disk content. Filtering to the given obligationId
  # keeps a later, unrelated obligation from failing the check on an extra.
  sharedObligationDriftCheck =
    {
      obligationId,
      orchestratorFragmentFile,
      brokenInlineContent,
    }:
    let
      checkName = "prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-${obligationId}";
      orchestratorSource = "fragments/${orchestratorFragmentFile}";
      realOrchestratorContent = builtins.readFile (
        ../../templates/default/prompts/fragments/${orchestratorFragmentFile}
      );
      out = promptContract.sharedObligationViolationsFor promptContract.sharedObligations {
        "fragments/review-loop-inline.md" = brokenInlineContent;
        "${orchestratorSource}" = realOrchestratorContent;
      };
      violations = builtins.filter (v: v.obligationId == obligationId) out;
    in
    assert assertMsg (builtins.length violations == 1)
      "sharedObligationViolationsFor must report exactly one ${obligationId} violation when the real sharedObligations registry's inline branch content is swapped for content missing the declared obligation, got ${toString (builtins.length violations)}";
    assert assertMsg ((builtins.head violations).branchId == "review-loop-inline")
      "sharedObligationViolationsFor must name the offending branch's id ('review-loop-inline'), got '${(builtins.head violations).branchId}'";
    pkgs.runCommand checkName { } "touch $out";
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
      # issue #2462: issue-prompt.md glues the CHECK block's endMarker
      # ("# REVIEW") straight onto the COMMIT_PUSH_* placeholder pair with no
      # blank line, because the fragment loop already appends "\n\n". The raw
      # slice therefore ends without a trailing blank line, so this reproduces
      # lib/prompt-contract.nix's own ensureTrailingBlankLine normalization.
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

  # mkHarness.nix injects each block at `marker` but canonicalText slices from
  # `startMarker`. The two fields are free to diverge and nothing else pins
  # them equal, so this is a real cross-check, not a value restatement.
  prompt-contract-inject-blocks-every-row-marker-equals-start-marker =
    let
      bad = builtins.filter (r: r.marker != r.startMarker) promptContract.injectBlocks;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every injectBlocks row's marker must equal its own startMarker, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-inject-blocks-every-row-marker-equals-start-marker" { }
      "touch $out";

  # forbiddenMarkers (issue #2464) is the opposite-direction registry from
  # validateMarkers: every row names a write-capable git/gh operation a
  # read-only Box's rendered prompt must never order the Driver to run.
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

  # issue #2996 decided pr-intent/issue-intent/research-issue-intent stay
  # "warn" rather than promote to "reject". A future severity edit must touch
  # this list by hand instead of silently flipping which rows block the build.
  prompt-contract-validate-markers-warn-row-ids =
    let
      sortIds = builtins.sort (a: b: a < b);
      wantWarnIds = sortIds [
        "pr-intent"
        "issue-intent"
        "research-issue-intent"
      ];
      gotWarnIds = sortIds (
        map (r: r.id) (builtins.filter (r: r.severity == "warn") promptContract.validateMarkers)
      );
    in
    assert assertMsg (gotWarnIds == wantWarnIds)
      "prompt-contract: the set of severity==\"warn\" validateMarkers row ids changed from [${concatStringsSep ", " wantWarnIds}] to [${concatStringsSep ", " gotWarnIds}] -- promoting a warn row to \"reject\" (or adding/removing a warn row) is a deliberate design decision (issue #2996), not a silent flip: update this check's wantWarnIds, and revisit the promoted row's fixtures in nix/checks/prompt-contract-parity.nix, whose prompt-contract-parity-warn-rows-never-reject derives its warn set from severityById and so starts demanding a different verdict for them";
    pkgs.runCommand "prompt-contract-validate-markers-warn-row-ids" { } "touch $out";

  # issue #3726: exactly the four signal-channel rows carry socketMarker (the
  # verb string Validate scans for instead of marker when
  # SIGNAL_CARRIER_SOCKET is active); reviewer-verdict never crosses the
  # Signal socket and must stay carrier-blind. socketMarker and socketMessage
  # are always both-present or both-absent -- a row carrying one without the
  # other is a half-wired socket variant that would either scan for an empty
  # string or emit an empty diagnostic.
  prompt-contract-validate-markers-socket-fields-match-signal-rows =
    let
      signalIds = [
        "verdict-comment-relay"
        "pr-intent"
        "issue-intent"
        "research-issue-intent"
      ];
      hasSocketMarker = r: r ? socketMarker && r.socketMarker != "";
      hasSocketMessage = r: r ? socketMessage && r.socketMessage != "";
      badCoverage = builtins.filter (
        r: hasSocketMarker r != builtins.elem r.id signalIds
      ) promptContract.validateMarkers;
      badPairing = builtins.filter (
        r: hasSocketMarker r != hasSocketMessage r
      ) promptContract.validateMarkers;
    in
    assert assertMsg (badCoverage == [ ])
      "exactly the validateMarkers rows [${concatStringsSep ", " signalIds}] must carry a non-empty socketMarker, offending ids: [${
        concatStringsSep ", " (map (r: r.id) badCoverage)
      }]";
    assert assertMsg (badPairing == [ ])
      "every validateMarkers row must carry socketMarker and socketMessage together (both non-empty or both absent/empty), offending ids: [${
        concatStringsSep ", " (map (r: r.id) badPairing)
      }]";
    pkgs.runCommand "prompt-contract-validate-markers-socket-fields-match-signal-rows" { } "touch $out";

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

  # issue #2499: structural coverage only, catching a typo'd kind.
  # promptassembly.Validate no longer branches on kind (issue #2513 deleted
  # its forbidden-marker loop); the two places that still do are pinned
  # separately, in nix/checks/prompts.nix and in readonlyguards_test.go's
  # TestInstall_GhAPIMutationRejectsMutatingMethod.
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

  # issue #2499: enforce names which runtime layer backstops the row, or
  # "prompt-only" where none can, since a runtime guard there would collide
  # with a legitimate in-box use of the same operation.
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

  # issue #2509 (Finding 2): the rows readonlyguards.go renders into a runtime
  # shim or hook script must carry a runtimeMessage distinct from the
  # prompt-validator-facing message. A "prompt-only" row is never
  # runtime-rendered, so it carries none.
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

  # issue #3726: the baked shim text cannot know a run's carrier, so the
  # three `gh` rows whose signal has a socket-mode verb must point at both
  # routes -- pin the verb string so a later edit cannot silently drop it.
  prompt-contract-forbidden-markers-gh-signal-rows-name-driver-exec-signal =
    let
      ids = [
        "forbidden-gh-pr-create"
        "forbidden-gh-issue-comment"
        "forbidden-gh-issue-create"
      ];
      rows = builtins.filter (r: builtins.elem r.id ids) promptContract.forbiddenMarkers;
      bad = builtins.filter (r: !(pkgs.lib.hasInfix "driver-exec signal" r.runtimeMessage)) rows;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (builtins.length rows == builtins.length ids)
      "expected exactly ${builtins.toString (builtins.length ids)} forbiddenMarkers rows [${concatStringsSep ", " ids}], found ${builtins.toString (builtins.length rows)}";
    assert assertMsg (bad == [ ])
      "every gh pr create / gh issue comment / gh issue create forbiddenMarkers row's runtimeMessage must name \"driver-exec signal\", offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-gh-signal-rows-name-driver-exec-signal" { }
      "touch $out";

  # buildTimeRejectVerdicts (issue #2250) resolves each validateMarkers
  # "reject" row into ok, reject, or advise from whatever static gate and
  # content knowledge exists at build time. lib/mkHarness.nix supplies the
  # real inputs; these checks exercise the pure function with inline fixtures.
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

  # buildTimeResearchDirectFileViolations (issue #2595, ADR 0041) proves a
  # research prompt never references a FILER_FILE_DIRECT*-gated row's envsubst
  # placeholder. Research filing is host-mediated and relay-only by design, so
  # wiring a direct-file var into a research prompt must fail the build.
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

  # The real-registry pass (issue #2595, ADR 0041) feeds lib/fragments.nix's
  # real FILER_FILE_DIRECT*-gated rows and both research prompts' on-disk
  # content, so the "holds by construction" claim documented at
  # lib/fragments.nix's research-file-issues-relay.md row fails the build the
  # moment it stops holding.
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

  # buildTimeSignalFragmentViolations (issue #3726) proves a _LOG/_SOCKET
  # fragment pair's socket half actually instructs the verb (driver-exec
  # signal <channel>) its carrier requires -- the missing half of "the prompt
  # contract proves the verb in both modes" that validateMarkers' runtime
  # socketMarker covers on the log/marker side.
  prompt-contract-build-time-signal-fragment-violations-detects-socket-fragment-missing-verb =
    let
      out = promptContract.buildTimeSignalFragmentViolations {
        fragmentRows = [
          {
            gate = "BOX_ACCESS_READ_ONLY_SOCKET";
            fragment = "fixture-socket.md";
            signalChannel = "pr-intent";
          }
        ];
        fragmentContentByFile = {
          "fixture-socket.md" = "no verb mentioned here";
        };
      };
    in
    assert assertMsg (builtins.length out == 1)
      "buildTimeSignalFragmentViolations must report one violation when a _SOCKET-gated fragment's content is missing its channel's driver-exec verb, got: ${toString (builtins.length out)}";
    pkgs.runCommand
      "prompt-contract-build-time-signal-fragment-violations-detects-socket-fragment-missing-verb"
      { }
      "touch $out";

  prompt-contract-build-time-signal-fragment-violations-detects-log-fragment-missing-marker =
    let
      out = promptContract.buildTimeSignalFragmentViolations {
        fragmentRows = [
          {
            gate = "BOX_ACCESS_READ_ONLY_LOG";
            fragment = "fixture-log.md";
            signalChannel = "pr-intent";
          }
        ];
        fragmentContentByFile = {
          "fixture-log.md" = "no marker mentioned here";
        };
      };
    in
    assert assertMsg (builtins.length out == 1)
      "buildTimeSignalFragmentViolations must report one violation when a _LOG-gated fragment's content is missing its channel's markerChannels token, got: ${toString (builtins.length out)}";
    pkgs.runCommand
      "prompt-contract-build-time-signal-fragment-violations-detects-log-fragment-missing-marker"
      { }
      "touch $out";

  # The real-registry pass: every _LOG/_SOCKET-paired row in lib/fragments.nix
  # against the real on-disk fragment content under
  # templates/default/prompts/fragments/, so a future edit that drops a
  # channel's verb or marker from one carrier's fragment fails the build.
  prompt-contract-build-time-signal-fragment-violations-real-registry-passes =
    let
      fragments = import ../../lib/fragments.nix;
      signalFragmentRows = builtins.filter (row: row ? signalChannel) fragments;
      socketRows = builtins.filter (row: hasSuffix "_SOCKET" row.gate) signalFragmentRows;
      fragmentContentByFile = builtins.listToAttrs (
        map (row: {
          name = row.fragment;
          value = builtins.readFile ../../templates/default/prompts/fragments/${row.fragment};
        }) signalFragmentRows
      );
      out = promptContract.buildTimeSignalFragmentViolations {
        fragmentRows = signalFragmentRows;
        inherit fragmentContentByFile;
      };
    in
    assert assertMsg (socketRows != [ ])
      "prompt-contract-build-time-signal-fragment-violations-real-registry-passes: expected at least one _SOCKET-gated signalChannel row in lib/fragments.nix, got none -- fixture is vacuous";
    assert assertMsg (out == [ ])
      "buildTimeSignalFragmentViolations must return no violations against the real fragments.nix registry and real fragment content (issue #3726: both carrier modes must instruct the same verb), got: ${builtins.toJSON out}";
    pkgs.runCommand "prompt-contract-build-time-signal-fragment-violations-real-registry-passes" { }
      "touch $out";

  # issue #2524: outcomeStatusSets' research row must derive from
  # lib/research-verdicts.nix's defaultVerdicts plus the "blocked" crash
  # escape hatch, never a hand-typed restatement, so the research vocabulary
  # is rooted in exactly one place.
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

  # Order matters (issue #2974, parent #2972): lib/renderers.nix's
  # renderMarkerChannelsGo walks this list in exactly this order to emit typed
  # constants (cmd/launcher/internal/outcome/markerchannels_gen.go), so a
  # reorder here breaks that generated code.
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

  # issue #3726: only the three signal rows cross the Signal socket, so only
  # they may carry socketCarrier/socketDefense -- outcome and review-verdict
  # never cross it.
  prompt-contract-marker-channels-only-signal-rows-carry-socket-fields =
    let
      signalIds = [
        "comment"
        "pr-intent"
        "issue-intent"
      ];
      bad = builtins.filter (
        r: (r ? socketCarrier || r ? socketDefense) != builtins.elem r.id signalIds
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "exactly the markerChannels rows [${concatStringsSep ", " signalIds}] must carry socketCarrier/socketDefense, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-only-signal-rows-carry-socket-fields" { }
      "touch $out";

  prompt-contract-marker-channels-socket-fields-have-known-value =
    let
      withSocketFields = builtins.filter (
        r: r ? socketCarrier || r ? socketDefense
      ) promptContract.markerChannels;
      bad = builtins.filter (
        r:
        !(r ? socketCarrier)
        || !(r ? socketDefense)
        || r.socketCarrier != "signal-socket"
        || r.socketDefense != "launcher-owned-listener"
      ) withSocketFields;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every markerChannels row carrying socketCarrier/socketDefense must have both present, with socketCarrier == \"signal-socket\" and socketDefense == \"launcher-owned-listener\", offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-socket-fields-have-known-value" { } "touch $out";

  # Unlike defense and carrier above, fieldShape has no enum to pin: it is a
  # human-readable grammar that markergate's substituteFieldShape consumes for
  # its whitespace and key=value layout. This is deliberately not a grammar
  # validator, just the non-empty assertion that catches a blank or missing
  # field.
  prompt-contract-marker-channels-every-row-field-shape-non-empty =
    let
      bad = builtins.filter (
        r: !(r ? fieldShape) || !(builtins.isString r.fieldShape) || r.fieldShape == ""
      ) promptContract.markerChannels;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every markerChannels row's fieldShape must be a non-empty string, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-marker-channels-every-row-field-shape-non-empty" { } "touch $out";

  # Cross-registry drift guard, so one marker spelling cannot diverge from the
  # other: markerChannels' `token` and validateMarkers' `marker` name the same
  # literal for every channel except "outcome", which validateMarkers never
  # scans for because the outcome contract is validated structurally (ADR 0039).
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

  # sharedObligationViolationsFor (issue #2699) checks that both branches of a
  # paired prompt fork carry every literal substring a shared obligation
  # declares. It takes contentBySource as an explicit argument so these tests
  # can prove the check actually fails, on fixture content independent of the
  # real registry (exercised separately below, against the on-disk fragments).
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
  # must name both the offending fork branch and the obligation it is missing.
  # The other tests in this group assert only on `.branchId` and list length,
  # so an edit dropping either id from the message template would stay green
  # everywhere else.
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

  # Proves the real sharedObligations registry's "commit-folding" row can go
  # red (issue #2699). See sharedObligationDriftCheck above for what this
  # actually swaps and asserts.
  prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-commit-folding =
    sharedObligationDriftCheck {
      obligationId = "commit-folding";
      orchestratorFragmentFile = "commit-rework-orchestrator.md";
      brokenInlineContent = "no folding instruction of any kind in this fragment";
    };

  # Proves the real sharedObligations registry's "triage-drop-arm" row can go
  # red (issue #3610). See sharedObligationDriftCheck above for what this
  # actually swaps and asserts.
  prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-triage-drop-arm =
    sharedObligationDriftCheck {
      obligationId = "triage-drop-arm";
      orchestratorFragmentFile = "review-loop-orchestrator.md";
      brokenInlineContent = "no drop outcome of any kind in this fragment";
    };

  # Proves the real sharedObligations registry's "triage-round-2-calibration"
  # row can go red (issue #3611). See sharedObligationDriftCheck above for
  # what this actually swaps and asserts.
  prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-triage-round-2-calibration =
    sharedObligationDriftCheck {
      obligationId = "triage-round-2-calibration";
      orchestratorFragmentFile = "review-loop-orchestrator.md";
      brokenInlineContent = "no round-2 calibration of any kind in this fragment";
    };

  # Proves the real sharedObligations registry's "triage-escape-hatch-scope"
  # row can go red (issue #3816). See sharedObligationDriftCheck above for
  # what this actually swaps and asserts.
  prompt-contract-shared-obligations-detects-drift-if-inline-branch-drops-triage-escape-hatch-scope =
    sharedObligationDriftCheck {
      obligationId = "triage-escape-hatch-scope";
      orchestratorFragmentFile = "review-loop-orchestrator.md";
      brokenInlineContent = "no escape-hatch scope gate of any kind in this fragment";
    };

  # Enforcing check (issue #2699): the real registry's rows must hold against
  # the on-disk fragment content every branch declares, so an edit to either
  # fragment file that drops a shared obligation the other branch still
  # carries fails loudly, naming the branch and the missing substrings.
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
