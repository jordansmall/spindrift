# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules (issue #419).
{
  pkgs,
  fixtures,
  nixpkgs,
  system,
  ...
}:
let
  inherit (fixtures)
    harness
    promptHarness
    fixPromptHarness
    researchPromptHarness
    researchVerdictsHarness
    batsHarness
    ;

  # Anti-drift caveman-coverage registry (issue #2709): one row per
  # top-level prompt template, declaring "covered" (with the envsubst
  # variable it must carry) or "exempt" (with a reason). Hoisted here, once,
  # so every caveman-coverage-* check below shares the same import instead
  # of each re-importing it.
  cavemanCoverageRegistry = import ../../lib/prompt-coverage.nix;

  # lib/prompt-contract.nix's own pure-data marker registries (validateMarkers,
  # workerForbiddenMarkers), imported once here so
  # caveman-coverage-exemption-list-covers-marker-registry below can derive
  # requiredMarkerNames from them instead of hand-retyping their marker
  # literals a second time.
  promptContract = import ../../lib/prompt-contract.nix;

  # lib/fragments.nix's own pure-data Conditional fragment registry, imported
  # once here so caveman-coverage-exemption-list-covers-marker-registry below
  # can derive the caveman fragment file list from its `gate == "CAVEMAN_BAKED"`
  # rows instead of hand-retyping the 4 caveman-default*.md paths a second
  # time.
  fragmentsRegistry = import ../../lib/fragments.nix;

  # Derived (not hand-typed) marker-name list for
  # caveman-coverage-exemption-list-covers-marker-registry below: every
  # marker from promptContract.validateMarkers plus every marker from
  # promptContract.workerForbiddenMarkers. So a FUTURE row added to either
  # of those two registries is picked up here automatically -- if nobody
  # then also names that new marker in at least one caveman fragment, the
  # check below fails. That's the literal acceptance criterion: adding a
  # machine-parsed marker without naming it in the exemption list fails the
  # check.
  #
  # The "issue-intent" row (SPINDRIFT_ISSUE_INTENT) is IN this union, not
  # excluded -- an earlier version of this comment argued it could be
  # dropped because its sole carrier, filer-prompt.md, is wholly
  # caveman-exempt per lib/prompt-coverage.nix. That's true of
  # filer-prompt.md, but false as a justification for the exclusion:
  # templates/default/prompts/issue-prompt.md -- a caveman-*covered* row
  # (cavemanVar = "CAVEMAN_STEP", rendered via caveman-default.md) --
  # itself interpolates FILE_ISSUES_RELAY_STEP (lib/fragments.nix), which
  # injects fragments/file-issues-relay.md, and that fragment's own text
  # names SPINDRIFT_ISSUE_INTENT. So a caveman-narrated issue-prompt.md can
  # carry a live SPINDRIFT_ISSUE_INTENT-emitting section, and
  # caveman-default.md's own "the machine-parsed marker grammar is exempt
  # too" paragraph must name it -- which it now does (issue #2709 review
  # finding).
  requiredMarkerNames =
    (map (r: r.marker) promptContract.validateMarkers)
    ++ (map (r: r.marker) promptContract.workerForbiddenMarkers);

  # The rendered CHECK section, sliced once here rather than once per check
  # across the never-background/git-add/anchor/scoped-target checks below
  # (issue #781) -- a marker rename only needs updating in one place, and
  # those checks just grep the shared output.
  # Anchored on end-of-line, not start-of-line (issue #3221): "# CHECK" now
  # trails the IMPLEMENT phase's own variable run
  # (${CODE_COMMENTS_STEP}# CHECK) in the raw, unrendered template this
  # slices, the same "vars-then-heading" idiom # COMMIT/# REVIEW/# LAND THE
  # CHANGE already use further down the same file -- a start-anchored match
  # would silently capture nothing.
  checkSectionSlices = pkgs.runCommand "check-section-slices" { } ''
    mkdir -p $out
    awk '/# CHECK$/{f=1} /# REVIEW$/{exit} f' \
      ${batsHarness.internals.promptDir}/issue-prompt.md > $out/issue-check.txt
  '';

  # The harness-owned skill body the CHECK section's guidance moved into
  # (issue #3220) -- the source SKILL.md, since lib/image.nix bakes this same
  # file verbatim.
  checkHygieneSkill = ../../templates/default/skills/check-hygiene/SKILL.md;

  # The dogfood-only skill body the CHECK section's Nix lore moved into
  # (issue #3223) -- the repo-root source SKILL.md, not the harness-baked
  # copy: nix-checks is not harnessOwned, so lib/image.nix never bakes it.
  nixChecksSkill = ../../skills/nix-checks/SKILL.md;

  # The CHECK-section anchor pointing at that skill. It renders from a
  # bakedness-gated fragment (lib/fragments.nix, CHECK_HYGIENE_BAKED), so the
  # CHECK slice above carries only the ${CHECK_HYGIENE_STEP} placeholder and
  # the anchor prose has to be pinned on the fragment body itself.
  checkHygieneAnchor = ../../templates/default/prompts/fragments/check-hygiene-default.md;

  # The CHECK-section anchor for the dogfood-only /nix-checks skill (issue
  # #3223), same bakedness-gated-fragment shape as checkHygieneAnchor above:
  # the CHECK slice carries only the ${NIX_CHECKS_STEP} placeholder, so the
  # anchor prose has to be pinned on the fragment body itself.
  nixChecksAnchor = ../../templates/default/prompts/fragments/nix-checks-default.md;

  # The IMPLEMENT-phase anchor pointing at the harness-owned code-comments
  # skill (nix/checks/image.nix's code-comments-skill-baked-into-image pins
  # the skill body itself). It renders from a bakedness-gated fragment
  # (lib/fragments.nix, CODE_COMMENTS_BAKED), so the raw template carries
  # only the ${CODE_COMMENTS_STEP} placeholder and the anchor prose has to
  # be pinned on the fragment body itself.
  codeCommentsAnchor = ../../templates/default/prompts/fragments/code-comments-default.md;

  # Broken fixture shared by both build-time-reject-research-verdict-comment-
  # relay-* checks below (issue #2250, parent #2244): the whole fragments
  # directory, cp -r'd from the real templates tree so every other fragment
  # fragmentRegistryPreamble's `cp -r` step needs is still present, but with
  # research-verdict-github-readonly.md swapped for a broken copy missing the
  # required SPINDRIFT_COMMENT marker -- mirrors the reviewPrompt fixture
  # build-time-reject-orchestrator-verdict-{missing,not-triggered} share
  # above.
  brokenResearchVerdictFragmentBody = ''
    Your GitHub token is read-only here -- you cannot comment on the issue
    yourself. Print the verdict as a single line on stdout instead -- the
    launcher finds it by this run's nonce, decodes it, and posts it to the
    issue, host-side, once you exit.

    This fixture deliberately omits the required marker line.
  '';
  brokenResearchVerdictFragmentsDir =
    pkgs.runCommand "broken-research-verdict-fragments"
      {
        passAsFile = [ "brokenBody" ];
        brokenBody = brokenResearchVerdictFragmentBody;
      }
      ''
        mkdir -p $out
        cp -r ${../../templates/default/prompts/fragments}/. $out/
        chmod -R u+w $out
        cp "$brokenBodyPath" $out/research-verdict-github-readonly.md
      '';

  # Broken fixture for the three forbidden-marker checks below (issue #2510,
  # parent #2498 campaign R): the whole fragments directory, cp -r'd from the
  # real templates tree (mirrors brokenResearchVerdictFragmentsDir above), but
  # with auto-format.md (gated on the plain, non-exempt "AUTO_FORMAT" gate)
  # swapped for a broken copy that carries the literal forbidden-marker
  # substring "git push" as authored fragment-body text.
  brokenForbiddenMarkerFragmentBody = ''
    Run `git push` here. This fixture deliberately injects a forbidden marker
    into a fragment gated on a plain, non-exempt gate.
  '';
  brokenForbiddenMarkerFragmentsDir =
    pkgs.runCommand "broken-forbidden-marker-fragments"
      {
        passAsFile = [ "brokenBody" ];
        brokenBody = brokenForbiddenMarkerFragmentBody;
      }
      ''
        mkdir -p $out
        cp -r ${../../templates/default/prompts/fragments}/. $out/
        chmod -R u+w $out
        cp "$brokenBodyPath" $out/auto-format.md
      '';

  # Exempt-gate counterpart fixture: same shape, but the broken content is
  # injected into open-pr-create-outbox.md (gated on "BOX_ACCESS_READ_ONLY",
  # which is exempt from the fragment-body forbidden-marker scan per lib/
  # mkHarness.nix's readOnlyReachableFragmentRows filter) instead of a
  # plain, non-exempt gate.
  exemptGateForbiddenMarkerFragmentBody = ''
    Do NOT run `git push` yourself here -- relay the branch instead. This
    fixture deliberately injects the forbidden-marker substring into a
    fragment gated on an exempt (read-only-labeled) gate, to prove the
    exemption rule protects it from a false positive.
  '';
  exemptGateForbiddenMarkerFragmentsDir =
    pkgs.runCommand "exempt-gate-forbidden-marker-fragments"
      {
        passAsFile = [ "exemptBody" ];
        exemptBody = exemptGateForbiddenMarkerFragmentBody;
      }
      ''
        mkdir -p $out
        cp -r ${../../templates/default/prompts/fragments}/. $out/
        chmod -R u+w $out
        cp "$exemptBodyPath" $out/open-pr-create-outbox.md
      '';

  # gh-api-mutation-kind counterpart (issue #2513): same plain, non-exempt
  # gate as brokenForbiddenMarkerFragmentsDir above, but carrying the
  # forbidden-gh-api-mutation row's marker text ("gh api") instead of a
  # kind == "substring" row's marker. buildTimeForbiddenMarkerViolations
  # (lib/prompt-contract.nix) only scans kind == "substring" rows -- a
  # "gh-api-mutation" row's marker is display-only there, enforced instead
  # by readonlyguards.go's command-shim argument scan (see
  # TestInstall_GhAPIMutationRejectsMutatingMethod) -- so this must NOT
  # throw, proving the kind filter still excludes it and hasn't regressed
  # to scanning every row regardless of kind.
  ghAPIMutationForbiddenMarkerFragmentBody = ''
    Never run `gh api` yourself here with a mutating method. This fixture
    deliberately injects the forbidden-gh-api-mutation row's marker text
    into a fragment gated on a plain, non-exempt gate, to prove the
    kind == "substring" filter still excludes it from the build-time scan.
  '';
  ghAPIMutationForbiddenMarkerFragmentsDir =
    pkgs.runCommand "gh-api-mutation-forbidden-marker-fragments"
      {
        passAsFile = [ "ghAPIMutationBody" ];
        ghAPIMutationBody = ghAPIMutationForbiddenMarkerFragmentBody;
      }
      ''
        mkdir -p $out
        cp -r ${../../templates/default/prompts/fragments}/. $out/
        chmod -R u+w $out
        cp "$ghAPIMutationBodyPath" $out/auto-format.md
      '';

  # Clean placeholder template text, carrying none of the forbiddenMarkers
  # substrings (issue #2510). The real templates/default/prompts/{issue,
  # filer}-prompt.md are both clean of forbiddenMarkers substrings as of
  # this branch's e9652e07 (the CODE_FORGE=git push text moved into a
  # gate-paired fragment). The three forbidden-marker checks below still
  # override `prompt`/`filerPrompt` with this placeholder wherever the check
  # isn't specifically exercising that param, so a check asserting success/
  # failure over the *fragment* scan (or over a deliberately broken `prompt`)
  # stays isolated from the real templates regardless of their current
  # content -- future template edits can't silently confound these checks.
  cleanForbiddenMarkerPlaceholder = "a clean placeholder prompt with no forbidden operations mentioned";

  # Expected content of the default-verdicts-rendered VERDICT..POST THE
  # VERDICT span (exclusive of the second marker), for
  # mkharness-prompt-research-verdicts-default-rendered below.
  researchVerdictDefaultFixture = pkgs.writeText "research-verdict-default-rendered.txt" ''
    # VERDICT

    Render exactly one of these verdicts:

    - `recommend` — relevant, now enriched with real context; promote it.
    - `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict.
    - `unclear` — relevance can't be determined without a human's answer.

  '';

  # Same, for the self-contained sub-mode prompt (ADR 0022, issue #2202):
  # keeps the template's own "Judge relevance..." sentence ahead of the
  # registry-rendered bullets, for
  # mkharness-prompt-research-self-contained-verdicts-default-rendered below.
  researchVerdictSelfContainedFixture = pkgs.writeText "research-verdict-self-contained-rendered.txt" ''
    # VERDICT

    Judge relevance from the issue content alone — there is no repo to explore.
    Render exactly one of these verdicts:

    - `recommend` — relevant, now enriched with real context; promote it.
    - `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict.
    - `unclear` — relevance can't be determined without a human's answer.

  '';

  # Issue #3228: several pinned review-prompt clauses run long enough to wrap
  # across source lines, which a raw `grep -qF` cannot see past. Match against
  # a whitespace-normalized copy instead — the same treatment
  # normalizeWhitespace gives these clauses in review_prompt_content_test.go,
  # and for the same reason: where the prose happens to wrap is not the
  # contract.
  normalizedGrep = ''
    normalized_grep() {
      tr -s '[:space:]' ' ' <"$1" | grep -qF "$2"
    }
  '';

  # Issue #3228: shared with the -not-in-gated-arms companion below so the
  # presence pin and the negative check can never drift apart -- a clause
  # added to only one side would either go unpinned or leave the negative
  # check blind to a regrown copy.
  phasedHuntAndTraceObligationClauses = [
    "before you record a single STANDARDS & SMELLS finding"
    "grep the tree for both the old and new forms"
    "read every caller, not just the definition"
    "name the shared state and walk one interleaving by hand"
    "trace where it propagates to"
  ];

  # Issue #3228: same drift-prevention rationale as
  # phasedHuntAndTraceObligationClauses above.
  failureScenarioAndProbedSectionClauses = [
    "constructing that scenario is the depth-forcing exercise, not a label"
    "A finding that cannot state that one-line failure scenario is Non-blocking by definition"
    "## Probed (APPROVE only)"
    "this is the receipt that turns APPROVE into work done, not an assertion taken on faith"
  ];
in
{
  # The configured `prompt` is rendered to a store-path directory and,
  # by default, baked into the image (see agentFiles) rather than
  # mounted — `run` only bind-mounts a dir under the
  # SPINDRIFT_PROMPT_DIR override. Eval/native only (the rendered
  # prompt dir is a host store path; the image bake is checked
  # Linux-side by prompt-baked-into-image below).
  # The conditional prompt mount is handled by the Go launcher binary,
  # so the bats suite verifies runtime behaviour rather than grepping
  # the wrapper's source.
  mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
    # The Consumer's prompt text is what lands in the rendered file.
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.internals.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still
  # ship an agent that emits the outcome line, so the launcher can learn the
  # PR (issue #419) — the harness appends the canonical contract exactly once.
  mkharness-prompt-outcome-injected = pkgs.runCommand "mkharness-prompt-outcome-injected" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${promptHarness.internals.promptDir}/issue-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the outcome contract injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # The default prompt already contains the contract, so injection must be a
  # no-op: no duplication (issue #419).
  mkharness-prompt-outcome-not-duplicated =
    pkgs.runCommand "mkharness-prompt-outcome-not-duplicated" { }
      ''
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.internals.promptDir}/issue-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the default prompt's outcome contract to stay single, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default box's rendered prompt must be byte-identical to the template
  # on disk — injection must not touch a prompt that already has the
  # contract (issue #419).
  mkharness-prompt-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/issue-prompt.md} ${batsHarness.internals.promptDir}/issue-prompt.md
        touch $out
      '';

  # The block injected into a prompt lacking the contract must be
  # byte-identical to the default prompt's own contract section — both are
  # sliced from the same marker in the same source file, so they cannot
  # drift apart (issue #419).
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.internals.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.internals.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # The no-drift check above only proves the injected block matches the
  # *same-source* contract slice -- it never asserts the slice says the right
  # thing. A source regression from `landing=` back to the pre-#638 `pr=`
  # grammar would still pass that diff, since both sides would drift
  # together. Pin the literal token directly (issue #654). Anchor the token
  # to the SPINDRIFT_OUTCOME line itself (not `^`, since the CODE_FORGE=git
  # example line is indented inside a fenced code block) -- an unanchored
  # grep would still pass if the real outcome line regressed to `pr=` while
  # some unrelated prose in the slice happened to mention "landing="
  # (issue #886).
  #
  # A single `grep -q` only proves *at least one* SPINDRIFT_OUTCOME example
  # line kept `landing=` -- a partial regression, where only one of several
  # example lines reverts to `pr=`, still passes because the surviving lines
  # mask it. Require every SPINDRIFT_OUTCOME line to carry `landing=`: count
  # the lines missing it and fail the build if that count isn't zero (issue
  # #887). A bare `! pipeline` won't do here -- `set -e` explicitly exempts
  # negated commands, so a failing assertion silently wouldn't stop the build.
  mkharness-prompt-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-outcome-contract-has-landing-token" { }
      ''
        # Floor guard: catches the degenerate case where every SPINDRIFT_OUTCOME
        # line -- and thus landing= itself -- vanishes from the contract, which
        # the per-line count below would otherwise wave through as 0 missing.
        grep -qE 'SPINDRIFT_OUTCOME.*landing=' ${batsHarness.internals.outcomeContractFile}
        missing=$(grep 'SPINDRIFT_OUTCOME' ${batsHarness.internals.outcomeContractFile} | grep -vc 'landing=' || true)
        [ "$missing" -eq 0 ] || {
          echo "expected every SPINDRIFT_OUTCOME line to carry landing=, $missing did not" >&2
          exit 1
        }
        touch $out
      '';

  # The #1582 dogfood run printed SPINDRIFT_OUTCOME backtick-wrapped, and the
  # extractor's anchored grep missed it -- the contract only ever *showed* the
  # line inside a fenced example, never told the driver its own output must be
  # raw text (issue #1612). Pin the explicit instruction adjacent to "print
  # exactly one line as your final output" so a future edit can't drop it or
  # relocate it away from that instruction. -z/-P with the (?s) modifier lets
  # "." span the line break the wording wraps across, so the check still
  # matches regardless of exactly where the prose wraps. The {0,60} window is
  # sized for "and stop —"/"—" separators plus one wrapped line (the widest
  # gap the current wording has) -- widen it if a future rewrap pushes the
  # phrase further from the instruction.
  mkharness-prompt-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.internals.outcomeContractFile}
        touch $out
      '';

  # fix-prompt.md's default template carries only its fix-specific preamble
  # (issue #455): the rendered prompt must still gain the COMMS,
  # CHECK/COMMIT, and outcome-contract blocks, each exactly once, mirroring
  # the issue prompt's own guard above.
  mkharness-prompt-fix-comms-injected = pkgs.runCommand "mkharness-prompt-fix-comms-injected" { } ''
    count=$(grep -c '# COMMS' ${batsHarness.internals.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's COMMS block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-check-injected = pkgs.runCommand "mkharness-prompt-fix-check-injected" { } ''
    count=$(grep -c '# CHECK' ${batsHarness.internals.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's CHECK/COMMIT block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-outcome-injected =
    pkgs.runCommand "mkharness-prompt-fix-outcome-injected" { }
      ''
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.internals.promptDir}/fix-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the fix prompt's outcome contract injected exactly once, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # A Consumer fixPrompt that carries only a fix-specific preamble — no
  # shared-block markers at all — must still gain all three, in COMMS,
  # CHECK, outcome-contract order, the same #420 runtime-override parity the
  # issue prompt already has (proven at the Nix layer here;
  # agent/entrypoint.sh's own runtime injection is covered by
  # tests/entrypoint-outcome-contract.bats).
  mkharness-prompt-fix-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-fix-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.internals.promptDir}/fix-prompt.md
        [ "$(grep -c '# COMMS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# CHECK' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# LAND THE CHANGE' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        comms_line=$(grep -n '# COMMS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        check_line=$(grep -n '# CHECK' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        outcome_line=$(grep -n '# LAND THE CHANGE' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$comms_line" ]
        [ "$comms_line" -lt "$check_line" ]
        [ "$check_line" -lt "$outcome_line" ]
        touch $out
      '';

  # The injected COMMS and CHECK/COMMIT blocks must be byte-identical to the
  # canonical sections mkHarness slices them from — same source, same bytes,
  # so fix-prompt.md and issue-prompt.md cannot drift apart (issue #455;
  # mirrors mkharness-prompt-outcome-no-drift above). CODE COMMENTS dropped
  # out of this pair (issue #3221): it's now the ${CODE_COMMENTS_STEP}
  # anchor, not a sliced/injected block, so there's nothing left to diff.
  mkharness-prompt-fix-comms-no-drift = pkgs.runCommand "mkharness-prompt-fix-comms-no-drift" { } ''
    awk '/^# COMMS$/{f=1} /^# CHECK$/{exit} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-comms.txt
    diff ${batsHarness.internals.commsContractFile} injected-comms.txt
    touch $out
  '';

  mkharness-prompt-fix-check-no-drift = pkgs.runCommand "mkharness-prompt-fix-check-no-drift" { } ''
    awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-check.txt
    diff ${batsHarness.internals.checkContractFile} injected-check.txt
    touch $out
  '';

  # The CHECK-phase never-background / emit-outcome guardrail (issue #592)
  # covers the CHECK phase's own blocking gates (`nix build .#checks-inbox`,
  # test suites). Written once in issue-prompt.md's CHECK section and
  # inherited by fix-prompt.md through the CHECK block injection above. Both
  # greps are scoped to issue-prompt's CHECK section itself (not the whole
  # file) -- OUTCOME carries its own "Do NOT run" phrasing further down, so
  # an unscoped grep would keep passing even if the #592 CHECK paragraph
  # were deleted. Fix-prompt side is covered by
  # mkharness-prompt-fix-check-no-drift's byte-for-byte diff, not re-pinned
  # here (issue #1009).
  mkharness-prompt-check-never-background =
    pkgs.runCommand "mkharness-prompt-check-never-background" { }
      ''
        grep -q 'never background it' ${checkSectionSlices}/issue-check.txt
        grep -q 'SPINDRIFT_OUTCOME' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Issue #3220 moved the elaborated foreground-gate guidance into the
  # check-hygiene skill but kept the terminal-outcome mandate inline: the
  # dispatcher parses the SPINDRIFT_OUTCOME line, so that contract must hold
  # for every run, including one where the agent never invokes the on-demand
  # skill. Pinned separately from the never-background greps above because
  # those pass on the surviving anchor prose alone.
  mkharness-prompt-check-terminal-outcome-inline =
    pkgs.runCommand "mkharness-prompt-check-terminal-outcome-inline" { }
      ''
        grep -qi 'do not stop this run' ${checkSectionSlices}/issue-check.txt
        grep -q 'status=blocked' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The defensive fallback for an agent that backgrounds a check gate anyway
  # (issue #713): a build killed outright (OOM, SIGKILL) never writes the
  # exit marker a background+poll loop waits on, so the wait must be bounded
  # and a vanished marker treated as failure, not still-pending. Issue #3220
  # moved this elaborated guidance out of the CHECK section and into the
  # harness-owned check-hygiene skill (baked unconditionally, so the prompt
  # anchor below always resolves), which is what this now pins.
  check-hygiene-skill-vanished-marker-is-failure =
    pkgs.runCommand "check-hygiene-skill-vanished-marker-is-failure" { }
      ''
        grep -qi 'vanished' ${checkHygieneSkill}
        grep -qi 'exit marker' ${checkHygieneSkill}
        touch $out
      '';

  # Issue #3220: the reduction only holds if the CHECK section still points
  # at the skill the relocated guidance moved into -- an anchorless CHECK
  # would leave every pin above green while the agent never reads the body.
  # Two halves, since the anchor is a bakedness-gated fragment: the CHECK
  # section must reference the fragment's variable, and the fragment must
  # carry the anchor prose.
  mkharness-prompt-check-hygiene-skill-anchor =
    pkgs.runCommand "mkharness-prompt-check-hygiene-skill-anchor" { }
      ''
        grep -qF 'CHECK_HYGIENE_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF '/check-hygiene' ${checkHygieneAnchor}
        touch $out
      '';

  # Issue #3223: same anchor-presence pin, for the dogfood-only /nix-checks
  # skill sitting beside /check-hygiene in the same CHECK-section anchor run.
  # The anchor is the CHECK section's only remaining trace of the Nix lore,
  # so an anchorless CHECK would strand the relocated guidance entirely.
  mkharness-prompt-nix-checks-skill-anchor =
    pkgs.runCommand "mkharness-prompt-nix-checks-skill-anchor" { }
      ''
        grep -qF 'NIX_CHECKS_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF '/nix-checks' ${nixChecksAnchor}
        touch $out
      '';

  # Issue #3221: same reduction, same two-half shape, for the CODE COMMENTS
  # heading that collapsed into an anchor pointing at /code-comments. The
  # variable renders on the IMPLEMENT phase's own trailing line
  # (${CODE_COMMENTS_STEP}# CHECK, see checkSectionSlices' own comment
  # above), which the CHECK-section awk slice already captures as its first
  # line -- reused here rather than standing up a second slice derivation
  # for one line.
  mkharness-prompt-code-comments-skill-anchor =
    pkgs.runCommand "mkharness-prompt-code-comments-skill-anchor" { }
      ''
        grep -qF 'CODE_COMMENTS_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF '/code-comments' ${codeCommentsAnchor}
        touch $out
      '';

  # A silently regrown inline copy would defeat the move (issue #3221) while
  # leaving the anchor pin above green -- pin the *absence* of the restated
  # policy prose in the raw template too, not just the anchor's presence.
  mkharness-prompt-code-comments-no-inline-restatement =
    pkgs.runCommand "mkharness-prompt-code-comments-no-inline-restatement" { }
      ''
        ! grep -qF '# CODE COMMENTS' ${batsHarness.internals.promptDir}/issue-prompt.md
        ! grep -qi 'non-obvious why' ${batsHarness.internals.promptDir}/issue-prompt.md
        touch $out
      '';

  # Issue #3226: the review prompt's four hunt dimensions
  # (SPEC/CORRECTNESS/SECURITY/STANDARDS & SMELLS) and the reconcile-into-
  # Blocking/Non-blocking obligation must render on EVERY run, baked or
  # unbaked -- the #3226 coordination comment treats them as always-inline
  # contract, not coaching gated behind the CODE_REVIEW_BAKED/UNBAKED pair,
  # since the baked arm defers to a pinned upstream skill spindrift cannot
  # edit and a depth obligation gated on that pair would vanish on exactly
  # the runs that need it. Pinned on the raw template (not a rendered
  # harness): "inline regardless of gate state" is a structural property of
  # review-prompt.md's own source text, not something bakedness can flip,
  # and lib/mkHarness.nix leaves the CODE_REVIEW_BAKED/UNBAKED placeholders
  # unresolved (that gate is bash-only, decided at runtime by
  # agent/entrypoint.sh from the actual skill dir), so a rendered harness
  # would only prove the placeholders survive, not the dimensions.
  review-prompt-hunt-dimensions-inline = pkgs.runCommand "review-prompt-hunt-dimensions-inline" { } ''
    p=${../../templates/default/prompts/review-prompt.md}
    grep -qi 'hunt every dimension' "$p"
    grep -qF '**SPEC**' "$p"
    grep -qF '**CORRECTNESS**' "$p"
    grep -qF '**SECURITY**' "$p"
    grep -qF '**STANDARDS & SMELLS**' "$p"
    grep -qi 'reconcile every finding' "$p"
    touch $out
  '';

  # Companion to review-prompt-hunt-dimensions-inline above: the gated
  # CODE_REVIEW_BAKED/UNBAKED fragment pair must NOT restate the dimensions
  # -- a silently regrown copy in either arm would defeat the move to the
  # always-rendered tier while leaving the presence pin above green.
  review-prompt-hunt-dimensions-not-in-gated-arms =
    pkgs.runCommand "review-prompt-hunt-dimensions-not-in-gated-arms" { }
      ''
        for f in ${../../templates/default/prompts/fragments/code-review-baked.md} \
                 ${../../templates/default/prompts/fragments/code-review-unbaked.md}; do
          ! grep -qF '**SPEC**' "$f" || {
            echo "$f restates the SPEC hunt dimension -- it belongs unconditionally in review-prompt.md, not a gated arm" >&2
            exit 1
          }
          ! grep -qF '**CORRECTNESS**' "$f" || {
            echo "$f restates the CORRECTNESS hunt dimension -- it belongs unconditionally in review-prompt.md, not a gated arm" >&2
            exit 1
          }
          ! grep -qF '**SECURITY**' "$f" || {
            echo "$f restates the SECURITY hunt dimension -- it belongs unconditionally in review-prompt.md, not a gated arm" >&2
            exit 1
          }
          ! grep -qF '**STANDARDS & SMELLS**' "$f" || {
            echo "$f restates the STANDARDS & SMELLS hunt dimension -- it belongs unconditionally in review-prompt.md, not a gated arm" >&2
            exit 1
          }
        done
        touch $out
      '';

  # Issue #3228: the CORRECTNESS/SECURITY-before-STANDARDS & SMELLS ordering
  # sentence and the four trace obligations (rename/mass-replacement,
  # changed signature, concurrency-adjacent change, new error path) are the
  # same always-inline contract as the hunt dimensions above -- gating either
  # behind CODE_REVIEW_BAKED/UNBAKED would make it vanish on exactly the
  # baked runs that defer to the pinned upstream skill. Same raw-template
  # rationale as review-prompt-hunt-dimensions-inline above.
  review-prompt-phased-hunt-and-trace-obligations-inline =
    pkgs.runCommand "review-prompt-phased-hunt-and-trace-obligations-inline" { }
      ''
        ${normalizedGrep}
        p=${../../templates/default/prompts/review-prompt.md}
        for clause in ${pkgs.lib.escapeShellArgs phasedHuntAndTraceObligationClauses}; do
          normalized_grep "$p" "$clause" || {
            echo "review-prompt.md no longer states: $clause" >&2
            exit 1
          }
        done
        touch $out
      '';

  # Companion to review-prompt-phased-hunt-and-trace-obligations-inline
  # above: the gated CODE_REVIEW_BAKED/UNBAKED fragment pair must NOT
  # restate the ordering rule or any trace obligation -- a silently regrown
  # copy in either arm would defeat the move to the always-rendered tier
  # while leaving the presence pin above green.
  review-prompt-phased-hunt-and-trace-obligations-not-in-gated-arms =
    pkgs.runCommand "review-prompt-phased-hunt-and-trace-obligations-not-in-gated-arms" { }
      ''
        ${normalizedGrep}
        for f in ${../../templates/default/prompts/fragments/code-review-baked.md} \
                 ${../../templates/default/prompts/fragments/code-review-unbaked.md}; do
          for clause in ${pkgs.lib.escapeShellArgs phasedHuntAndTraceObligationClauses}; do
            ! normalized_grep "$f" "$clause" || {
              echo "$f restates a phased-hunt or trace obligation -- it belongs unconditionally in review-prompt.md, not a gated arm: $clause" >&2
              exit 1
            }
          done
        done
        touch $out
      '';

  # Issue #3228: the Blocking one-line-failure-scenario requirement (and its
  # Non-blocking scenario-less corollary) and the APPROVE probed section are
  # the same always-inline contract as the hunt dimensions and trace
  # obligations above -- gating either behind CODE_REVIEW_BAKED/UNBAKED would
  # make it vanish on exactly the baked runs that defer to the pinned
  # upstream skill. Same raw-template rationale as
  # review-prompt-hunt-dimensions-inline above.
  review-prompt-failure-scenario-and-probed-section-inline =
    pkgs.runCommand "review-prompt-failure-scenario-and-probed-section-inline" { }
      ''
        ${normalizedGrep}
        p=${../../templates/default/prompts/review-prompt.md}
        for clause in ${pkgs.lib.escapeShellArgs failureScenarioAndProbedSectionClauses}; do
          normalized_grep "$p" "$clause" || {
            echo "review-prompt.md no longer states: $clause" >&2
            exit 1
          }
        done
        touch $out
      '';

  # Companion to review-prompt-failure-scenario-and-probed-section-inline
  # above: the gated CODE_REVIEW_BAKED/UNBAKED fragment pair must NOT restate
  # the failure-scenario rule or the Probed section -- a silently regrown
  # copy in either arm would defeat the move to the always-rendered tier
  # while leaving the presence pin above green.
  review-prompt-failure-scenario-and-probed-section-not-in-gated-arms =
    pkgs.runCommand "review-prompt-failure-scenario-and-probed-section-not-in-gated-arms" { }
      ''
        ${normalizedGrep}
        for f in ${../../templates/default/prompts/fragments/code-review-baked.md} \
                 ${../../templates/default/prompts/fragments/code-review-unbaked.md}; do
          for clause in ${pkgs.lib.escapeShellArgs failureScenarioAndProbedSectionClauses}; do
            ! normalized_grep "$f" "$clause" || {
              echo "$f restates the Blocking failure-scenario rule or the APPROVE probed section -- it belongs unconditionally in review-prompt.md, not a gated arm: $clause" >&2
              exit 1
            }
          done
        done
        touch $out
      '';

  # Nix flakes only evaluate git-tracked files (issue #714): an agent that
  # creates a new file and runs `nix build` before staging it hits a
  # spurious "not tracked by Git" failure and burns a checks cycle. Issue
  # #3223 moved this guidance out of the CHECK section and into the
  # dogfood-only nix-checks skill, so it is pinned there now.
  # Fix-prompt side is covered by mkharness-prompt-fix-check-no-drift's
  # byte-for-byte diff, not re-pinned here (issue #1009).
  mkharness-prompt-check-git-add-before-nix-build =
    pkgs.runCommand "mkharness-prompt-check-git-add-before-nix-build" { }
      ''
        grep -qi 'git add' ${nixChecksSkill}
        grep -qi 'tracked by' ${nixChecksSkill}
        touch $out
      '';

  # Issue #1990: the agent must not regrow the redundant manual
  # output-routing advice the bash-output interceptor (#1988) now handles,
  # and must keep the explicit no-cat-a-whole-log rule. Issue #3220 moved
  # that rule into the check-hygiene skill body, so it is pinned there; its
  # scoped-check-target sibling below moved into the nix-checks skill instead
  # (issue #3223).
  check-hygiene-skill-no-cat-log = pkgs.runCommand "check-hygiene-skill-no-cat-log" { } ''
    grep -qi 'never `cat`' ${checkHygieneSkill}
    touch $out
  '';

  # The scoped-check-target steering moved into the dogfood-only nix-checks
  # skill (issue #3223), so it is pinned there now, not on the CHECK slice.
  mkharness-prompt-check-scoped-target = pkgs.runCommand "mkharness-prompt-check-scoped-target" { } ''
    grep -qi 'scoped check target' ${nixChecksSkill}
    touch $out
  '';

  # Issue #3215: the redirect-to-file discipline above (no-cat-log) covers
  # build/test logs but not diffs -- a bare `git diff` streamed to the
  # conversation hits the same tool-result truncation cap a streamed build
  # log does, so the CHECK section must extend the same file-then-grep
  # pattern to diffs explicitly. Same CHECK-section scoping as the
  # never-background/vanished-marker/git-add/no-cat-log checks above. The
  # second pin anchors on the whole "`--stat` first for shape" phrase, not a
  # bare `--stat`: that token is unique to this paragraph today, but any
  # future unrelated mention elsewhere in the slice would hollow the pin out
  # silently.
  mkharness-prompt-check-diff-redirect-discipline =
    pkgs.runCommand "mkharness-prompt-check-diff-redirect-discipline" { }
      ''
        grep -qi 'never stream a bare `git diff`' ${checkSectionSlices}/issue-check.txt
        grep -qi -- '`--stat` first for shape' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Issue #2377: the scoped-check-target steering above must be a firm rule,
  # not a soft preference -- an explicit prohibition on running the full
  # `nix flake check` in-box, overriding any issue acceptance criteria that
  # loosely ask for it, with the one legitimate exception (the diff touches
  # what's baked into the image) spelled out by file reference. Issue #3223
  # moved this guidance into the dogfood-only nix-checks skill along with its
  # scoped-check-target sibling above, so it is pinned there now.
  mkharness-prompt-check-full-flake-check-firm-rule =
    pkgs.runCommand "mkharness-prompt-check-full-flake-check-firm-rule" { }
      ''
        grep -Pzqi \
          '(?s)(do not|must not) run.{0,80}full.{0,80}nix flake check.{0,300}(nix/checks/image\.nix|lib/image\.nix)' \
          ${nixChecksSkill}
        touch $out
      '';

  # Issue #3223: the CHECK section is ecosystem-neutral now that the Nix
  # lore lives in the dogfood-only /nix-checks skill instead -- a regrown
  # inline mention of any of these terms would defeat the move while every
  # anchor/skill-body pin above stayed green.
  mkharness-prompt-check-no-nix-wording =
    pkgs.runCommand "mkharness-prompt-check-no-nix-wording" { }
      ''
        if grep -qiE 'flake|devshell|nix build|nix develop|checks-inbox|git add' \
          ${checkSectionSlices}/issue-check.txt; then
          echo "CHECK section still carries Nix wording -- it moved into skills/nix-checks/SKILL.md (issue #3223)" >&2
          exit 1
        fi
        touch $out
      '';

  mkharness-prompt-fix-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-fix-outcome-no-drift" { }
      ''
        awk '/# LAND THE CHANGE/{f=1} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-contract.txt
        diff ${batsHarness.internals.outcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The research dispatch kind's own outcome contract (issue #640): a
  # Consumer researchPrompt that drops "# POST THE VERDICT" must still ship
  # an agent that posts the verdict comment and emits the outcome line --
  # the harness appends the canonical contract exactly once.
  mkharness-prompt-research-outcome-injected =
    pkgs.runCommand "mkharness-prompt-research-outcome-injected" { }
      ''
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.internals.promptDir}/research-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the research prompt's outcome contract injected exactly once, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default research prompt already contains the contract, so injection
  # must be a no-op: no duplication (mirrors mkharness-prompt-outcome-not-duplicated).
  mkharness-prompt-research-outcome-not-duplicated =
    pkgs.runCommand "mkharness-prompt-research-outcome-not-duplicated" { }
      ''
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.internals.promptDir}/research-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the default research prompt's outcome contract to stay single, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # Issue #2525: lib/research-verdicts.nix's `render` always rewrites the
  # VERDICT section (bullets, status alternation, backtick enumeration) from
  # the configured verdict set -- `defaultVerdicts` when the knob is empty,
  # the parsed custom list otherwise -- for both the default and a custom
  # set. There is no more byte-identical-to-template no-op case, since the
  # checked-in template no longer carries hand-typed bullets to be a no-op
  # copy of. A byte diff (not five separate grep presence checks) so any
  # other prose in the VERDICT..POST THE VERDICT span -- deleted, duplicated,
  # or reordered by a rendering regression -- fails loudly instead of being
  # invisible to a presence-only assertion (the class of bug that silently
  # dropped research-self-contained-prompt.md's "Judge relevance..." sentence
  # before this fix).
  mkharness-prompt-research-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-prompt.md > rendered.txt
        diff -u ${researchVerdictDefaultFixture} rendered.txt \
          || { echo "default research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # Companion to mkharness-prompt-research-verdicts-default-rendered above,
  # for the self-contained sub-mode prompt (ADR 0022, issue #2202): no check
  # anywhere previously read the *rendered* self-contained prompt's VERDICT
  # section, so the render-time deletion of its "Judge relevance..." sentence
  # (the one line distinguishing the sub-mode from the normal research
  # prompt) went uncaught. Pins that the sentence survives rendering
  # untouched, ahead of the registry-generated bullets.
  mkharness-prompt-research-self-contained-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-self-contained-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-self-contained-prompt.md > rendered.txt
        diff -u ${researchVerdictSelfContainedFixture} rendered.txt \
          || { echo "self-contained research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # The two checks above only inspect the VERDICT..POST THE VERDICT span, so
  # a rendering regression that fails to resolve enumMarker
  # (`` `<RESEARCH_VERDICT_ENUM>` ``, which lives on the "Structure the
  # verdict" line *after* the `# POST THE VERDICT` heading) would leave the
  # literal placeholder in the baked prompt invisible to them. Scan the whole
  # baked file for both markers -- neither may survive rendering -- and pin
  # that the default set's resolved backtick enumeration is actually present.
  mkharness-prompt-research-verdicts-markers-resolved =
    pkgs.runCommand "mkharness-prompt-research-verdicts-markers-resolved" { }
      ''
        p=${batsHarness.internals.promptDir}/research-prompt.md
        ! grep -qF -- '<RESEARCH_VERDICT_ENUM>' "$p" || {
          echo "enumMarker survived rendering in $p" >&2
          exit 1
        }
        ! grep -qF -- '<!-- RESEARCH_VERDICT_BULLETS -->' "$p" || {
          echo "bulletsMarker survived rendering in $p" >&2
          exit 1
        }
        grep -qF -- '`recommend` / `reject` / `unclear`' "$p" || {
          echo "resolved default backtick enumeration missing from $p" >&2
          exit 1
        }
        touch $out
      '';

  # Companion to mkharness-prompt-research-verdicts-markers-resolved above,
  # for the self-contained sub-mode prompt.
  mkharness-prompt-research-self-contained-verdicts-markers-resolved =
    pkgs.runCommand "mkharness-prompt-research-self-contained-verdicts-markers-resolved" { }
      ''
        p=${batsHarness.internals.promptDir}/research-self-contained-prompt.md
        ! grep -qF -- '<RESEARCH_VERDICT_ENUM>' "$p" || {
          echo "enumMarker survived rendering in $p" >&2
          exit 1
        }
        ! grep -qF -- '<!-- RESEARCH_VERDICT_BULLETS -->' "$p" || {
          echo "bulletsMarker survived rendering in $p" >&2
          exit 1
        }
        grep -qF -- '`recommend` / `reject` / `unclear`' "$p" || {
          echo "resolved default backtick enumeration missing from $p" >&2
          exit 1
        }
        touch $out
      '';

  # A custom RESEARCH_VERDICTS set (issue #2201) flows into the baked research
  # prompt's verdict contract: the VERDICT bullets, the enumeration, and the
  # status alternation all render from the configured set, and no default
  # verdict token survives in the contract. Proves the set reaches the prompt,
  # not only the launcher.
  mkharness-prompt-research-verdicts-custom-rendered =
    pkgs.runCommand "mkharness-prompt-research-verdicts-custom-rendered" { }
      ''
        p=${researchVerdictsHarness.internals.promptDir}/research-prompt.md
        grep -qF -- '- `approve` — relevant and worth doing; promote it.' "$p" \
          || { echo "custom verdict bullet missing from rendered research prompt" >&2; exit 1; }
        grep -qF -- '- `decline` — not worth doing.' "$p" \
          || { echo "custom verdict bullet missing from rendered research prompt" >&2; exit 1; }
        grep -qF -- 'status=<approve|decline>' "$p" \
          || { echo "custom status alternation missing from rendered research prompt" >&2; exit 1; }
        grep -qF -- '`approve` / `decline`' "$p" \
          || { echo "custom enumeration missing from rendered research prompt" >&2; exit 1; }
        if grep -qF -- 'status=<recommend|reject|unclear>' "$p"; then
          echo "default status alternation must not survive a custom verdict set" >&2
          exit 1
        fi
        # The outcome contract is still injected exactly once.
        [ "$(grep -c '# POST THE VERDICT' "$p")" -eq 1 ] \
          || { echo "outcome contract not injected exactly once under a custom set" >&2; exit 1; }
        touch $out
      '';

  # The block injected into a research prompt lacking the contract must be
  # byte-identical to the default research prompt's own contract section --
  # both sliced from the same marker in the same source file (issue #640,
  # mirrors mkharness-prompt-outcome-no-drift).
  mkharness-prompt-research-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-research-outcome-no-drift" { }
      ''
        awk '/# POST THE VERDICT/{f=1} f' ${researchPromptHarness.internals.promptDir}/research-prompt.md > injected-contract.txt
        diff ${batsHarness.internals.researchOutcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The self-contained research prompt's `# POST THE VERDICT` tail is
  # hand-maintained: injectResearchOutcomeContract (lib/mkHarness.nix:593-594)
  # no-ops on both source templates because each already owns the
  # `# POST THE VERDICT` marker, so nothing structurally pins the
  # self-contained copy to the canonical research-prompt.md. This check slices
  # the tail (marker -> EOF) from both source templates and asserts they stay
  # byte-identical, catching silent drift (issue #2230, found during #2202).
  mkharness-prompt-research-self-contained-outcome-parity =
    pkgs.runCommand "mkharness-prompt-research-self-contained-outcome-parity" { }
      ''
        awk '/^# POST THE VERDICT$/{f=1} f' \
          ${../../templates/default/prompts/research-prompt.md} > canonical-tail.txt
        awk '/^# POST THE VERDICT$/{f=1} f' \
          ${../../templates/default/prompts/research-self-contained-prompt.md} > self-contained-tail.txt
        diff canonical-tail.txt self-contained-tail.txt || {
          echo "research-self-contained-prompt.md and research-prompt.md '# POST THE VERDICT' tails have drifted; keep them byte-identical" >&2
          exit 1
        }
        touch $out
      '';

  # Same gap as mkharness-prompt-outcome-contract-has-landing-token, for the
  # research kind's own contract (issue #654), including the same
  # SPINDRIFT_OUTCOME anchoring fix (issue #886) and the partial-revert
  # strengthening (issue #887).
  mkharness-prompt-research-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-has-landing-token" { }
      ''
        # Floor guard, same reasoning as the issue-side check above.
        grep -qE 'SPINDRIFT_OUTCOME.*landing=' ${batsHarness.internals.researchOutcomeContractFile}
        missing=$(grep 'SPINDRIFT_OUTCOME' ${batsHarness.internals.researchOutcomeContractFile} | grep -vc 'landing=' || true)
        [ "$missing" -eq 0 ] || {
          echo "expected every SPINDRIFT_OUTCOME line to carry landing=, $missing did not" >&2
          exit 1
        }
        touch $out
      '';

  # Same raw-text pin as mkharness-prompt-outcome-contract-raw-text, for the
  # research kind's own contract (issue #1612).
  mkharness-prompt-research-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.internals.researchOutcomeContractFile}
        touch $out
      '';

  # A Consumer researchPrompt carrying only a research-specific preamble --
  # no "# POST THE VERDICT" marker at all -- must still gain the contract,
  # and survive the round trip byte-identical to what a runtime
  # SPINDRIFT_PROMPT_DIR override receives (issue #640, mirrors
  # mkharness-prompt-fix-consumer-override-injected; agent/entrypoint.sh's
  # own runtime injection is covered by tests/entrypoint-research-kind.bats).
  mkharness-prompt-research-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-research-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.internals.promptDir}/research-prompt.md
        [ "$(grep -c '# POST THE VERDICT' ${researchPromptHarness.internals.promptDir}/research-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.internals.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        contract_line=$(grep -n '# POST THE VERDICT' ${researchPromptHarness.internals.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$contract_line" ]
        touch $out
      '';

  # Grep pin (issue #1653): the Driver no longer polls CI itself -- the
  # launcher already gates on CI green before flipping the PR ready and
  # merging (issue #1651) -- so the WATCH CI GraphQL query must not appear
  # in any prompt *source* file on disk. fix-prompt.md's CONTEXT section
  # legitimately references the unrelated `statusCheckRollup` JSON field
  # name via `gh pr view --json`, so the query body itself -- distinctive to
  # the old shared WATCH CI block -- is the pin, not the field name alone. A
  # regression here means someone pasted the block back in.
  prompt-source-statusCheckRollup-query-absent =
    pkgs.runCommand "prompt-source-statusCheckRollup-query-absent" { }
      ''
        # `|| true`: under stdenv's pipefail, a no-match exit (grep's status
        # 1) would otherwise abort the script right here, before the
        # assertion below ever runs. The grep in the error branch below
        # needs no such guard -- it only runs once count != 0, i.e. once a
        # match is already known to exist.
        count=$(grep -rlF 'query($owner:String!' ${../../templates/default/prompts} | wc -l || true)
        [ "$count" -eq 0 ] || {
          echo "expected the WATCH CI GraphQL query in no prompt source file, got $count" >&2
          grep -rlF 'query($owner:String!' ${../../templates/default/prompts} >&2
          exit 1
        }
        touch $out
      '';

  # ORCHESTRATOR master-switch fork-well-formedness (issue #2047, ADR 0035
  # amendment): ORCHESTRATOR_ENABLED is a master feature-flag switch that
  # forks the rendered prompt/--agents, not a scatter of ad-hoc checks --
  # so exactly one line in agent/entrypoint.sh may test the raw
  # ORCHESTRATOR_ENABLED env var (the canonical `local ORCHESTRATOR=`
  # computation itself); every fork downstream (the filer-relay compound
  # condition, the driver-invoker binary swap) must read that one computed
  # $ORCHESTRATOR gate instead of testing the env var independently. Every
  # conditional that branches on $ORCHESTRATOR must also declare both an
  # on-row and an off-row -- an explicit `else`, never a bare `if` whose off
  # case is left merely implicit -- so a segment added later with only one
  # side fails here instead of silently rendering the same fork for every
  # input. Same grep-based, eval-only shape as
  # prompt-source-statusCheckRollup-query-absent above.
  orchestrator-fork-well-formed = pkgs.runCommand "orchestrator-fork-well-formed" { } ''
    entrypoint=${../../agent/entrypoint.sh}

    # Excludes comment-only lines (prose is free to name the env var) so this
    # doesn't pin one exact bash parameter-expansion form -- any variant
    # (default-value, alternate-value, ...) counts as the one code reference
    # this guards.
    gate_computations=$(awk '/ORCHESTRATOR_ENABLED/ && $0 !~ /^[[:space:]]*#/' "$entrypoint" | wc -l)
    [ "$gate_computations" -eq 1 ] || {
      echo "expected exactly one ORCHESTRATOR_ENABLED test (the canonical gate computation) in agent/entrypoint.sh, got $gate_computations" >&2
      grep -n 'ORCHESTRATOR_ENABLED' "$entrypoint" >&2
      exit 1
    }

    # Issue #2356 deleted the one bash if/else $ORCHESTRATOR conditional
    # this loop used to always find (_validate_prompt_contract's
    # orchestratorEnabled row) along with the rest of the reject/warn
    # matrix -- the Go verb now owns that fork's gating end to end, covered
    # by its own unit tests, not this grep. Every remaining $ORCHESTRATOR
    # read left in entrypoint.sh is the bare `[ -n "$ORCHESTRATOR" ] && ...`
    # form, which this pattern doesn't match, so zero sites is now the
    # expected steady state -- this loop still catches a *future* if/else
    # $ORCHESTRATOR conditional missing its off-row, it just no longer
    # requires one to exist.
    sites=$(grep -n 'if .*\$ORCHESTRATOR\b' "$entrypoint" | cut -d: -f1 || true)
    for start in $sites; do
      branch=$(awk -v start="$start" '
        NR <= start { next }
        /^[[:space:]]*else[[:space:]]*$/ { print "else"; exit }
        /^[[:space:]]*fi[[:space:]]*$/ { exit }
      ' "$entrypoint")
      [ "$branch" = "else" ] || {
        echo "agent/entrypoint.sh:$start -- \$ORCHESTRATOR conditional has no else (off-row) before its closing fi" >&2
        exit 1
      }
    done
    touch $out
  '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # search open issues beyond the `agent-review-finding` label -- a
  # regression back to the old `--label agent-review-finding --state all`
  # query would silently stop catching human-filed/ready-for-agent/
  # /to-tickets duplicates. Neither pin above catches a *narrower* regression:
  # re-adding a `--label` flag to the `--state open` line itself (e.g.
  # `--label agent-review-finding --state open`) still contains the literal
  # substring `--state open` and never matches the old `--state all` string,
  # so both pins stay green while the dedup silently narrows back to only
  # `agent-review-finding`-labeled issues (issue #921). Extract the line
  # carrying `--state open` and count how many of its occurrences also carry
  # `--label` -- must be zero. All assertions below use the explicit
  # `[ "$n" -eq 0 ] || exit 1` shape, not a bare `! pipeline`, since `set -e`
  # exempts negated commands (issue #887).
  filer-prompt-dedup-searches-all-open-issues =
    pkgs.runCommand "filer-prompt-dedup-searches-all-open-issues" { }
      ''
        grep -q -- '--state open' ${../../templates/default/prompts/filer-prompt.md}
        old=$(grep -c -- '--label agent-review-finding --state all' \
          ${../../templates/default/prompts/filer-prompt.md} || true)
        [ "$old" -eq 0 ] || {
          echo "expected the old --label agent-review-finding --state all query gone, found $old occurrence(s)" >&2
          exit 1
        }
        bad=$(grep -- '--state open' ${../../templates/default/prompts/filer-prompt.md} \
          | grep -c -- '--label' || true)
        [ "$bad" -eq 0 ] || {
          echo "expected the --state open dedup search to carry no --label flag, $bad line(s) did" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #781 acceptance criteria): the CHECK-section awk slice
  # used by the never-background/git-add/anchor/scoped-target checks above
  # must be defined once, not copy-pasted -- a marker rename applied to one copy
  # and forgotten in the others would leave those checks silently reading
  # stale content. Extended (issue #1154) to also pin the fix-prompt half
  # of the same slice pattern (`# LAND THE CHANGE` exit instead of
  # `# REVIEW`), used solely by mkharness-prompt-fix-check-no-drift above --
  # the original check only ever guarded the issue-prompt half.
  prompts-nix-check-section-awk-defined-once =
    pkgs.runCommand "prompts-nix-check-section-awk-defined-once" { }
      ''
        # Split so this line's own source text never contains the
        # contiguous target pattern -- else this check would count itself.
        # issue-prompt half is end-of-line-anchored only, not
        # start-of-line (issue #3221): the raw, unrendered template it
        # slices now has "# CHECK" trailing the IMPLEMENT phase's own
        # variable run, not alone on its own line.
        issue_half1='/# CHECK$/{f=1}'
        issue_half2=' /# REVIEW$/{exit} f'
        count=$(grep -cF "$issue_half1$issue_half2" ${./prompts.nix} || true)
        [ "$count" -le 1 ] || {
          echo "expected the CHECK-section awk slice defined at most once in prompts.nix, got $count" >&2
          exit 1
        }
        # fix-prompt half slices the rendered fix-prompt.md, where the
        # injected CHECK/COMMIT block still starts "# CHECK" on its own
        # line, so it keeps the start-of-line anchor.
        fix_half1='/^# CHECK$/{f=1}'
        fix_half2=' /^# LAND THE CHANGE$/{exit} f'
        fix_count=$(grep -cF "$fix_half1$fix_half2" ${./prompts.nix} || true)
        [ "$fix_count" -le 1 ] || {
          echo "expected the fix-prompt CHECK-section awk slice defined at most once in prompts.nix, got $fix_count" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # also treat closed `agent-research-reject` issues -- a research pass's
  # deliberate false-positive/not-worth-doing/duplicate verdict -- as
  # suppressing matches, the same triage-decision class as a closed
  # `agent-review-finding`. Anchored to the full `--label ... --state closed`
  # search command, not the bare label token -- a bare-token match would
  # still pass if the closed-dedup search line lost the label while an
  # unrelated prose mention of `agent-research-reject` survived elsewhere in
  # the file (issue #922), the same class of regression #921 guards against
  # for the sibling `--state open` check above.
  filer-prompt-dedup-names-research-reject =
    pkgs.runCommand "filer-prompt-dedup-names-research-reject" { }
      ''
        grep -q -- '--label agent-research-reject --state closed' ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';

  # Grep pin (issue #3226 slice 3 acceptance criteria): the filer's
  # issue-authoring obligations -- both provenance rules and the
  # never-the-dispatch-label rule -- are contract, not coaching, and stay
  # byte-intact through any editorial pass. Each grep is anchored to the
  # literal, actionable sentence rather than a loose keyword, so a future
  # edit that keeps the word "provenance" around but drops the actual rule
  # still fails. Rule 2 takes two greps rather than one: its sentence wraps
  # across two source lines, and grep is line-oriented.
  #   1. work-path issues carry the exact backlink shape
  #      `Found by review during #<issue> (PR <url>)`.
  #   2. research-path issues get NO such line of their own -- the launcher
  #      appends its own backlink after the filer exits.
  #   3. filed issues never carry the dispatch label itself.
  filer-prompt-issue-authoring-obligations =
    pkgs.runCommand "filer-prompt-issue-authoring-obligations" { }
      ''
        grep -qF -- 'Found by review during #<issue> (PR <url>)' \
          ${../../templates/default/prompts/filer-prompt.md} || {
          echo "expected the work-path provenance line's exact text 'Found by review during #<issue> (PR <url>)' in filer-prompt.md" >&2
          exit 1
        }
        grep -qF -- 'For a research delegation, add no provenance' \
          ${../../templates/default/prompts/filer-prompt.md} || {
          echo "expected the research-path 'add no provenance line of your own' rule in filer-prompt.md" >&2
          exit 1
        }
        grep -qF -- 'the launcher appends its own' \
          ${../../templates/default/prompts/filer-prompt.md} || {
          echo "expected the research-path launcher-appends-its-own-backlink reasoning in filer-prompt.md" >&2
          exit 1
        }
        grep -qF -- 'NEVER the dispatch label' \
          ${../../templates/default/prompts/filer-prompt.md} || {
          echo "expected the never-the-dispatch-label rule in filer-prompt.md" >&2
          exit 1
        }
        touch $out
      '';

  # The PR-body ticket-reference toggle (issue #1429, ADR 0029): the three
  # PR_BODY_CLOSES/PR_BODY_LOCAL_REF/PR_BODY_LOCAL_NOREF fragment files are
  # each unconditional prose for their one case (agent/entrypoint.sh's
  # precompute block picks exactly one gate per run, never a nix-time
  # rendering choice), so a static grep on the fragment file source -- the
  # same eval-only, no-image-build shape as filer-prompt-dedup-* above --
  # pins each case's contract without needing a live container run:
  #   github (unchanged):  `Closes #${ISSUE_NUMBER}` stays, byte-identical to
  #                         the pre-#1429 unconditional instruction.
  #   local, toggle off:   no reference to the ticket at all, and neither
  #                         auto-close keyword.
  #   local, toggle on:    a `Local-issue: <slug>` breadcrumb, and neither
  #                         auto-close keyword -- the footgun fix.
  # The runtime wiring that picks the right gate from ISSUE_TRACKER x
  # LOCAL_ISSUE_REFERENCE is covered by tests/entrypoint-prompt-fragments.bats,
  # not here -- that needs a live entrypoint.sh run, out of scope for an
  # eval-only checks-inbox check.
  pr-body-reference-github-unchanged = pkgs.runCommand "pr-body-reference-github-unchanged" { } ''
    grep -qF 'Closes #''${ISSUE_NUMBER}' ${../../templates/default/prompts/fragments/pr-body-closes.md}
    touch $out
  '';

  pr-body-reference-local-off-has-no-reference =
    pkgs.runCommand "pr-body-reference-local-off-has-no-reference" { }
      ''
        ! grep -qi 'closes\|fixes\|local-issue\|''${ISSUE_NUMBER}' \
          ${../../templates/default/prompts/fragments/pr-body-local-noref.md}
        touch $out
      '';

  pr-body-reference-local-on-has-breadcrumb-not-closing-keyword =
    pkgs.runCommand "pr-body-reference-local-on-has-breadcrumb-not-closing-keyword" { }
      ''
        grep -qF 'Local-issue: ''${ISSUE_NUMBER}' ${../../templates/default/prompts/fragments/pr-body-local-ref.md}
        ! grep -qi 'closes\|fixes' ${../../templates/default/prompts/fragments/pr-body-local-ref.md}
        touch $out
      '';

  # The issue-read step (issue #1691, ADR 0032): the four local-tracker
  # fragments must never invoke `gh issue view` -- for a numeric slug it can
  # silently fetch an unrelated real issue on the Target repo, the exact
  # footgun the read-only /issues mount exists to close -- and must reference
  # /issues instead. Fragment content itself is otherwise unchecked, so a
  # future edit reintroducing `gh issue view` into a local variant would
  # otherwise go uncaught. Same static, eval-only grep shape as the
  # pr-body-reference-* checks above.
  issue-read-local-fragments-never-invoke-gh-issue-view =
    pkgs.runCommand "issue-read-local-fragments-never-invoke-gh-issue-view" { }
      ''
        for f in issue-read-local.md research-issue-read-local.md \
          scout-issue-read-local.md review-issue-read-local.md; do
          n=$(grep -c 'gh issue view' ${../../templates/default/prompts/fragments}/"$f" || true)
          [ "$n" -eq 0 ] || {
            echo "$f: expected no 'gh issue view', found $n occurrence(s)" >&2
            exit 1
          }
          grep -q '/issues/''${ISSUE_NUMBER}\.md' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The github-side counterpart: each of the four github variants keeps
  # `gh issue view ''${ISSUE_NUMBER}` unchanged, exactly as it read before
  # issue #1691's branch existed.
  issue-read-github-fragments-keep-gh-issue-view-unchanged =
    pkgs.runCommand "issue-read-github-fragments-keep-gh-issue-view-unchanged" { }
      ''
        for f in issue-read-github.md research-issue-read-github.md \
          scout-issue-read-github.md review-issue-read-github.md; do
          grep -q 'gh issue view ''${ISSUE_NUMBER}' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side counterpart (issue #1963): each of the four forgejo
  # variants speaks fj issue view, never gh issue view.
  issue-read-forgejo-fragments-speak-fj-not-gh =
    pkgs.runCommand "issue-read-forgejo-fragments-speak-fj-not-gh" { }
      ''
        for f in issue-read-forgejo.md research-issue-read-forgejo.md \
          scout-issue-read-forgejo.md review-issue-read-forgejo.md; do
          grep -q 'fj issue view ''${ISSUE_NUMBER}' ${../../templates/default/prompts/fragments}/"$f"
          n=$(grep -c 'gh issue view' ${../../templates/default/prompts/fragments}/"$f" || true)
          [ "$n" -eq 0 ] || {
            echo "$f: expected no 'gh issue view', found $n occurrence(s)" >&2
            exit 1
          }
        done
        touch $out
      '';

  # Issue #1990: unbounded `--comments` pulls a meta-issue's entire comment
  # history into the agent's context on every turn. Each of the four github
  # variants must cap intake to the last 10 comments (`comments[-10:]`)
  # instead of the bare `--comments` flag.
  issue-read-github-fragments-cap-comment-intake =
    pkgs.runCommand "issue-read-github-fragments-cap-comment-intake" { }
      ''
        for f in issue-read-github.md research-issue-read-github.md \
          scout-issue-read-github.md review-issue-read-github.md; do
          grep -q 'comments\[-10:\]' ${../../templates/default/prompts/fragments}/"$f" || {
            echo "$f: expected a bounded comments[-10:] read" >&2
            exit 1
          }
          ! grep -qE -- '--comments\b' ${../../templates/default/prompts/fragments}/"$f" || {
            echo "$f: still uses the unbounded --comments flag" >&2
            exit 1
          }
        done
        touch $out
      '';

  # The read-write write-step fragments (issue #1917) must keep
  # `gh issue comment` unchanged -- byte-for-byte the same in-box write these
  # two steps always rendered before BOX_FORGE_AND_ISSUE_ACCESS existed. Same
  # static, eval-only grep shape as issue-read-github-fragments-* above.
  github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged =
    pkgs.runCommand "github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged" { }
      ''
        for f in issue-blocked-comment-github.md research-verdict-github.md; do
          grep -q 'gh issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The read-only counterpart (issue #1917): a read-only Box holds no write
  # token, so its blocked-note/verdict-comment fragments must never invoke
  # `gh issue comment` -- the exact footgun a read-only token can't satisfy --
  # and must carry the host-mediated relay instead: the blocked-note fragment
  # points at the SPINDRIFT_OUTCOME note= field (mirroring local's own
  # blocked-note relay, issue-blocked-comment-local.md), and the
  # research-verdict fragment emits a single nonce-guarded SPINDRIFT_COMMENT
  # line (mirroring research-verdict-local.md; issue #1940 replaced the
  # earlier SPINDRIFT_COMMENT_BEGIN/END block form with this single-line,
  # nonce-bearing, base64-encoded grammar so the signal survives a
  # stream-json JSONL box log). Same static, eval-only grep shape as
  # issue-read-local-fragments-never-invoke-gh-issue-view above.
  github-readonly-comment-fragments-never-invoke-gh-issue-comment =
    pkgs.runCommand "github-readonly-comment-fragments-never-invoke-gh-issue-comment" { }
      ''
        for f in issue-blocked-comment-github-readonly.md research-verdict-github-readonly.md; do
          n=$(grep -c 'gh issue comment' ${../../templates/default/prompts/fragments}/"$f" || true)
          [ "$n" -eq 0 ] || {
            echo "$f: expected no 'gh issue comment', found $n occurrence(s)" >&2
            exit 1
          }
        done
        grep -q 'note=' ${../../templates/default/prompts/fragments/issue-blocked-comment-github-readonly.md}
        for f in research-verdict-github-readonly.md research-verdict-local.md; do
          grep -q 'SPINDRIFT_COMMENT ''${RUN_NONCE}' ${../../templates/default/prompts/fragments}/"$f"
          ! grep -q 'SPINDRIFT_COMMENT_BEGIN' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side counterpart of github-readwrite-comment-fragments-*
  # above (issue #1963): the read-write write-step fragments must keep
  # `fj issue comment` -- same static, eval-only grep shape.
  forgejo-readwrite-comment-fragments-keep-fj-issue-comment =
    pkgs.runCommand "forgejo-readwrite-comment-fragments-keep-fj-issue-comment" { }
      ''
        for f in issue-blocked-comment-forgejo.md research-verdict-forgejo.md; do
          grep -q 'fj issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side counterpart of github-readonly-comment-fragments-*
  # above (issue #1963): a read-only Box holds no write-capable
  # FORGEJO_TOKEN, so its blocked-note/verdict-comment fragments must never
  # invoke `fj issue comment` and must carry the same host-mediated relay
  # forms (note= field / SPINDRIFT_COMMENT line) as the github/local
  # counterparts.
  forgejo-readonly-comment-fragments-never-invoke-fj-issue-comment =
    pkgs.runCommand "forgejo-readonly-comment-fragments-never-invoke-fj-issue-comment" { }
      ''
        for f in issue-blocked-comment-forgejo-readonly.md research-verdict-forgejo-readonly.md; do
          n=$(grep -c 'fj issue comment' ${../../templates/default/prompts/fragments}/"$f" || true)
          [ "$n" -eq 0 ] || {
            echo "$f: expected no 'fj issue comment', found $n occurrence(s)" >&2
            exit 1
          }
        done
        grep -q 'note=' ${../../templates/default/prompts/fragments/issue-blocked-comment-forgejo-readonly.md}
        grep -q 'SPINDRIFT_COMMENT ''${RUN_NONCE}' ${../../templates/default/prompts/fragments/research-verdict-forgejo-readonly.md}
        ! grep -q 'SPINDRIFT_COMMENT_BEGIN' ${../../templates/default/prompts/fragments/research-verdict-forgejo-readonly.md}
        touch $out
      '';

  # The filer write-mechanism split (issue #2019): the direct-mode fragments
  # must keep `gh label create`/`gh issue create` unchanged -- byte-for-byte
  # the same in-box writes filer-prompt.md's steps always rendered before
  # this split existed. Same static, eval-only grep shape as the
  # github-readwrite-comment-fragments-* check above.
  filer-direct-fragments-keep-gh-write-unchanged =
    pkgs.runCommand "filer-direct-fragments-keep-gh-write-unchanged" { }
      ''
        grep -q 'gh label create agent-review-finding' ${../../templates/default/prompts/fragments/filer-label-direct.md}
        grep -q 'gh issue create' ${../../templates/default/prompts/fragments/filer-file-direct.md}
        touch $out
      '';

  # The read-only counterpart (issue #2019): a read-only Box under
  # ORCHESTRATOR_ENABLED holds no write token, so the filer's relay
  # fragments must never invoke `gh label create` -- the exact footgun a
  # read-only token can't satisfy -- and must carry the host-mediated
  # SPINDRIFT_ISSUE_INTENT relay instead (mirroring open-pr-create-outbox.md's
  # SPINDRIFT_PR_INTENT form). `gh issue create`'s absence here is already
  # covered by the mkHarness structural forbidden-marker eval assert (issue
  # #2510/#2513). Same static, eval-only grep shape as
  # github-readonly-comment-fragments-* above.
  filer-relay-fragments-never-invoke-gh-write =
    pkgs.runCommand "filer-relay-fragments-never-invoke-gh-write" { }
      ''
        for f in filer-label-relay.md filer-file-relay.md file-issues-relay.md; do
          n=$(grep -c -- 'gh label create' ${../../templates/default/prompts/fragments}/"$f" || true)
          [ "$n" -eq 0 ] || {
            echo "$f: expected no 'gh label create', found $n occurrence(s)" >&2
            exit 1
          }
        done
        grep -q 'SPINDRIFT_ISSUE_INTENT ''${RUN_NONCE}' ${../../templates/default/prompts/fragments/filer-file-relay.md}
        touch $out
      '';

  # The forgejo counterpart of filer-direct-fragments-keep-gh-write-unchanged
  # above (issue #1963): fj has no label verb and `fj issue create` has no
  # --label flag, so the forgejo direct-mode fragments must speak `fj issue
  # create` (never `gh issue create`) and the REST API (curl) for the label
  # (never `gh label create`). Same static, eval-only grep shape.
  filer-direct-forgejo-fragments-speak-fj-and-curl =
    pkgs.runCommand "filer-direct-forgejo-fragments-speak-fj-and-curl" { }
      ''
        grep -q 'fj issue create' ${../../templates/default/prompts/fragments/filer-file-direct-forgejo.md}
        ! grep -q 'gh issue create' ${../../templates/default/prompts/fragments/filer-file-direct-forgejo.md}
        grep -q '/api/v1/repos' ${../../templates/default/prompts/fragments/filer-label-direct-forgejo.md}
        grep -q 'agent-review-finding' ${../../templates/default/prompts/fragments/filer-label-direct-forgejo.md}
        ! grep -q 'gh label create' ${../../templates/default/prompts/fragments/filer-label-direct-forgejo.md}
        touch $out
      '';

  # The OPEN A PULL REQUEST read-write create step forks on CODE_FORGE
  # (issue #1963, OPEN_PR_CREATE_RW_GH/OPEN_PR_CREATE_RW_FORGEJO computed in
  # entrypoint.sh): the github fragment must keep `gh pr create` and never
  # invoke `fj pr create`, and the new forgejo fragment must invoke
  # `fj pr create` and never `gh pr create`. Same static, eval-only grep
  # shape as the other fragment-content checks above.
  open-pr-create-fragments-fork-forge-on-read-write =
    pkgs.runCommand "open-pr-create-fragments-fork-forge-on-read-write" { }
      ''
        grep -q 'gh pr create' ${../../templates/default/prompts/fragments/open-pr-create-git.md}
        ! grep -q 'fj pr create' ${../../templates/default/prompts/fragments/open-pr-create-git.md}
        grep -q 'fj pr create' ${../../templates/default/prompts/fragments/open-pr-create-forgejo.md}
        ! grep -q 'gh pr create' ${../../templates/default/prompts/fragments/open-pr-create-forgejo.md}
        touch $out
      '';

  # The fix-pass CONTEXT CI-read step forks on CODE_FORGE (issue #1963,
  # FIX_CI_READ_GH/FIX_CI_READ_FORGEJO computed in entrypoint.sh): the github
  # fragment must keep `gh pr view` and never invoke `fj pr status`, and the
  # forgejo fragment must invoke `fj pr status` and never `gh pr view`. Same
  # static, eval-only grep shape as open-pr-create-fragments-fork-forge-on-
  # read-write above.
  fix-ci-read-fragments-fork-forge = pkgs.runCommand "fix-ci-read-fragments-fork-forge" { } ''
    grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    ! grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    ! grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    touch $out
  '';

  # Build-time reject arm (issue #2250, parent #2244): mkHarness.nix wires the
  # `reviewer-verdict` validateMarkers row (lib/prompt-contract.nix's
  # buildTimeRejectVerdicts) into a real build-time failure when the
  # orchestrator is statically enabled and reviewPrompt is missing the
  # required `VERDICT:` marker -- mirrors nix/checks/equivalence.nix's
  # `flakemodule-rejects-unknown-settings` tryEval-based "this must throw"
  # idiom. Each mkHarness.nix call here is a broken fixture built INLINE,
  # never exported from nix/fixtures.nix -- a fixture there would be forced
  # by every other consumer of that file, but this reject case must stay
  # local to this one check (mirrors equivalence.nix's badSection/badKnob).
  build-time-reject-orchestrator-verdict-missing =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          defaults = {
            orchestratorEnabled = true;
          };
          reviewPrompt = "a reviewer prompt preamble with no verdict marker at all";
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when orchestratorEnabled is statically true and reviewPrompt is missing the required VERDICT: marker";
    pkgs.runCommand "build-time-reject-orchestrator-verdict-missing" { } "touch $out";

  # The gate-not-triggered counterpart (AC3): the same missing-marker
  # reviewPrompt, but orchestratorEnabled left at its schema default (false)
  # -- the omission is real but its gating condition isn't statically known
  # true, so buildTimeRejectVerdicts resolves "advise", not "reject", and the
  # build must succeed.
  build-time-reject-orchestrator-verdict-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          defaults = {
            orchestratorEnabled = false;
          };
          reviewPrompt = "a reviewer prompt preamble with no verdict marker at all";
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when orchestratorEnabled is not statically true, even with a missing VERDICT: marker";
    pkgs.runCommand "build-time-reject-orchestrator-verdict-not-triggered" { } "touch $out";

  # The `verdict-comment-relay` counterpart (issue #2250, parent #2244):
  # brokenResearchVerdictFragmentsDir (defined in the `let` above) swaps in
  # a research-verdict-github-readonly.md missing the required
  # SPINDRIFT_COMMENT marker. Shared by both checks below (missing/not-
  # triggered), mirroring the reviewPrompt fixture build-time-reject-
  # orchestrator-verdict-{missing,not-triggered} share above.
  build-time-reject-research-verdict-comment-relay-missing =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = brokenResearchVerdictFragmentsDir;
          defaults = {
            boxForgeAndIssueAccess = "read-only";
          };
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when boxForgeAndIssueAccess is statically read-only and research-verdict-github-readonly.md is missing the required SPINDRIFT_COMMENT marker";
    pkgs.runCommand "build-time-reject-research-verdict-comment-relay-missing" { } "touch $out";

  # The gate-not-triggered counterpart (AC3): the same broken fragments
  # directory, but boxForgeAndIssueAccess left at its schema default
  # (read-write) -- the omission is real but its gating condition isn't
  # statically known true, so buildTimeRejectVerdicts resolves "advise", not
  # "reject", and the build must succeed.
  build-time-reject-research-verdict-comment-relay-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = brokenResearchVerdictFragmentsDir;
          defaults = {
            boxForgeAndIssueAccess = "read-write";
          };
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when boxForgeAndIssueAccess is not statically read-only, even with a missing SPINDRIFT_COMMENT marker";
    pkgs.runCommand "build-time-reject-research-verdict-comment-relay-not-triggered" { } "touch $out";

  # Structural forbidden-marker check (issue #2510, parent #2498 campaign R):
  # a forbidden marker (lib/prompt-contract.nix forbiddenMarkers) authored as
  # literal fragment-body text in a fragment gated on a plain, non-exempt gate
  # must fail the build -- unconditionally, unlike buildTimeRejectVerdicts
  # above, since a forbidden marker in the shipped corpus is a problem for
  # any Consumer that might configure boxAccessReadOnly, not just this
  # particular build's own static gates.
  build-time-reject-forbidden-marker-fragment =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = brokenForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan: real filer-prompt.md
          # (and real issue-prompt.md, if left default) already carry an
          # unrelated, already-known template violation (see
          # cleanForbiddenMarkerPlaceholder's doc comment above), which
          # would make this assertion pass for the wrong reason.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when a fragment gated on a plain, non-exempt gate (AUTO_FORMAT) carries a forbidden marker ('git push') as literal fragment-body text";
    pkgs.runCommand "build-time-reject-forbidden-marker-fragment" { } "touch $out";

  # The exempt-gate counterpart (regression guard): the same forbidden-marker
  # substring, but injected into a fragment gated on an exempt gate
  # (BOX_ACCESS_READ_ONLY) instead -- many shipped fragments legitimately
  # carry forbidden-marker text as a negation ("do NOT git push") since
  # they're the read-only half of an explicit access-mode pair, so the check
  # must not false-positive on them. Proves the exemption rule actually
  # protects legitimate read-only-labeled fragments.
  build-time-forbidden-marker-fragment-exempt-gate-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = exemptGateForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan -- see the sibling
          # build-time-reject-forbidden-marker-fragment check above for why.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when a fragment gated on an exempt gate (BOX_ACCESS_READ_ONLY) carries a forbidden marker as literal fragment-body text";
    pkgs.runCommand "build-time-forbidden-marker-fragment-exempt-gate-not-triggered" { } "touch $out";

  # The gh-api-mutation-kind counterpart (issue #2513): a plain, non-exempt
  # gate (same shape as build-time-reject-forbidden-marker-fragment above)
  # carrying the forbidden-gh-api-mutation row's marker ("gh api") instead
  # of a kind == "substring" row's marker. buildTimeForbiddenMarkerViolations
  # filters to kind == "substring" rows only, so this must NOT throw --
  # proves that filter still excludes the gh-api-mutation row rather than
  # having silently regressed to scanning every row regardless of kind.
  build-time-forbidden-marker-fragment-gh-api-mutation-kind-not-scanned =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = ghAPIMutationForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan -- see the sibling
          # build-time-reject-forbidden-marker-fragment check above for why.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when a fragment carries the forbidden-gh-api-mutation row's marker ('gh api') as literal text -- that row's kind is 'gh-api-mutation', not 'substring', so it must be excluded from the build-time scan";
    pkgs.runCommand "build-time-forbidden-marker-fragment-gh-api-mutation-kind-not-scanned" { }
      "touch $out";

  # The shared top-level template counterpart (issue #2510): `prompt`
  # (issue-prompt.md's default) gets no exemption at all -- its raw text is
  # scanned unconditionally against every forbiddenMarkers substring row.
  build-time-reject-forbidden-marker-template =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          prompt = "some issue prompt text containing gh pr create somewhere";
          # Isolates this check to the deliberately-broken `prompt` param:
          # real filer-prompt.md carries an unrelated, already-known
          # template violation (see cleanForbiddenMarkerPlaceholder's doc
          # comment above), which would make this assertion pass for the
          # wrong reason.
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when the shared `prompt` template carries a forbidden marker ('gh pr create') as literal text";
    pkgs.runCommand "build-time-reject-forbidden-marker-template" { } "touch $out";

  # Same as above, but exercising `reviewPrompt` instead of `prompt` --
  # templateContentByFile's three entries are hand-written attrset keys
  # (lib/mkHarness.nix), so a check that only ever overrides `prompt` would
  # never notice if the `reviewPrompt` (or `filerPrompt`, below) entry were
  # silently dropped or mis-keyed.
  build-time-reject-forbidden-marker-review-template =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          prompt = cleanForbiddenMarkerPlaceholder;
          reviewPrompt = "some review prompt text containing gh pr merge somewhere";
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when the shared `reviewPrompt` template carries a forbidden marker ('gh pr merge') as literal text";
    pkgs.runCommand "build-time-reject-forbidden-marker-review-template" { } "touch $out";

  # Same again for `filerPrompt`.
  build-time-reject-forbidden-marker-filer-template =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = "some filer prompt text containing gh issue create somewhere";
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when the shared `filerPrompt` template carries a forbidden marker ('gh issue create') as literal text";
    pkgs.runCommand "build-time-reject-forbidden-marker-filer-template" { } "touch $out";

  # Build-time research-direct-file check (issue #2595, ADR 0041: "Research
  # filing is host-mediated and relay-only"): a research prompt must never
  # statically carry a FILER_FILE_DIRECT*-gated fragment's envsubst
  # placeholder -- research issues are always filed through the host-mediated
  # SPINDRIFT_ISSUE_INTENT relay, never `gh`/`fj` straight from the agent.
  # FILER_FILE_DIRECT_STEP is filer-file-direct.md's var (gate
  # FILER_FILE_DIRECT_GH, lib/fragments.nix), so wiring it into `researchPrompt`
  # here is standing in for the regression this check exists to catch.
  build-time-reject-research-direct-file-prompt =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          researchPrompt = "some research prompt text with \${FILER_FILE_DIRECT_STEP} embedded";
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when researchPrompt statically carries a direct-file fragment's \${VAR} placeholder (ADR 0041)";
    pkgs.runCommand "build-time-reject-research-direct-file-prompt" { } "touch $out";

  # Same as above, but exercising researchSelfContainedPrompt (the
  # self-contained research sub-mode's own prompt template, issue #2202) --
  # the acceptance criteria for #2595 names both research prompt kinds
  # explicitly, and researchPrompt/researchSelfContainedPrompt are two
  # separately hand-keyed entries in lib/mkHarness.nix's
  # researchPromptContentByName, so a check that only ever overrides
  # researchPrompt would never notice if the self-contained entry were
  # silently dropped or mis-keyed.
  build-time-reject-research-direct-file-self-contained-prompt =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          researchSelfContainedPrompt = "some self-contained research prompt text with \${FILER_FILE_DIRECT_STEP} embedded";
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when researchSelfContainedPrompt statically carries a direct-file fragment's \${VAR} placeholder (ADR 0041)";
    pkgs.runCommand "build-time-reject-research-direct-file-self-contained-prompt" { } "touch $out";

  # The "not triggered" counterpart: the real, unmodified default templates
  # must build clean today -- lib/fragments.nix's real DIRECT-gated rows
  # never wire their var into either research prompt template (see that
  # file's own doc comment on research-file-issues-relay.md), so this proves
  # the current configuration passes rather than only ever exercising the
  # deliberately-broken fixtures above.
  build-time-research-direct-file-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw for the real, unmodified research prompt templates -- neither carries a direct-file fragment's \${VAR} placeholder today (ADR 0041)";
    pkgs.runCommand "build-time-research-direct-file-not-triggered" { } "touch $out";

  # Anti-drift registry check (issue #2709, slice 1): lib/prompt-coverage.nix
  # declares one row per prompt template under templates/default/prompts/,
  # classifying it "covered" (its assembled text must carry a caveman
  # envsubst variable) or "exempt" (with a reason) -- before this registry
  # existed, caveman coverage was decided once by hand per template, so a
  # new prompt kind added later would silently default to uncovered. This
  # check only guards the registry's own completeness against the templates
  # directory, both directions: a template on disk missing a registry row,
  # and a stale registry row naming a template that no longer exists. It
  # deliberately does NOT check that a "covered" row's assembled text
  # actually carries its declared variable, nor does it tie into the
  # validateMarkers/forbiddenMarkers registries above -- those assertions
  # are the caveman-coverage-covered-templates-carry-directive and
  # caveman-coverage-exemption-list-covers-marker-registry checks below
  # (issue #2709, slices 2 and 3).
  caveman-coverage-registry-matches-templates-dir =
    let
      inherit (pkgs.lib) concatMapStringsSep;
      # Trailing "\n": matches the sibling list files below (coveredRowsFile,
      # exemptFiles, requiredMarkerNamesFile), which all carry one for the
      # same reason (a `while read` loop would otherwise drop the final
      # line). Harmless here under `sort`/`comm`/`uniq` today, but keeps this
      # file consistent with its siblings for any future `while read` reuse.
      registryFiles = pkgs.writeText "caveman-coverage-registry-files.txt" (
        concatMapStringsSep "\n" (r: r.promptFile) cavemanCoverageRegistry + "\n"
      );
    in
    pkgs.runCommand "caveman-coverage-registry-matches-templates-dir" { } ''
      registry_files=$(sort ${registryFiles})
      disk_files=$(find ${../../templates/default/prompts} -maxdepth 1 -name '*.md' -printf '%f\n' | sort)

      # A duplicate row (two entries naming the same promptFile) would
      # otherwise surface through `comm -13` below as the misleading
      # "names a promptFile that does not exist" -- comm's multiset
      # semantics report the second occurrence of a duplicate registry line
      # as "unique to registry" once the single disk copy is consumed.
      # Catch the real fault directly, with its own message, first.
      duplicates=$(echo "$registry_files" | uniq -d)
      [ -z "$duplicates" ] || {
        echo "lib/prompt-coverage.nix has more than one row for the following promptFile(s):" >&2
        echo "$duplicates" >&2
        exit 1
      }

      missing=$(comm -23 <(echo "$disk_files") <(echo "$registry_files"))
      [ -z "$missing" ] || {
        echo "lib/prompt-coverage.nix is missing a row for the following template(s) under templates/default/prompts/:" >&2
        echo "$missing" >&2
        echo "every *.md file directly under templates/default/prompts/ must have a covered/exempt row" >&2
        exit 1
      }

      stale=$(comm -13 <(echo "$disk_files") <(echo "$registry_files"))
      [ -z "$stale" ] || {
        echo "lib/prompt-coverage.nix names a promptFile that does not exist under templates/default/prompts/:" >&2
        echo "$stale" >&2
        exit 1
      }

      touch $out
    '';

  # Row-shape guards (blocking code-review finding on issue #2709): the
  # registry's header comment documents three invariants -- coverage is
  # "covered" or "exempt"; cavemanVar is required iff covered; reason is
  # required iff exempt -- but nothing previously enforced them. A typo'd
  # `coverage = "Covered";` would silently drop that row from every
  # `filter (r: r.coverage == "covered")` / `filter (r: r.coverage ==
  # "exempt")` call above, so the two content checks above would pass
  # vacuously and reinstate the exact "silently defaults to uncovered" drift
  # this registry exists to kill. Pure eval-time asserts, mirroring
  # nix/checks/prompt-contract.nix's `bad = filter …; assert assertMsg (bad
  # == [ ])` idiom (added there for this same typo class, #2499) -- the
  # assertion fires during `nix eval`/`nix build`, before any derivation
  # builds; the check derivation itself is a no-op `touch $out` that exists
  # only so `nix build`/`nix flake check` forces the assertion.
  caveman-coverage-registry-coverage-is-known-value =
    let
      inherit (pkgs.lib) assertMsg concatMapStringsSep filter;
      bad = filter (r: r.coverage != "covered" && r.coverage != "exempt") cavemanCoverageRegistry;
    in
    assert assertMsg (bad == [ ])
      "every row's coverage must be 'covered' or 'exempt', offending promptFile(s): [ ${
        concatMapStringsSep ", " (r: r.promptFile) bad
      } ]";
    pkgs.runCommand "caveman-coverage-registry-coverage-is-known-value" { } "touch $out";

  caveman-coverage-registry-caveman-var-required-iff-covered =
    let
      inherit (pkgs.lib) assertMsg concatMapStringsSep filter;
      bad = filter (r: (r.coverage == "covered") != (r.cavemanVar != null)) cavemanCoverageRegistry;
    in
    assert assertMsg (bad == [ ])
      "every row's cavemanVar must be non-null iff coverage == \"covered\" (and null iff \"exempt\"), offending promptFile(s): [ ${
        concatMapStringsSep ", " (r: r.promptFile) bad
      } ]";
    pkgs.runCommand "caveman-coverage-registry-caveman-var-required-iff-covered" { } "touch $out";

  caveman-coverage-registry-reason-required-iff-exempt =
    let
      inherit (pkgs.lib) assertMsg concatMapStringsSep filter;
      bad = filter (r: (r.coverage == "exempt") != (r.reason != null)) cavemanCoverageRegistry;
    in
    assert assertMsg (bad == [ ])
      "every row's reason must be non-null iff coverage == \"exempt\" (and null iff \"covered\"), offending promptFile(s): [ ${
        concatMapStringsSep ", " (r: r.promptFile) bad
      } ]";
    pkgs.runCommand "caveman-coverage-registry-reason-required-iff-exempt" { } "touch $out";

  # Structural tie from cavemanVar to lib/fragments.nix (blocking code-review
  # finding on issue #2709): the sibling check below,
  # caveman-coverage-covered-templates-carry-directive, only greps the
  # assembled prompt for the literal, unsubstituted "${cavemanVar}"
  # placeholder text -- it never checks that cavemanVar names a real
  # CAVEMAN_BAKED fragment var at all. A typo'd cavemanVar (or one that
  # simply doesn't correspond to any fragment) would still pass that grep by
  # coincidence if the placeholder text happens to appear in the assembled
  # prompt for any reason, and would render with no caveman skill actually
  # baked in. This check cross-references cavemanVar against
  # fragmentsRegistry's own CAVEMAN_BAKED-gated rows -- a pure Nix data
  # comparison, not another string-content grep -- so the tie is structural
  # rather than coincidental. It complements, not replaces, the
  # carries-directive check below.
  caveman-coverage-covered-templates-caveman-var-known-to-fragments-registry =
    let
      inherit (pkgs.lib) assertMsg concatMapStringsSep filter;
      knownCavemanVars = map (r: r.var) (filter (r: r.gate == "CAVEMAN_BAKED") fragmentsRegistry);
      bad = filter (
        r: r.coverage == "covered" && !(builtins.elem r.cavemanVar knownCavemanVars)
      ) cavemanCoverageRegistry;
    in
    assert assertMsg (bad == [ ])
      "every covered row's cavemanVar must be a var defined by one of lib/fragments.nix's CAVEMAN_BAKED-gated rows, offending: [ ${
        concatMapStringsSep ", " (r: r.promptFile + ": " + toString r.cavemanVar) bad
      } ]";
    pkgs.runCommand "caveman-coverage-covered-templates-caveman-var-known-to-fragments-registry" { }
      "touch $out";

  # Per-row directive check (issue #2709, slice 2): for every "covered" row
  # in lib/prompt-coverage.nix, assert the *assembled* prompt (via
  # batsHarness -- the same harness the mkharness-prompt-* checks above read
  # from) contains the literal, unsubstituted envsubst placeholder for its
  # declared cavemanVar (e.g. "${CAVEMAN_STEP_WORKER}"). envsubst
  # substitution happens at container runtime, not at nix build time, so the
  # literal ${VAR} text is what's on disk in the assembled prompt dir. Reads
  # uniformly from the assembled dir for every covered row, not the raw
  # on-disk template -- fix-prompt.md's directive only exists post-injection
  # (see lib/prompt-coverage.nix's comment on that row), and reading every
  # row the same way keeps this check from needing to special-case it.
  #
  # The search string per row is built in real Nix string concatenation
  # (not inside the runCommand shell), so the shell script never has to
  # reconstruct a literal "${...}" from a dynamic variable name -- each
  # line of coveredRowsFile already carries the exact target directive.
  caveman-coverage-covered-templates-carry-directive =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      coveredRows = filter (r: r.coverage == "covered") cavemanCoverageRegistry;
      # Trailing "\n" matters: bash's `while read` skips a final line with
      # no trailing newline (its exit status goes nonzero right when the
      # loop body would otherwise run), so a bare concatMapStringsSep here
      # would silently drop the last covered row from the scan.
      coveredRowsFile = pkgs.writeText "caveman-coverage-covered-rows.txt" (
        concatMapStringsSep "\n" (r: r.promptFile + " \${" + r.cavemanVar + "}") coveredRows + "\n"
      );
    in
    pkgs.runCommand "caveman-coverage-covered-templates-carry-directive" { } ''
      promptDir=${batsHarness.internals.promptDir}
      while read -r promptFile directive; do
        [ -n "$promptFile" ] || continue
        [ -f "$promptDir/$promptFile" ] || {
          echo "$promptFile: expected the assembled prompt directory to contain this file (registry row in lib/prompt-coverage.nix) -- not found" >&2
          exit 1
        }
        grep -qF -- "$directive" "$promptDir/$promptFile" || {
          echo "$promptFile: expected the assembled prompt to carry the literal directive $directive (declared covered in lib/prompt-coverage.nix), not found" >&2
          exit 1
        }
      done < ${coveredRowsFile}
      touch $out
    '';

  # Exempt-row check, the filer-prompt half of the same registry (issue
  # #2709, slice 2 acceptance criteria): every "exempt" row's assembled
  # prompt must carry NO case-insensitive occurrence of "caveman" at all --
  # currently just filer-prompt.md, which authors GitHub issue titles/bodies
  # directly and so must stay human prose end to end (see its `reason` row).
  # Derives the file list from the registry's exempt rows instead of
  # hardcoding "filer-prompt.md" here, so this generalizes if a future
  # exempt row is added without a second hand-maintained list (the same
  # single-sourcing principle issue #2709 asks for).
  caveman-coverage-exempt-templates-carry-no-caveman-mention =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      exemptRows = filter (r: r.coverage == "exempt") cavemanCoverageRegistry;
      # Same trailing-newline guard as coveredRowsFile above.
      exemptFiles = pkgs.writeText "caveman-coverage-exempt-files.txt" (
        concatMapStringsSep "\n" (r: r.promptFile) exemptRows + "\n"
      );
    in
    pkgs.runCommand "caveman-coverage-exempt-templates-carry-no-caveman-mention" { } ''
      promptDir=${batsHarness.internals.promptDir}
      while read -r promptFile; do
        [ -n "$promptFile" ] || continue
        [ -f "$promptDir/$promptFile" ] || {
          echo "$promptFile: expected the assembled prompt directory to contain this file (registry row in lib/prompt-coverage.nix) -- not found" >&2
          exit 1
        }
        # `|| true`: under stdenv's pipefail, a no-match exit (grep's status
        # 1) would otherwise abort the script right here, before the
        # assertion below ever runs.
        n=$(grep -ic 'caveman' "$promptDir/$promptFile" || true)
        [ "$n" -eq 0 ] || {
          echo "$promptFile: expected no case-insensitive 'caveman' mention in the assembled prompt (declared exempt in lib/prompt-coverage.nix), found $n" >&2
          exit 1
        }
      done < ${exemptFiles}
      touch $out
    '';

  # Ties the caveman narration directive to the machine-parsed marker
  # registries (issue #2709, slice 3; fixed to derive rather than hardcode
  # the fragment list per the #2709 review finding): every fragment row in
  # lib/fragments.nix gated `CAVEMAN_BAKED` carries a "the machine-parsed
  # marker grammar is exempt too" paragraph naming a subset of
  # requiredMarkerNames (defined above in the shared let), so a marker
  # that's parsed by code but never named in any caveman fragment risks a
  # Box caveman-compressing it into an unparseable line. The fragment file
  # list itself is derived from fragmentsRegistry's CAVEMAN_BAKED rows
  # (currently 4: caveman-default.md/-worker.md/-review.md/-research.md)
  # rather than hand-typed here a second time, so a future 5th CAVEMAN_BAKED
  # fragment is picked up automatically instead of silently unscanned --
  # the same single-sourcing principle the sibling checks above already
  # apply to their own file lists.
  caveman-coverage-exemption-list-covers-marker-registry =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      cavemanFragmentsDir = ../../templates/default/prompts/fragments;
      cavemanFragmentRows = filter (r: r.gate == "CAVEMAN_BAKED") fragmentsRegistry;
      cavemanFragmentPaths = map (r: cavemanFragmentsDir + "/${r.fragment}") cavemanFragmentRows;
      # Same trailing-newline guard as coveredRowsFile/exemptFiles above --
      # a pkgs.writeText list without a trailing "\n" silently drops the
      # last line from a `while read` loop.
      requiredMarkerNamesFile = pkgs.writeText "caveman-coverage-required-marker-names.txt" (
        concatMapStringsSep "\n" (m: m) requiredMarkerNames + "\n"
      );
    in
    pkgs.runCommand "caveman-coverage-exemption-list-covers-marker-registry" { } ''
      cat ${concatMapStringsSep " " (p: "${p}") cavemanFragmentPaths} > fragments-union.txt

      while read -r marker; do
        [ -n "$marker" ] || continue
        grep -qF -- "$marker" fragments-union.txt || {
          echo "marker '$marker' (from lib/prompt-contract.nix's validateMarkers/workerForbiddenMarkers) is not named in any CAVEMAN_BAKED-gated fragment (lib/fragments.nix) under templates/default/prompts/fragments/ -- name it in the 'machine-parsed marker grammar is exempt too' paragraph of at least one" >&2
          exit 1
        }
      done < ${requiredMarkerNamesFile}
      touch $out
    '';

  # Regression test for lib/mkHarness.nix's isDirectFileGate predicate (issue
  # #2595 review finding A): directFileFragmentRows and
  # readOnlyReachableFragmentRows' exclusion list used to spell "is this a
  # FILER_FILE_DIRECT*-gated row" two different ways -- a hasInfix substring
  # check there, three hand-typed gate-name equality checks here -- so a
  # future FILER_FILE_DIRECT_GITLAB (or similar) gate added only to
  # lib/fragments.nix would be picked up by the former but silently miss the
  # latter. Builds a real harness with a synthetic FILER_FILE_DIRECT_GITLAB
  # row appended to the real fragment registry and reads back
  # `internals.directFileFragmentRows`/`internals.readOnlyReachableFragmentRows`
  # (the harness' own computed values, not a reimplementation of the
  # predicate) so a future re-drift in lib/mkHarness.nix itself is caught
  # here, not just in a parallel copy of the logic. The synthetic row points
  # at skill-preamble.md (inert content, no forbiddenMarkers substring)
  # rather than a real filer-file-direct*.md fragment, so a leak is caught
  # by this check's own assertion instead of surfacing indirectly as an
  # unrelated-looking forbiddenMarkerCheckOk build failure. Against the
  # pre-fix two-spellings code this went red: the equality-list spelling in
  # readOnlyReachableFragmentRows did not know the string
  # "FILER_FILE_DIRECT_GITLAB", so the synthetic row stayed in
  # readOnlyReachableFragmentRows instead of being excluded.
  mkharness-read-only-reachable-fragment-rows-excludes-hypothetical-direct-file-gate =
    let
      inherit (pkgs.lib) assertMsg;
      syntheticGate = "FILER_FILE_DIRECT_GITLAB";
      syntheticRow = {
        gate = syntheticGate;
        fragment = "skill-preamble.md";
        var = "FILER_FILE_DIRECT_GITLAB_STEP";
      };
      testHarness = import ../../lib/mkHarness.nix {
        inherit nixpkgs system;
        packages = p: [ p.hello ];
        fragments = (import ../../lib/fragments.nix) ++ [ syntheticRow ];
      };
      directRows = testHarness.internals.directFileFragmentRows;
      readOnlyReachableRows = testHarness.internals.readOnlyReachableFragmentRows;
      leakedIntoReadOnlyReachable = builtins.filter (
        row: row.gate == syntheticGate
      ) readOnlyReachableRows;
    in
    assert assertMsg (builtins.any (row: row.gate == syntheticGate) directRows)
      "directFileFragmentRows must pick up a synthetic ${syntheticGate}-gated row via the shared hasInfix \"FILER_FILE_DIRECT\" predicate -- fixture is broken";
    assert assertMsg (leakedIntoReadOnlyReachable == [ ])
      "readOnlyReachableFragmentRows must exclude a synthetic ${syntheticGate}-gated row the same way it excludes FILER_FILE_DIRECT_GH/_FORGEJO/_ANY (lib/mkHarness.nix's isDirectFileGate predicate), but it leaked through";
    pkgs.runCommand "mkharness-read-only-reachable-fragment-rows-excludes-hypothetical-direct-file-gate"
      { }
      "touch $out";

  # Anti-drift check for lib/mkHarness.nix's researchPromptContentByName
  # (issue #2595 review finding B): that map hand-keys exactly the research
  # prompt names its build-time direct-file scan (researchDirectFileViolations)
  # covers -- a future third templates/default/prompts/research*-prompt.md
  # template would silently miss the scan unless someone also adds a row to
  # researchPromptContentByName. Reads the real, on-disk prompt directory
  # (not a hand-typed second copy of the file list) and compares it against
  # `internals.researchPromptContentByName`'s own keys (the harness' real
  # computed value, not a reimplementation), so this fails loudly the moment
  # the two disagree in either direction.
  mkharness-research-prompt-content-by-name-covers-every-research-prompt-file =
    let
      inherit (pkgs.lib)
        assertMsg
        concatStringsSep
        filterAttrs
        hasPrefix
        hasSuffix
        ;
      promptsDir = ../../templates/default/prompts;
      dirEntries = builtins.readDir promptsDir;
      onDiskResearchPromptFiles = builtins.attrNames (
        filterAttrs (
          name: type: type == "regular" && hasPrefix "research" name && hasSuffix "-prompt.md" name
        ) dirEntries
      );
      coveredNames = builtins.attrNames harness.internals.researchPromptContentByName;
      sort = builtins.sort (a: b: a < b);
    in
    assert assertMsg (onDiskResearchPromptFiles != [ ])
      "mkharness-research-prompt-content-by-name-covers-every-research-prompt-file: expected at least one research*-prompt.md file under templates/default/prompts/, got none -- fixture is vacuous";
    assert assertMsg (sort coveredNames == sort onDiskResearchPromptFiles)
      "lib/mkHarness.nix's researchPromptContentByName must cover exactly every research*-prompt.md file under templates/default/prompts/ (issue #2595 review finding B); on disk: [${concatStringsSep ", " (sort onDiskResearchPromptFiles)}], covered: [${concatStringsSep ", " (sort coveredNames)}]";
    pkgs.runCommand "mkharness-research-prompt-content-by-name-covers-every-research-prompt-file" { }
      "touch $out";
}
