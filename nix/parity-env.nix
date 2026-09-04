# Shared env wiring for the prompt-assembly byte-parity suite (issue #2951):
# both nix/checks/promptassembly.nix's `promptassembly-parity` check (verify
# mode) and nix/regen-goldens.nix's `regen-goldens` app (update mode, `bats
# UPDATE_GOLDENS=1`) run tests/prompt-assembly-parity.bats against the exact
# same rendered fixture set -- extracted here so the two callers can't drift
# apart on what "the same env" means.
{ pkgs, fixtures }:
let
  inherit (fixtures) batsHarness;

  # lib/fragments.nix is a bare list of attrsets, pure builtins (no pkgs.lib
  # needed to evaluate it) -- see its own header comment.
  registry = import ../lib/fragments.nix;

  fragmentsRegistryJsonFile = pkgs.writeText "fragments-registry.json" (builtins.toJSON registry);

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).validateMarkers
  );

  # lib/prompt-contract.nix's forbiddenMarkers list, rendered the same way as
  # promptContractRegistryJsonFile above: this suite's read-only cells
  # (BOX_WRITE_ENABLED unset, e.g. github-read-only, forgejo-read-only)
  # exercise agent/entrypoint.sh's install_readonly_guards, which calls
  # `driver-exec readonly-guards --forbidden-markers-registry`, so both the
  # promptassembly-parity check and the regen-goldens app need this JSON
  # file alongside promptContractRegistryJsonFile even though assemble-prompt
  # itself no longer reads a forbidden-markers registry.
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
    # Rendered fallback-default preamble for the 8 baked /agent/* path
    # literals (issue #2531); helper.bash prepends this between
    # DRIVER_PREAMBLE_FILE and FRAGMENT_REGISTRY_FILE, matching
    # lib/image.nix's real concatenation order, so this suite exercises
    # the same rendered bytes the image bakes in, not the values these
    # vars merely happen to already carry elsewhere in this env.
    AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
    FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
    DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
    # Built once, here, from the same `registry` value nix/checks/promptassembly.nix's
    # promptassembly-registry-drift check reuses via `inherit (parity) registry`
    # -- no second render of lib/fragments.nix.
    PROMPTASSEMBLY_REGISTRY_FILE = fragmentsRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
  };
}
