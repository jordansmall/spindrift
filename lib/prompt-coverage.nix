# Caveman-coverage registry (issue #2709): one row per top-level template
# under templates/default/prompts/, not recursing into fragments/. Without
# it, a prompt kind added later silently defaults to uncovered.
# nix/checks/prompts.nix keeps the list in sync with that directory and
# drives the per-row coverage and exemption assertions off it.

# Pure builtins only (no `pkgs.lib`), so a bare `nix eval` can evaluate and
# unit-test this file without a locked nixpkgs.

# cavemanVar names the envsubst variable and is set only when coverage is
# "covered"; reason is set only when it is "exempt". The other is null.
[
  {
    promptFile = "conflict-resolve-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP";
    reason = null;
  }
  {
    promptFile = "filer-prompt.md";
    coverage = "exempt";
    cavemanVar = null;
    reason = "authors GitHub issue titles and bodies directly, so its output must stay human prose end to end";
  }
  {
    promptFile = "fix-prompt.md";
    coverage = "covered";
    # The on-disk template carries no directive. lib/prompt-contract.nix
    # injects issue-prompt.md's "# COMMS" section, which carries
    # CAVEMAN_STEP, at build time (injectBlocks id "comms"), so only the
    # assembled prompt satisfies this row.
    cavemanVar = "CAVEMAN_STEP";
    reason = null;
  }
  {
    promptFile = "issue-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP";
    reason = null;
  }
  {
    promptFile = "research-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP_RESEARCH";
    reason = null;
  }
  {
    promptFile = "research-self-contained-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP_RESEARCH";
    reason = null;
  }
  {
    promptFile = "review-axis-prompt.md";
    coverage = "exempt";
    cavemanVar = null;
    reason = "output is findings text that reaches a human cold, through the reviewer's finding lines and the Filer's issue bodies, so it must stay human prose end to end";
  }
  {
    promptFile = "review-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP_REVIEW";
    reason = null;
  }
  {
    promptFile = "scout-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP";
    reason = null;
  }
  {
    promptFile = "worker-prompt.md";
    coverage = "covered";
    cavemanVar = "CAVEMAN_STEP_WORKER";
    reason = null;
  }
]
