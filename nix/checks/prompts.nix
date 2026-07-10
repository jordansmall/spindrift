
# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules (issue #419).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    promptHarness
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
}
