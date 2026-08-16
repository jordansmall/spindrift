# Eval-level pins for lib/prompt-contract.nix (issue #2245): a pure-data
# registry of the harness-owned shared prompt blocks (outcome contract,
# COMMS, CHECK/COMMIT, research verdict) that lib/mkHarness.nix now
# slices/injects from instead of hand-wiring via lib/prompt-inject.nix
# (issue #2246), plus (below) the registry of markers a Box's own output is
# expected to emit (validateMarkers, consumed by
# cmd/launcher/internal/promptassembly's Validate function, issue #2405).
# This check pins both registries' row shape and content so a future
# consumer can't silently change which blocks go where or which omissions
# matter.
{ pkgs, ... }:
let
  promptContract = import ../../lib/prompt-contract.nix;
  promptInject = import ../../lib/prompt-inject.nix;
  inherit (pkgs.lib) assertMsg concatStringsSep hasSuffix removeSuffix;
  inherit (promptContract) byId;
  markerById = id: builtins.head (builtins.filter (r: r.id == id) promptContract.validateMarkers);
  forbiddenMarkerById = id: builtins.head (builtins.filter (r: r.id == id) promptContract.forbiddenMarkers);
  issuePromptSource = builtins.readFile ../../templates/default/prompts/issue-prompt.md;
  researchPromptSource = builtins.readFile ../../templates/default/prompts/research-prompt.md;
in
{
  prompt-contract-canonical-text-outcome-matches-live-slice =
    let
      expected = promptInject.sliceFromMarker "# LAND THE CHANGE" issuePromptSource;
      out = promptContract.canonicalText.outcome;
      startMarker = "# LAND THE CHANGE";
    in
    assert assertMsg (out == expected)
      "canonicalText.outcome must equal a from-scratch sliceFromMarker of issue-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0)
      "canonicalText.outcome must be non-empty";
    assert assertMsg (builtins.substring 0 (builtins.stringLength startMarker) out == startMarker)
      "canonicalText.outcome must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-outcome-matches-live-slice" { } "touch $out";

  prompt-contract-canonical-text-comms-matches-live-slice =
    let
      expected = promptInject.sliceBetween "# COMMS" "# SCOUT" issuePromptSource;
      out = promptContract.canonicalText.comms;
      startMarker = "# COMMS";
    in
    assert assertMsg (out == expected)
      "canonicalText.comms must equal a from-scratch sliceBetween of issue-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0)
      "canonicalText.comms must be non-empty";
    assert assertMsg (builtins.substring 0 (builtins.stringLength startMarker) out == startMarker)
      "canonicalText.comms must start with its own startMarker '${startMarker}'";
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
    assert assertMsg (builtins.stringLength out > 0)
      "canonicalText.check must be non-empty";
    assert assertMsg (builtins.substring 0 (builtins.stringLength startMarker) out == startMarker)
      "canonicalText.check must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-check-matches-live-slice" { } "touch $out";

  prompt-contract-canonical-text-research-verdict-matches-live-slice =
    let
      expected = promptInject.sliceFromMarker "# POST THE VERDICT" researchPromptSource;
      out = promptContract.canonicalText.research-verdict;
      startMarker = "# POST THE VERDICT";
    in
    assert assertMsg (out == expected)
      "canonicalText.research-verdict must equal a from-scratch sliceFromMarker of research-prompt.md's own text";
    assert assertMsg (builtins.stringLength out > 0)
      "canonicalText.research-verdict must be non-empty";
    assert assertMsg (builtins.substring 0 (builtins.stringLength startMarker) out == startMarker)
      "canonicalText.research-verdict must start with its own startMarker '${startMarker}'";
    pkgs.runCommand "prompt-contract-canonical-text-research-verdict-matches-live-slice" { } "touch $out";

  prompt-contract-marker-list-equals-expected =
    let
      out = promptContract.markerList;
      expected = [ "# LAND THE CHANGE" "# COMMS" "# CHECK" "# POST THE VERDICT" ];
    in
    assert assertMsg (out == expected)
      "markerList must equal [# LAND THE CHANGE, # COMMS, # CHECK, # POST THE VERDICT], got: [${concatStringsSep ", " out}]";
    assert assertMsg (builtins.length out == builtins.length promptContract.injectBlocks)
      "markerList's length must equal injectBlocks' length (derived-from-injectBlocks property)";
    pkgs.runCommand "prompt-contract-marker-list-equals-expected" { } "touch $out";

  prompt-contract-inject-blocks-has-four-rows =
    let
      out = builtins.length promptContract.injectBlocks;
    in
    assert assertMsg (out == 4)
      "injectBlocks must have exactly 4 rows (outcome, comms, check, research-verdict), got: ${toString out}";
    pkgs.runCommand "prompt-contract-inject-blocks-has-four-rows" { } "touch $out";

  prompt-contract-inject-blocks-row-order =
    let
      out = map (r: r.id) promptContract.injectBlocks;
      expected = [ "outcome" "comms" "check" "research-verdict" ];
    in
    assert assertMsg (out == expected)
      "injectBlocks rows must appear in order [outcome, comms, check, research-verdict], got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-inject-blocks-row-order" { } "touch $out";

  prompt-contract-outcome-row-shape =
    let
      row = byId "outcome";
    in
    assert assertMsg (row.marker == "# LAND THE CHANGE")
      "outcome row's marker must be '# LAND THE CHANGE', got: ${row.marker}";
    assert assertMsg (row.source == "issue-prompt.md")
      "outcome row's source must be 'issue-prompt.md', got: ${row.source}";
    assert assertMsg (row.startMarker == "# LAND THE CHANGE")
      "outcome row's startMarker must be '# LAND THE CHANGE', got: ${row.startMarker}";
    assert assertMsg (row.endMarker == null)
      "outcome row's endMarker must be null (slice to EOF), got: ${toString row.endMarker}";
    assert assertMsg (row.kinds == [ "issue" "fix" ])
      "outcome row's kinds must be [issue, fix], got: [${concatStringsSep ", " row.kinds}]";
    pkgs.runCommand "prompt-contract-outcome-row-shape" { } "touch $out";

  prompt-contract-comms-row-shape =
    let
      row = byId "comms";
    in
    assert assertMsg (row.marker == "# COMMS")
      "comms row's marker must be '# COMMS', got: ${row.marker}";
    assert assertMsg (row.source == "issue-prompt.md")
      "comms row's source must be 'issue-prompt.md', got: ${row.source}";
    assert assertMsg (row.startMarker == "# COMMS")
      "comms row's startMarker must be '# COMMS', got: ${row.startMarker}";
    assert assertMsg (row.endMarker == "# SCOUT")
      "comms row's endMarker must be '# SCOUT', got: ${toString row.endMarker}";
    assert assertMsg (row.kinds == [ "fix" ])
      "comms row's kinds must be [fix], got: [${concatStringsSep ", " row.kinds}]";
    pkgs.runCommand "prompt-contract-comms-row-shape" { } "touch $out";

  prompt-contract-check-row-shape =
    let
      row = byId "check";
    in
    assert assertMsg (row.marker == "# CHECK")
      "check row's marker must be '# CHECK', got: ${row.marker}";
    assert assertMsg (row.source == "issue-prompt.md")
      "check row's source must be 'issue-prompt.md', got: ${row.source}";
    assert assertMsg (row.startMarker == "# CHECK")
      "check row's startMarker must be '# CHECK', got: ${row.startMarker}";
    assert assertMsg (row.endMarker == "# REVIEW")
      "check row's endMarker must be '# REVIEW', got: ${toString row.endMarker}";
    assert assertMsg (row.kinds == [ "fix" ])
      "check row's kinds must be [fix], got: [${concatStringsSep ", " row.kinds}]";
    pkgs.runCommand "prompt-contract-check-row-shape" { } "touch $out";

  prompt-contract-research-verdict-row-shape =
    let
      row = byId "research-verdict";
    in
    assert assertMsg (row.marker == "# POST THE VERDICT")
      "research-verdict row's marker must be '# POST THE VERDICT', got: ${row.marker}";
    assert assertMsg (row.source == "research-prompt.md")
      "research-verdict row's source must be 'research-prompt.md', got: ${row.source}";
    assert assertMsg (row.startMarker == "# POST THE VERDICT")
      "research-verdict row's startMarker must be '# POST THE VERDICT', got: ${row.startMarker}";
    assert assertMsg (row.endMarker == null)
      "research-verdict row's endMarker must be null (slice to EOF), got: ${toString row.endMarker}";
    assert assertMsg (row.kinds == [ "research" "research-self-contained" ])
      "research-verdict row's kinds must be [research, research-self-contained], got: [${concatStringsSep ", " row.kinds}]";
    pkgs.runCommand "prompt-contract-research-verdict-row-shape" { } "touch $out";

  prompt-contract-validate-markers-has-four-rows =
    let
      out = builtins.length promptContract.validateMarkers;
    in
    assert assertMsg (out == 4)
      "validateMarkers must have exactly 4 rows (verdict-comment-relay, reviewer-verdict, pr-intent, issue-intent), got: ${toString out}";
    pkgs.runCommand "prompt-contract-validate-markers-has-four-rows" { } "touch $out";

  prompt-contract-validate-markers-row-order =
    let
      out = map (r: r.id) promptContract.validateMarkers;
      expected = [ "verdict-comment-relay" "reviewer-verdict" "pr-intent" "issue-intent" ];
    in
    assert assertMsg (out == expected)
      "validateMarkers rows must appear in order [verdict-comment-relay, reviewer-verdict, pr-intent, issue-intent], got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-validate-markers-row-order" { } "touch $out";

  prompt-contract-verdict-comment-relay-row-shape =
    let
      row = markerById "verdict-comment-relay";
    in
    assert assertMsg (row.marker == "SPINDRIFT_COMMENT")
      "verdict-comment-relay row's marker must be 'SPINDRIFT_COMMENT', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "verdict-comment-relay row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "verdict-comment-relay row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "readOnlyResearch")
      "verdict-comment-relay row's when must be 'readOnlyResearch', got: ${row.when}";
    pkgs.runCommand "prompt-contract-verdict-comment-relay-row-shape" { } "touch $out";

  prompt-contract-reviewer-verdict-row-shape =
    let
      row = markerById "reviewer-verdict";
    in
    assert assertMsg (row.marker == "VERDICT:")
      "reviewer-verdict row's marker must be 'VERDICT:', got: ${row.marker}";
    assert assertMsg (row.carrier == "subagent-first-line")
      "reviewer-verdict row's carrier must be 'subagent-first-line', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "reviewer-verdict row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "orchestratorEnabled")
      "reviewer-verdict row's when must be 'orchestratorEnabled', got: ${row.when}";
    pkgs.runCommand "prompt-contract-reviewer-verdict-row-shape" { } "touch $out";

  prompt-contract-pr-intent-row-shape =
    let
      row = markerById "pr-intent";
    in
    assert assertMsg (row.marker == "SPINDRIFT_PR_INTENT")
      "pr-intent row's marker must be 'SPINDRIFT_PR_INTENT', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "pr-intent row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "warn")
      "pr-intent row's severity must be 'warn', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "pr-intent row's when must be 'boxAccessReadOnly', got: ${row.when}";
    pkgs.runCommand "prompt-contract-pr-intent-row-shape" { } "touch $out";

  prompt-contract-issue-intent-row-shape =
    let
      row = markerById "issue-intent";
    in
    assert assertMsg (row.marker == "SPINDRIFT_ISSUE_INTENT")
      "issue-intent row's marker must be 'SPINDRIFT_ISSUE_INTENT', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "issue-intent row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "warn")
      "issue-intent row's severity must be 'warn', got: ${row.severity}";
    assert assertMsg (row.when == "filerFileRelay")
      "issue-intent row's when must be 'filerFileRelay', got: ${row.when}";
    pkgs.runCommand "prompt-contract-issue-intent-row-shape" { } "touch $out";

  # Pins forbiddenMarkers (issue #2464): the opposite-direction registry from
  # validateMarkers above -- every row here names a write-capable git/gh
  # operation a read-only Box's rendered prompt must never order the Driver
  # to run.
  prompt-contract-forbidden-markers-has-thirteen-rows =
    let
      out = builtins.length promptContract.forbiddenMarkers;
    in
    assert assertMsg (out == 13)
      "forbiddenMarkers must have exactly 13 rows (forbidden-git-push, forbidden-gh-pr-create, forbidden-gh-pr-ready, forbidden-gh-pr-merge, forbidden-gh-issue-comment, forbidden-gh-issue-create, forbidden-git-bundle-create, forbidden-gh-api-mutation, forbidden-fj-pr-create, forbidden-fj-pr-ready, forbidden-fj-pr-merge, forbidden-fj-issue-comment, forbidden-fj-issue-create), got: ${toString out}";
    pkgs.runCommand "prompt-contract-forbidden-markers-has-thirteen-rows" { } "touch $out";

  prompt-contract-forbidden-markers-row-order =
    let
      out = map (r: r.id) promptContract.forbiddenMarkers;
      expected = [
        "forbidden-git-push"
        "forbidden-gh-pr-create"
        "forbidden-gh-pr-ready"
        "forbidden-gh-pr-merge"
        "forbidden-gh-issue-comment"
        "forbidden-gh-issue-create"
        "forbidden-git-bundle-create"
        "forbidden-gh-api-mutation"
        "forbidden-fj-pr-create"
        "forbidden-fj-pr-ready"
        "forbidden-fj-pr-merge"
        "forbidden-fj-issue-comment"
        "forbidden-fj-issue-create"
      ];
    in
    assert assertMsg (out == expected)
      "forbiddenMarkers rows must appear in order [${concatStringsSep ", " expected}], got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-row-order" { } "touch $out";

  prompt-contract-forbidden-markers-markers-in-order =
    let
      out = map (r: r.marker) promptContract.forbiddenMarkers;
      expected = [
        "git push"
        "gh pr create"
        "gh pr ready"
        "gh pr merge"
        "gh issue comment"
        "gh issue create"
        "git bundle create"
        "gh api"
        "fj pr create"
        "fj pr ready"
        "fj pr merge"
        "fj issue comment"
        "fj issue create"
      ];
    in
    assert assertMsg (out == expected)
      "forbiddenMarkers rows' markers must appear in order [${concatStringsSep ", " expected}], got: [${concatStringsSep ", " out}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-markers-in-order" { } "touch $out";

  prompt-contract-forbidden-markers-every-row-carrier-fragment-body =
    let
      bad = builtins.filter (r: r.carrier != "fragment-body") promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's carrier must be 'fragment-body', offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-carrier-fragment-body" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-when-box-access-read-only" { } "touch $out";

  prompt-contract-forbidden-markers-every-row-message-mentions-own-marker =
    let
      bad = builtins.filter (r: !(pkgs.lib.hasInfix r.marker r.message)) promptContract.forbiddenMarkers;
      badIds = map (r: r.id) bad;
    in
    assert assertMsg (bad == [ ])
      "every forbiddenMarkers row's message must contain its own marker substring, offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-forbidden-markers-every-row-message-mentions-own-marker" { } "touch $out";

  # issue #2499: every row's kind must be a known value -- structural
  # coverage only (does the field hold a value someone typo'd), not
  # behavioral: this Nix check has no way to invoke promptassembly.Validate
  # and confirm it actually branches on kind. That behavior -- a
  # "gh-api-mutation" row's marker is display-only and never scanned as a
  # forbidden substring, unlike a "substring" row -- is pinned Go-side by
  # cmd/launcher/internal/promptassembly/validate_test.go's
  # TestValidateForbiddenMarkerGhAPIMutationKindNeverScannedAsSubstring and
  # TestValidateForbiddenMarkerFjRowStillRejectsImperative.
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

  prompt-contract-forbidden-git-push-row-shape =
    let
      row = forbiddenMarkerById "forbidden-git-push";
    in
    assert assertMsg (row.marker == "git push")
      "forbidden-git-push row's marker must be 'git push', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-git-push row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-git-push row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-git-push row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-git-push row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "git-hook")
      "forbidden-git-push row's enforce must be 'git-hook', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-git-push-row-shape" { } "touch $out";

  # issue #2499: git bundle create's row is enforced only at the prompt level
  # (see the rationale comment on the row itself in lib/prompt-contract.nix)
  # since driver-exec's bundle-out step legitimately runs `git bundle create`
  # in-box as the harness's own code-out mechanism -- a runtime backstop would
  # sabotage that use, so this is the one row pinned to enforce=="prompt-only".
  prompt-contract-forbidden-git-bundle-create-row-shape =
    let
      row = forbiddenMarkerById "forbidden-git-bundle-create";
    in
    assert assertMsg (row.marker == "git bundle create")
      "forbidden-git-bundle-create row's marker must be 'git bundle create', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-git-bundle-create row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-git-bundle-create row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-git-bundle-create row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-git-bundle-create row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "prompt-only")
      "forbidden-git-bundle-create row's enforce must be 'prompt-only', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-git-bundle-create-row-shape" { } "touch $out";

  # issue #2499: documents the bespoke bash argument-scanner in
  # agent/entrypoint.sh's install_readonly_gh_shim, which rejects `gh api`
  # calls carrying a mutating HTTP method (-X/--method POST/PATCH/PUT/DELETE)
  # -- kind "gh-api-mutation" rather than "substring" because the real
  # enforcement is that argument-scan, not a literal `gh api` substring
  # match (a Box may legitimately run read-only `gh api` calls).
  prompt-contract-forbidden-gh-api-mutation-row-shape =
    let
      row = forbiddenMarkerById "forbidden-gh-api-mutation";
    in
    assert assertMsg (row.marker == "gh api")
      "forbidden-gh-api-mutation row's marker must be 'gh api', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-gh-api-mutation row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-gh-api-mutation row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-gh-api-mutation row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "gh-api-mutation")
      "forbidden-gh-api-mutation row's kind must be 'gh-api-mutation', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-gh-api-mutation row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-gh-api-mutation-row-shape" { } "touch $out";

  # issue #2509: the five fj rows mirror the gh rows above one-for-one and are
  # pinned enforce=="command-shim" now that driver-exec's readonly-guards
  # verb installs a real fj shim -- see the rationale comment on these rows
  # in lib/prompt-contract.nix.
  prompt-contract-forbidden-fj-pr-create-row-shape =
    let
      row = forbiddenMarkerById "forbidden-fj-pr-create";
    in
    assert assertMsg (row.marker == "fj pr create")
      "forbidden-fj-pr-create row's marker must be 'fj pr create', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-fj-pr-create row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-fj-pr-create row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-fj-pr-create row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-fj-pr-create row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-fj-pr-create row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-fj-pr-create-row-shape" { } "touch $out";

  prompt-contract-forbidden-fj-pr-ready-row-shape =
    let
      row = forbiddenMarkerById "forbidden-fj-pr-ready";
    in
    assert assertMsg (row.marker == "fj pr ready")
      "forbidden-fj-pr-ready row's marker must be 'fj pr ready', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-fj-pr-ready row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-fj-pr-ready row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-fj-pr-ready row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-fj-pr-ready row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-fj-pr-ready row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-fj-pr-ready-row-shape" { } "touch $out";

  prompt-contract-forbidden-fj-pr-merge-row-shape =
    let
      row = forbiddenMarkerById "forbidden-fj-pr-merge";
    in
    assert assertMsg (row.marker == "fj pr merge")
      "forbidden-fj-pr-merge row's marker must be 'fj pr merge', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-fj-pr-merge row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-fj-pr-merge row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-fj-pr-merge row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-fj-pr-merge row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-fj-pr-merge row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-fj-pr-merge-row-shape" { } "touch $out";

  prompt-contract-forbidden-fj-issue-comment-row-shape =
    let
      row = forbiddenMarkerById "forbidden-fj-issue-comment";
    in
    assert assertMsg (row.marker == "fj issue comment")
      "forbidden-fj-issue-comment row's marker must be 'fj issue comment', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-fj-issue-comment row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-fj-issue-comment row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-fj-issue-comment row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-fj-issue-comment row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-fj-issue-comment row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-fj-issue-comment-row-shape" { } "touch $out";

  prompt-contract-forbidden-fj-issue-create-row-shape =
    let
      row = forbiddenMarkerById "forbidden-fj-issue-create";
    in
    assert assertMsg (row.marker == "fj issue create")
      "forbidden-fj-issue-create row's marker must be 'fj issue create', got: ${row.marker}";
    assert assertMsg (row.carrier == "fragment-body")
      "forbidden-fj-issue-create row's carrier must be 'fragment-body', got: ${row.carrier}";
    assert assertMsg (row.severity == "reject")
      "forbidden-fj-issue-create row's severity must be 'reject', got: ${row.severity}";
    assert assertMsg (row.when == "boxAccessReadOnly")
      "forbidden-fj-issue-create row's when must be 'boxAccessReadOnly', got: ${row.when}";
    assert assertMsg (row.kind == "substring")
      "forbidden-fj-issue-create row's kind must be 'substring', got: ${row.kind}";
    assert assertMsg (row.enforce == "command-shim")
      "forbidden-fj-issue-create row's enforce must be 'command-shim', got: ${row.enforce}";
    pkgs.runCommand "prompt-contract-forbidden-fj-issue-create-row-shape" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-reject-when-gate-true-and-marker-missing" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-advise-when-gate-false-and-marker-missing" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-ok-when-marker-present" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-advise-when-fully-unresolved" { } "touch $out";

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
    pkgs.runCommand "prompt-contract-build-time-reject-verdicts-covers-every-reject-row" { } "touch $out";
}
