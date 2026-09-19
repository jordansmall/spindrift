# The single source for the baked /agent/* path literals: lib/image.nix's
# agentFiles copy destinations and lib/mkHarness.nix's agentPathsPreamble both
# read it, so a rename here moves both and nix/checks/image.nix can assert they
# never diverge (issue #2531). HARNESS_SKILLS_DIR belongs to the separate
# skill-name single-sourcing slice, not here.
{
  # A SPINDRIFT_PROMPT_DIR mount shadows this path and only this path
  # (agent/entrypoint.sh, lib/mkHarness.nix).
  PROMPTS_DIR = "/agent/prompts";
  # The canonical SPINDRIFT_OUTCOME contract (issue #419), a sibling of
  # PROMPTS_DIR so a SPINDRIFT_PROMPT_DIR mount never hides it (issue #420).
  # The driver-exec assemble-prompt verb reads the marker off this file's own
  # first line (injectSharedBlock, cmd/launcher/internal/promptassembly), so
  # it cannot drift from the block's heading (issue #2354).
  OUTCOME_CONTRACT_FILE = "/agent/outcome-contract.md";
  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455). The image bakes and injects them like the
  # outcome contract, so a SPINDRIFT_PROMPT_DIR override of the fix prompt gets
  # them too.
  COMMS_CONTRACT_FILE = "/agent/comms-contract.md";
  CHECK_CONTRACT_FILE = "/agent/check-contract.md";
  # The research dispatch kind's own outcome contract (ADR 0022, issue #640).
  # The image bakes and injects it like the work contract above, so a
  # SPINDRIFT_PROMPT_DIR override of research-prompt.md gets it too.
  RESEARCH_OUTCOME_CONTRACT_FILE = "/agent/research-outcome-contract.md";
  # The Conditional fragment registry as JSON (issues #622, #2354). The
  # `driver-exec assemble-prompt` verb reads it through its `--registry` flag.
  PROMPTASSEMBLY_REGISTRY_FILE = "/agent/fragments-registry.json";
  # lib/prompt-contract.nix's validateMarkers list as JSON (issue #2356). The
  # `driver-exec assemble-prompt` verb reads it through its
  # `--validate-markers-registry` flag.
  PROMPT_CONTRACT_REGISTRY_FILE = "/agent/prompt-contract-registry.json";
  # lib/prompt-contract.nix's forbiddenMarkers list as JSON (issue #2464). The
  # `driver-exec readonly-guards` verb reads it through its
  # `--forbidden-markers-registry` flag; assemble-prompt no longer takes it
  # (issue #2513).
  FORBIDDEN_MARKERS_REGISTRY_FILE = "/agent/forbidden-markers-registry.json";
}
