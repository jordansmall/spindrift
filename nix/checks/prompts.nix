# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules (issue #419).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    promptHarness
    fixPromptHarness
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

  mkharness-prompt-fix-outcome-injected = pkgs.runCommand "mkharness-prompt-fix-outcome-injected" { } ''
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
  # own runtime injection is covered by tests/entrypoint.bats).
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

  mkharness-prompt-fix-outcome-no-drift = pkgs.runCommand "mkharness-prompt-fix-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-contract.txt
    diff ${batsHarness.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # Grep pin (issue #455 acceptance criteria): a distinctive literal from the
  # shared WATCH CI block must appear in exactly one prompt *source* file on
  # disk. fix-prompt.md gets this block by injection now, never as hand-copied
  # source text — a regression here means someone pasted the block back in.
  prompt-source-statusCheckRollup-appears-once =
    pkgs.runCommand "prompt-source-statusCheckRollup-appears-once" { }
      ''
        count=$(grep -rl 'statusCheckRollup' ${../../templates/default/prompts} | wc -l)
        [ "$count" -eq 1 ] || {
          echo "expected 'statusCheckRollup' in exactly one prompt source file, got $count" >&2
          grep -rl 'statusCheckRollup' ${../../templates/default/prompts} >&2
          exit 1
        }
        touch $out
      '';
}
