# Pure-data registry of the harness-owned shared prompt blocks (issue #2245).
# lib/mkHarness.nix and the marker-parity checks under nix/checks/ derive from
# it (issue #2246), so the row shape below is the single place the
# block-to-prompt-kind mapping is written down. Pure builtins only (no
# `pkgs.lib`), so a bare `nix eval` can unit-test it (issue #512, issue #402).
let
  promptInject = import ./prompt-inject.nix;
  researchVerdicts = import ./research-verdicts.nix;
  builtinsCompat = import ./builtins-compat.nix;

  # Literal (non-regex) substring test, the same idiom lib/prompt-inject.nix
  # uses: split on a regex-escaped needle and check for more than one part.
  hasInfix =
    needle: text: builtins.length (builtins.split (builtinsCompat.escapeRegex needle) text) > 1;

  # A sliceBetween slice normally ends with the blank line that separated the
  # block from the next heading, but issue #2462's COMMIT_PUSH_* placeholder
  # sits directly on issue-prompt.md's "# REVIEW" endMarker with no blank line
  # (the fragment loop already appends "\n\n", and a template-level blank line
  # would double it), so that one slice needs the invariant restored here.
  ensureTrailingBlankLine =
    s: if builtinsCompat.hasSuffix "\n\n" s then s else builtinsCompat.removeSuffix "\n" s + "\n\n";

  # Slices one injectBlocks row's canonical text live from its declared
  # `source` prompt file, never a standalone contract file, so the injected
  # copy can never drift from the default prompt's own copy (issue #419).
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
  # Row fields: `source` is the default prompt the text is sliced from; there
  # is no standalone contract file, which is what keeps the injected copy
  # identical to that prompt's own (issue #419). `marker` is what
  # injectSection scans a target prompt for to skip an already-present block;
  # `startMarker` is inclusive, `endMarker` exclusive or `null` for the end.
  injectBlocks = [
    {
      id = "outcome";
      marker = "# LAND THE CHANGE";
      source = "issue-prompt.md";
      startMarker = "# LAND THE CHANGE";
      endMarker = null;
      # issue-prompt.md is the source and carries the section inline;
      # research-prompt.md has its own outcome contract (see
      # "research-verdict" below), so only fix-prompt.md needs this injected.
      kinds = [
        "issue"
        "fix"
      ];
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
      # research-prompt.md is the source and carries the section inline;
      # research-self-contained-prompt.md shares the same verdict-posting
      # contract and needs it injected.
      kinds = [
        "research"
        "research-self-contained"
      ];
    }
  ];

  # Markers a Box's prompt output must emit (issue #2245, parent #2244),
  # consumed by promptassembly.Validate (issue #2405) via lib/mkHarness.nix's
  # promptContractRegistryJson. `when` names a gate validate.go resolves.
  # severity "warn" is a standing choice, not an unfinished promotion to
  # "reject": each warn row's message names the fallback (issue #2996).
  validateMarkers = [
    {
      id = "verdict-comment-relay";
      marker = "SPINDRIFT_COMMENT";
      carrier = "fragment-body";
      severity = "reject";
      when = "readOnlyResearch";
      message = "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'SPINDRIFT_COMMENT' marker -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.";
      socketMarker = "driver-exec signal comment";
      socketMessage = "_validate_prompt_contract: read-only research dispatch's rendered prompt is missing the required 'driver-exec signal comment' call -- this belongs in research-prompt.md's (or a SPINDRIFT_PROMPT_DIR override's) POST THE VERDICT section; without it a read-only Box has no way to hand its verdict to the launcher. Refusing to invoke the Driver.";
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
      socketMarker = "driver-exec signal pr-intent";
      socketMessage = "_validate_prompt_contract: warning -- read-only dispatch's rendered prompt is missing the 'driver-exec signal pr-intent' call (belongs in issue-prompt.md's, or fix-prompt.md's injected, OPEN A PULL REQUEST section). Proceeding: a status=ready run with no PR-intent line still gets one resume-nudge attempt post-driver, and a genuinely exhausted attempt falls back to the merge-blocked report rather than losing the branch.";
    }
    {
      id = "issue-intent";
      marker = "SPINDRIFT_ISSUE_INTENT";
      carrier = "fragment-body";
      severity = "warn";
      when = "filerFileRelay";
      message = "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.";
      socketMarker = "driver-exec signal issue-intent";
      socketMessage = "_validate_prompt_contract: warning -- filer-relay dispatch's rendered filer prompt is missing the 'driver-exec signal issue-intent' call (belongs in filer-prompt.md's, or a SPINDRIFT_PROMPT_DIR override's, filer-file-relay-injected section). Proceeding: the filer's own best-effort PR-body fallback still records the issue reference even without the relay.";
    }
    {
      id = "research-issue-intent";
      marker = "SPINDRIFT_ISSUE_INTENT";
      carrier = "fragment-body";
      severity = "warn";
      when = "researchFileRelay";
      message = "_validate_prompt_contract: warning -- research dispatch's rendered prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker under an active Filer-relay gate (belongs in research-prompt.md's, or research-self-contained-prompt.md's, POST THE VERDICT section, research-file-issues-relay.md-substituted). Proceeding: any finding the filer can't relay still surfaces inline via its own best-effort fallback (describe it directly in the verdict body), and the researcher's posted verdict comment is unaffected either way.";
      socketMarker = "driver-exec signal issue-intent";
      socketMessage = "_validate_prompt_contract: warning -- research dispatch's rendered prompt is missing the 'driver-exec signal issue-intent' call under an active Filer-relay gate (belongs in research-prompt.md's, or research-self-contained-prompt.md's, POST THE VERDICT section, research-file-issues-relay.md-substituted). Proceeding: any finding the filer can't relay still surfaces inline via its own best-effort fallback (describe it directly in the verdict body), and the researcher's posted verdict comment is unaffected either way.";
    }
  ];

  # Each injectBlocks row's canonical text, keyed by `id`, derived from the
  # list so a new row needs no hand-written case.
  canonicalText = builtins.listToAttrs (
    map (row: {
      name = row.id;
      value = sliceRow row;
    }) injectBlocks
  );

  # Shared with lib/mkHarness.nix instead of re-declared there (issue #2246).
  byId = id: builtins.head (builtins.filter (r: r.id == id) injectBlocks);

  # The inverse of validateMarkers (issue #2464): every write-capable git/gh
  # operation a read-only Box's rendered prompt must never name, since such a
  # Box holds no write-capable token for it. Presence is a bare substring
  # scan, so a negation ("never run git push") counts too (issue #2513).
  # `enforce` names the readonlyguards.go runtime backstop, if any.
  forbiddenMarkers = [
    {
      id = "forbidden-git-push";
      marker = "git push";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "git-hook";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git push' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the committed branch is relayed via the outbox instead, so a Box must never run 'git push' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: your committed branch is relayed via the outbox; do not push -- this push has been blocked locally.";
    }
    {
      id = "forbidden-gh-pr-create";
      marker = "gh pr create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; PRs are opened via the PR-intent relay (SPINDRIFT_PR_INTENT), so a Box must never run 'gh pr create' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: PRs are opened via the PR-intent relay -- `driver-exec signal pr-intent` under BOX_SIGNAL_CARRIER=socket, a SPINDRIFT_PR_INTENT line otherwise; do not run `gh pr create` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-gh-pr-ready";
      marker = "gh pr ready";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher flips the PR ready once CI is green, so a Box must never run 'gh pr ready' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: the launcher flips the PR ready once CI is green; do not run `gh pr ready` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-gh-pr-merge";
      marker = "gh pr merge";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher merges the PR once CI is green, so a Box must never run 'gh pr merge' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: the launcher merges the PR once CI is green; do not run `gh pr merge` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-gh-issue-comment";
      marker = "gh issue comment";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issue comments are relayed via the outcome contract's `note=` field, so a Box must never run 'gh issue comment' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: a WORK Box relays comments via the outcome note= field; a RESEARCH Box relays its verdict via `driver-exec signal comment` under BOX_SIGNAL_CARRIER=socket, a SPINDRIFT_COMMENT line otherwise; do not run `gh issue comment` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-gh-issue-create";
      marker = "gh issue create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issues are filed via the issue-intent relay (SPINDRIFT_ISSUE_INTENT), so a Box must never run 'gh issue create' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: issues are filed via the issue-intent relay -- `driver-exec signal issue-intent` under BOX_SIGNAL_CARRIER=socket, a SPINDRIFT_ISSUE_INTENT line otherwise; do not run `gh issue create` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-git-bundle-create";
      marker = "git bundle create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      # prompt-only: driver-exec's bundle-out step runs `git bundle create`
      # in-box as the legitimate way to relay committed work out of a
      # read-only Box, so a hook or shim here would block that too. Only the
      # rendered prompt ordering it is rejected.
      enforce = "prompt-only";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-api-mutation";
      marker = "gh api";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      # kind "gh-api-mutation", not "substring": agent/entrypoint.sh's
      # install_readonly_gh_shim rejects only `gh api` calls carrying a
      # mutating method (-X/--method POST/PATCH/PUT/DELETE), since read-only
      # `gh api` calls are legitimate, so `marker` here is display-only.
      kind = "gh-api-mutation";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh api' with a mutating method (-X/--method POST/PATCH/PUT/DELETE) -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: gh api does not accept a mutating method under read-only; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use -- this call has been blocked locally.";
    }
    # The fj rows below mirror the gh rows one-for-one and are shimmed the
    # same way (issue #2509). agent/entrypoint.sh installs the command-shim
    # guards unconditionally, while the git-hook guard stays gated on
    # BOX_HOST_MEDIATED_REMOTE/BOX_OUTBOX_RELAY_CAPABLE. No backend leaves
    # both false today (issue #2927), but the gate stays for one that might.
    {
      id = "forbidden-fj-pr-create";
      marker = "fj pr create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; forgejo PRs are opened via the PR-intent relay (SPINDRIFT_PR_INTENT), the same host-mediated relay a read-only github Box uses for `gh pr create`, applied over the forgejo relay path. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: PRs are opened via the PR-intent relay (SPINDRIFT_PR_INTENT); do not run `fj pr create` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-fj-pr-ready";
      marker = "fj pr ready";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr ready' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher flips the PR ready once CI is green over the forgejo relay path, so a Box must never run 'fj pr ready' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: the launcher flips the PR ready once CI is green; do not run `fj pr ready` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-fj-pr-merge";
      marker = "fj pr merge";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj pr merge' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; the launcher merges the PR once CI is green over the forgejo relay path, so a Box must never run 'fj pr merge' itself. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: the launcher merges the PR once CI is green; do not run `fj pr merge` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-fj-issue-comment";
      marker = "fj issue comment";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj issue comment' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issue comments are relayed via the outcome contract's `note=` field, the same relay a read-only github Box uses for `gh issue comment`, applied over the forgejo path. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: issue comments are relayed via the outcome note= field; do not run `fj issue comment` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-fj-issue-create";
      marker = "fj issue create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'fj issue create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; issues are filed via the issue-intent relay (SPINDRIFT_ISSUE_INTENT), the same relay a read-only github Box uses for `gh issue create`, applied over the forgejo path. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: issues are filed via the issue-intent relay (SPINDRIFT_ISSUE_INTENT); do not run `fj issue create` -- this call has been blocked locally.";
    }
  ];

  # The worker role (lib/roster.nix's `name = "worker"`) must never carry the
  # outcome or verdict markers in its rendered prompt (issue #2491), so a
  # stray marker cannot terminate the run. Role-scoped, so it is a separate
  # list from forbiddenMarkers. Nothing enforces it at runtime: the worker
  # path is quarantined (issue #2059) and Go tests in markers_test.go pin it.
  workerForbiddenMarkers = [
    {
      id = "worker-role-forbids-outcome";
      role = "worker";
      marker = "SPINDRIFT_OUTCOME";
      message = "prompt-contract: the worker role's rendered prompt (worker-prompt.md) must never carry the 'SPINDRIFT_OUTCOME' marker -- the worker's prompt contract carries no outcome grammar by design (issue #2491), so a stray marker from a misbehaving worker can never terminate the run or satisfy the launcher's outcome scanner.";
    }
    {
      id = "worker-role-forbids-verdict-approve";
      role = "worker";
      marker = "VERDICT: APPROVE";
      message = "prompt-contract: the worker role's rendered prompt (worker-prompt.md) must never carry the 'VERDICT: APPROVE' marker -- the worker's prompt contract carries no outcome grammar by design (issue #2491), so a stray marker from a misbehaving worker can never terminate the run or satisfy the launcher's outcome scanner.";
    }
    {
      id = "worker-role-forbids-verdict-block";
      role = "worker";
      marker = "VERDICT: BLOCK";
      message = "prompt-contract: the worker role's rendered prompt (worker-prompt.md) must never carry the 'VERDICT: BLOCK' marker -- the worker's prompt contract carries no outcome grammar by design (issue #2491), so a stray marker from a misbehaving worker can never terminate the run or satisfy the launcher's outcome scanner.";
    }
  ];

  # Resolves each validateMarkers "reject" row into ok/reject/advise from
  # whatever a caller can supply at build time (issue #2250, parent #2244). A
  # `when` or `id` absent from the arguments is unresolved, never false. A
  # present marker always wins; only available content that is missing the
  # marker under a provably-true gate rejects, everything else advises.
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

  # The inverse of buildTimeRejectVerdicts (issue #2510, parent #2498):
  # asserts forbiddenMarkers are absent from the raw content a caller hands
  # it. Unconditional, with no advise branch, because a forbidden marker in
  # the corpus breaks any Consumer that configures boxAccessReadOnly. Callers
  # pre-filter the fragments; only kind == "substring" rows can be scanned.
  buildTimeForbiddenMarkerViolations =
    {
      fragmentContentByFile,
      templateContentByFile,
    }:
    let
      substringRows = builtins.filter (row: row.kind == "substring") forbiddenMarkers;
      violationsIn =
        contentByFile:
        builtins.concatMap (
          file:
          builtins.concatMap (
            row:
            if hasInfix row.marker contentByFile.${file} then
              [
                {
                  inherit file;
                  marker = row.marker;
                  id = row.id;
                }
              ]
            else
              [ ]
          ) substringRows
        ) (builtins.attrNames contentByFile);
    in
    (violationsIn fragmentContentByFile) ++ (violationsIn templateContentByFile);

  # Asserts a research prompt never carries the envsubst placeholder for one
  # of lib/fragments.nix's FILER_FILE_DIRECT*-gated rows, which would file
  # issues directly instead of through the SPINDRIFT_ISSUE_INTENT relay (issue
  # #2595, ADR 0041). True by construction today; this is the backstop that
  # turns a future regression into a build failure. Unconditional, like above.
  buildTimeResearchDirectFileViolations =
    { directFileFragmentRows, researchPromptContentByName }:
    let
      hasDirectPlaceholder =
        var: content:
        let
          pattern = "\\$[{]" + builtinsCompat.escapeRegex var + "[}]";
        in
        # Not hasInfix: escapeRegex would emit `\{`/`\}`, which Nix's ERE
        # engine rejects outright, so the braces are hand-wrapped in a
        # character class. Braced spelling only, so a bare `$VAR` placeholder
        # would not match.
        builtins.length (builtins.split pattern content) > 1;
    in
    builtins.concatMap (
      promptName:
      let
        content = researchPromptContentByName.${promptName};
      in
      builtins.concatMap (
        row:
        if hasDirectPlaceholder row.var content then
          [
            {
              inherit promptName;
              fragment = row.fragment;
              var = row.var;
            }
          ]
        else
          [ ]
      ) directFileFragmentRows
    ) (builtins.attrNames researchPromptContentByName);

  # Asserts every _LOG/_SOCKET fragment pair fork (issue #3726) actually
  # instructs the verb its carrier requires: a _SOCKET-gated fragment must
  # contain "driver-exec signal <channel>", and a _LOG-gated fragment must
  # contain its channel's markerChannels token. Which channel a row belongs
  # to comes from the row's own `signalChannel` field (lib/fragments.nix),
  # never a hand-listed filename table, so a new paired fragment only needs
  # that one field to be covered here. Rows with no `signalChannel` (every
  # non-signal fragment) are silently skipped. Takes the fragment rows and a
  # content map as arguments, like buildTimeResearchDirectFileViolations
  # above, so a check can hand it synthetic content and prove it really fails.
  buildTimeSignalFragmentViolations =
    { fragmentRows, fragmentContentByFile }:
    let
      channelToken = id: (builtins.head (builtins.filter (c: c.id == id) markerChannels)).token;
      requiredFor =
        row:
        if !(row ? signalChannel) then
          null
        else if builtinsCompat.hasSuffix "_SOCKET" row.gate then
          {
            kind = "verb";
            needle = "driver-exec signal " + row.signalChannel;
          }
        else if builtinsCompat.hasSuffix "_LOG" row.gate then
          {
            kind = "marker";
            needle = channelToken row.signalChannel;
          }
        else
          null;
    in
    builtins.concatMap (
      row:
      let
        required = requiredFor row;
        content = fragmentContentByFile.${row.fragment} or "";
      in
      if required == null || hasInfix required.needle content then
        [ ]
      else
        [
          {
            inherit (row) fragment gate;
            inherit (row) signalChannel;
            kind = required.kind;
            missing = required.needle;
            message = "prompt-contract: fragment '${row.fragment}' (gate '${row.gate}', signal channel '${row.signalChannel}') is missing the required ${required.kind} '${required.needle}' -- the Signal socket carrier fork (issue #3726) must instruct the same verb in both carrier modes.";
          }
        ]
    ) fragmentRows;

  # Folds a buildTimeRejectVerdicts verdict to "must the runtime validator NOT
  # block" (issue #2320, parent #2244). The runtime validator sees a resolved
  # gate and has no "advise" state, so only "reject" folds to "must block".
  parityFold = verdict: verdict != "reject";

  # One fixture per (validateMarkers row) x gate x markerPresent, driving
  # tests/prompt-contract-parity.bats against the real runtime validator
  # (issue #2320, widened to every row by issue #2356). A warn row's verdict
  # is "advise" by construction rather than by calling
  # buildTimeRejectVerdicts, since its runtime validator never blocks.
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
            {
              gate = true;
              markerPresent = true;
            }
            {
              gate = true;
              markerPresent = false;
            }
            {
              gate = false;
              markerPresent = true;
            }
            {
              gate = false;
              markerPresent = false;
            }
          ];
    in
    builtins.concatMap fixturesFor validateMarkers;

  # The single source of truth for the status= words a Box may print per
  # dispatch kind (issue #2504, parent #2498); regen renders it into Go
  # constants and into every prompt spelling of the valid values. Host-side
  # dispositions (ADR 0039) and `merged` are deliberately absent: no prompt
  # fragment ever instructs a Box to print them.
  outcomeStatusSets = [
    {
      kind = "work";
      statuses = [
        "ready"
        "blocked"
        "ambiguous"
      ];
    }
    {
      kind = "research";
      # Derived from lib/research-verdicts.nix's defaultVerdicts rather than
      # hand-typed, keeping the research vocabulary in one place (issue
      # #2524). Only the default set: RESEARCH_VERDICTS overrides it at
      # runtime, so read forge/verdict.go before treating this as closed.
      statuses = (map (v: v.verdict) researchVerdicts.defaultVerdicts) ++ [ "blocked" ];
    }
  ];

  outcomeStatusesFor =
    kind: (builtins.head (builtins.filter (r: r.kind == kind) outcomeStatusSets)).statuses;

  # The 5 marker channels a Box's output can carry (issue #2974, parent
  # #2972), rendered into Go by lib/renderers.nix's renderMarkerChannelsGo.
  # `defense` is what stops a corpus echoing a token back: a structurally
  # scoped extractor (ADR 0039, issue #2980) or RUN_NONCE. `fieldShape` reads
  # as prose, but markergate.substituteFieldShape parses its key=value layout.
  # `carrier`/`defense` describe the `log` Signal carrier (a marker line in
  # the mid-run log or final message); the three signal rows (comment,
  # pr-intent, issue-intent) additionally carry `socketCarrier`/
  # `socketDefense`, describing the `socket` Signal carrier
  # (BOX_SIGNAL_CARRIER, ADR 0052, issue #3726). The socket's defense is the
  # launcher-owned listener rather than RUN_NONCE: the Box reaches a socket
  # only the launcher opened, so no token in the corpus can be echoed into a
  # signal.
  markerChannels = [
    {
      id = "outcome";
      token = "SPINDRIFT_OUTCOME";
      fieldShape = "issue=<num> landing=<landing-ref> status=<status> note=<text>";
      defense = "structural";
      carrier = "final-message";
    }
    {
      id = "comment";
      token = "SPINDRIFT_COMMENT";
      fieldShape = "<nonce> <base64-payload>";
      defense = "nonce";
      carrier = "mid-run-log";
      socketCarrier = "signal-socket";
      socketDefense = "launcher-owned-listener";
    }
    {
      id = "pr-intent";
      token = "SPINDRIFT_PR_INTENT";
      fieldShape = "<nonce> <base64-payload>";
      defense = "nonce";
      carrier = "mid-run-log";
      socketCarrier = "signal-socket";
      socketDefense = "launcher-owned-listener";
    }
    {
      id = "issue-intent";
      token = "SPINDRIFT_ISSUE_INTENT";
      fieldShape = "<nonce> <base64-payload>";
      defense = "nonce";
      carrier = "mid-run-log";
      socketCarrier = "signal-socket";
      socketDefense = "launcher-owned-listener";
    }
    {
      id = "review-verdict";
      token = "VERDICT:";
      fieldShape = "APPROVE | BLOCK";
      defense = "structural";
      carrier = "subagent-first-line";
    }
  ];

  # Every (obligation, branch) pair whose content is missing one of the
  # obligation's requiredSubstrings (issue #2699). Takes the content map as an
  # argument rather than reading each branch's `source`, so a test can prove
  # the check really fails by handing it synthetic content.
  sharedObligationViolationsFor =
    obligations: contentBySource:
    builtins.concatMap (
      obligation:
      builtins.concatMap (
        branch:
        let
          content = contentBySource.${branch.source} or "";
          missing = builtins.filter (needle: !(hasInfix needle content)) obligation.requiredSubstrings;
        in
        if missing == [ ] then
          [ ]
        else
          [
            {
              obligationId = obligation.id;
              branchId = branch.id;
              source = branch.source;
              inherit missing;
              message = "prompt-contract: fork branch '${branch.id}' (${branch.source}) is missing shared obligation '${obligation.id}' -- missing substring(s): [${builtins.concatStringsSep ", " missing}].";
            }
          ]
      ) obligation.branches
    ) obligations;

  # Obligations both branches of a paired prompt fork must satisfy, so a fork
  # cannot silently drop a shared instruction the way commit-folding almost
  # did across REVIEW's inline/orchestrator split (issue #2698, issue #2699).
  # Each branch may word its own copy differently; only the literal
  # requiredSubstrings are compared. `source` is the raw, unexpanded file.
  sharedObligations = [
    {
      id = "commit-folding";
      branches = [
        {
          id = "review-loop-inline";
          source = "fragments/review-loop-inline.md";
        }
        {
          id = "commit-rework-orchestrator";
          source = "fragments/commit-rework-orchestrator.md";
        }
      ];
      requiredSubstrings = [
        "git commit --amend"
        "fold"
        "fixup"
        "force-pushes"
      ];
    }
    {
      # Pins the third triage outcome -- drop -- across both review loops, so
      # neither fork can regress to the old binary fix/escalate choice
      # (issue #3610).
      id = "triage-drop-arm";
      branches = [
        {
          id = "review-loop-inline";
          source = "fragments/review-loop-inline.md";
        }
        {
          id = "review-loop-orchestrator";
          source = "fragments/review-loop-orchestrator.md";
        }
      ];
      requiredSubstrings = [
        "three outcomes: fix inline, drop, or escalate."
        "Drop a finding that is correct but trivial"
        "legitimate third outcome"
        "The floor is worth, not certainty"
      ];
    }
  ];

  # sharedObligations checked against the real on-disk content of each
  # branch's declared `source` file (issue #2699), the live counterpart to
  # sharedObligationViolationsFor above.
  sharedObligationViolations = sharedObligationViolationsFor sharedObligations (
    builtins.listToAttrs (
      map (b: {
        name = b.source;
        value = builtins.readFile (../templates/default/prompts/${b.source});
      }) (builtins.concatMap (o: o.branches) sharedObligations)
    )
  );
}
