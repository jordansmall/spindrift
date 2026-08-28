# The 9 baked /agent/* path literals (contracts, registries, prompts dir) --
# the single nix binding lib/image.nix's agentFiles cp destinations and
# lib/mkHarness.nix's agentPathsPreamble both read, so a rename in this file
# updates the image's copy destination and the rendered preamble default
# together, and nix/checks/image.nix can assert they never diverge (issue
# #2531). HARNESS_SKILLS_DIR is deliberately absent: skill-name
# single-sourcing is its own separate campaign-F slice, not "contracts,
# registries, prompts".
{
  # Prompt templates; overridable at runtime via SPINDRIFT_PROMPT_DIR, a mount
  # that shadows only this path (agent/entrypoint.sh, lib/mkHarness.nix).
  PROMPTS_DIR = "/agent/prompts";
  # The canonical SPINDRIFT_OUTCOME contract (issue #419), baked at a sibling
  # path to PROMPTS_DIR so a SPINDRIFT_PROMPT_DIR mount never hides it (issue
  # #420). The driver-exec assemble-prompt verb (issue #2354) reads the
  # marker straight off this file's own first line (injectSharedBlock,
  # cmd/launcher/internal/promptassembly), so it cannot drift from the
  # block's canonical source-file heading.
  OUTCOME_CONTRACT_FILE = "/agent/outcome-contract.md";
  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455 extends #419/#420's slice mechanism beyond
  # the outcome contract): baked and injected the same way, so a
  # SPINDRIFT_PROMPT_DIR override of the fix prompt gets the identical
  # treatment.
  COMMS_CONTRACT_FILE = "/agent/comms-contract.md";
  CHECK_CONTRACT_FILE = "/agent/check-contract.md";
  # The CODE COMMENTS block fix-prompt.md shares with issue-prompt.md (issue
  # #2880): baked and injected the same way as COMMS/CHECK above.
  CODE_COMMENTS_CONTRACT_FILE = "/agent/code-comments-contract.md";
  # The research dispatch kind's own harness-owned outcome contract (ADR
  # 0022, issue #640): posting the verdict comment and emitting the outcome
  # line. Baked and injected the same way as the work contract above, so a
  # SPINDRIFT_PROMPT_DIR override of research-prompt.md gets it too.
  RESEARCH_OUTCOME_CONTRACT_FILE = "/agent/research-outcome-contract.md";
  # The Conditional fragment registry as JSON (issue #622, #2354), for the
  # `driver-exec assemble-prompt` verb's `--registry` flag.
  PROMPTASSEMBLY_REGISTRY_FILE = "/agent/fragments-registry.json";
  # lib/prompt-contract.nix's validateMarkers list as JSON (issue #2356), for
  # the `driver-exec assemble-prompt` verb's `--validate-markers-registry`
  # flag.
  PROMPT_CONTRACT_REGISTRY_FILE = "/agent/prompt-contract-registry.json";
  # lib/prompt-contract.nix's forbiddenMarkers list as JSON (issue #2464), for
  # the `driver-exec readonly-guards` verb's `--forbidden-markers-registry`
  # flag (issue #2513: assemble-prompt no longer takes this flag).
  FORBIDDEN_MARKERS_REGISTRY_FILE = "/agent/forbidden-markers-registry.json";
}
