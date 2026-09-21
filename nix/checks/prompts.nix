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

  # Caveman-coverage registry (issue #2709): one row per top-level prompt
  # template, declaring "covered" or "exempt". Hoisted here so every
  # caveman-coverage-* check below shares one import.
  cavemanCoverageRegistry = import ../../lib/prompt-coverage.nix;

  # The marker registries (validateMarkers, workerForbiddenMarkers), so
  # requiredMarkerNames below derives from them instead of retyping their
  # marker literals.
  promptContract = import ../../lib/prompt-contract.nix;

  # The Conditional fragment registry, so the caveman fragment file list
  # below derives from its `gate == "CAVEMAN_BAKED"` rows instead of
  # retyping the caveman-default*.md paths.
  fragmentsRegistry = import ../../lib/fragments.nix;

  # Derived, not hand-typed: a marker row added to either registry later is
  # picked up automatically, so a marker no caveman fragment names fails the
  # check below. SPINDRIFT_ISSUE_INTENT stays in the union: caveman-covered
  # issue-prompt.md injects file-issues-relay.md, whose text names it
  # (issue #2709 review finding).
  requiredMarkerNames =
    (map (r: r.marker) promptContract.validateMarkers)
    ++ (map (r: r.marker) promptContract.workerForbiddenMarkers);

  # The rendered CHECK section, sliced once here rather than once per check
  # (issue #781), so a marker rename lands in one place. Anchored on end of
  # line, not start (issue #3221): "# CHECK" now trails the IMPLEMENT phase's
  # own variable run (${PRINCIPLE_LAZINESS_PROTOCOL_STEP}# CHECK) in the raw
  # template, so a start-anchored match would silently capture nothing
  # (issues #3221, #3505).
  checkSectionSlices = pkgs.runCommand "check-section-slices" { } ''
    mkdir -p $out
    awk '/# CHECK$/{f=1} /# REVIEW$/{exit} f' \
      ${batsHarness.internals.promptDir}/issue-prompt.md > $out/issue-check.txt
  '';

  # The harness-owned skill body the CHECK section's guidance moved into
  # (issue #3220). The source SKILL.md, since lib/image.nix bakes this same
  # file verbatim.
  checkHygieneSkill = ../../templates/default/skills/check-hygiene/SKILL.md;

  # The dogfood-only skill body the CHECK section's Nix lore moved into
  # (issue #3223). The repo-root source, not a harness-baked copy:
  # nix-checks is not harnessOwned, so lib/image.nix never bakes it.
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

  # The anchors for the three pstack-derived principle skills, same
  # bakedness-gated shape as the two above: the rendered slices carry only the
  # ${..._STEP} placeholder, so each anchor's prose is pinned on its fragment.
  principleFixRootCausesAnchor = ../../templates/default/prompts/fragments/principle-fix-root-causes-default.md;
  principleLazinessProtocolAnchor = ../../templates/default/prompts/fragments/principle-laziness-protocol-default.md;
  principleRedesignFromFirstPrinciplesAnchor = ../../templates/default/prompts/fragments/principle-redesign-from-first-principles-default.md;

  # Issue #3419 (worker-prompt.md), extended by #3505 to the three
  # coordinator prompts: the skill body every inlined copy is pinned
  # against, so a reworded skill fails the inline-copy check below instead
  # of silently drifting from it.
  codeCommentsSkillSource = ../../templates/default/skills/code-comments/SKILL.md;

  # Issue #3268: the harness-owned auto-lint skill, so
  # auto-lint-skill-keeps-nix-wording can pin its deliberate Nix mention.
  # The source SKILL.md, since lib/image.nix bakes this same file verbatim.
  autoLintSkill = ../../templates/default/skills/auto-lint/SKILL.md;

  # Broken fixture shared by both build-time-reject-research-verdict-comment-
  # relay-* checks below (issue #2250, parent #2244): the whole fragments
  # directory copied from the real templates tree, so every other fragment is
  # still present, but with research-verdict-github-readonly.md swapped for a
  # copy missing the required SPINDRIFT_COMMENT marker.
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
  # parent #2498 campaign R): same shape as
  # brokenResearchVerdictFragmentsDir above, but with auto-format.md (gated
  # on the plain, non-exempt "AUTO_FORMAT" gate) carrying the forbidden-marker
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
  # forbidden-gh-api-mutation row's marker text ("gh api").
  # buildTimeForbiddenMarkerViolations scans only kind == "substring" rows
  # (readonlyguards.go enforces that row), so this must NOT throw.
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

  # Clean placeholder carrying none of the forbiddenMarkers substrings (issue
  # #2510). The forbidden-marker checks below override `prompt`/`filerPrompt`
  # with it wherever the check isn't exercising that param, so a check over
  # the fragment scan stays isolated from the real templates and a future
  # template edit can't silently confound it.
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

  # Issue #3228: several pinned review-prompt clauses wrap across source
  # lines, which a raw `grep -qF` cannot see past. Match against a
  # whitespace-normalized copy instead, the same treatment
  # normalizeWhitespace gives these clauses in review_prompt_content_test.go:
  # where the prose happens to wrap is not the contract.
  normalizedGrep = ''
    normalized_grep() {
      tr -s '[:space:]' ' ' <"$1" | grep -qF "$2"
    }
  '';

  # Issue #3228: shared with the -not-in-gated-arms companion below so the
  # presence pin and the negative check cannot drift apart. A clause added to
  # only one side would either go unpinned or leave the negative check blind
  # to a regrown copy.
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
  # The configured `prompt` is rendered to a store-path directory and, by
  # default, baked into the image (see agentFiles) rather than mounted; `run`
  # only bind-mounts a dir under the SPINDRIFT_PROMPT_DIR override.
  # Eval/native only: the rendered prompt dir is a host store path, and
  # prompt-baked-into-image below checks the image bake Linux-side.
  mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
    # The Consumer's prompt text is what lands in the rendered file.
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.internals.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still
  # ship an agent that emits the outcome line, so the launcher can learn the
  # PR (issue #419). The harness appends the canonical contract exactly once.
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
  # on disk: injection must not touch a prompt that already has the contract
  # (issue #419).
  mkharness-prompt-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/issue-prompt.md} ${batsHarness.internals.promptDir}/issue-prompt.md
        touch $out
      '';

  # The block injected into a prompt lacking the contract must be
  # byte-identical to the default prompt's own contract section. Both are
  # sliced from the same marker in the same source file, so they cannot
  # drift apart (issue #419).
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.internals.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.internals.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # Pins the literal `landing=` token (issue #654): the no-drift check above
  # diffs two slices of one source, so a revert to the pre-#638 `pr=` grammar
  # passes it. Anchored to the SPINDRIFT_OUTCOME line and counted per line
  # (issues #886, #887), since one surviving example masks a partial revert;
  # scoped to `note=` lines, which #3224's bare counter-examples lack.
  mkharness-prompt-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-outcome-contract-has-landing-token" { }
      ''
        # Floor guard: catches the degenerate case where every SPINDRIFT_OUTCOME
        # line -- and thus landing= itself -- vanishes from the contract, which
        # the per-line count below would otherwise wave through as 0 missing.
        grep -qE 'SPINDRIFT_OUTCOME.*landing=' ${batsHarness.internals.outcomeContractFile}
        missing=$(grep 'SPINDRIFT_OUTCOME' ${batsHarness.internals.outcomeContractFile} | grep 'note=' | grep -vc 'landing=' || true)
        [ "$missing" -eq 0 ] || {
          echo "expected every SPINDRIFT_OUTCOME line to carry landing=, $missing did not" >&2
          exit 1
        }
        touch $out
      '';

  # The #1582 dogfood run printed SPINDRIFT_OUTCOME backtick-wrapped and the
  # extractor's anchored grep missed it, because the contract only showed the
  # line inside a fenced example and never said the driver's own output must
  # be raw text (issue #1612). The {0,60} window holds the current separators
  # plus one wrapped line; widen it if a rewrap pushes the phrase further.
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

  # A Consumer fixPrompt carrying only a fix-specific preamble, with no
  # shared-block markers at all, must still gain all three in COMMS, CHECK,
  # outcome-contract order: the same #420 runtime-override parity the issue
  # prompt has. agent/entrypoint.sh's own runtime injection is covered by
  # tests/entrypoint-outcome-contract.bats.
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
  # canonical sections mkHarness slices them from, so fix-prompt.md and
  # issue-prompt.md cannot drift apart (issue #455). CODE COMMENTS dropped
  # out of this pair (issue #3221) and, since #3505, is inlined verbatim in
  # each prompt rather than sliced, so there is nothing left to diff here.
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

  # The CHECK-phase never-background / emit-outcome guardrail (issue #592).
  # Both greps are scoped to issue-prompt's CHECK section, not the whole
  # file: OUTCOME carries its own "Do NOT run" phrasing further down, so an
  # unscoped grep would keep passing with the #592 paragraph deleted. The
  # fix-prompt side rides the no-drift diff above (issue #1009).
  mkharness-prompt-check-never-background =
    pkgs.runCommand "mkharness-prompt-check-never-background" { }
      ''
        grep -q 'never background it' ${checkSectionSlices}/issue-check.txt
        grep -q 'SPINDRIFT_OUTCOME' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Issue #3220 moved the foreground-gate guidance into the check-hygiene
  # skill but kept the terminal-outcome mandate inline: the dispatcher parses
  # the SPINDRIFT_OUTCOME line, so that contract must hold even on a run
  # where the agent never invokes the skill. Pinned separately, because the
  # never-background greps above pass on the anchor prose alone.
  mkharness-prompt-check-terminal-outcome-inline =
    pkgs.runCommand "mkharness-prompt-check-terminal-outcome-inline" { }
      ''
        grep -qi 'do not stop this run' ${checkSectionSlices}/issue-check.txt
        grep -q 'status=blocked' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The fallback for an agent that backgrounds a check gate anyway (issue
  # #713): a build killed outright (OOM, SIGKILL) never writes the exit
  # marker a background+poll loop waits on, so the wait must be bounded and a
  # vanished marker treated as failure, not still-pending. Issue #3220 moved
  # this guidance into the check-hygiene skill, which is what this now pins.
  check-hygiene-skill-vanished-marker-is-failure =
    pkgs.runCommand "check-hygiene-skill-vanished-marker-is-failure" { }
      ''
        grep -qi 'vanished' ${checkHygieneSkill}
        grep -qi 'exit marker' ${checkHygieneSkill}
        touch $out
      '';

  # Issue #3220: the reduction only holds if the CHECK section still points
  # at the skill the guidance moved into; an anchorless CHECK leaves every
  # pin above green while the agent never reads the body. Two halves, since
  # the anchor is a bakedness-gated fragment: the CHECK section references
  # the fragment's variable, and the fragment carries the anchor prose.
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

  # Same anchor-presence pin for /principle-fix-root-causes, which renders in
  # two places: the worker path's CHECK section and the warm fix pass. The fix
  # pass is the load-bearing half -- it exists only because a check went red,
  # which is where a nil check or loosened assertion buys green CI and buries
  # the bug.
  mkharness-prompt-principle-fix-root-causes-skill-anchor =
    pkgs.runCommand "mkharness-prompt-principle-fix-root-causes-skill-anchor" { }
      ''
        grep -qF 'PRINCIPLE_FIX_ROOT_CAUSES_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF 'PRINCIPLE_FIX_ROOT_CAUSES_STEP' ${batsHarness.internals.promptDir}/fix-prompt.md
        grep -qF '/principle-fix-root-causes' ${principleFixRootCausesAnchor}
        touch $out
      '';

  # The other two anchor IMPLEMENT only. The last line pins an *absence*:
  # fix-prompt.md step 2 says not to redesign, so an anchor there would
  # contradict the prompt it renders in, and a later "add it everywhere" edit
  # is how that regression arrives.
  mkharness-prompt-principle-implement-skill-anchors =
    pkgs.runCommand "mkharness-prompt-principle-implement-skill-anchors" { }
      ''
        grep -qF 'PRINCIPLE_LAZINESS_PROTOCOL_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF 'PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_STEP' ${checkSectionSlices}/issue-check.txt
        grep -qF '/principle-laziness-protocol' ${principleLazinessProtocolAnchor}
        grep -qF '/principle-redesign-from-first-principles' ${principleRedesignFromFirstPrinciplesAnchor}
        ! grep -qF 'PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_STEP' \
          ${batsHarness.internals.promptDir}/fix-prompt.md
        touch $out
      '';

  # Issue #3419: a worker never writes a commit message, the coordinator owns
  # COMMIT, so caveman-default-worker.md must carry only the narrowed
  # code/commands/error-messages exemption. Pinned on the raw fragment
  # template, since its own source text is what must never regrow the clause;
  # presence half plus absence half.
  caveman-default-worker-no-commit-message =
    pkgs.runCommand "caveman-default-worker-no-commit-message" { }
      ''
        p=${../../templates/default/prompts/fragments/caveman-default-worker.md}
        grep -qF '/caveman' "$p"
        grep -qF 'Code, commands, and error messages are exempt and stay verbatim.' "$p"
        ! grep -qi 'commit message' "$p"
        touch $out
      '';

  # Issue #3419 (worker-prompt.md) and #3505 (the three coordinator prompts):
  # every prompt inlines the code-comments policy verbatim rather than
  # carrying the removed ${CODE_COMMENTS_STEP} anchor / fragment, so one
  # derivation loops over all four templates instead of duplicating the same
  # derivation four times. Compares whitespace-normalized text, latched at
  # the second `---` and tested with a literal `grep -F`, so a reworded
  # skill fails here rather than drifting silently.
  prompt-code-comments-inlined = pkgs.runCommand "prompt-code-comments-inlined" { } ''
    skill=${codeCommentsSkillSource}
    policy=$(awk 'seen >= 2 { print } /^---$/ && seen < 2 { seen++ }' "$skill" \
      | tr -s '[:space:]' ' ' | sed -e 's/^ *//' -e 's/ *$//')
    [ -n "$policy" ] || {
      echo "SKILL.md yielded an empty policy body -- missing its second '---' frontmatter delimiter?" >&2
      exit 1
    }
    for p in \
      ${../../templates/default/prompts/worker-prompt.md} \
      ${../../templates/default/prompts/issue-prompt.md} \
      ${../../templates/default/prompts/fix-prompt.md} \
      ${../../templates/default/prompts/conflict-resolve-prompt.md} \
    ; do
      # Store paths arrive hash-prefixed; strip it so a failure names the
      # template a reader can actually open.
      name=$(basename "$p" | cut -d- -f2-)
      tr -s '[:space:]' ' ' <"$p" | grep -qF -- "$policy" || {
        echo "$name is missing the code-comments policy body inlined verbatim (issues #3419, #3505)" >&2
        exit 1
      }
      ! grep -qF 'CODE_COMMENTS_STEP' "$p" || {
        echo "$name still references the removed CODE_COMMENTS_STEP anchor variable (issue #3505)" >&2
        exit 1
      }
      ! grep -qF '/code-comments' "$p" || {
        echo "$name still references /code-comments -- the policy is inlined now, not a skill invocation (issue #3505)" >&2
        exit 1
      }
      ! grep -qF '# CODE COMMENTS' "$p" || {
        echo "$name has a regrown '# CODE COMMENTS' heading -- the policy is inlined body text now, not a separate section (issue #3505)" >&2
        exit 1
      }
    done
    touch $out
  '';

  # Issue #3226: the review prompt's four hunt dimensions and the
  # reconcile-into-Blocking/Non-blocking obligation must render on every run,
  # baked or unbaked; the baked arm defers to a pinned upstream skill
  # spindrift cannot edit. Pinned on the raw template, since mkHarness leaves
  # CODE_REVIEW_BAKED/UNBAKED for entrypoint.sh to resolve at runtime.
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
  # CODE_REVIEW_BAKED/UNBAKED fragment pair must NOT restate the dimensions.
  # A regrown copy in either arm would defeat the move to the always-rendered
  # tier while leaving the presence pin above green.
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
  # sentence and the four trace obligations are the same always-inline
  # contract as the hunt dimensions above; gating either behind
  # CODE_REVIEW_BAKED/UNBAKED would make it vanish on exactly the baked runs
  # that defer to the pinned upstream skill.
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
  # above: the gated CODE_REVIEW_BAKED/UNBAKED pair must NOT restate the
  # ordering rule or any trace obligation. A regrown copy in either arm would
  # defeat the move while leaving the presence pin above green.
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

  # Issue #3228: the Blocking one-line-failure-scenario requirement and the
  # APPROVE probed section are the same always-inline contract as the hunt
  # dimensions and trace obligations above; gating either behind
  # CODE_REVIEW_BAKED/UNBAKED would make it vanish on exactly the baked runs
  # that defer to the pinned upstream skill.
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
  # above: the gated CODE_REVIEW_BAKED/UNBAKED pair must NOT restate the
  # failure-scenario rule or the Probed section. A regrown copy in either arm
  # would defeat the move while leaving the presence pin above green.
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
  # creates a file and runs `nix build` before staging it hits a spurious
  # "not tracked by Git" failure and burns a checks cycle. Issue #3223 moved
  # this guidance into the dogfood-only nix-checks skill, so it is pinned
  # there; the fix-prompt side rides the no-drift diff (issue #1009).
  mkharness-prompt-check-git-add-before-nix-build =
    pkgs.runCommand "mkharness-prompt-check-git-add-before-nix-build" { }
      ''
        grep -qi 'git add' ${nixChecksSkill}
        grep -qi 'tracked by' ${nixChecksSkill}
        touch $out
      '';

  # Issue #1990: the agent must not regrow the manual output-routing advice
  # the bash-output interceptor (#1988) now handles, and must keep the
  # explicit no-cat-a-whole-log rule. Issue #3220 moved that rule into the
  # check-hygiene skill body; its scoped-check-target sibling below moved
  # into the nix-checks skill instead (issue #3223).
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

  # Issue #3215: the no-cat-log discipline above covers build/test logs but
  # not diffs, and a bare `git diff` hits the same tool-result truncation
  # cap, so the CHECK section must extend file-then-grep to diffs. The second
  # pin anchors on the whole "`--stat` first for shape" phrase: a bare
  # `--stat` would hollow out silently on any future unrelated mention.
  mkharness-prompt-check-diff-redirect-discipline =
    pkgs.runCommand "mkharness-prompt-check-diff-redirect-discipline" { }
      ''
        grep -qi 'never stream a bare `git diff`' ${checkSectionSlices}/issue-check.txt
        grep -qi -- '`--stat` first for shape' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Issue #2377: the scoped-check-target steering must be a firm rule, not a
  # soft preference: an explicit prohibition on the full `nix flake check`
  # in-box that overrides looser issue acceptance criteria, with the one
  # legitimate exception (the diff touches what is baked into the image)
  # named by file. Issue #3223 moved it into the nix-checks skill.
  mkharness-prompt-check-full-flake-check-firm-rule =
    pkgs.runCommand "mkharness-prompt-check-full-flake-check-firm-rule" { }
      ''
        grep -Pzqi \
          '(?s)(do not|must not) run.{0,80}full.{0,80}nix flake check.{0,300}(nix/checks/image\.nix|lib/image\.nix)' \
          ${nixChecksSkill}
        touch $out
      '';

  # Issue #3448: without `-L`/`--print-build-logs`, a failed check prints only
  # the store path and a `[build failed]` line, so the agent burns a second
  # turn re-running `nix log` to see the actual compile/test error.
  nix-checks-skill-surfaces-build-log = pkgs.runCommand "nix-checks-skill-surfaces-build-log" { } ''
    grep -qi 'print-build-logs' ${nixChecksSkill} || {
      echo "nix-checks skill missing 'print-build-logs' -- the -L flag guidance" >&2
      exit 1
    }
    grep -qi 'build failed' ${nixChecksSkill} || {
      echo "nix-checks skill missing 'build failed' -- the without-\`-L\` failure mode it should warn about" >&2
      exit 1
    }
    touch $out
  '';

  # Issue #3448: the Box's baked nix.conf pins `cores = N` and one derivation
  # at a time is Nix's own default, so the skill must tell the agent not to
  # hand-tune `-j`/`--max-jobs`/`--cores` outside the one `EXIT:137` carve-out
  # the skill itself fences (issue #3452). The pin here is value-agnostic:
  # the value belongs to nix-checks-lore-cores-matches-nix-conf, so the two
  # checks never give contradictory remedies on a cores bump; the carve-out
  # itself is pinned by nix-checks-lore-parity-clause-oom-cores-carve-out.
  nix-checks-skill-no-resource-flag-tuning =
    pkgs.runCommand "nix-checks-skill-no-resource-flag-tuning" { }
      ''
        grep -qi -- '--max-jobs' ${nixChecksSkill} || {
          echo "nix-checks skill missing '--max-jobs' -- the resource flag it should forbid hand-tuning" >&2
          exit 1
        }
        grep -qiE 'cores = [0-9]+' ${nixChecksSkill} || {
          echo "nix-checks skill missing a 'cores = <N>' pin -- see nix-checks-lore-cores-matches-nix-conf for the value" >&2
          exit 1
        }
        touch $out
      '';

  # Issue #3448: a check failure is deterministic, since the same derivation
  # fails the same way regardless of scheduling, so the skill must forbid
  # re-running a failed check unchanged (a multi-minute build for a foregone
  # conclusion).
  nix-checks-skill-failure-is-deterministic =
    pkgs.runCommand "nix-checks-skill-failure-is-deterministic" { }
      ''
        grep -qi 'deterministic' ${nixChecksSkill} || {
          echo "nix-checks skill missing 'deterministic' -- the property a failed check has" >&2
          exit 1
        }
        grep -qi 'never re-run a failed check unchanged' ${nixChecksSkill} || {
          echo "nix-checks skill missing 'never re-run a failed check unchanged'" >&2
          exit 1
        }
        touch $out
      '';

  # Issue #3223: the CHECK section is ecosystem-neutral now that the Nix lore
  # lives in the dogfood-only /nix-checks skill instead. A regrown inline
  # mention of any of these terms would defeat the move while every anchor
  # and skill-body pin above stayed green.
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

  # Issue #3268: the CHECK-section ban above (#3223) is scoped to
  # issue-check.txt only -- it never touched skills/*. The harness-owned
  # auto-lint skill bakes into every image unconditionally (not gated by a
  # Consumer's skills list) and its flake/devShell mention is a deliberate,
  # ecosystem-agnostic guardrail, not #3223 residue. Pin that it stays.
  auto-lint-skill-keeps-nix-wording = pkgs.runCommand "auto-lint-skill-keeps-nix-wording" { } ''
    grep -qi 'nix flake or devshell' ${autoLintSkill} || {
      echo "auto-lint skill missing its deliberate, ecosystem-agnostic Nix flake/devShell mention (issue #3268)" >&2
      exit 1
    }
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
  # an agent that posts the verdict comment and emits the outcome line. The
  # harness appends the canonical contract exactly once.
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
  # VERDICT section from the configured verdict set, so there is no
  # byte-identical-to-template no-op case left. A byte diff rather than
  # presence greps, so prose deleted, duplicated or reordered by a rendering
  # regression fails loudly instead of slipping past.
  mkharness-prompt-research-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-prompt.md > rendered.txt
        diff -u ${researchVerdictDefaultFixture} rendered.txt \
          || { echo "default research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # Companion for the self-contained sub-mode prompt (ADR 0022, issue #2202):
  # no check previously read its rendered VERDICT section, so the render-time
  # deletion of its "Judge relevance..." sentence, the one line that
  # distinguishes the sub-mode, went uncaught. Pins that the sentence
  # survives rendering ahead of the registry-generated bullets.
  mkharness-prompt-research-self-contained-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-self-contained-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-self-contained-prompt.md > rendered.txt
        diff -u ${researchVerdictSelfContainedFixture} rendered.txt \
          || { echo "self-contained research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # The two checks above inspect only the VERDICT..POST THE VERDICT span, so
  # an unresolved enumMarker, which sits after the `# POST THE VERDICT`
  # heading, would stay invisible to them. Scan the whole baked file for both
  # markers, neither of which may survive rendering, and pin that the default
  # set's resolved backtick enumeration is present.
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
  # byte-identical to the default research prompt's own contract section,
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
  # hand-maintained: injectResearchOutcomeContract no-ops on both source
  # templates because each already owns the marker, so nothing structurally
  # pins the self-contained copy to research-prompt.md. This diffs the two
  # tails to catch silent drift (issue #2230, found during #2202).
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

  # A Consumer researchPrompt carrying only a research-specific preamble,
  # with no "# POST THE VERDICT" marker at all, must still gain the contract
  # and survive the round trip byte-identical to what a SPINDRIFT_PROMPT_DIR
  # override receives (issue #640). agent/entrypoint.sh's own runtime
  # injection is covered by tests/entrypoint-research-kind.bats.
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

  # Grep pin (issue #1653): the launcher gates on CI green itself before
  # flipping the PR ready and merging (issue #1651), so the WATCH CI GraphQL
  # query must not appear in any prompt source file. fix-prompt.md
  # legitimately references the unrelated `statusCheckRollup` JSON field, so
  # the query body, not the field name, is the pin.
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
  # amendment): only the canonical `local ORCHESTRATOR=` computation in
  # agent/entrypoint.sh may test the raw ORCHESTRATOR_ENABLED env var, and
  # every fork downstream reads that computed gate. Each $ORCHESTRATOR
  # conditional needs an explicit else, so a one-sided segment fails here.
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

  # Grep pin (issue #908): the filer's dedup step must search open issues
  # beyond the `agent-review-finding` label, or it stops catching human-filed
  # duplicates. Re-adding a `--label` flag to the `--state open` line slips
  # past both other pins (issue #921), so count that line's `--label`
  # occurrences too. `[ "$n" -eq 0 ] ||`, since `set -e` exempts `!` (#887).
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

  # Grep pin (issue #781): the CHECK-section awk slice must be defined once,
  # not copy-pasted; a marker rename applied to one copy would leave the
  # other checks silently reading stale content. Extended (issue #1154) to
  # the fix-prompt half of the same pattern, which only
  # mkharness-prompt-fix-check-no-drift uses.
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

  # Grep pin (issue #908): the filer's dedup must also treat closed
  # `agent-research-reject` issues as suppressing matches, the same triage
  # class as a closed `agent-review-finding`. Anchored to the full
  # `--label ... --state closed` command, since a bare-token match would pass
  # on an unrelated prose mention (issue #922, sibling of #921 above).
  filer-prompt-dedup-names-research-reject =
    pkgs.runCommand "filer-prompt-dedup-names-research-reject" { }
      ''
        grep -q -- '--label agent-research-reject --state closed' ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';

  # Grep pin (issue #3226 slice 3): the filer's issue-authoring obligations
  # (work-path backlink shape, no provenance line on a research-path issue,
  # never the dispatch label) are contract, not coaching. Each grep anchors on
  # the literal sentence, so an edit that keeps "provenance" but drops the
  # rule still fails. Rule 2's sentence wraps, so grep needs two lines for it.
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

  # The PR-body ticket-reference toggle (issue #1429, ADR 0029): each of the
  # three PR_BODY_* fragments is unconditional prose for one case, since
  # agent/entrypoint.sh picks exactly one gate per run, so a static grep on
  # the fragment source pins each case. The runtime gate selection needs a
  # live entrypoint.sh run and lives in tests/entrypoint-prompt-fragments.bats.
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

  # The issue-read step no longer reads the subject issue (issue #3445): the
  # host injects its body and comments at dispatch, so a fragment that still
  # fetches them burns a tracker call and pushes run-varying bytes into the
  # stable prefix. Subsumes ADR 0032's #1691 ban, the fetch-shape pins and
  # #1990's comment cap; globbed, so a fragment added later is covered.
  issue-read-fragments-never-fetch-the-subject-issue =
    pkgs.runCommand "issue-read-fragments-never-fetch-the-subject-issue" { }
      ''
        shopt -s nullglob
        for f in ${../../templates/default/prompts/fragments}/*issue-read-*.md; do
          ! grep -q 'gh issue view' "$f" || {
            echo "$f: expected no 'gh issue view' (subject-issue fetch), found one" >&2
            exit 1
          }
          ! grep -q 'fj issue view' "$f" || {
            echo "$f: expected no 'fj issue view' (subject-issue fetch), found one" >&2
            exit 1
          }
          ! grep -qF '/issues/''${ISSUE_NUMBER}.md' "$f" || {
            echo "$f: expected no /issues/ISSUE_NUMBER.md read (subject-issue fetch), found one" >&2
            exit 1
          }
        done
        touch $out
      '';

  # The /issues host mount is removed (issue #3471): local issue content
  # arrives as host-injected ISSUE_TEXT, so a template naming /issues is that
  # directory-read guidance creeping back. `sed` strips the one legitimate
  # api/v1 URL from the line rather than dropping the line whole; nothing
  # here is `grep -q`, whose SIGPIPE under pipefail makes `!` a false pass.
  prompt-templates-never-name-issues-mount =
    pkgs.runCommand "prompt-templates-never-name-issues-mount" { }
      ''
        shopt -s nullglob
        dir=${../../templates/default/prompts}
        n=0
        for f in "$dir"/*.md "$dir"/fragments/*.md; do
          n=$((n + 1))
          hits=$(grep -n '/issues' "$f" | sed 's|api/v1[^ ]*issues||g' | grep '/issues' || true)
          [ -z "$hits" ] || {
            echo "$f: expected no /issues (removed host mount) outside an api/v1 URL, found:" >&2
            echo "$hits" >&2
            exit 1
          }
        done
        [ "$n" -gt 0 ] || {
          echo "expected at least one prompt template or fragment under templates/default/prompts, found none" >&2
          exit 1
        }
        touch $out
      '';

  # The read-write write-step fragments (issue #1917) must keep
  # `gh issue comment` byte-for-byte: the same in-box write these two steps
  # always rendered before BOX_FORGE_AND_ISSUE_ACCESS existed.
  github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged =
    pkgs.runCommand "github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged" { }
      ''
        for f in issue-blocked-comment-github.md research-verdict-github.md; do
          grep -q 'gh issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The read-only counterpart (issue #1917): a read-only Box holds no write
  # token, so its blocked-note and verdict-comment fragments never invoke
  # `gh issue comment`. They relay through the host instead: note= on
  # SPINDRIFT_OUTCOME, and one nonce-guarded SPINDRIFT_COMMENT line (issue
  # #1940 chose that grammar so it survives a stream-json box log).
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
  # `fj issue comment`.
  forgejo-readwrite-comment-fragments-keep-fj-issue-comment =
    pkgs.runCommand "forgejo-readwrite-comment-fragments-keep-fj-issue-comment" { }
      ''
        for f in issue-blocked-comment-forgejo.md research-verdict-forgejo.md; do
          grep -q 'fj issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side counterpart of github-readonly-comment-fragments-* above
  # (issue #1963): a read-only Box holds no write-capable FORGEJO_TOKEN, so
  # these fragments must never invoke `fj issue comment` and must carry the
  # same relay forms (note= field, SPINDRIFT_COMMENT line) as the others.
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
  # must keep `gh label create`/`gh issue create` byte-for-byte, the same
  # in-box writes filer-prompt.md's steps rendered before this split existed.
  filer-direct-fragments-keep-gh-write-unchanged =
    pkgs.runCommand "filer-direct-fragments-keep-gh-write-unchanged" { }
      ''
        grep -q 'gh label create agent-review-finding' ${../../templates/default/prompts/fragments/filer-label-direct.md}
        grep -q 'gh issue create' ${../../templates/default/prompts/fragments/filer-file-direct.md}
        touch $out
      '';

  # The read-only counterpart (issue #2019): a read-only Box under
  # ORCHESTRATOR_ENABLED holds no write token, so the filer's relay fragments
  # must never invoke `gh label create` and must carry the host-mediated
  # SPINDRIFT_ISSUE_INTENT relay instead. `gh issue create`'s absence is
  # already covered by the mkHarness eval assert (issues #2510, #2513).
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
  # --label flag, so the forgejo direct-mode fragments must speak
  # `fj issue create` and the REST API (curl) for the label.
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

  # The OPEN A PULL REQUEST read-write create step forks on CODE_FORGE (issue
  # #1963, OPEN_PR_CREATE_RW_GH/OPEN_PR_CREATE_RW_FORGEJO computed in
  # entrypoint.sh): the github fragment keeps `gh pr create` and never
  # invokes `fj pr create`, and the forgejo fragment the reverse.
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
  # fragment keeps `gh pr view` and never invokes `fj pr status`, and the
  # forgejo fragment the reverse.
  fix-ci-read-fragments-fork-forge = pkgs.runCommand "fix-ci-read-fragments-fork-forge" { } ''
    grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    ! grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    ! grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    touch $out
  '';

  # Build-time reject arm (issue #2250, parent #2244): mkHarness.nix turns the
  # `reviewer-verdict` validateMarkers row into a build-time failure when the
  # orchestrator is statically enabled and reviewPrompt lacks the required
  # `VERDICT:` marker. The broken fixture is built inline, never exported
  # from nix/fixtures.nix, where every other consumer would force it.
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
  # reviewPrompt, but orchestratorEnabled left at its schema default (false).
  # The omission is real but its gating condition is not statically known
  # true, so buildTimeRejectVerdicts resolves "advise" and the build
  # succeeds.
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
  # brokenResearchVerdictFragmentsDir swaps in a
  # research-verdict-github-readonly.md missing the required
  # SPINDRIFT_COMMENT marker, shared by both checks below.
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
  # (read-write). The gating condition is not statically known true, so
  # buildTimeRejectVerdicts resolves "advise" and the build succeeds.
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
  # a forbidden marker authored as literal fragment-body text in a fragment
  # gated on a plain, non-exempt gate must fail the build unconditionally,
  # unlike buildTimeRejectVerdicts above: it is a problem for any Consumer
  # that might configure boxAccessReadOnly, not just this build's own gates.
  build-time-reject-forbidden-marker-fragment =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = brokenForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan: the real
          # filer-prompt.md and issue-prompt.md already carry an unrelated
          # template violation that would pass this assertion for the wrong
          # reason.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when a fragment gated on a plain, non-exempt gate (AUTO_FORMAT) carries a forbidden marker ('git push') as literal fragment-body text";
    pkgs.runCommand "build-time-reject-forbidden-marker-fragment" { } "touch $out";

  # The exempt-gate counterpart: the same forbidden-marker substring, but in
  # a fragment gated on an exempt gate (BOX_ACCESS_READ_ONLY). Many shipped
  # fragments carry forbidden-marker text as a negation ("do NOT git push")
  # because they are the read-only half of an access-mode pair, so the scan
  # must not false-positive on them.
  build-time-forbidden-marker-fragment-exempt-gate-not-triggered =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = exemptGateForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan, as in the sibling
          # build-time-reject-forbidden-marker-fragment check above.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg ok.success
      "mkHarness.nix must not throw when a fragment gated on an exempt gate (BOX_ACCESS_READ_ONLY) carries a forbidden marker as literal fragment-body text";
    pkgs.runCommand "build-time-forbidden-marker-fragment-exempt-gate-not-triggered" { } "touch $out";

  # The gh-api-mutation-kind counterpart (issue #2513): a plain, non-exempt
  # gate carrying the forbidden-gh-api-mutation row's marker ("gh api")
  # instead of a kind == "substring" row's.
  # buildTimeForbiddenMarkerViolations filters to substring rows, so this
  # must NOT throw, proving that filter still excludes the gh-api-mutation row.
  build-time-forbidden-marker-fragment-gh-api-mutation-kind-not-scanned =
    let
      inherit (pkgs.lib) assertMsg;
      ok = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = ghAPIMutationForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan, as in the sibling
          # build-time-reject-forbidden-marker-fragment check above.
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
  # (issue-prompt.md's default) gets no exemption at all, so its raw text is
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
          # the real filer-prompt.md carries an unrelated template violation
          # that would pass this assertion for the wrong reason.
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when the shared `prompt` template carries a forbidden marker ('gh pr create') as literal text";
    pkgs.runCommand "build-time-reject-forbidden-marker-template" { } "touch $out";

  # Same as above, but exercising `reviewPrompt` instead of `prompt`.
  # templateContentByFile's three entries are hand-written attrset keys
  # (lib/mkHarness.nix), so a check that only overrides `prompt` would never
  # notice the `reviewPrompt` (or `filerPrompt`, below) entry being silently
  # dropped or mis-keyed.
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

  # Build-time research-direct-file check (issue #2595, ADR 0041, "Research
  # filing is host-mediated and relay-only"): a research prompt must never
  # statically carry a FILER_FILE_DIRECT*-gated fragment's envsubst
  # placeholder, since research issues are always filed through the
  # SPINDRIFT_ISSUE_INTENT relay, never `gh`/`fj` straight from the agent.
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

  # Same as above for researchSelfContainedPrompt (issue #2202): #2595's
  # acceptance criteria names both research prompt kinds, and the two are
  # separately hand-keyed entries in lib/mkHarness.nix's
  # researchPromptContentByName, so a check that only overrides
  # researchPrompt would never notice the self-contained entry dropped.
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
  # must build clean today, since lib/fragments.nix's DIRECT-gated rows never
  # wire their var into either research prompt template. Proves the current
  # configuration passes, not only the deliberately-broken fixtures above.
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

  # Anti-drift registry check (issue #2709, slice 1): before
  # lib/prompt-coverage.nix existed, caveman coverage was decided by hand per
  # template, so a prompt kind added later would silently default to
  # uncovered. This guards only the registry's completeness against the
  # templates directory, both directions; slices 2 and 3 below check content.
  caveman-coverage-registry-matches-templates-dir =
    let
      inherit (pkgs.lib) concatMapStringsSep;
      # Trailing "\n" for consistency with the sibling list files below,
      # where a `while read` loop would otherwise drop the final line. It is
      # inert here under sort, comm and uniq.
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

  # Row-shape guards (issue #2709): coverage is "covered" or "exempt",
  # cavemanVar required iff covered, reason iff exempt. A typo'd `"Covered"`
  # would drop the row from every filter above and the content checks would
  # pass vacuously. Eval-time asserts, as nix/checks/prompt-contract.nix does
  # for this typo class (issue #2499); `touch $out` only forces them.
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
  # finding on issue #2709): the sibling check below only greps for the
  # literal "${cavemanVar}" text, so a typo'd var could pass by coincidence
  # and still render with no caveman skill baked in. This compares against
  # fragmentsRegistry's CAVEMAN_BAKED rows as data, so the tie is structural.
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

  # Per-row directive check (issue #2709, slice 2): for every "covered" row,
  # assert the assembled prompt carries the literal, unsubstituted envsubst
  # placeholder for its cavemanVar; envsubst runs at container runtime, so
  # the literal ${VAR} text is what is on disk. Reads the assembled dir for
  # every row, since fix-prompt.md's directive exists only post-injection.
  caveman-coverage-covered-templates-carry-directive =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      coveredRows = filter (r: r.coverage == "covered") cavemanCoverageRegistry;
      # Trailing "\n" matters: bash's `while read` skips a final line with no
      # trailing newline, so a bare concatMapStringsSep here would silently
      # drop the last covered row from the scan.
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

  # Exempt-row check (issue #2709, slice 2): every "exempt" row's assembled
  # prompt must carry no case-insensitive "caveman" at all. Currently just
  # filer-prompt.md, which authors issue titles and bodies directly and so
  # must stay human prose end to end. The file list derives from the
  # registry's exempt rows, so a future exempt row needs no second list.
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

  # Ties the caveman narration directive to the marker registries (issue
  # #2709, slice 3): every CAVEMAN_BAKED fragment carries a "machine-parsed
  # marker grammar is exempt too" paragraph, so a marker parsed by code but
  # named in no such fragment risks a Box compressing it into an unparseable
  # line. The fragment list derives from fragmentsRegistry, not a copy.
  caveman-coverage-exemption-list-covers-marker-registry =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      cavemanFragmentsDir = ../../templates/default/prompts/fragments;
      cavemanFragmentRows = filter (r: r.gate == "CAVEMAN_BAKED") fragmentsRegistry;
      cavemanFragmentPaths = map (r: cavemanFragmentsDir + "/${r.fragment}") cavemanFragmentRows;
      # Same trailing-newline guard as coveredRowsFile and exemptFiles above.
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

  # Regression test for lib/mkHarness.nix's isDirectFileGate (issue #2595
  # review finding A): its two call sites once spelled the gate test
  # differently, so a future FILER_FILE_DIRECT_GITLAB would reach one and
  # miss the other. Reads back the harness' own computed rows, and uses the
  # inert skill-preamble.md so a leak fails here, not as a marker error.
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

  # Grep pin (issue #3227): the research prompts' advise-only prohibition
  # enumeration once appeared twice in research-prompt.md, loosely reworded
  # in TASK and in full in the byte-shared POST THE VERDICT trailer. Pins
  # each phrase to exactly one occurrence, the trailer's, which a Consumer
  # with no section of its own still receives; TASK keeps only the posture.
  mkharness-prompt-research-advise-only-enumeration-dedup =
    pkgs.runCommand "mkharness-prompt-research-advise-only-enumeration-dedup" { }
      ''
        p=${batsHarness.internals.promptDir}/research-prompt.md
        # Join hard-wrapped lines before counting: the trailer prose wraps
        # at the terminal width, so "never close the issue" can straddle a
        # line break in the source file even though it reads as one phrase.
        flat=$(tr '\n' ' ' < "$p")
        for phrase in 'never edit the issue body' 'never close the issue' 'never promote it to dispatchable'; do
          count=$(grep -io -- "$phrase" <<<"$flat" | wc -l)
          [ "$count" -eq 1 ] || {
            echo "expected \"$phrase\" exactly once (trailer copy only) in $p, got $count" >&2
            exit 1
          }
        done
        task=$(awk '/^# TASK$/{f=1} /^# CONTEXT$/{exit} f' "$p" | tr '\n' ' ')
        echo "$task" | grep -qi -- 'advise-only' || {
          echo "expected the TASK section of $p to still state the advise-only posture" >&2
          exit 1
        }
        echo "$task" | grep -qF -- 'launcher owns every lifecycle transition' || {
          echo "expected the TASK section of $p to keep the launcher-owns-every-lifecycle-transition clause" >&2
          exit 1
        }
        touch $out
      '';

  # Companion to mkharness-prompt-research-advise-only-enumeration-dedup
  # above, for the self-contained sub-mode prompt (issue #3227).
  mkharness-prompt-research-self-contained-advise-only-enumeration-dedup =
    pkgs.runCommand "mkharness-prompt-research-self-contained-advise-only-enumeration-dedup" { }
      ''
        p=${batsHarness.internals.promptDir}/research-self-contained-prompt.md
        # Join hard-wrapped lines before counting: the trailer prose wraps
        # at the terminal width, so "never close the issue" can straddle a
        # line break in the source file even though it reads as one phrase.
        flat=$(tr '\n' ' ' < "$p")
        for phrase in 'never edit the issue body' 'never close the issue' 'never promote it to dispatchable'; do
          count=$(grep -io -- "$phrase" <<<"$flat" | wc -l)
          [ "$count" -eq 1 ] || {
            echo "expected \"$phrase\" exactly once (trailer copy only) in $p, got $count" >&2
            exit 1
          }
        done
        task=$(awk '/^# TASK$/{f=1} /^# CONTEXT$/{exit} f' "$p" | tr '\n' ' ')
        echo "$task" | grep -qi -- 'advise-only' || {
          echo "expected the TASK section of $p to still state the advise-only posture" >&2
          exit 1
        }
        echo "$task" | grep -qF -- 'launcher owns every lifecycle transition' || {
          echo "expected the TASK section of $p to keep the launcher-owns-every-lifecycle-transition clause" >&2
          exit 1
        }
        touch $out
      '';

  # Anti-drift check for lib/mkHarness.nix's researchPromptContentByName
  # (issue #2595 review finding B): it hand-keys exactly the research prompt
  # names its direct-file scan covers, so a third research*-prompt.md would
  # silently miss the scan. Compares the real on-disk directory against the
  # harness' own computed keys, so either direction fails loudly.
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

  # Issue #3227: research-prompt.md's EXPLORE section has the researcher run
  # the repo's own suite, a check gate that must still end in a printed
  # SPINDRIFT_OUTCOME line, which is what /check-hygiene exists for. Two
  # halves, as in mkharness-prompt-check-hygiene-skill-anchor above; sliced
  # inline, since this is the only consumer.
  mkharness-prompt-research-check-hygiene-skill-anchor =
    pkgs.runCommand "mkharness-prompt-research-check-hygiene-skill-anchor" { }
      ''
        awk '/^# EXPLORE$/{f=1} /^# VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-prompt.md > explore.txt
        grep -qF 'CHECK_HYGIENE_STEP' explore.txt
        grep -qF '/check-hygiene' ${checkHygieneAnchor}
        touch $out
      '';

  # Companion to mkharness-prompt-research-check-hygiene-skill-anchor above
  # (issue #3227): research-self-contained-prompt.md has no repo and nothing
  # to run, so the anchor would be dead text. Pin its absence, so a later
  # copy-paste from research-prompt.md's EXPLORE section cannot quietly
  # regrow it here.
  mkharness-prompt-research-self-contained-no-check-hygiene-anchor =
    pkgs.runCommand "mkharness-prompt-research-self-contained-no-check-hygiene-anchor" { }
      ''
        ! grep -qF 'CHECK_HYGIENE_STEP' ${batsHarness.internals.promptDir}/research-self-contained-prompt.md || {
          echo "research-self-contained-prompt.md must not reference CHECK_HYGIENE_STEP -- it has no repo and nothing to run, so the anchor would be dead text" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #3227): fragments/research-file-issues-relay.md's relay
  # rules are contract, not coaching; the greps' own messages name them. A
  # second half pins the trim itself: the gloss on why this fragment is
  # unconditional explains the fragment registry, not anything a researcher
  # can act on, so it must stay gone.
  research-file-issues-relay-fragment-obligations =
    pkgs.runCommand "research-file-issues-relay-fragment-obligations" { }
      ''
        f=${../../templates/default/prompts/fragments/research-file-issues-relay.md}
        # Join hard-wrapped lines before matching: this fragment's prose
        # wraps at the terminal width, so a phrase like "the filer is
        # relay-only here" can straddle a line break in the source file
        # even though it reads as one clause.
        flat=$(tr '\n' ' ' < "$f")
        grep -qF -- 'Research never writes to the Issue Tracker itself' <<<"$flat" || {
          echo "expected the research-never-writes-to-the-tracker rule in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'the filer is relay-only here' <<<"$flat" || {
          echo "expected the relay-only rule in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'SPINDRIFT_ISSUE_INTENT' <<<"$flat" || {
          echo "expected the SPINDRIFT_ISSUE_INTENT mechanism in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'agent-research-finding' <<<"$flat" || {
          echo "expected the launcher-applied agent-research-finding label in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'Filed from research on #<N>' <<<"$flat" || {
          echo "expected the launcher-applied backlink text in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'no issue URL is known yet' <<<"$flat" || {
          echo "expected the no-issue-URL-known-yet rule in research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'in any mode, read-only or read-write' <<<"$flat" && {
          echo "expected the design-history gloss 'in any mode, read-only or read-write' to be trimmed from research-file-issues-relay.md" >&2
          exit 1
        }
        grep -qF -- 'with no orchestrator condition' <<<"$flat" && {
          echo "expected the design-history gloss 'with no orchestrator condition' to be trimmed from research-file-issues-relay.md" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #3478): commit-unbaked.md must restate both of the
  # subject-limit tiers the upstream /commit skill teaches -- ≤50 preferred,
  # never exceed 72 -- not the flat ≤50 whose lossy paraphrase let the
  # documented and the practiced subject length drift apart. The assembled
  # goldens do embed the sentence, but `nix run .#regen-goldens` rewrites
  # them, so they catch an unregenerated edit rather than a deliberate
  # rewording; only a pin encodes the intent. The clauses are upstream's own
  # phrasing, so re-syncing the fragment verbatim to the skill keeps this
  # green. commit-fragment-parity.nix leaves the column bounds unpinned on
  # both sides; #3486 tracks pinning them there.
  commit-unbaked-fragment-two-tier-subject-limit =
    pkgs.runCommand "commit-unbaked-fragment-two-tier-subject-limit" { }
      ''
        f=${../../templates/default/prompts/fragments/commit-unbaked.md}
        # Join hard-wrapped lines before matching: this fragment's prose wraps
        # at the terminal width, so a tier can straddle a line break.
        flat=$(tr '\n' ' ' < "$f")
        for tier in 'subject ≤50, never exceed 72' 'body ≤72'; do
          grep -qF -- "$tier" <<<"$flat" || {
            echo "expected the wrap tier '$tier' in commit-unbaked.md" >&2
            exit 1
          }
        done
        touch $out
      '';
}
