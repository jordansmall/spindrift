# Pure-data registry of the harness-owned shared prompt blocks (issue #2245):
# the outcome contract, COMMS, CHECK/COMMIT, and research-verdict blocks that
# lib/mkHarness.nix used to slice out of the default prompts by hand via
# lib/prompt-inject.nix, one hardcoded marker/source pair at a time.
# lib/mkHarness.nix and the marker-parity checks under nix/checks/ now derive
# from this registry instead (issue #2246), so this file's row shape is the
# single place the block-to-prompt-kind mapping is written down.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without needing a locked nixpkgs (mirrors
# lib/prompt-inject.nix, issue #512, and lib/renderers.nix, issue #402).
let
  promptInject = import ./prompt-inject.nix;

  # Matches `lib.escapeShellArg` byte for byte without depending on pkgs.lib
  # (mirrors lib/preambles.nix's copy of the same logic, issue #513): a
  # string of only shell-safe characters passes through unquoted; anything
  # else gets single-quote-wrapped, with embedded `'` escaped as `'\''`.
  escapeShellArg =
    arg:
    let
      string = builtins.toString arg;
    in
    if builtins.match "[[:alnum:],._+:@%/-]+" string == null then
      "'" + builtins.replaceStrings [ "'" ] [ "'\\''" ] string + "'"
    else
      string;

  # Does `text` contain the literal (non-regex) `needle` -- mirrors
  # lib/prompt-inject.nix's own splitOnce/injectSection idiom
  # (builtins.split on a regex-escaped literal, checking for more than the
  # one no-match part) without depending on that file's private, unexported
  # escapeRegex (pure builtins only, same reasoning as escapeShellArg above).
  hasInfix =
    needle: text:
    builtins.length (
      builtins.split (
        builtins.replaceStrings
          [ "\\" "^" "$" "." "|" "?" "*" "+" "(" ")" "[" "]" "{" "}" ]
          [ "\\\\" "\\^" "\\$" "\\." "\\|" "\\?" "\\*" "\\+" "\\(" "\\)" "\\[" "\\]" "\\{" "\\}" ]
          needle
      ) text
    ) > 1;

  # Byte-for-byte copies of lib/prompt-inject.nix's own private hasSuffix/
  # removeSuffix (not exported there, and this file stays pure-builtins per
  # the header comment above, same reasoning as escapeShellArg/hasInfix
  # above) -- needed only by ensureTrailingBlankLine below.
  hasSuffix =
    suffix: content:
    let
      lenContent = builtins.stringLength content;
      lenSuffix = builtins.stringLength suffix;
    in
    lenContent >= lenSuffix && builtins.substring (lenContent - lenSuffix) lenSuffix content == suffix;

  removeSuffix =
    suffix: content:
    if hasSuffix suffix content then
      builtins.substring 0 (builtins.stringLength content - builtins.stringLength suffix) content
    else
      content;

  # sliceBetween's own doc comment (lib/prompt-inject.nix) notes "a sliced
  # shared block already ends with the blank line that separated it from the
  # next heading in its source file" -- true of every row until issue #2462's
  # COMMIT_PUSH_READ_WRITE_STEP/COMMIT_PUSH_READ_ONLY_STEP pair, whose gate is
  # never off (exactly one of the pair always renders, the same
  # BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY exactly-one-on invariant every
  # other paired gate in lib/fragments.nix carries), so
  # templates/default/prompts/issue-prompt.md glues that placeholder directly
  # onto the "# REVIEW" endMarker with no blank line in between -- the only
  # way to keep the *rendered* prompt byte-identical, since the registry's
  # own fragment loop already appends the block's "\n\n" separator (see
  # lib/mkHarness.nix's fragmentRegistryPreamble /
  # cmd/launcher/internal/promptassembly's Assemble, both driven from this
  # same lib/fragments.nix registry) -- a template-level blank line on top of
  # that would double it up. That leaves the raw (unrendered) slice ending
  # exactly at the placeholder token, with no trailing blank line at all, so
  # this normalizes the sliceBetween case back onto the doc comment's
  # documented invariant a no-op for every row that already carried the
  # invariant naturally (the "check" block's comms sibling included), and a
  # single appended "\n\n" for a row -- like "check" now -- whose source text
  # doesn't.
  ensureTrailingBlankLine = s: if hasSuffix "\n\n" s then s else removeSuffix "\n" s + "\n\n";

  # Slices one injectBlocks row's canonical text live from its declared
  # `source` prompt file -- never a standalone contract file, so this can
  # never drift from the default prompt's own copy (issue #419). Re-derives,
  # as pure data-driven code, the same slicing lib/mkHarness.nix's
  # outcomeContract/commsBlock/checkBlock/researchOutcomeContract already do
  # by hand, one hardcoded marker/source pair at a time.
  sliceRow =
    row:
    let
      sourceText = builtins.readFile (../templates/default/prompts/${row.source});
    in
    if row.endMarker == null then
      promptInject.sliceFromMarker row.startMarker sourceText
    else
      ensureTrailingBlankLine (promptInject.sliceBetween row.startMarker row.endMarker sourceText);
in
rec {
  # Each row describes one shared block:
  #   id          -- short, stable identifier for the block.
  #   marker      -- the heading text injectSection scans a target prompt for
  #                  to decide whether the block is already present.
  #   source      -- the default prompt file (relative to
  #                  templates/default/prompts/) this block's canonical text
  #                  is sliced from. There is no separate standalone contract
  #                  file: slicing from the live default prompt is what keeps
  #                  the injected copy and that prompt's own copy byte-
  #                  identical, so they can never drift apart (issue #419).
  #   startMarker -- the heading the slice starts at (inclusive).
  #   endMarker   -- the heading the slice stops before (exclusive), or
  #                  `null` to slice from startMarker all the way to the end
  #                  of the source file (sliceFromMarker instead of
  #                  sliceBetween in lib/prompt-inject.nix terms).
  #   kinds       -- every prompt kind (issue/fix/research/
  #                  research-self-contained) this block is injected into.
  #                  Deliberately excludes the prompt named by `source`
  #                  itself where that prompt already carries its own copy of
  #                  the block inline -- injection only ever targets prompts
  #                  that would otherwise be missing it.
  injectBlocks = [
    {
      id = "outcome";
      marker = "# LAND THE CHANGE";
      source = "issue-prompt.md";
      startMarker = "# LAND THE CHANGE";
      endMarker = null;
      # issue-prompt.md carries the section inline (it's the source), so
      # outcome injection only targets fix-prompt.md. research-prompt.md has
      # its own separate outcome contract (see "research-verdict" below), so
      # this block is not injected there.
      kinds = [ "issue" "fix" ];
    }
    {
      id = "comms";
      marker = "# COMMS";
      source = "issue-prompt.md";
      startMarker = "# COMMS";
      endMarker = "# SCOUT";
      # fix-prompt.md runs a FIX step in place of issue-prompt.md's COMMS
      # step, so it's the only other prompt that needs COMMS injected.
      kinds = [ "fix" ];
    }
    {
      id = "check";
      marker = "# CHECK";
      source = "issue-prompt.md";
      startMarker = "# CHECK";
      endMarker = "# REVIEW";
      # fix-prompt.md has no review step of its own, but still needs the
      # CHECK/COMMIT block injected the same way COMMS is above.
      kinds = [ "fix" ];
    }
    {
      id = "research-verdict";
      marker = "# POST THE VERDICT";
      source = "research-prompt.md";
      startMarker = "# POST THE VERDICT";
      endMarker = null;
      # research-prompt.md carries the section inline (it's the source);
      # research-self-contained-prompt.md shares the same verdict-posting
      # contract and needs it injected the same way fix-prompt.md needs the
      # other three blocks above.
      kinds = [ "research" "research-self-contained" ];
    }
  ];

  # Second pure-data registry (issue #2245, drawn from parent issue #2244's
  # classification of the harness's inject/outject markers): every marker a
  # Box's own prompt output is expected to emit, so a later slice can drive a
  # post-run validation pass from this data instead of the omission going
  # unnoticed until something downstream (a merge, a comment relay) silently
  # no-ops. validateMarkersBashRows/validateMarkersBashPreamble below render
  # this list into the bash array a following slice's in-box validator reads
  # (issue #2249) -- the list itself stays data-only; runtime consumption
  # lives in agent/entrypoint.sh.
  #   id       -- short, stable identifier for the expected marker.
  #   marker   -- the literal marker text a Box's output is scanned for.
  #   carrier  -- where the marker is expected to appear:
  #               "subagent-first-line" for a marker that must be the first
  #               line of a review subagent's own output (see
  #               templates/default/prompts/review-prompt.md), vs
  #               "fragment-body" for a marker embedded anywhere in the body
  #               of a rendered prompt fragment (see
  #               templates/default/prompts/fragments/*.md).
  #   severity -- "reject" for the two provably-fatal, condition-gated
  #               omissions parent issue #2244 named (a missing verdict-
  #               comment relay when research is read-only can never post its
  #               verdict; a missing reviewer VERDICT: line when the
  #               orchestrator is enabled can never gate the multi-pass
  #               review loop) -- both narrow and already condition-gated, so
  #               a missing marker there is unambiguous. "warn" for the other
  #               two, which already have a working non-fatal backstop (PR
  #               intent: the existing nudge + bundle-adopt salvage path;
  #               issue intent: the filer's best-effort PR-body fallback), so
  #               treating their absence as fatal would be a false positive.
  #   when     -- a symbolic gating-condition name, not yet consumed by any
  #               bash/Nix logic -- wiring `when` to an actual runtime
  #               condition is future work, out of scope for this issue.
  #   message  -- the row's fully pre-rendered diagnostic prose (marker
  #               already interpolated), surfaced verbatim as the reject-
  #               error or warn-entry text by promptassembly.Validate's
  #               data-driven dispatch (issue #2405) -- no runtime %s/
  #               Sprintf substitution needed.
  validateMarkers = [
    {
      id = "verdict-comment-relay";
      marker = "SPINDRIFT_COMMENT";
      carrier = "fragment-body";
      severity = "reject";
      when = "readOnlyResearch";
      message = "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'SPINDRIFT_COMMENT' marker -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.";
    }
    {
      id = "reviewer-verdict";
      marker = "VERDICT:";
      carrier = "subagent-first-line";
      severity = "reject";
      when = "orchestratorEnabled";
      message = "_validate_prompt_contract: the orchestrator's rendered review prompt is missing the required 'VERDICT:' marker -- this belongs in review-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) verdict line; without it the code-owned review loop has nothing to gate on. Refusing to invoke the Driver.";
    }
    {
      id = "pr-intent";
      marker = "SPINDRIFT_PR_INTENT";
      carrier = "fragment-body";
      severity = "warn";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: warning -- read-only dispatch's rendered prompt is missing the 'SPINDRIFT_PR_INTENT' marker (belongs in issue-prompt.md's, or fix-prompt.md's injected, OPEN A PULL REQUEST section). Proceeding: a status=ready run with no PR-intent line still gets one resume-nudge attempt post-driver, and a genuinely exhausted attempt falls back to the merge-blocked report rather than losing the branch.";
    }
    {
      id = "issue-intent";
      marker = "SPINDRIFT_ISSUE_INTENT";
      carrier = "fragment-body";
      severity = "warn";
      when = "filerFileRelay";
      message = "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.";
    }
  ];

  # Each injectBlocks row's canonical text, keyed by `id`, sliced live from
  # its declared `source` prompt file -- pure derivation off injectBlocks, so
  # a new row picked up here automatically without a hand-written case per
  # block.
  canonicalText = builtins.listToAttrs (
    map (row: {
      name = row.id;
      value = sliceRow row;
    }) injectBlocks
  );

  # The single list of every injectBlocks row's marker, in row order -- one
  # place a future Nix bake / bash runtime injector / parity check can all
  # read instead of independently re-declaring the same marker strings
  # (issue #2244 user story 19).
  markerList = map (row: row.marker) injectBlocks;

  # Look up one injectBlocks row by id -- the single copy every consumer
  # (nix/checks/prompt-contract.nix, nix/checks/image.nix, lib/mkHarness.nix)
  # shares instead of each re-declaring the same filter-and-head one-liner
  # (issue #2246 review).
  byId = id: builtins.head (builtins.filter (r: r.id == id) injectBlocks);

  # Each injectBlocks row rendered into a pipe-joined string, in row order --
  # the same "row -> pipe-joined string" shape lib/mkHarness.nix's
  # fragmentRegistryRows uses for the fragment registry (see
  # fragmentRegistryRows/fragmentRegistryPreamble there). `endMarker` renders
  # as the empty string for a null (slice-to-EOF) row, so the field is still
  # present -- just empty -- rather than the whole row shrinking a field.
  # `kinds` renders as a single space-joined field.
  injectBlocksBashRows = map (
    row:
    let
      endMarkerRendered = if row.endMarker == null then "" else row.endMarker;
      kindsRendered = builtins.concatStringsSep " " row.kinds;
    in
    "${row.id}|${row.marker}|${row.source}|${row.startMarker}|${endMarkerRendered}|${kindsRendered}"
  ) injectBlocks;

  # injectBlocksBashRows wrapped into a bash array literal, formatted exactly
  # like lib/mkHarness.nix's fragmentRegistryPreamble renders
  # fragmentRegistryRows into `_FRAGMENT_ROWS`. Not yet consumed anywhere --
  # same "parallel, not-yet-wired data source" status as injectBlocks/
  # validateMarkers above.
  injectBlocksBashPreamble =
    "_INJECT_BLOCK_ROWS=(\n"
    + builtins.concatStringsSep "" (map (row: "  " + escapeShellArg row + "\n") injectBlocksBashRows)
    + ")\n";

  # Each validateMarkers row rendered into a pipe-joined string, in row
  # order -- mirrors injectBlocksBashRows above, minus the injectBlocks-only
  # fields (source/startMarker/endMarker/kinds) and minus message, which
  # bash rows don't need since only the Go decoder consumes it.
  validateMarkersBashRows = map (
    row: "${row.id}|${row.marker}|${row.carrier}|${row.severity}|${row.when}"
  ) validateMarkers;

  # validateMarkersBashRows wrapped into a bash array literal, formatted
  # exactly like injectBlocksBashPreamble above renders injectBlocksBashRows
  # into `_INJECT_BLOCK_ROWS` -- gives a following slice's in-box validator a
  # `_VALIDATE_MARKER_ROWS` array to scan by id (issue #2249), the same way
  # `_contract_marker`/`_INJECT_BLOCK_ROWS` already work for injectBlocks.
  validateMarkersBashPreamble =
    "_VALIDATE_MARKER_ROWS=(\n"
    + builtins.concatStringsSep "" (map (row: "  " + escapeShellArg row + "\n") validateMarkersBashRows)
    + ")\n";

  # Third pure-data registry (issue #2464): the OPPOSITE direction from
  # validateMarkers above. validateMarkers asserts a marker is *present* in a
  # rendered prompt under an active gate; forbiddenMarkers asserts a marker is
  # *absent* -- specifically, never rendered as an imperative telling a
  # read-only Box to perform the operation -- under an active gate. Every row
  # here names a write-capable git/gh operation a read-only Box's rendered
  # prompt must never order the Driver to run, since a read-only Box holds no
  # write-capable token for it.
  #
  # Same row shape as validateMarkers (id/marker/carrier/severity/when/
  # message), with one difference in how "present" is decided at validation
  # time: unlike validateMarkers, where a bare substring scan is sufficient
  # (any occurrence of the marker means it's present), whether a
  # forbiddenMarkers row's marker counts as a forbidden *occurrence* is
  # decided by the Go-side promptassembly.Validate function's shape-aware
  # imperative-vs-negation check (a later slice) -- e.g. "never run git push"
  # mentioning the marker in a negation is not itself a violation, while "run
  # git push now" is. This Nix list stays pure data either way, same as
  # validateMarkers -- it names the marker/carrier/severity/when/message per
  # row; it does not itself decide presence.
  #   id       -- short, stable identifier for the forbidden marker.
  #   marker   -- the literal marker text a Box's rendered prompt must not
  #               carry as an imperative.
  #   carrier  -- where the marker would appear if it were (wrongly) present;
  #               every row here is "fragment-body" (embedded anywhere in the
  #               body of a rendered prompt fragment), mirroring the
  #               validateMarkers carrier vocabulary above.
  #   severity -- "reject" for every row here: a read-only Box's rendered
  #               prompt ordering one of these write-capable operations is
  #               always fatal, never a soft warn -- there is no non-fatal
  #               backstop for a read-only Box being told to push or open a
  #               PR with no write-capable token to do it with.
  #   when     -- a symbolic gating-condition name, same vocabulary as
  #               validateMarkers' `when` -- every row here gates on
  #               "boxAccessReadOnly".
  #   message  -- the row's fully pre-rendered diagnostic prose (marker
  #               already interpolated), same "no runtime templating needed"
  #               contract as validateMarkers' `message` field.
  forbiddenMarkers = [
    {
      id = "forbidden-git-push";
      marker = "git push";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git push' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-pr-create";
      marker = "gh pr create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-pr-ready";
      marker = "gh pr ready";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-pr-merge";
      marker = "gh pr merge";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-issue-comment";
      marker = "gh issue comment";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-issue-create";
      marker = "gh issue create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-git-bundle-create";
      marker = "git bundle create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
  ];

  # Build-time reject arm (issue #2250, parent #2244): resolves each
  # validateMarkers "reject" row into one of ok/reject/advise from whatever
  # static gate/content knowledge a caller (lib/mkHarness.nix, a later slice)
  # can supply at build time. Iterates validateMarkers itself -- filtered to
  # severity == "reject" -- rather than a hand-duplicated id list, so a
  # future third reject row is picked up here automatically.
  #
  #   staticGates     -- attrset from a row's `when` symbol to a bool. A
  #                      `when` not present as a key is unresolved (not
  #                      knowable at build time), never treated as false.
  #   contentByRowId  -- attrset from a row's `id` to the prompt/fragment
  #                      text to search for that row's `marker`. An `id` not
  #                      present as a key means the content isn't available
  #                      at build time, also unresolved.
  #
  # A marker confirmed present in its content always wins ("ok"), regardless
  # of what the gate says -- a present marker is never a problem, known-
  # triggered or not. Absent from unresolvable content, or present-but-
  # missing-marker content under a false/unresolved gate, is "advise" (a
  # non-fatal nudge, since the condition isn't provably triggered); only
  # present-but-missing-marker content under a provably-true gate is
  # "reject" (the omission is unambiguous, mirroring
  # agent/entrypoint.sh's runtime _validate_prompt_contract reject arm).
  buildTimeRejectVerdicts =
    {
      staticGates,
      contentByRowId,
    }:
    let
      rejectRows = builtins.filter (row: row.severity == "reject") validateMarkers;
      verdictFor =
        row:
        let
          hasContent = builtins.hasAttr row.id contentByRowId;
          content = contentByRowId.${row.id} or "";
          markerPresent = hasContent && hasInfix row.marker content;
          gateKnownTrue = builtins.hasAttr row.when staticGates && staticGates.${row.when};
        in
        if markerPresent then
          {
            inherit (row) id marker;
            verdict = "ok";
            message = "";
          }
        else if hasContent && gateKnownTrue then
          {
            inherit (row) id marker;
            verdict = "reject";
            message = "mkHarness: '${row.id}' content is missing the required '${row.marker}' marker, and its gating condition '${row.when}' is statically known true at build time -- this omission can never be recovered from at runtime, so the build must fail now.";
          }
        else
          {
            inherit (row) id marker;
            verdict = "advise";
            message =
              if !hasContent then
                "mkHarness: '${row.id}' content is not available at build time, so its required '${row.marker}' marker can't be checked -- deferring to the runtime validator."
              else
                "mkHarness: '${row.id}' content is missing the required '${row.marker}' marker, but its gating condition '${row.when}' is not statically known true at build time -- deferring to the runtime validator instead of failing the build.";
          };
    in
    map verdictFor rejectRows;

  # Fold from a buildTimeRejectVerdicts verdict to "must the runtime bash
  # validator NOT block" (issue #2320, parent #2244): the runtime validator
  # (agent/entrypoint.sh's _validate_prompt_contract) only ever has a
  # resolved gate (0/1) at runtime, so it only ever blocks or doesn't --
  # there is no runtime "advise" state. "ok" and "advise" both fold to "must
  # not block"; only "reject" folds to "must block". `true` means "must not
  # block at runtime", `false` means "must block at runtime".
  parityFold = verdict: verdict != "reject";

  # Build-time/runtime parity fixtures (issue #2320, parent #2244; widened to
  # every row by issue #2356): one fixture per (validateMarkers row) x (gate
  # in [true false]) x (markerPresent in [true false]) combination. Iterates
  # ALL of validateMarkers now, not just the severity=="reject" rows --
  # buildTimeRejectVerdicts itself stays scoped to reject rows by design (see
  # its own doc comment above), but fixturesFor branches internally so a
  # severity=="warn" row still gets a fixture per combo: since a warn row's
  # runtime validator (promptassembly.Validate, issue #2356) never blocks
  # regardless of gate/markerPresent -- that is the whole point of
  # severity=="warn" already having a working non-fatal backstop -- its
  # verdict is always "advise" (the same non-fatal vocabulary
  # buildTimeRejectVerdicts already uses), by construction rather than by
  # calling buildTimeRejectVerdicts at all. This lets
  # tests/prompt-contract-parity.bats drive the real runtime validator
  # against every row, including warn ones, proving a warn row's actual
  # runtime behavior (never blocks) matches its always-non-"reject"
  # build-time verdict too, not just that reject rows fold correctly.
  parityFixtures =
    let
      fixturesFor =
        row:
        map
          (
            { gate, markerPresent }:
            let
              verdict =
                if row.severity == "reject" then
                  let
                    content = if markerPresent then "before ${row.marker} after" else "no marker here";
                    verdicts = buildTimeRejectVerdicts {
                      staticGates = {
                        ${row.when} = gate;
                      };
                      contentByRowId = {
                        ${row.id} = content;
                      };
                    };
                  in
                  (builtins.head (builtins.filter (r: r.id == row.id) verdicts)).verdict
                else
                  "advise";
            in
            {
              inherit (row) id;
              inherit gate markerPresent verdict;
            }
          )
          [
            { gate = true; markerPresent = true; }
            { gate = true; markerPresent = false; }
            { gate = false; markerPresent = true; }
            { gate = false; markerPresent = false; }
          ];
    in
    builtins.concatMap fixturesFor validateMarkers;
}
