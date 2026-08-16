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
  researchVerdicts = import ./research-verdicts.nix;
  builtinsCompat = import ./builtins-compat.nix;

  # Does `text` contain the literal (non-regex) `needle` -- mirrors
  # lib/prompt-inject.nix's own splitOnce/injectSection idiom
  # (builtins.split on a regex-escaped literal, checking for more than the
  # one no-match part).
  hasInfix =
    needle: text: builtins.length (builtins.split (builtinsCompat.escapeRegex needle) text) > 1;

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
  ensureTrailingBlankLine =
    s: if builtinsCompat.hasSuffix "\n\n" s then s else builtinsCompat.removeSuffix "\n" s + "\n\n";

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

  # Second pure-data registry (issue #2245, drawn from parent issue #2244's
  # classification of the harness's inject/outject markers): every marker a
  # Box's own prompt output is expected to emit, so a later slice can drive a
  # post-run validation pass from this data instead of the omission going
  # unnoticed until something downstream (a merge, a comment relay) silently
  # no-ops. The list itself stays data-only; runtime consumption is the Go
  # promptassembly.Validate function (issue #2405), driven from this data via
  # lib/mkHarness.nix's promptContractRegistryJson (forbiddenMarkers below has
  # its own sibling forbiddenMarkersRegistryJson).
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
  #   when     -- a symbolic gating-condition name, resolved to an actual
  #               runtime condition by cmd/launcher/internal/promptassembly's
  #               Validate function (validate.go's row.When switch), which is
  #               fed this data via lib/mkHarness.nix's
  #               promptContractRegistryJson below.
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
  # message). Presence is decided by a bare substring scan, same as
  # validateMarkers -- any occurrence of a kind == "substring" row's marker
  # in scanned content counts as a violation, whether it's an imperative
  # ("run git push now") or a negation ("never run git push"); issue #2513
  # deleted the prose-imperative heuristic that used to distinguish the two
  # Go-side. The only exemption is structural, not text-shape-based: a
  # fragment whose `gate` proves it's the read-only-labeled half of an
  # explicit access-mode pair is excluded from the scan entirely (see
  # buildTimeForbiddenMarkerViolations' own doc comment below, and
  # lib/mkHarness.nix's readOnlyReachableFragmentRows filter). This Nix list
  # stays pure data either way -- it names the marker/carrier/severity/when/
  # message per row; it does not itself decide presence.
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
  #   kind     -- how the marker is matched: a plain "substring" scan for
  #               most rows here (buildTimeForbiddenMarkerViolations below,
  #               build-time only as of issue #2513), or "gh-api-mutation"
  #               for the row keyed off a `gh api` mutating verb+endpoint
  #               pair (readonlyguards.go's command-shim argument scan)
  #               instead of a literal command string -- that row's marker
  #               is display-only, excluded from the build-time substring
  #               scan (buildTimeForbiddenMarkerViolations filters to
  #               kind == "substring" only).
  #   enforce  -- which layer (if any) backstops this row at runtime, beyond
  #               the build-time corpus scan every "substring" row gets:
  #               "git-hook" or "command-shim" name a runtime guard
  #               (readonlyguards.go) that also blocks the operation
  #               in-box, while "prompt-only" means no runtime backstop
  #               exists at all -- a runtime guard would collide with a
  #               legitimate in-box use of the same operation, and (for a
  #               row like forbidden-git-bundle-create) the build-time
  #               corpus scan is this row's only enforcement, since it never
  #               sees a Consumer's own `--prompts-dir` override.
  #   runtimeMessage -- present only on rows whose `enforce` is "git-hook" or
  #               "command-shim": the distinct, runtime-facing wording that
  #               readonlyguards.go renders into the installed shim/hook
  #               script itself. Deliberately not the same text as `message`
  #               above, which stays written for a rendered-prompt-facing
  #               diagnostic ("the rendered prompt orders...", "Refusing to
  #               invoke the Driver") -- nonsensical printed by a runtime
  #               shim after the agent typed the command itself mid-run and
  #               only that one command is being rejected, not the whole run
  #               aborting. A row with no runtime backstop
  #               (enforce = "prompt-only") carries no runtimeMessage.
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
      # runs `git bundle create` in-box as the harness's legitimate mechanism
      # for relaying committed work out of a read-only Box. A runtime
      # git-hook or command-shim guard on this marker would block that
      # in-box use along with the thing this row actually guards against
      # (a rendered prompt imperatively ordering a read-only Box to run it).
      # So this row is enforced only at the prompt level: Validate rejects
      # it if the *rendered prompt* orders it imperatively, never by a
      # runtime block.
      enforce = "prompt-only";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'git bundle create' -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation. Refusing to invoke the Driver.";
    }
    {
      id = "forbidden-gh-api-mutation";
      marker = "gh api";
      carrier = "fragment-body";
      severity = "reject";
      when = "boxAccessReadOnly";
      # kind "gh-api-mutation", not "substring": this row documents the
      # bespoke bash argument-scanner in agent/entrypoint.sh's
      # install_readonly_gh_shim, which rejects `gh api` calls carrying a
      # mutating HTTP method (-X/--method POST/PATCH/PUT/DELETE), not every
      # `gh api` invocation -- a read-only Box may legitimately run
      # read-only `gh api` calls. The marker field above is display-only
      # under this kind; the real match is the shim's argument-scan.
      kind = "gh-api-mutation";
      enforce = "command-shim";
      message = "_validate_prompt_contract: read-only dispatch's rendered prompt orders a read-only Box to run 'gh api' with a mutating method (-X/--method POST/PATCH/PUT/DELETE) -- gated under boxAccessReadOnly, a read-only Box holds no write-capable token for this operation; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use. Refusing to invoke the Driver.";
      runtimeMessage = "read-only Box: gh api does not accept a mutating method under read-only; make this change through the same relay a `gh pr create`/`gh issue create`/`gh issue comment` write would use -- this call has been blocked locally.";
    }
    # The five fj rows below mirror the gh rows above them one-for-one, and
    # are enforced the same way: "command-shim". driver-exec's
    # readonly-guards verb (issue #2509) renders a real fj command-shim from
    # these rows exactly like it does the gh rows above, and
    # agent/entrypoint.sh's install_readonly_guards installs the command-shim
    # guards (gh and fj alike) for every read-only Box unconditionally --
    # decoupled from the git-hook guard's own outbox-capability gate via the
    # readonly-guards verb's -skip-git-hook flag
    # (readonlyguards.Config.SkipGitHook). Only the git-hook guard stays
    # gated on BOX_HOST_MEDIATED_REMOTE/BOX_OUTBOX_RELAY_CAPABLE, since
    # blocking `git push` locally would break the only hand-off a Box like
    # forgejo (outboxRelayCapable: false, cmd/launcher/backend.go) has.
    # readonlyguards.installCommandShims resolves each row's argv0 on PATH
    # before shimming it and skips gracefully when absent, so this never
    # falsely claims a shim on a Box whose image doesn't bake that binary --
    # a real fj shim installs on any read-only Box where `fj` resolves on
    # PATH (a forgejo Box bakes it; a github Box doesn't and skips it), same
    # as the gh rows install where `gh` resolves.
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

  # Fourth pure-data registry (issue #2491): records that the "worker" role
  # (lib/roster.nix's roster entry `name = "worker"`, `promptFile =
  # "worker-prompt.md"`) is forbidden from carrying the SPINDRIFT_OUTCOME
  # marker or either verdict marker ("VERDICT: APPROVE"/"VERDICT: BLOCK") in
  # its rendered prompt -- the worker's prompt contract carries no outcome
  # grammar by design, so a stray marker from a misbehaving worker must
  # never be able to terminate the run or satisfy the launcher's outcome
  # scanner.
  #
  # Separate list from forbiddenMarkers above, not an addition to it: the two
  # registries have different scoping semantics (forbiddenMarkers is
  # exclusively boxAccessReadOnly-scoped -- every row's `when` is that one
  # literal -- while this registry is role-scoped, keyed on the "worker"
  # roster entry, with no `when`/boxAccessReadOnly gating concept at all).
  # This registry is intentionally a lighter shape than forbiddenMarkers: no
  # carrier/kind/enforce fields, since the worker role has no "read-only
  # Box" runtime shim mechanism for those fields to describe.
  #
  # Data-only, same as the other three registries, but for a different
  # reason: cmd/launcher/internal/promptassembly/validate.go's Validate
  # function is the sole runtime consumer of forbiddenMarkers (via
  # lib/mkHarness.nix's forbiddenMarkersRegistryJson, baked into the image by
  # lib/image.nix), and Validate is invoked only from
  # cmd/launcher/driver-exec/assembleprompt_cmd.go -- the coordinator/
  # main-box prompt assembly path. The worker prompt is assembled by a
  # wholly separate, simpler code path,
  # cmd/launcher/orchestrator/workers.go's seedWorkerPrompt, which never
  # calls promptassembly.Validate and is never fed through this (or any)
  # nix-baked registry JSON -- a deliberate structural quarantine (issue
  # #2059): worker logs live in their own workdir and are never scanned by
  # the orchestrator's outcome scanner (scanPassLog in
  # cmd/launcher/orchestrator/run.go), so there is no live enforcement point
  # analogous to Validate for the worker path. So this registry is not wired
  # into promptassembly.Validate or lib/mkHarness.nix/lib/image.nix; it is
  # pinned by two Go tests in cmd/launcher/orchestrator/markers_test.go --
  # TestWorkerPromptCarriesNoOutcomeGrammar checks these marker literals
  # directly against the rendered templates/default/prompts/worker-prompt.md
  # file, and TestWorkerForbiddenMarkersRegistryMatchesGoPin hand-transcribes
  # this registry's own marker set and asserts it matches -- not by any
  # runtime validation pass.
  #
  #   id      -- short, stable identifier for the forbidden marker.
  #   role    -- the roster entry name this row applies to; every row here is
  #              "worker", matching lib/roster.nix's `name = "worker"` entry.
  #   marker  -- the literal marker text the worker role's rendered prompt
  #              must never carry.
  #   message -- the row's fully pre-rendered diagnostic prose (marker
  #              already interpolated), same "no runtime templating needed"
  #              contract as the other registries' `message` field.
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

  # Build-time forbidden-marker check (issue #2510, parent #2498 campaign R):
  # the INVERSE of buildTimeRejectVerdicts above -- that one asserts a
  # required marker is *present* under a statically-known-true gate;
  # this one asserts a forbidden marker (forbiddenMarkers above) is *absent*
  # from the raw, unrendered content a caller hands it. Unlike
  # buildTimeRejectVerdicts, this check is unconditional -- it does not take
  # a `staticGates` argument and does not consult any Consumer's
  # staticGates/mergedDefaults, because a forbidden marker shipped in the
  # corpus is a problem for *any* Consumer that might configure
  # `boxAccessReadOnly`, not just one particular build's own configuration.
  # There is no "not triggered"/advise branch here the way there is for
  # buildTimeRejectVerdicts: every violation is unconditionally reported.
  #
  #   fragmentContentByFile -- attrset from a fragment's basename (under
  #                            templates/default/prompts/fragments/) to its
  #                            raw file content. Callers are expected to
  #                            have already filtered this down to whichever
  #                            fragment rows should actually be scanned
  #                            (e.g. lib/mkHarness.nix excludes fragments
  #                            whose `gate` proves they're the access-mode-
  #                            aware half of an explicit read-only/read-write
  #                            pair, or the filer's independent-token direct-
  #                            write path).
  #   templateContentByFile -- attrset from a shared top-level template's
  #                            basename (issue-prompt.md/review-prompt.md/
  #                            filer-prompt.md) to its raw, unsubstituted
  #                            text -- scanned unconditionally, no exemption.
  #
  # Only forbiddenMarkers rows with kind == "substring" are checked here: the
  # "gh-api-mutation" row's marker is display-only (see forbiddenMarkers'
  # own doc comment above), matched by agent/entrypoint.sh's bash argument
  # scanner instead of a literal substring, so it can't be usefully scanned
  # this way.
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

  # Per-kind agent-emittable SPINDRIFT_OUTCOME status sets (issue #2504,
  # parent #2498 campaign V). The single source of truth for "what status=
  # words a Box may legitimately print" for each dispatch kind -- regen
  # renders these into typed Go constants (lib/renderers.nix's
  # renderOutcomeStatusGo -> cmd/launcher/internal/outcome/status_gen.go)
  # and into every prompt/template/nudge spelling of the valid values, so
  # none of them can drift from another by a hand-typed edit to just one
  # side.
  #
  # Host-side dispositions (failed, merge verification) are a separate typed
  # family (ADR 0039: the Box advises, the host decides) and are
  # deliberately absent here -- this registry is scoped to what a Box itself
  # may emit. `merged` is also deliberately absent: settle's switch
  # (cmd/launcher/internal/settle/gate.go) tolerates it as an off-script,
  # unprovenanced arm -- no prompt fragment ever instructs a Box to print
  # it, so it is never a documented, emittable value.
  #
  # The research row's statuses are the compiled-DEFAULT research verdict
  # vocabulary (cmd/launcher/internal/forge/verdict.go's
  # ResearchVerdictLabels): research verdicts are actually
  # operator-configurable via RESEARCH_VERDICTS, so this row is the fallback
  # default set, not a closed enum -- read forge/verdict.go before touching
  # this row.
  #
  #   kind     -- the dispatch kind this status set applies to ("work",
  #               "research").
  #   statuses -- the ordered list of valid status= words for that kind.
  #               `blocked` legitimately appears in both -- a real,
  #               independent escape-hatch word for each kind, not a
  #               collision.
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
}
