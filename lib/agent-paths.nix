# The 8 baked /agent/* path literals (contracts, registries, prompts dir) --
# the single nix binding lib/image.nix's agentFiles cp destinations and
# lib/mkHarness.nix's agentPathsPreamble both read, so a rename in this file
# updates the image's copy destination and the rendered preamble default
# together, and nix/checks/image.nix can assert they never diverge (issue
# #2531). HARNESS_SKILLS_DIR is deliberately absent: skill-name
# single-sourcing is its own separate campaign-F slice, not "contracts,
# registries, prompts".
{
  PROMPTS_DIR = "/agent/prompts";
  OUTCOME_CONTRACT_FILE = "/agent/outcome-contract.md";
  COMMS_CONTRACT_FILE = "/agent/comms-contract.md";
  CHECK_CONTRACT_FILE = "/agent/check-contract.md";
  RESEARCH_OUTCOME_CONTRACT_FILE = "/agent/research-outcome-contract.md";
  PROMPTASSEMBLY_REGISTRY_FILE = "/agent/fragments-registry.json";
  PROMPT_CONTRACT_REGISTRY_FILE = "/agent/prompt-contract-registry.json";
  FORBIDDEN_MARKERS_REGISTRY_FILE = "/agent/forbidden-markers-registry.json";
}
