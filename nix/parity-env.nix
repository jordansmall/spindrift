# Shared env for the prompt-assembly byte-parity suite (issue #2951). The
# promptassembly-parity check and the regen-goldens app both run
# tests/prompt-assembly-parity.bats, so this file keeps them from drifting
# apart on what "the same env" means.
{ pkgs, fixtures }:
let
  inherit (fixtures) batsHarness;

  # lib/fragments.nix is a bare list of attrsets and needs no pkgs.lib to evaluate.
  registry = import ../lib/fragments.nix;

  fragmentsRegistryJsonFile = pkgs.writeText "fragments-registry.json" (builtins.toJSON registry);

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).validateMarkers
  );

  # The read-only cells (BOX_WRITE_ENABLED unset) run agent/entrypoint.sh's
  # install_readonly_guards, which calls `driver-exec readonly-guards
  # --forbidden-markers-registry`, so both callers still need this file even
  # though assemble-prompt itself no longer reads a forbidden-markers registry.
  forbiddenMarkersRegistryJsonFile = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).forbiddenMarkers
  );
in
{
  inherit registry fragmentsRegistryJsonFile;

  env = {
    ENTRYPOINT = ../agent/entrypoint.sh;
    PROMPTS_DIR = ../templates/default/prompts;
    OUTCOME_CONTRACT_FILE = batsHarness.internals.outcomeContractFile;
    COMMS_CONTRACT_FILE = batsHarness.internals.commsContractFile;
    CHECK_CONTRACT_FILE = batsHarness.internals.checkContractFile;
    RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.internals.researchOutcomeContractFile;
    DRIVER_PREAMBLE_FILE = batsHarness.internals.driverPreambleFile;
    # helper.bash prepends this between DRIVER_PREAMBLE_FILE and
    # FRAGMENT_REGISTRY_FILE, matching lib/image.nix's concatenation order, so
    # the suite checks the bytes the image really bakes for the 8 /agent/* path
    # literals (issue #2531).
    AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
    FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
    DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
    # nix/checks/promptassembly.nix's promptassembly-registry-drift check
    # reuses this same `registry` via `inherit (parity) registry`, so
    # lib/fragments.nix renders only once.
    PROMPTASSEMBLY_REGISTRY_FILE = fragmentsRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
  };
}
