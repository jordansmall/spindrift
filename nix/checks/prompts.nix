# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules (issue #419).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    promptHarness
    fixPromptHarness
    researchPromptHarness
    batsHarness
    ;
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
      ${promptHarness.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still
  # ship an agent that emits the outcome line, so the launcher can learn the
  # PR (issue #419) — the harness appends the canonical contract exactly once.
  mkharness-prompt-outcome-injected = pkgs.runCommand "mkharness-prompt-outcome-injected" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${promptHarness.promptDir}/issue-prompt.md)
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
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.promptDir}/issue-prompt.md)
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
        diff ${../../templates/default/prompts/issue-prompt.md} ${batsHarness.promptDir}/issue-prompt.md
        touch $out
      '';

  # The block injected into a prompt lacking the contract must be
  # byte-identical to the default prompt's own contract section — both are
  # sliced from the same marker in the same source file, so they cannot
  # drift apart (issue #419).
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # The no-drift check above only proves the injected block matches the
  # *same-source* contract slice -- it never asserts the slice says the right
  # thing. A source regression from `landing=` back to the pre-#638 `pr=`
  # grammar would still pass that diff, since both sides would drift
  # together. Pin the literal token directly (issue #654).
  mkharness-prompt-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-outcome-contract-has-landing-token" { }
      ''
        grep -q 'landing=' ${batsHarness.outcomeContractFile}
        touch $out
      '';

  # fix-prompt.md's default template carries only its fix-specific preamble
  # (issue #455): the rendered prompt must still gain the COMMS, CHECK/COMMIT,
  # and outcome-contract blocks, each exactly once, mirroring the issue
  # prompt's own guard above.
  mkharness-prompt-fix-comms-injected = pkgs.runCommand "mkharness-prompt-fix-comms-injected" { } ''
    count=$(grep -c '# COMMS' ${batsHarness.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's COMMS block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-check-injected = pkgs.runCommand "mkharness-prompt-fix-check-injected" { } ''
    count=$(grep -c '# CHECK' ${batsHarness.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's CHECK/COMMIT block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-outcome-injected =
    pkgs.runCommand "mkharness-prompt-fix-outcome-injected" { }
      ''
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.promptDir}/fix-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the fix prompt's outcome contract injected exactly once, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # A Consumer fixPrompt that carries only a fix-specific preamble — no
  # shared-block markers at all — must still gain all three, in COMMS, CHECK,
  # outcome-contract order, the same #420 runtime-override parity the issue
  # prompt already has (proven at the Nix layer here; agent/entrypoint.sh's
  # own runtime injection is covered by tests/entrypoint-outcome-contract.bats).
  mkharness-prompt-fix-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-fix-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.promptDir}/fix-prompt.md
        [ "$(grep -c '# COMMS' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# CHECK' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# LAND THE CHANGE' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        comms_line=$(grep -n '# COMMS' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        check_line=$(grep -n '# CHECK' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        outcome_line=$(grep -n '# LAND THE CHANGE' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$comms_line" ]
        [ "$comms_line" -lt "$check_line" ]
        [ "$check_line" -lt "$outcome_line" ]
        touch $out
      '';

  # The injected COMMS and CHECK/COMMIT blocks must be byte-identical to the
  # canonical sections mkHarness slices them from — same source, same bytes,
  # so fix-prompt.md and issue-prompt.md cannot drift apart (issue #455,
  # mirrors mkharness-prompt-outcome-no-drift above).
  mkharness-prompt-fix-comms-no-drift = pkgs.runCommand "mkharness-prompt-fix-comms-no-drift" { } ''
    awk '/^# COMMS$/{f=1} /^# CHECK$/{exit} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-comms.txt
    diff ${batsHarness.commsContractFile} injected-comms.txt
    touch $out
  '';

  mkharness-prompt-fix-check-no-drift = pkgs.runCommand "mkharness-prompt-fix-check-no-drift" { } ''
    awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-check.txt
    diff ${batsHarness.checkContractFile} injected-check.txt
    touch $out
  '';

  # The CHECK-phase never-background / emit-outcome guardrail (issue #592)
  # generalizes WATCH CI's rule to the CHECK phase's own blocking gates
  # (`nix build .#checks-inbox`, test suites). Written once in
  # issue-prompt.md's CHECK section and inherited by fix-prompt.md through
  # the CHECK block injection above. Both greps are scoped to the CHECK
  # section itself (not the whole file) -- WATCH CI carries the same
  # "never background it" phrase further down, so an unscoped grep would
  # keep passing even if the #592 CHECK paragraph were deleted.
  mkharness-prompt-check-never-background =
    pkgs.runCommand "mkharness-prompt-check-never-background" { }
      ''
        awk '/^# CHECK$/{f=1} /^# REVIEW$/{exit} f' \
          ${batsHarness.promptDir}/issue-prompt.md > issue-check.txt
        awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' \
          ${batsHarness.promptDir}/fix-prompt.md > fix-check.txt
        grep -q 'never background it' issue-check.txt
        grep -q 'never background it' fix-check.txt
        grep -q 'SPINDRIFT_OUTCOME' issue-check.txt
        grep -q 'SPINDRIFT_OUTCOME' fix-check.txt
        touch $out
      '';

  # The defensive fallback for an agent that backgrounds a check gate anyway
  # (issue #713): a build killed outright (OOM, SIGKILL) never writes the
  # exit marker a background+poll loop waits on, so the wait must be bounded
  # and a vanished marker treated as failure, not still-pending. Same
  # CHECK-section scoping as the never-background check above.
  mkharness-prompt-check-vanished-marker-is-failure =
    pkgs.runCommand "mkharness-prompt-check-vanished-marker-is-failure" { }
      ''
        awk '/^# CHECK$/{f=1} /^# REVIEW$/{exit} f' \
          ${batsHarness.promptDir}/issue-prompt.md > issue-check.txt
        awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' \
          ${batsHarness.promptDir}/fix-prompt.md > fix-check.txt
        grep -qi 'vanished' issue-check.txt
        grep -qi 'vanished' fix-check.txt
        grep -qi 'exit marker' issue-check.txt
        grep -qi 'exit marker' fix-check.txt
        touch $out
      '';

  # Nix flakes only evaluate git-tracked files (issue #714): an agent that
  # creates a new file and runs `nix build` before staging it hits a
  # spurious "not tracked by Git" failure and burns a checks cycle. Same
  # CHECK-section scoping as the never-background/vanished-marker checks
  # above.
  mkharness-prompt-check-git-add-before-nix-build =
    pkgs.runCommand "mkharness-prompt-check-git-add-before-nix-build" { }
      ''
        awk '/^# CHECK$/{f=1} /^# REVIEW$/{exit} f' \
          ${batsHarness.promptDir}/issue-prompt.md > issue-check.txt
        awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' \
          ${batsHarness.promptDir}/fix-prompt.md > fix-check.txt
        grep -qi 'git add' issue-check.txt
        grep -qi 'git add' fix-check.txt
        grep -qi 'tracked' issue-check.txt
        grep -qi 'tracked' fix-check.txt
        touch $out
      '';

  mkharness-prompt-fix-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-fix-outcome-no-drift" { }
      ''
        awk '/# LAND THE CHANGE/{f=1} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-contract.txt
        diff ${batsHarness.outcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The research dispatch kind's own outcome contract (issue #640): a
  # Consumer researchPrompt that drops "# POST THE VERDICT" must still ship
  # an agent that posts the verdict comment and emits the outcome line --
  # the harness appends the canonical contract exactly once.
  mkharness-prompt-research-outcome-injected =
    pkgs.runCommand "mkharness-prompt-research-outcome-injected" { }
      ''
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.promptDir}/research-prompt.md)
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
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.promptDir}/research-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the default research prompt's outcome contract to stay single, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default box's rendered research prompt must be byte-identical to the
  # template on disk -- injection must not touch a prompt that already has
  # the contract (mirrors mkharness-prompt-outcome-default-unchanged).
  mkharness-prompt-research-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-research-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/research-prompt.md} ${batsHarness.promptDir}/research-prompt.md
        touch $out
      '';

  # The block injected into a research prompt lacking the contract must be
  # byte-identical to the default research prompt's own contract section --
  # both sliced from the same marker in the same source file (issue #640,
  # mirrors mkharness-prompt-outcome-no-drift).
  mkharness-prompt-research-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-research-outcome-no-drift" { }
      ''
        awk '/# POST THE VERDICT/{f=1} f' ${researchPromptHarness.promptDir}/research-prompt.md > injected-contract.txt
        diff ${batsHarness.researchOutcomeContractFile} injected-contract.txt
        touch $out
      '';

  # Same gap as mkharness-prompt-outcome-contract-has-landing-token, for the
  # research kind's own contract (issue #654).
  mkharness-prompt-research-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-has-landing-token" { }
      ''
        grep -q 'landing=' ${batsHarness.researchOutcomeContractFile}
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
        grep -q 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.promptDir}/research-prompt.md
        [ "$(grep -c '# POST THE VERDICT' ${researchPromptHarness.promptDir}/research-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        contract_line=$(grep -n '# POST THE VERDICT' ${researchPromptHarness.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$contract_line" ]
        touch $out
      '';

  # Grep pin (issue #455 acceptance criteria): the WATCH CI GraphQL query
  # literal must appear in exactly one prompt *source* file on disk.
  # fix-prompt.md's CONTEXT section legitimately references the unrelated
  # `statusCheckRollup` JSON field name via `gh pr view --json`, so the query
  # body itself -- distinctive to the shared WATCH CI block -- is the pin, not
  # the field name alone. fix-prompt.md gets the WATCH CI block by injection
  # now, never as hand-copied source text -- a regression here means someone
  # pasted the block back in.
  prompt-source-statusCheckRollup-query-appears-once =
    pkgs.runCommand "prompt-source-statusCheckRollup-query-appears-once" { }
      ''
        count=$(grep -rlF 'query($owner:String!' ${../../templates/default/prompts} | wc -l)
        [ "$count" -eq 1 ] || {
          echo "expected the WATCH CI GraphQL query in exactly one prompt source file, got $count" >&2
          grep -rlF 'query($owner:String!' ${../../templates/default/prompts} >&2
          exit 1
        }
        touch $out
      '';

  # The Conditional fragment registry's computed-gate rows (lib/fragments.nix,
  # issue #622) name a bash variable entrypoint.sh's precompute block sets;
  # nothing else forces the two to agree, so a typo in either would leave the
  # fragment loop's `"${!_fgate}"` indirection silently reading an unset
  # variable -- the row just never renders, no error (issue #689). Same
  # drift-guard shape as outcome-contract-marker-parity in nix/checks/image.nix,
  # grep-based and eval-only so it belongs in checks-inbox, not the
  # image-realizing checks.
  fragment-gate-parity = pkgs.runCommand "fragment-gate-parity" { } ''
    for gate in SKILLS_FOUND CAVEMAN_BAKED TDD_BAKED COMMIT_BAKED CODE_REVIEW_BAKED FILER_ENABLED; do
      grep -qF "gate = \"$gate\";" ${../../lib/fragments.nix}
      grep -qF "local $gate=" ${../../agent/entrypoint.sh}
    done
    touch $out
  '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # search open issues beyond the `agent-review-finding` label -- a
  # regression back to the old `--label agent-review-finding --state all`
  # query would silently stop catching human-filed/ready-for-agent/
  # /to-tickets duplicates.
  filer-prompt-dedup-searches-all-open-issues =
    pkgs.runCommand "filer-prompt-dedup-searches-all-open-issues" { }
      ''
        grep -q -- '--state open' ${../../templates/default/prompts/filer-prompt.md}
        ! grep -q -- '--label agent-review-finding --state all' \
          ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # also treat closed `agent-research-reject` issues -- a research pass's
  # deliberate false-positive/not-worth-doing/duplicate verdict -- as
  # suppressing matches, the same triage-decision class as a closed
  # `agent-review-finding`.
  filer-prompt-dedup-names-research-reject =
    pkgs.runCommand "filer-prompt-dedup-names-research-reject" { }
      ''
        grep -q 'agent-research-reject' ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';
}
