# Pure-data registry of the harness-owned shared prompt blocks: the outcome
# contract, COMMS, CHECK/COMMIT, and research-verdict blocks. lib/mkHarness.nix
# and the marker-parity checks under nix/checks/ derive from this registry, so
# this file's row shape is the single place the block-to-prompt-kind mapping is
# written down.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-testable
# with a bare `nix eval`, without needing a locked nixpkgs.
let
  promptInject = import ./prompt-inject.nix;
  researchVerdicts = import ./research-verdicts.nix;
  builtinsCompat = import ./builtins-compat.nix;

  # Does `text` contain the literal (non-regex) `needle`.
  hasInfix =
    needle: text: builtins.length (builtins.split (builtinsCompat.escapeRegex needle) text) > 1;

  # sliceBetween's contract assumes a sliced block already ends with the blank
  # line separating it from the next heading in its source. The "check" block
  # breaks that: issue-prompt.md glues the COMMIT_PUSH_* placeholder directly
  # onto the "# REVIEW" endMarker with no blank line, because the fragment loop
  # already appends the block's own "\n\n" separator and a template-level blank
  # line would double it up. So the raw slice ends exactly at the placeholder
  # token. This normalizes that case back onto the documented invariant: a no-op
  # for every row that already satisfies it, one appended "\n\n" otherwise.
  ensureTrailingBlankLine =
    s: if builtinsCompat.hasSuffix "\n\n" s then s else builtinsCompat.removeSuffix "\n" s + "\n\n";

  # Slices one injectBlocks row's canonical text live from its declared `source`
  # prompt file -- never a standalone contract file, so it can never drift from
  # the default prompt's own copy.
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
  #                  the injected copy and that prompt's own copy byte-identical.
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
      # issue-prompt.md carries the section inline (it's the source), so outcome
      # injection only targets fix-prompt.md. research-prompt.md has its own
      # outcome contract (see "research-verdict" below).
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
      id = "code-comments";
      marker = "# CODE COMMENTS";
      source = "issue-prompt.md";
      startMarker = "# CODE COMMENTS";
      endMarker = "# CHECK";
      # fix-prompt.md has no comment-discipline section of its own, but a fix
      # touches code the same way an issue slice does. Injected right before the
      # CHECK/COMMIT block, mirroring issue-prompt.md's own
      # IMPLEMENT -> CODE COMMENTS -> CHECK order.
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
      kinds = [
        "research"
        "research-self-contained"
      ];
    }
  ];

  # Second pure-data registry: every marker a Box's own prompt output is expected
  # to emit, so a post-run validation pass can be driven from this data instead
  # of the omission going unnoticed until something downstream (a merge, a
  # comment relay) silently no-ops. Data-only; runtime consumption is the Go
  # promptassembly.Validate function, fed via lib/mkHarness.nix's
  # promptContractRegistryJson.
  #   id       -- short, stable identifier for the expected marker.
  #   marker   -- the literal marker text a Box's output is scanned for.
  #   carrier  -- where the marker is expected to appear:
  #               "subagent-first-line" for a marker that must be the first
  #               line of a review subagent's own output, vs "fragment-body"
  #               for one embedded anywhere in a rendered fragment's body.
  #   severity -- "reject" for a provably-fatal, condition-gated omission (a
  #               missing verdict-comment relay under read-only research can
  #               never post its verdict; a missing reviewer VERDICT: line
  #               under the orchestrator can never gate the review loop).
  #               "warn" where a working non-fatal backstop already exists,
  #               so treating absence as fatal would be a false positive.
  #   when     -- a symbolic gating-condition name, resolved to a runtime
  #               condition by promptassembly.Validate's row.When switch.
  #   message  -- fully pre-rendered diagnostic prose, surfaced verbatim by
  #               Validate -- no runtime Sprintf substitution needed.
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
    {
      id = "research-issue-intent";
      marker = "SPINDRIFT_ISSUE_INTENT";
      carrier = "fragment-body";
      severity = "warn";
      when = "researchFileRelay";
      message = "_validate_prompt_contract: warning -- research dispatch's rendered prompt is missing the 'SPINDRIFT_ISSUE_INTENT' marker under an active Filer-relay gate (belongs in research-prompt.md's, or research-self-contained-prompt.md's, POST THE VERDICT section, research-file-issues-relay.md-substituted). Proceeding: any finding the filer can't relay still surfaces inline via its own best-effort fallback (describe it directly in the verdict body), and the researcher's posted verdict comment is unaffected either way.";
    }
  ];

  # Each injectBlocks row's canonical text, keyed by `id`, sliced live from its
  # declared `source` prompt file -- so a new row is picked up automatically.
  canonicalText = builtins.listToAttrs (
    map (row: {
      name = row.id;
      value = sliceRow row;
    }) injectBlocks
  );

  # Look up one injectBlocks row by id.
  byId = id: builtins.head (builtins.filter (r: r.id == id) injectBlocks);

  # Third pure-data registry: the OPPOSITE direction from validateMarkers.
  # validateMarkers asserts a marker is *present* under an active gate;
  # forbiddenMarkers asserts a marker is *absent*. Every row names a
  # write-capable git/gh operation a read-only Box's rendered prompt must never
  # order the Driver to run, since it holds no write-capable token for it.
  #
  # Presence is a bare substring scan: any occurrence of a kind == "substring"
  # row's marker counts, whether imperative ("run git push now") or negation
  # ("never run git push") -- there is deliberately no prose-shape heuristic. The
  # only exemption is structural: a fragment whose `gate` proves it's the
  # read-only-labeled half of an access-mode pair is excluded from the scan
  # entirely (see buildTimeForbiddenMarkerViolations below).
  #   id       -- short, stable identifier for the forbidden marker.
  #   marker   -- the literal marker text a rendered prompt must not carry.
  #   carrier  -- same vocabulary as validateMarkers; every row here is
  #               "fragment-body".
  #   severity -- "reject" for every row: a read-only Box ordered to perform a
  #               write-capable operation it has no token for is always fatal,
  #               with no non-fatal backstop.
  #   when     -- same vocabulary as validateMarkers' `when`; every row here
  #               gates on "boxAccessReadOnly".
  #   message  -- fully pre-rendered diagnostic prose, as in validateMarkers.
  #   kind     -- how the marker is matched: a plain "substring" scan, or
  #               "gh-api-mutation" for the row keyed off a `gh api` mutating
  #               verb+endpoint pair. A "gh-api-mutation" marker is
  #               display-only, excluded from the build-time substring scan.
  #   enforce  -- which layer backstops this row at runtime, beyond the
  #               build-time corpus scan: "git-hook" or "command-shim" name a
  #               readonlyguards.go guard that also blocks the operation in-box;
  #               "prompt-only" means none exists, because a runtime guard would
  #               collide with a legitimate in-box use of the same operation.
  #   runtimeMessage -- present only when `enforce` is a runtime guard: the
  #               runtime-facing wording readonlyguards.go renders into the
  #               installed shim/hook. Deliberately distinct from `message`,
  #               which is phrased for a pre-run prompt diagnostic ("Refusing
  #               to invoke the Driver") and would be nonsense printed after
  #               the agent typed the command mid-run.
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
      runtimeMessage = "read-only Box: PRs are opened via the PR-intent relay (SPINDRIFT_PR_INTENT); do not run `gh pr create` -- this call has been blocked locally.";
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
      runtimeMessage = "read-only Box: issue comments are relayed via the outcome note= field; do not run `gh issue comment` -- this call has been blocked locally.";
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
      runtimeMessage = "read-only Box: issues are filed via the issue-intent relay (SPINDRIFT_ISSUE_INTENT); do not run `gh issue create` -- this call has been blocked locally.";
    }
    {
      id = "forbidden-git-bundle-create";
      marker = "git bundle create";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      kind = "substring";
      # prompt-only, never a runtime block: driver-exec's own bundle-out step
      # legitimately runs `git bundle create` in-box to relay committed work out
      # of a read-only Box, so a runtime guard would block that along with the
      # rendered-prompt case this row actually guards against.
      enforce = "prompt-only";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-api-mutation";
      marker = "gh api";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      # kind "gh-api-mutation", not "substring": the shim rejects `gh api` calls
      # carrying a mutating HTTP method (-X/--method POST/PATCH/PUT/DELETE), not
      # every invocation -- a read-only Box may legitimately run read-only
      # `gh api` calls. The marker field is display-only under this kind.
      kind = "gh-api-mutation";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh api' with a mutating method (-X/--method POST/PATCH/PUT/DELETE) -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: gh api does not accept a mutating method under read-only; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use -- this call has been blocked locally.";
    }
    # The fj rows below mirror the gh rows one-for-one and are enforced the same
    # way. Command-shim guards (gh and fj alike) install unconditionally for
    # every read-only Box; only the git-hook guard stays gated on
    # BOX_HOST_MEDIATED_REMOTE/BOX_OUTBOX_RELAY_CAPABLE, since blocking
    # `git push` locally would break the only hand-off a backend leaving both
    # false would have. No registered backend leaves both false today, but the
    # gate stays live for a future one that does.
    # installCommandShims resolves each row's argv0 on PATH before shimming and
    # skips gracefully when absent, so a github Box (no `fj` baked) simply skips
    # the fj shims rather than falsely claiming them.
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

  # Fourth pure-data registry: the "worker" role is forbidden from carrying the
  # SPINDRIFT_OUTCOME marker or either verdict marker in its rendered prompt --
  # the worker's prompt contract carries no outcome grammar by design, so a stray
  # marker from a misbehaving worker must never terminate the run or satisfy the
  # launcher's outcome scanner.
  #
  # Separate from forbiddenMarkers rather than added to it: that registry is
  # exclusively boxAccessReadOnly-scoped, while this one is role-scoped with no
  # gating concept at all. Hence the lighter shape -- no carrier/kind/enforce,
  # since the worker role has no runtime shim mechanism for them to describe.
  #
  # NOT wired into promptassembly.Validate or lib/mkHarness.nix. Validate is the
  # sole runtime consumer of forbiddenMarkers and runs only on the
  # coordinator/main-box assembly path; the worker prompt is assembled by
  # orchestrator/workers.go's seedWorkerPrompt, a deliberate structural
  # quarantine — worker logs live in their own workdir and are never scanned by
  # the orchestrator's outcome scanner, so no enforcement point analogous to
  # Validate exists. This registry is pinned instead by two Go tests in
  # cmd/launcher/orchestrator/markers_test.go.
  #
  #   id      -- short, stable identifier for the forbidden marker.
  #   role    -- the roster entry name this row applies to; every row here is
  #              "worker".
  #   marker  -- the literal marker text the worker's rendered prompt must
  #              never carry.
  #   message -- fully pre-rendered diagnostic prose, as in the other
  #              registries.
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

  # Build-time reject arm: resolves each validateMarkers "reject" row into one of
  # ok/reject/advise from whatever static gate/content knowledge the caller can
  # supply at build time. Iterates validateMarkers itself, filtered to
  # severity == "reject", so a future reject row is picked up automatically.
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

  # The INVERSE of buildTimeRejectVerdicts above: asserts a forbiddenMarkers
  # marker is *absent* from the raw, unrendered content a caller hands it.
  # Unconditional -- it takes no `staticGates` and consults no Consumer config,
  # because a forbidden marker shipped in the corpus is a problem for *any*
  # Consumer that might configure `boxAccessReadOnly`. There is no
  # "not triggered"/advise branch: every violation is reported.
  #
  #   fragmentContentByFile -- attrset from a fragment's basename to its raw
  #                            file content. Callers are expected to have
  #                            already filtered out exempt rows (see
  #                            lib/mkHarness.nix's
  #                            readOnlyReachableFragmentRows).
  #   templateContentByFile -- attrset from a shared top-level template's
  #                            basename to its raw, unsubstituted text --
  #                            scanned unconditionally, no exemption.
  #
  # Only kind == "substring" rows are checked: the "gh-api-mutation" row's marker
  # is display-only, matched by a bash argument scanner rather than a literal
  # substring, so it can't be usefully scanned this way.
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

  # ADR 0041: asserts a research prompt never carries the envsubst placeholder
  # for one of lib/fragments.nix's FILER_FILE_DIRECT*-gated rows -- the rows that
  # render `gh issue create`/`fj issue create` instructions straight into the
  # prompt instead of going through the SPINDRIFT_ISSUE_INTENT relay. Today this
  # holds only by construction; this function is the backstop that turns a future
  # regression into a build failure instead of a silent one.
  #
  # Unconditional, like buildTimeForbiddenMarkerViolations: a research prompt
  # structurally must never carry a direct-file var, for any Consumer's build.
  #
  # Scans for the literal `${VAR}` placeholder, not the fragment's rendered body:
  # substitution happens at runtime, so build time only ever sees the raw
  # template. GOTCHA: only the braced spelling is checked -- envsubst expands a
  # bare `$VAR` identically, so a template wiring a var in that way would slip
  # past. Not live today, since every research prompt renders through
  # promptassembly.substTokenRe, which only emits the braced form.
  #
  # GOTCHA: deliberately not this file's own `hasInfix`. escapeRegex
  # backslash-escapes `{`/`}`, which Nix's POSIX-extended regex engine rejects
  # outright ("invalid regular expression") -- braces are ERE interval
  # metacharacters with no backslash-escaped literal form. Wrapping them in a
  # single-character class (`[{]`/`[}]`) matches literally instead, so the
  # `${`/`}` wrapper is hand-built here; `row.var` itself still goes through
  # escapeRegex.
  #
  #   directFileFragmentRows    -- the rows to check for (each needs at least
  #                                 `fragment` and `var`). Callers filter
  #                                 lib/fragments.nix down to the DIRECT-gated
  #                                 rows themselves, so this function stays
  #                                 decoupled from the gate-naming convention
  #                                 and never looks at `gate`.
  #   researchPromptContentByName -- attrset from a research prompt's name
  #                                 (e.g. "research-prompt.md") to its raw,
  #                                 unsubstituted text.
  #
  # Returns a flat list of violation records, one per (promptName, row) pair
  # whose content contains that row's `${var}` placeholder: { promptName;
  # fragment; var; }.
  buildTimeResearchDirectFileViolations =
    { directFileFragmentRows, researchPromptContentByName }:
    let
      hasDirectPlaceholder =
        var: content:
        let
          pattern = "\\$[{]" + builtinsCompat.escapeRegex var + "[}]";
        in
        # Braced-only by convention -- a bare `$VAR` would not match.
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

  # Fold from a buildTimeRejectVerdicts verdict to "must the runtime validator
  # NOT block". The runtime validator only ever has a resolved gate, so it only
  # ever blocks or doesn't -- there is no runtime "advise" state. "ok" and
  # "advise" both fold to true (must not block); only "reject" folds to false.
  parityFold = verdict: verdict != "reject";

  # Build-time/runtime parity fixtures: one per (validateMarkers row) x (gate in
  # [true false]) x (markerPresent in [true false]). Iterates ALL of
  # validateMarkers, not just the reject rows buildTimeRejectVerdicts is scoped
  # to. A warn row's verdict is always "advise", derived by construction rather
  # than by calling buildTimeRejectVerdicts, since its runtime validator never
  # blocks regardless of gate/markerPresent. That lets
  # tests/prompt-contract-parity.bats drive the real runtime validator against
  # every row, proving a warn row's runtime behavior matches its build-time
  # verdict too, not just that reject rows fold correctly.
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

  # Per-kind agent-emittable SPINDRIFT_OUTCOME status sets: the single source of
  # truth for what status= words a Box may legitimately print for each dispatch
  # kind. regen renders these into typed Go constants and into every
  # prompt/template/nudge spelling of the valid values, so none can drift.
  #
  # Host-side dispositions are a separate typed family (ADR 0039: the Box
  # advises, the host decides) and are deliberately absent -- this registry is
  # scoped to what a Box itself may emit. `merged` is likewise absent: settle's
  # switch tolerates it as an off-script, unprovenanced arm, but no prompt
  # fragment ever instructs a Box to print it.
  #
  # The research row is the compiled-DEFAULT verdict vocabulary, not a closed
  # enum -- research verdicts are operator-configurable via RESEARCH_VERDICTS, so
  # read forge/verdict.go before touching it.
  #
  #   kind     -- the dispatch kind this status set applies to.
  #   statuses -- the ordered list of valid status= words for that kind.
  #               `blocked` legitimately appears in both -- an independent
  #               escape-hatch word per kind, not a collision.
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
      # Derived from lib/research-verdicts.nix's defaultVerdicts (the single
      # source of truth for the built-in research verdict tokens) plus the
      # "blocked" crash/no-verdict escape hatch, rather than a hand-typed
      # restatement of that list -- keeps the research vocabulary rooted in
      # exactly one place (issue #2524).
      statuses = (map (v: v.verdict) researchVerdicts.defaultVerdicts) ++ [ "blocked" ];
    }
  ];

  # Look up one outcomeStatusSets row's statuses by kind -- mirrors byId
  # above.
  outcomeStatusesFor =
    kind: (builtins.head (builtins.filter (r: r.kind == kind) outcomeStatusSets)).statuses;

  # The single authoritative statement of the marker channels a Box's rendered
  # prompt output can carry. The other marker registries in this file are each
  # scoped to one narrower question ("is the marker present", "is its operation
  # forbidden"); this one names the channel itself: what token spells it, what
  # grammar follows, how a scan defends against a prompt-injected corpus echoing
  # the token back, and where in the output stream it appears. Rendered into Go
  # constants, but Parse/Line stay hand-written against those constants rather
  # than table-driven off this registry.
  #
  #   id         -- short, stable channel identifier.
  #   token      -- the exact marker literal a Box emits for this channel.
  #                 Matches validateMarkers' `marker` for every channel that
  #                 has a row there; nix/checks/prompt-contract.nix's
  #                 cross-registry drift guard enforces the pairing. The
  #                 "outcome" row has no counterpart -- the outcome contract is
  #                 validated structurally (ADR 0047), never by scanning.
  #   fieldShape -- human-readable grammar of the fields after the token; not
  #                 machine-parsed here.
  #   defense    -- how the channel resists a prompt-injected corpus merely
  #                 echoing the token back:
  #                 "structural" (outcome, review-verdict) -- the channel has
  #                 its own extractor scoping the scan to the transcript span
  #                 it is allowed to speak from, rather than a substring match
  #                 anywhere in the corpus. outcome's is the in-box
  #                 `.result`/`.part.text` extraction plus the host's
  #                 leading-line requirement (ADR 0047); review-verdict's is
  #                 the reviewer-subagent tag `RenderTranscriptWithRole`
  #                 stamps onto a tool_result answering a completed reviewer
  #                 spawn, so a verdict-shaped string in an untagged
  #                 tool_result cannot count.
  #                 "nonce" (comment, pr-intent, issue-intent) -- RUN_NONCE is
  #                 the sole replay defense, per that same ADR.
  #                 "fold" -- unused. It named review-verdict's defense before
  #                 that channel got a structural extractor; the BLOCK-dominant
  #                 fold `passmachine.Scan` still applies is now
  #                 defense-in-depth, not the primary defense.
  #   carrier    -- where the token physically appears: "final-message" (the
  #                 driver's terminal result event), "mid-run-log" (anywhere
  #                 in a raw teed NDJSON line, no leading-line requirement,
  #                 per ADR 0047), or "subagent-first-line" (same vocabulary
  #                 value validateMarkers uses).
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
    }
    {
      id = "pr-intent";
      token = "SPINDRIFT_PR_INTENT";
      fieldShape = "<nonce> <base64-payload>";
      defense = "nonce";
      carrier = "mid-run-log";
    }
    {
      id = "issue-intent";
      token = "SPINDRIFT_ISSUE_INTENT";
      fieldShape = "<nonce> <base64-payload>";
      defense = "nonce";
      carrier = "mid-run-log";
    }
    {
      id = "review-verdict";
      token = "VERDICT:";
      fieldShape = "APPROVE | BLOCK";
      defense = "structural";
      carrier = "subagent-first-line";
    }
  ];

  # Every (obligation, branch) pair whose branch content is missing one of the
  # obligation's declared `requiredSubstrings`. A pure function of its argument,
  # not of sharedObligations' own `source` files, so a test can hand it synthetic
  # content proving the check can fail without touching a real fragment on disk.
  #   contentBySource -- attrset from a branch's `source` string to the text
  #                      to check it against.
  # Returns a list of records: { obligationId, branchId, source, missing,
  # message }, one per (obligation, branch) pair with at least one missing
  # substring. `missing` is the list of substrings that branch's content
  # lacked. `message` is fully pre-rendered prose naming both the offending
  # branch and the obligation.
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

  # Sixth pure-data registry: obligations BOTH branches of a paired prompt fork
  # must satisfy, so a future fork can't silently drop a shared instruction the
  # way commit-folding almost did across REVIEW's inline/orchestrator split.
  # Declares WHAT each branch's content must contain, not HOW it's worded --
  # each branch may phrase its copy differently.
  #
  #   id       -- short, stable identifier for the obligation.
  #   branches -- every fork branch this obligation applies to. Each entry:
  #     id     -- short, stable identifier for the branch (named in a
  #               violation message, so a human can tell forks apart).
  #     source -- the fragment/prompt file (relative to
  #               templates/default/prompts/) this branch's raw fragment
  #               text (unexpanded, e.g. `${BASE_BRANCH}` unsubstituted) is
  #               read from.
  #   requiredSubstrings -- every literal substring each branch's content
  #               must contain to satisfy the obligation.
  #
  # The commit-folding row: REVIEW's inline and orchestrator branches each
  # instruct folding a fix into an existing commit via amend/fixup rather than
  # stacking a new one, and both call out that the branch force-pushes so
  # rewriting history is expected -- worded differently in each file, but every
  # substring below is verbatim in both.
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
  ];

  # Every sharedObligations row checked against the real, on-disk content of each
  # branch's declared `source` -- the live counterpart to
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
