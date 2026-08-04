# Eval-level pins for lib/prompt-contract.nix (issue #2245): a pure-data
# registry of the harness-owned shared prompt blocks (outcome contract,
# COMMS, CHECK/COMMIT, research verdict) that lib/mkHarness.nix currently
# slices/injects by hand via lib/prompt-inject.nix, plus (below) the
# registry of markers a Box's own output is expected to emit
# (validateMarkers). This check pins both registries' row shape and content
# ahead of any consumer wiring, so a later slice of #2245 can drive
# mkHarness.nix and a post-run validation pass from this data without
# silently changing which blocks go where or which omissions matter.
{ pkgs, ... }:
let
  promptContract = import ../../lib/prompt-contract.nix;
  promptInject = import ../../lib/prompt-inject.nix;
  inherit (pkgs.lib) assertMsg concatStringsSep;
  byId = id: builtins.head (builtins.filter (r: r.id == id) promptContract.injectBlocks);
  markerById = id: builtins.head (builtins.filter (r: r.id == id) promptContract.validateMarkers);
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
      expected = promptInject.sliceBetween "# CHECK" "# REVIEW" issuePromptSource;
      out = promptContract.canonicalText.check;
      startMarker = "# CHECK";
    in
    assert assertMsg (out == expected)
      "canonicalText.check must equal a from-scratch sliceBetween of issue-prompt.md's own text";
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

  prompt-contract-inject-blocks-bash-rows-has-four-entries =
    let
      out = builtins.length promptContract.injectBlocksBashRows;
    in
    assert assertMsg (out == 4)
      "injectBlocksBashRows must have exactly 4 entries (one per injectBlocks row), got: ${toString out}";
    assert assertMsg (out == builtins.length promptContract.injectBlocks)
      "injectBlocksBashRows' length must equal injectBlocks' length (derived-from-injectBlocks property)";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-rows-has-four-entries" { } "touch $out";

  prompt-contract-inject-blocks-bash-rows-order =
    let
      ids = map (row: builtins.head (pkgs.lib.splitString "|" row)) promptContract.injectBlocksBashRows;
      expected = [ "outcome" "comms" "check" "research-verdict" ];
    in
    assert assertMsg (ids == expected)
      "injectBlocksBashRows must appear in injectBlocks row order [outcome, comms, check, research-verdict], got: [${concatStringsSep ", " ids}]";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-rows-order" { } "touch $out";

  prompt-contract-inject-blocks-bash-rows-outcome-exact-string =
    let
      out = builtins.elemAt promptContract.injectBlocksBashRows 0;
      expected = "outcome|# LAND THE CHANGE|issue-prompt.md|# LAND THE CHANGE||issue fix";
    in
    assert assertMsg (out == expected)
      "injectBlocksBashRows' outcome row must equal '${expected}' (empty endMarker field for the null-endMarker case), got: '${out}'";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-rows-outcome-exact-string" { } "touch $out";

  prompt-contract-inject-blocks-bash-rows-comms-exact-string =
    let
      out = builtins.elemAt promptContract.injectBlocksBashRows 1;
      expected = "comms|# COMMS|issue-prompt.md|# COMMS|# SCOUT|fix";
    in
    assert assertMsg (out == expected)
      "injectBlocksBashRows' comms row must equal '${expected}' (non-null endMarker case), got: '${out}'";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-rows-comms-exact-string" { } "touch $out";

  prompt-contract-inject-blocks-bash-preamble-starts-with-array-open =
    let
      out = promptContract.injectBlocksBashPreamble;
      prefix = "_INJECT_BLOCK_ROWS=(\n";
    in
    assert assertMsg (builtins.substring 0 (builtins.stringLength prefix) out == prefix)
      "injectBlocksBashPreamble must start with '_INJECT_BLOCK_ROWS=(\\n', got: '${builtins.substring 0 40 out}...'";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-preamble-starts-with-array-open" { } "touch $out";

  prompt-contract-inject-blocks-bash-preamble-ends-with-array-close =
    let
      out = promptContract.injectBlocksBashPreamble;
      suffix = ")\n";
      len = builtins.stringLength out;
      suffixLen = builtins.stringLength suffix;
    in
    assert assertMsg (builtins.substring (len - suffixLen) suffixLen out == suffix)
      "injectBlocksBashPreamble must end with ')\\n', got tail: '${builtins.substring (len - 40) 40 out}'";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-preamble-ends-with-array-close" { } "touch $out";

  prompt-contract-inject-blocks-bash-preamble-contains-every-quoted-row =
    let
      preamble = promptContract.injectBlocksBashPreamble;
      # Every row's content (`#`, `|`, and space are all outside
      # `[[:alnum:],._+:@%/-]+`) trips the quoted branch, so each row must
      # appear single-quote-wrapped on its own indented line.
      expectedLines = map (row: "  '${row}'\n") promptContract.injectBlocksBashRows;
      missing = builtins.filter (line: !(pkgs.lib.hasInfix line preamble)) expectedLines;
    in
    assert assertMsg (missing == [ ])
      "injectBlocksBashPreamble must contain every injectBlocksBashRows entry single-quote-wrapped and indented 2 spaces; missing: [${concatStringsSep ", " missing}]";
    pkgs.runCommand "prompt-contract-inject-blocks-bash-preamble-contains-every-quoted-row" { } "touch $out";
}
