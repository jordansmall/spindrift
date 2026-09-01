# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules.
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

  # Anti-drift caveman-coverage registry: one row per top-level prompt template,
  # declaring "covered" (with the envsubst variable it must carry) or "exempt"
  # (with a reason). Hoisted so every caveman-coverage-* check shares one import.
  cavemanCoverageRegistry = import ../../lib/prompt-coverage.nix;

  promptContract = import ../../lib/prompt-contract.nix;

  fragmentsRegistry = import ../../lib/fragments.nix;

  # Derived, not hand-typed, so a FUTURE row added to either registry is picked
  # up automatically -- if nobody then names the new marker in at least one
  # caveman fragment, the check below fails.
  #
  # SPINDRIFT_ISSUE_INTENT is deliberately IN this union. Its sole direct
  # carrier, filer-prompt.md, is wholly caveman-exempt, but issue-prompt.md
  # (caveman-*covered*) interpolates FILE_ISSUES_RELAY_STEP, whose fragment text
  # names SPINDRIFT_ISSUE_INTENT -- so a caveman-narrated issue-prompt.md can
  # carry a live SPINDRIFT_ISSUE_INTENT-emitting section.
  requiredMarkerNames =
    (map (r: r.marker) promptContract.validateMarkers)
    ++ (map (r: r.marker) promptContract.workerForbiddenMarkers);

  # The rendered CHECK section, sliced once rather than three times across the
  # never-background/vanished-marker/git-add checks below, so a marker rename
  # only needs updating in one place.
  checkSectionSlices = pkgs.runCommand "check-section-slices" { } ''
    mkdir -p $out
    awk '/^# CHECK$/{f=1} /^# REVIEW$/{exit} f' \
      ${batsHarness.internals.promptDir}/issue-prompt.md > $out/issue-check.txt
  '';

  # Broken fixture for the build-time-reject-research-verdict-comment-relay-*
  # checks: the whole fragments directory cp -r'd from the real templates tree so
  # every other fragment is still present, with research-verdict-github-readonly.md
  # swapped for a copy missing the required SPINDRIFT_COMMENT marker.
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

  # Same shape, for the three forbidden-marker checks: auto-format.md (gated on
  # the plain, non-exempt "AUTO_FORMAT" gate) carries the literal
  # forbidden-marker substring "git push" as authored fragment-body text.
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

  # Exempt-gate counterpart: the broken content goes into open-pr-create-outbox.md,
  # gated on "BOX_ACCESS_READ_ONLY", which mkHarness's
  # readOnlyReachableFragmentRows filter exempts from the scan.
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

  # gh-api-mutation-kind counterpart: same non-exempt gate, but carrying the
  # forbidden-gh-api-mutation row's marker text rather than a kind == "substring"
  # row's. buildTimeForbiddenMarkerViolations only scans kind == "substring"
  # rows -- a "gh-api-mutation" marker is display-only there, enforced instead by
  # readonlyguards.go's command-shim argument scan -- so this must NOT throw.
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

  # Same, for the self-contained sub-mode prompt (ADR 0022): keeps the template's
  # own "Judge relevance..." sentence ahead of the registry-rendered bullets.
  researchVerdictSelfContainedFixture = pkgs.writeText "research-verdict-self-contained-rendered.txt" ''
    # VERDICT

    Judge relevance from the issue content alone — there is no repo to explore.
    Render exactly one of these verdicts:

    - `recommend` — relevant, now enriched with real context; promote it.
    - `reject` — false positive, not worth doing, or a duplicate. Name the duplicate issue by number in your rationale; duplicate is a reason under `reject`, not a separate verdict.
    - `unclear` — relevance can't be determined without a human's answer.

  '';
in
{
  # The configured `prompt` is rendered to a store-path directory and, by
  # default, baked into the image rather than mounted — `run` only bind-mounts a
  # dir under the SPINDRIFT_PROMPT_DIR override. Eval/native only; the image bake
  # is checked Linux-side by prompt-baked-into-image below.
  mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
    # The Consumer's prompt text is what lands in the rendered file.
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.internals.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still ship
  # an agent that emits the outcome line, so the launcher can learn the PR — the
  # harness appends the canonical contract exactly once.
  mkharness-prompt-outcome-injected = pkgs.runCommand "mkharness-prompt-outcome-injected" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${promptHarness.internals.promptDir}/issue-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the outcome contract injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # The default prompt already contains the contract, so injection is a no-op.
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

  # The default box's rendered prompt must be byte-identical to the template on
  # disk — injection must not touch a prompt that already has the contract.
  mkharness-prompt-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/issue-prompt.md} ${batsHarness.internals.promptDir}/issue-prompt.md
        touch $out
      '';

  # The block injected into a prompt lacking the contract must be byte-identical
  # to the default prompt's own contract section — both are sliced from the same
  # marker in the same source file, so they cannot drift apart.
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.internals.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.internals.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # The no-drift check above only proves the injected block matches the
  # *same-source* contract slice; it never asserts the slice says the right
  # thing, so a regression from `landing=` back to the older `pr=` grammar would
  # pass with both sides drifting together. Pin the literal token directly.
  #
  # The token is anchored to the SPINDRIFT_OUTCOME line itself, not `^` (the
  # CODE_FORGE=git example line is indented inside a fenced code block) -- an
  # unanchored grep would pass if the real outcome line regressed while unrelated
  # prose in the slice happened to mention "landing=".
  #
  # Every SPINDRIFT_OUTCOME line must carry it, not just one: a partial
  # regression where a single example line reverts is otherwise masked by the
  # survivors. A bare `! pipeline` won't do -- `set -e` exempts negated commands,
  # so a failing assertion silently wouldn't stop the build.
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

  # A dogfood run once printed SPINDRIFT_OUTCOME backtick-wrapped and the
  # extractor's anchored grep missed it: the contract only ever *showed* the line
  # inside a fenced example, never told the driver its output must be raw text.
  # Pin the explicit instruction adjacent to "print exactly one line as your
  # final output" so a future edit can't drop or relocate it. -z/-P with (?s)
  # lets "." span the line break the wording wraps across. The {0,60} window is
  # sized for the widest gap the current wording has -- widen it if a rewrap
  # pushes the phrase further from the instruction.
  mkharness-prompt-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.internals.outcomeContractFile}
        touch $out
      '';

  # fix-prompt.md's default template carries only its fix-specific preamble: the
  # rendered prompt must still gain the COMMS, CODE COMMENTS, CHECK/COMMIT, and
  # outcome-contract blocks, each exactly once.
  mkharness-prompt-fix-comms-injected = pkgs.runCommand "mkharness-prompt-fix-comms-injected" { } ''
    count=$(grep -c '# COMMS' ${batsHarness.internals.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's COMMS block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # fix-prompt.md shares issue-prompt.md's comment-discipline rule the same way
  # it already shares COMMS and CHECK/COMMIT.
  mkharness-prompt-fix-code-comments-injected =
    pkgs.runCommand "mkharness-prompt-fix-code-comments-injected" { }
      ''
        count=$(grep -c '# CODE COMMENTS' ${batsHarness.internals.promptDir}/fix-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the fix prompt's CODE COMMENTS block injected exactly once, got $count" >&2
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

  # A Consumer fixPrompt carrying no shared-block markers at all must still gain
  # all four, in COMMS, CODE COMMENTS, CHECK, outcome-contract order — the same
  # runtime-override parity the issue prompt has. Proven at the Nix layer here;
  # entrypoint.sh's runtime injection is covered by
  # tests/entrypoint-outcome-contract.bats.
  mkharness-prompt-fix-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-fix-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.internals.promptDir}/fix-prompt.md
        [ "$(grep -c '# COMMS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# CODE COMMENTS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# CHECK' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# LAND THE CHANGE' ${fixPromptHarness.internals.promptDir}/fix-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        comms_line=$(grep -n '# COMMS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        code_comments_line=$(grep -n '# CODE COMMENTS' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        check_line=$(grep -n '# CHECK' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        outcome_line=$(grep -n '# LAND THE CHANGE' ${fixPromptHarness.internals.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$comms_line" ]
        [ "$comms_line" -lt "$code_comments_line" ]
        [ "$code_comments_line" -lt "$check_line" ]
        [ "$check_line" -lt "$outcome_line" ]
        touch $out
      '';

  # The injected COMMS, CODE COMMENTS, and CHECK/COMMIT blocks must be
  # byte-identical to the canonical sections mkHarness slices them from, so
  # fix-prompt.md and issue-prompt.md cannot drift apart.
  mkharness-prompt-fix-comms-no-drift = pkgs.runCommand "mkharness-prompt-fix-comms-no-drift" { } ''
    awk '/^# COMMS$/{f=1} /^# CODE COMMENTS$/{exit} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-comms.txt
    diff ${batsHarness.internals.commsContractFile} injected-comms.txt
    touch $out
  '';

  mkharness-prompt-fix-code-comments-no-drift =
    pkgs.runCommand "mkharness-prompt-fix-code-comments-no-drift" { }
      ''
        awk '/^# CODE COMMENTS$/{f=1} /^# CHECK$/{exit} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-code-comments.txt
        diff ${batsHarness.internals.codeCommentsContractFile} injected-code-comments.txt
        touch $out
      '';

  # fragments/code-comments.md is a hand-maintained second copy of
  # issue-prompt.md's "# CODE COMMENTS" body -- worker-prompt.md and
  # conflict-resolve-prompt.md splice it in as inline prose, not a headed
  # section, so it can't reuse the injectBlocks mechanism. Pins the two together
  # so an edit to one without the other fails loudly.
  mkharness-fragment-code-comments-no-drift =
    pkgs.runCommand "mkharness-fragment-code-comments-no-drift" { }
      ''
        tail -n +3 ${batsHarness.internals.codeCommentsContractFile} | head -n -1 > canonical-code-comments-body.txt
        diff canonical-code-comments-body.txt ${../../templates/default/prompts/fragments/code-comments.md}
        touch $out
      '';

  mkharness-prompt-fix-check-no-drift = pkgs.runCommand "mkharness-prompt-fix-check-no-drift" { } ''
    awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-check.txt
    diff ${batsHarness.internals.checkContractFile} injected-check.txt
    touch $out
  '';

  # The CHECK-phase never-background / emit-outcome guardrail covers the CHECK
  # phase's own blocking gates. Both greps are scoped to issue-prompt's CHECK
  # section, not the whole file: OUTCOME carries its own "Do NOT run" phrasing
  # further down, so an unscoped grep would keep passing even if the CHECK
  # paragraph were deleted. The fix-prompt side rides
  # mkharness-prompt-fix-check-no-drift's byte-for-byte diff.
  mkharness-prompt-check-never-background =
    pkgs.runCommand "mkharness-prompt-check-never-background" { }
      ''
        grep -q 'never background it' ${checkSectionSlices}/issue-check.txt
        grep -q 'SPINDRIFT_OUTCOME' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The defensive fallback for an agent that backgrounds a check gate anyway: a
  # build killed outright (OOM, SIGKILL) never writes the exit marker a
  # background+poll loop waits on, so the wait must be bounded and a vanished
  # marker treated as failure, not still-pending. Same CHECK-section scoping as
  # above.
  mkharness-prompt-check-vanished-marker-is-failure =
    pkgs.runCommand "mkharness-prompt-check-vanished-marker-is-failure" { }
      ''
        grep -qi 'vanished' ${checkSectionSlices}/issue-check.txt
        grep -qi 'exit marker' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Nix flakes only evaluate git-tracked files (issue #714): an agent that
  # creates a new file and runs `nix build` before staging it hits a
  # spurious "not tracked by Git" failure and burns a checks cycle. Same
  # CHECK-section scoping as the never-background/vanished-marker checks
  # above. Fix-prompt side is covered by mkharness-prompt-fix-check-no-drift's
  # byte-for-byte diff, not re-pinned here (issue #1009).
  mkharness-prompt-check-git-add-before-nix-build =
    pkgs.runCommand "mkharness-prompt-check-git-add-before-nix-build" { }
      ''
        grep -qi 'git add' ${checkSectionSlices}/issue-check.txt
        grep -qi 'tracked by' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The CHECK section must keep the explicit no-cat-a-whole-log rule and the
  # scoped-check-target steering, and must not regrow the manual output-routing
  # advice the bash-output interceptor now handles. Same CHECK-section scoping.
  mkharness-prompt-check-no-cat-log-and-scoped-target =
    pkgs.runCommand "mkharness-prompt-check-no-cat-log-and-scoped-target" { }
      ''
        grep -qi 'never `cat`' ${checkSectionSlices}/issue-check.txt
        grep -qi 'scoped check target' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The scoped-check-target steering must be a firm rule, not a soft preference:
  # an explicit prohibition on running the full `nix flake check` in-box that
  # overrides loosely-worded issue acceptance criteria, with the one legitimate
  # exception (the diff touches what's baked into the image) named by file.
  mkharness-prompt-check-full-flake-check-firm-rule =
    pkgs.runCommand "mkharness-prompt-check-full-flake-check-firm-rule" { }
      ''
        grep -Pzqi \
          '(?s)(do not|must not) run.{0,80}full.{0,80}nix flake check.{0,300}(nix/checks/image\.nix|lib/image\.nix)' \
          ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  mkharness-prompt-fix-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-fix-outcome-no-drift" { }
      ''
        awk '/# LAND THE CHANGE/{f=1} f' ${fixPromptHarness.internals.promptDir}/fix-prompt.md > injected-contract.txt
        diff ${batsHarness.internals.outcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The research dispatch kind's own outcome contract: a Consumer researchPrompt
  # that drops "# POST THE VERDICT" must still ship an agent that posts the
  # verdict comment and emits the outcome line.
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

  # The default research prompt already contains the contract, so injection is a
  # no-op.
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

  # lib/research-verdicts.nix's `render` always rewrites the VERDICT section
  # from the configured verdict set, so there is no byte-identical-to-template
  # no-op case. A byte diff rather than presence greps, so any other prose in the
  # VERDICT..POST THE VERDICT span -- deleted, duplicated, or reordered by a
  # rendering regression -- fails loudly instead of slipping past a
  # presence-only assertion.
  mkharness-prompt-research-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-prompt.md > rendered.txt
        diff -u ${researchVerdictDefaultFixture} rendered.txt \
          || { echo "default research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # Same, for the self-contained sub-mode prompt (ADR 0022). Pins that its
  # "Judge relevance..." sentence -- the one line distinguishing the sub-mode --
  # survives rendering untouched, ahead of the registry-generated bullets.
  mkharness-prompt-research-self-contained-verdicts-default-rendered =
    pkgs.runCommand "mkharness-prompt-research-self-contained-verdicts-default-rendered" { }
      ''
        awk '/^# VERDICT$/{f=1} /^# POST THE VERDICT$/{exit} f' \
          ${batsHarness.internals.promptDir}/research-self-contained-prompt.md > rendered.txt
        diff -u ${researchVerdictSelfContainedFixture} rendered.txt \
          || { echo "self-contained research prompt's rendered VERDICT section drifted from the expected registry-rendered content" >&2; exit 1; }
        touch $out
      '';

  # The two checks above only inspect the VERDICT..POST THE VERDICT span, so an
  # unresolved enumMarker -- which lives on the "Structure the verdict" line
  # *after* the `# POST THE VERDICT` heading -- would stay invisible to them.
  # Scan the whole baked file: neither marker may survive rendering.
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

  # A custom RESEARCH_VERDICTS set flows into the baked prompt's verdict
  # contract: bullets, enumeration, and status alternation all render from the
  # configured set, with no default verdict token surviving. Proves the set
  # reaches the prompt, not only the launcher.
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
  # byte-identical to the default research prompt's own contract section -- both
  # sliced from the same marker in the same source file.
  mkharness-prompt-research-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-research-outcome-no-drift" { }
      ''
        awk '/# POST THE VERDICT/{f=1} f' ${researchPromptHarness.internals.promptDir}/research-prompt.md > injected-contract.txt
        diff ${batsHarness.internals.researchOutcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The self-contained research prompt's `# POST THE VERDICT` tail is
  # hand-maintained: injectResearchOutcomeContract no-ops on both source
  # templates because each already owns the marker, so nothing structurally pins
  # the self-contained copy to the canonical research-prompt.md. Slices the tail
  # from both sources and asserts they stay byte-identical.
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

  # The research kind's counterpart to
  # mkharness-prompt-outcome-contract-has-landing-token, with the same anchoring
  # and every-line reasoning.
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
  # research kind's own contract.
  mkharness-prompt-research-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.internals.researchOutcomeContractFile}
        touch $out
      '';

  # A Consumer researchPrompt carrying no "# POST THE VERDICT" marker at all must
  # still gain the contract, and survive the round trip byte-identical to what a
  # runtime SPINDRIFT_PROMPT_DIR override receives. entrypoint.sh's own runtime
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

  # The Driver no longer polls CI itself -- the launcher gates on CI green before
  # flipping the PR ready and merging -- so the WATCH CI GraphQL query must not
  # appear in any prompt *source* file. fix-prompt.md legitimately references the
  # unrelated `statusCheckRollup` JSON field name, so the query body itself is
  # the pin, not the field name.
  prompt-source-statusCheckRollup-query-absent =
    pkgs.runCommand "prompt-source-statusCheckRollup-query-absent" { }
      ''
        # `|| true`: under stdenv's pipefail a no-match exit would abort the
        # script here, before the assertion below runs. The grep in the error
        # branch needs no such guard -- it only runs once a match is known.
        count=$(grep -rlF 'query($owner:String!' ${../../templates/default/prompts} | wc -l || true)
        [ "$count" -eq 0 ] || {
          echo "expected the WATCH CI GraphQL query in no prompt source file, got $count" >&2
          grep -rlF 'query($owner:String!' ${../../templates/default/prompts} >&2
          exit 1
        }
        touch $out
      '';

  # ORCHESTRATOR_ENABLED is a master feature-flag switch (ADR 0035 amendment),
  # not a scatter of ad-hoc checks: exactly one line in agent/entrypoint.sh may
  # test the raw env var (the canonical `local ORCHESTRATOR=` computation), and
  # every downstream fork must read that computed gate. Every conditional
  # branching on $ORCHESTRATOR must declare both an on-row and an off-row -- an
  # explicit `else`, never a bare `if` -- so a segment added later with only one
  # side fails here instead of silently rendering the same fork for every input.
  orchestrator-fork-well-formed = pkgs.runCommand "orchestrator-fork-well-formed" { } ''
    entrypoint=${../../agent/entrypoint.sh}

    # Excludes comment-only lines (prose is free to name the env var) so this
    # doesn't pin one exact bash parameter-expansion form.
    gate_computations=$(awk '/ORCHESTRATOR_ENABLED/ && $0 !~ /^[[:space:]]*#/' "$entrypoint" | wc -l)
    [ "$gate_computations" -eq 1 ] || {
      echo "expected exactly one ORCHESTRATOR_ENABLED test (the canonical gate computation) in agent/entrypoint.sh, got $gate_computations" >&2
      grep -n 'ORCHESTRATOR_ENABLED' "$entrypoint" >&2
      exit 1
    }

    # Zero sites is the expected steady state: every $ORCHESTRATOR read left in
    # entrypoint.sh is the bare `[ -n "$ORCHESTRATOR" ] && ...` form, which this
    # pattern doesn't match. The loop still catches a *future* if/else
    # $ORCHESTRATOR conditional missing its off-row; it just no longer requires
    # one to exist.
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

  # The filer's dedup step must search open issues beyond the
  # `agent-review-finding` label, or it silently stops catching human-filed
  # duplicates. Three assertions, because the obvious two leave a gap: re-adding
  # a `--label` flag to the `--state open` line still contains `--state open` and
  # never matches the old `--state all` string, so both would stay green while
  # the dedup narrows. Hence the third: the `--state open` line must carry no
  # `--label` at all.
  # The explicit `[ "$n" -eq 0 ] || exit 1` shape, not a bare `! pipeline`, since
  # `set -e` exempts negated commands.
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

  # The CHECK-section awk slices must each be defined once, not copy-pasted -- a
  # marker rename applied to one copy and forgotten in the others would leave
  # those checks silently reading stale content. Covers both the issue-prompt
  # half (`# REVIEW` exit) and the fix-prompt half (`# LAND THE CHANGE` exit).
  prompts-nix-check-section-awk-defined-once =
    pkgs.runCommand "prompts-nix-check-section-awk-defined-once" { }
      ''
        # Split so this line's own source text never contains the
        # contiguous target pattern -- else this check would count itself.
        half1='/^# CHECK$/{f=1}'
        half2=' /^# REVIEW$/{exit} f'
        count=$(grep -cF "$half1$half2" ${./prompts.nix})
        [ "$count" -le 1 ] || {
          echo "expected the CHECK-section awk slice defined at most once in prompts.nix, got $count" >&2
          exit 1
        }
        fix_half2=' /^# LAND THE CHANGE$/{exit} f'
        fix_count=$(grep -cF "$half1$fix_half2" ${./prompts.nix})
        [ "$fix_count" -le 1 ] || {
          echo "expected the fix-prompt CHECK-section awk slice defined at most once in prompts.nix, got $fix_count" >&2
          exit 1
        }
        touch $out
      '';

  # The filer's dedup step must also treat closed `agent-research-reject` issues
  # as suppressing matches, the same triage-decision class as a closed
  # `agent-review-finding`. Anchored to the full `--label ... --state closed`
  # command, not the bare label token, which would still match an unrelated prose
  # mention if the search line itself lost the label.
  filer-prompt-dedup-names-research-reject =
    pkgs.runCommand "filer-prompt-dedup-names-research-reject" { }
      ''
        grep -q -- '--label agent-research-reject --state closed' ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';

  # The PR-body ticket-reference toggle (ADR 0029). Each of the three fragment
  # files is unconditional prose for its one case -- entrypoint.sh's precompute
  # block picks exactly one gate per run -- so a static grep pins each contract:
  #   github:            `Closes #${ISSUE_NUMBER}` stays.
  #   local, toggle off: no ticket reference at all, and neither auto-close
  #                      keyword.
  #   local, toggle on:  a `Local-issue: <slug>` breadcrumb, and neither
  #                      auto-close keyword -- the footgun fix.
  # The runtime wiring that picks the gate from ISSUE_TRACKER x
  # LOCAL_ISSUE_REFERENCE needs a live entrypoint.sh run, so it lives in
  # tests/entrypoint-prompt-fragments.bats instead.
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

  # The issue-read step (ADR 0032): the four local-tracker fragments must never
  # invoke `gh issue view` -- for a numeric slug it can silently fetch an
  # unrelated real issue on the Target repo, the exact footgun the read-only
  # /issues mount exists to close -- and must reference /issues instead.
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
  # `gh issue view ''${ISSUE_NUMBER}`.
  issue-read-github-fragments-keep-gh-issue-view-unchanged =
    pkgs.runCommand "issue-read-github-fragments-keep-gh-issue-view-unchanged" { }
      ''
        for f in issue-read-github.md research-issue-read-github.md \
          scout-issue-read-github.md review-issue-read-github.md; do
          grep -q 'gh issue view ''${ISSUE_NUMBER}' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side counterpart: each of the four forgejo variants speaks
  # fj issue view, never gh issue view.
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

  # Unbounded `--comments` pulls a meta-issue's entire comment history into the
  # agent's context on every turn, so each of the four github variants must cap
  # intake to `comments[-10:]` instead of the bare `--comments` flag.
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

  # The read-write write-step fragments must keep `gh issue comment`.
  github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged =
    pkgs.runCommand "github-readwrite-comment-fragments-keep-gh-issue-comment-unchanged" { }
      ''
        for f in issue-blocked-comment-github.md research-verdict-github.md; do
          grep -q 'gh issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The read-only counterpart: a read-only Box holds no write token, so its
  # blocked-note/verdict-comment fragments must never invoke `gh issue comment`
  # and must carry the host-mediated relay instead -- the blocked-note fragment
  # points at the SPINDRIFT_OUTCOME note= field, and the research-verdict
  # fragment emits a single nonce-guarded SPINDRIFT_COMMENT line. The
  # single-line, base64-encoded grammar (not the earlier
  # SPINDRIFT_COMMENT_BEGIN/END block) is what survives a stream-json JSONL box
  # log.
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

  # The forgejo-side counterpart: the read-write write-step fragments must keep
  # `fj issue comment`.
  forgejo-readwrite-comment-fragments-keep-fj-issue-comment =
    pkgs.runCommand "forgejo-readwrite-comment-fragments-keep-fj-issue-comment" { }
      ''
        for f in issue-blocked-comment-forgejo.md research-verdict-forgejo.md; do
          grep -q 'fj issue comment' ${../../templates/default/prompts/fragments}/"$f"
        done
        touch $out
      '';

  # The forgejo-side read-only counterpart: no write-capable FORGEJO_TOKEN, so
  # the same host-mediated relay forms (note= field / SPINDRIFT_COMMENT line)
  # apply.
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

  # The filer write-mechanism split: the direct-mode fragments must keep
  # `gh label create`/`gh issue create`.
  filer-direct-fragments-keep-gh-write-unchanged =
    pkgs.runCommand "filer-direct-fragments-keep-gh-write-unchanged" { }
      ''
        grep -q 'gh label create agent-review-finding' ${../../templates/default/prompts/fragments/filer-label-direct.md}
        grep -q 'gh issue create' ${../../templates/default/prompts/fragments/filer-file-direct.md}
        touch $out
      '';

  # The read-only counterpart: a read-only Box holds no write token, so the
  # filer's relay fragments must never invoke `gh label create` and must carry
  # the host-mediated SPINDRIFT_ISSUE_INTENT relay instead. `gh issue create`'s
  # absence is already covered by mkHarness's structural forbidden-marker assert.
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

  # The forgejo counterpart. fj has no label verb and `fj issue create` has no
  # --label flag, so these fragments must speak `fj issue create` and reach for
  # the REST API (curl) to create the label.
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

  # The OPEN A PULL REQUEST read-write create step forks on CODE_FORGE: each
  # fragment must invoke its own forge's CLI and never the other's.
  open-pr-create-fragments-fork-forge-on-read-write =
    pkgs.runCommand "open-pr-create-fragments-fork-forge-on-read-write" { }
      ''
        grep -q 'gh pr create' ${../../templates/default/prompts/fragments/open-pr-create-git.md}
        ! grep -q 'fj pr create' ${../../templates/default/prompts/fragments/open-pr-create-git.md}
        grep -q 'fj pr create' ${../../templates/default/prompts/fragments/open-pr-create-forgejo.md}
        ! grep -q 'gh pr create' ${../../templates/default/prompts/fragments/open-pr-create-forgejo.md}
        touch $out
      '';

  # The fix-pass CONTEXT CI-read step forks on CODE_FORGE the same way.
  fix-ci-read-fragments-fork-forge = pkgs.runCommand "fix-ci-read-fragments-fork-forge" { } ''
    grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    ! grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-github.md}
    grep -q 'fj pr status' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    ! grep -q 'gh pr view' ${../../templates/default/prompts/fragments/fix-ci-read-forgejo.md}
    touch $out
  '';

  # Build-time reject arm: mkHarness.nix must turn the `reviewer-verdict`
  # validateMarkers row into a real build failure when the orchestrator is
  # statically enabled and reviewPrompt lacks the required `VERDICT:` marker.
  # Each mkHarness.nix call here is a broken fixture built INLINE, never exported
  # from nix/fixtures.nix -- a fixture there would be forced by every other
  # consumer of that file, but a reject case must stay local to its own check.
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

  # The gate-not-triggered counterpart: the same missing-marker reviewPrompt with
  # orchestratorEnabled left false -- the omission is real but its gating
  # condition isn't statically known true, so buildTimeRejectVerdicts resolves
  # "advise", not "reject", and the build must succeed.
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

  # The `verdict-comment-relay` counterpart, over the shared
  # brokenResearchVerdictFragmentsDir fixture.
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

  # The gate-not-triggered counterpart: the same broken fragments directory with
  # boxForgeAndIssueAccess left read-write, so the row resolves "advise".
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

  # A forbidden marker authored as literal fragment-body text in a fragment gated
  # on a plain, non-exempt gate must fail the build -- unconditionally, unlike
  # buildTimeRejectVerdicts above, since such a marker in the shipped corpus is a
  # problem for any Consumer that might configure boxAccessReadOnly.
  build-time-reject-forbidden-marker-fragment =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          fragmentsDir = brokenForbiddenMarkerFragmentsDir;
          # Isolates this check to the fragment scan: the real filer/issue
          # prompts carry an unrelated known template violation that would make
          # this assertion pass for the wrong reason.
          prompt = cleanForbiddenMarkerPlaceholder;
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when a fragment gated on a plain, non-exempt gate (AUTO_FORMAT) carries a forbidden marker ('git push') as literal fragment-body text";
    pkgs.runCommand "build-time-reject-forbidden-marker-fragment" { } "touch $out";

  # The exempt-gate counterpart: the same substring in a fragment gated on
  # BOX_ACCESS_READ_ONLY. Many shipped fragments legitimately carry
  # forbidden-marker text as a negation ("do NOT git push"), being the read-only
  # half of an access-mode pair, so the scan must not false-positive on them.
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

  # The gh-api-mutation-kind counterpart: a non-exempt gate carrying the
  # forbidden-gh-api-mutation row's marker. buildTimeForbiddenMarkerViolations
  # filters to kind == "substring" rows only, so this must NOT throw.
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

  # The shared top-level template counterpart: `prompt` gets no exemption at all
  # -- its raw text is scanned against every forbiddenMarkers substring row.
  build-time-reject-forbidden-marker-template =
    let
      inherit (pkgs.lib) assertMsg;
      broken = builtins.tryEval (
        (import ../../lib/mkHarness.nix {
          inherit nixpkgs system;
          packages = p: [ p.hello ];
          prompt = "some issue prompt text containing gh pr create somewhere";
          # Isolates this check to the deliberately-broken `prompt` param -- see
          # cleanForbiddenMarkerPlaceholder's doc comment above.
          filerPrompt = cleanForbiddenMarkerPlaceholder;
        }).spindrift
      );
    in
    assert assertMsg (!broken.success)
      "mkHarness.nix must throw when the shared `prompt` template carries a forbidden marker ('gh pr create') as literal text";
    pkgs.runCommand "build-time-reject-forbidden-marker-template" { } "touch $out";

  # Same, for `reviewPrompt`. templateContentByFile's three entries are
  # hand-written attrset keys, so a check that only overrides `prompt` would
  # never notice the other two being dropped or mis-keyed.
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

  # ADR 0041: a research prompt must never statically carry a
  # FILER_FILE_DIRECT*-gated fragment's envsubst placeholder -- research issues
  # are always filed through the host-mediated SPINDRIFT_ISSUE_INTENT relay,
  # never `gh`/`fj` straight from the agent.
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

  # Same, for researchSelfContainedPrompt. The two are separately hand-keyed
  # entries in researchPromptContentByName, so a check that only overrides
  # researchPrompt would never notice the self-contained entry being mis-keyed.
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

  # The "not triggered" counterpart: the real, unmodified templates must build
  # clean, so the checks above aren't only ever exercising broken fixtures.
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

  # Guards lib/prompt-coverage.nix's completeness against the templates
  # directory, in both directions: a template on disk with no registry row, and a
  # stale row naming a template that no longer exists. Without the registry, a
  # new prompt kind would silently default to uncovered.
  # It deliberately does NOT check that a "covered" row's assembled text carries
  # its declared variable -- that's caveman-coverage-covered-templates-carry-
  # directive below.
  caveman-coverage-registry-matches-templates-dir =
    let
      inherit (pkgs.lib) concatMapStringsSep;
      # Trailing "\n" matches the sibling list files below, which need one so a
      # `while read` loop doesn't drop the final line. Harmless here under
      # sort/comm/uniq, but keeps the files consistent.
      registryFiles = pkgs.writeText "caveman-coverage-registry-files.txt" (
        concatMapStringsSep "\n" (r: r.promptFile) cavemanCoverageRegistry + "\n"
      );
    in
    pkgs.runCommand "caveman-coverage-registry-matches-templates-dir" { } ''
      registry_files=$(sort ${registryFiles})
      disk_files=$(find ${../../templates/default/prompts} -maxdepth 1 -name '*.md' -printf '%f\n' | sort)

      # A duplicate row would otherwise surface through `comm -13` below as the
      # misleading "names a promptFile that does not exist" -- comm's multiset
      # semantics report the second occurrence as unique-to-registry once the
      # single disk copy is consumed. Catch the real fault first.
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

  # Row-shape guards for the registry's three documented invariants: coverage is
  # "covered" or "exempt"; cavemanVar is required iff covered; reason is required
  # iff exempt. A typo'd `coverage = "Covered";` would silently drop that row
  # from every `filter (r: r.coverage == ...)` call, making the content checks
  # pass vacuously and reinstating the "silently defaults to uncovered" drift
  # this registry exists to kill.
  # These are pure eval-time asserts; the `touch $out` derivation exists only so
  # `nix build` forces them.
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

  # Structural tie from cavemanVar to lib/fragments.nix. The sibling
  # carries-directive check below only greps the assembled prompt for the literal
  # "${cavemanVar}" text; it never checks that cavemanVar names a real
  # CAVEMAN_BAKED fragment var, so a typo'd one could pass by coincidence and
  # render with no caveman skill actually baked in. This cross-references against
  # fragmentsRegistry's CAVEMAN_BAKED rows as a Nix data comparison, making the
  # tie structural rather than coincidental.
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

  # For every "covered" row, assert the *assembled* prompt contains the literal,
  # unsubstituted envsubst placeholder for its declared cavemanVar. envsubst runs
  # at container runtime, not nix build time, so the literal ${VAR} text is
  # what's on disk. Reads from the assembled dir rather than the raw template for
  # every row uniformly -- fix-prompt.md's directive only exists post-injection,
  # and reading every row the same way avoids special-casing it.
  #
  # The per-row search string is built with Nix string concatenation, not inside
  # the runCommand shell, so the script never has to reconstruct a literal
  # "${...}" from a dynamic variable name.
  caveman-coverage-covered-templates-carry-directive =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      coveredRows = filter (r: r.coverage == "covered") cavemanCoverageRegistry;
      # Trailing "\n" matters: bash's `while read` skips a final line with no
      # trailing newline, so a bare concatMapStringsSep would silently drop the
      # last covered row from the scan.
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

  # Exempt-row half: every "exempt" row's assembled prompt must carry NO
  # case-insensitive occurrence of "caveman" -- currently just filer-prompt.md,
  # which authors issue titles/bodies directly and so must stay human prose end
  # to end. The file list is derived from the registry rather than hardcoded, so
  # a future exempt row needs no second hand-maintained list.
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
        # `|| true`: under stdenv's pipefail a no-match exit would abort the
        # script here, before the assertion below runs.
        n=$(grep -ic 'caveman' "$promptDir/$promptFile" || true)
        [ "$n" -eq 0 ] || {
          echo "$promptFile: expected no case-insensitive 'caveman' mention in the assembled prompt (declared exempt in lib/prompt-coverage.nix), found $n" >&2
          exit 1
        }
      done < ${exemptFiles}
      touch $out
    '';

  # Ties the caveman narration directive to the machine-parsed marker registries:
  # every CAVEMAN_BAKED-gated fragment carries a "the machine-parsed marker
  # grammar is exempt too" paragraph naming a subset of requiredMarkerNames, so a
  # marker parsed by code but never named in any caveman fragment risks a Box
  # caveman-compressing it into an unparseable line. The fragment file list is
  # derived from fragmentsRegistry, so a future CAVEMAN_BAKED fragment is picked
  # up automatically instead of silently unscanned.
  caveman-coverage-exemption-list-covers-marker-registry =
    let
      inherit (pkgs.lib) concatMapStringsSep filter;
      cavemanFragmentsDir = ../../templates/default/prompts/fragments;
      cavemanFragmentRows = filter (r: r.gate == "CAVEMAN_BAKED") fragmentsRegistry;
      cavemanFragmentPaths = map (r: cavemanFragmentsDir + "/${r.fragment}") cavemanFragmentRows;
      # Same trailing-newline guard as coveredRowsFile/exemptFiles above.
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

  # Pins lib/mkHarness.nix's isDirectFileGate predicate: directFileFragmentRows
  # and readOnlyReachableFragmentRows' exclusion list must agree on "is this a
  # FILER_FILE_DIRECT*-gated row", so a future FILER_FILE_DIRECT_GITLAB gate
  # added only to lib/fragments.nix can't be picked up by one and missed by the
  # other. Appends a synthetic row to the real registry and reads back the
  # harness' own computed values, not a reimplementation of the predicate, so a
  # re-drift inside mkHarness.nix is caught here rather than in a parallel copy.
  # The synthetic row points at skill-preamble.md (inert, no forbiddenMarkers
  # substring) so a leak fails this check's own assertion instead of surfacing as
  # an unrelated-looking forbiddenMarkerCheckOk failure.
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

  # researchPromptContentByName hand-keys exactly the research prompt names its
  # build-time direct-file scan covers, so a future third research*-prompt.md
  # would silently miss the scan without a matching row. Reads the real on-disk
  # prompt directory and compares against the harness' own computed keys, failing
  # the moment the two disagree in either direction.
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
