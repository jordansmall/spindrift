# Anti-drift caveman-coverage registry (issue #2709): one row per top-level
# prompt template under templates/default/prompts/ (not its fragments/
# subdirectory), declaring whether that template's assembled text is
# expected to carry a caveman envsubst directive ("covered") or is
# deliberately exempt from carrying one at all ("exempt", with a reason).
# Before this registry existed, caveman coverage was decided once by hand
# per template, at whatever time that template was authored or the caveman
# skill was wired in -- a new prompt kind added later had no forcing
# function and would silently default to uncovered. nix/checks/prompts.nix's
# caveman-coverage-registry-matches-templates-dir check (issue #2709, slice
# 1) keeps this list in sync with the templates directory; that same file's
# slice 2 and slice 3 checks drive the actual per-row coverage/exemption
# assertions off this same list.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without needing a locked nixpkgs (mirrors
# lib/fragments.nix and lib/prompt-inject.nix).
#
# Each row:
#   promptFile -- basename under templates/default/prompts/ (not recursing
#                 into fragments/) this row describes.
#   coverage   -- the literal string "covered" or "exempt".
#   cavemanVar -- required when coverage == "covered": the envsubst variable
#                 name (e.g. "CAVEMAN_STEP", "CAVEMAN_STEP_WORKER",
#                 "CAVEMAN_STEP_REVIEW", "CAVEMAN_STEP_RESEARCH") this
#                 template's assembled text must carry. `null` when exempt.
#   reason     -- required when coverage == "exempt", explaining why. `null`
#                 when covered.
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
    # Not present in the raw fix-prompt.md source itself -- lib/prompt-
    # contract.nix's injectBlocks registry (id = "comms") slices
    # issue-prompt.md's own "# COMMS" section (which carries
    # ${CAVEMAN_STEP}) and injects it into the assembled fix-prompt.md at
    # build time, so this row's coverage is only verifiable against the
    # assembled prompt, not the on-disk template.
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
