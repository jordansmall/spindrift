# The rendered artifacts the Go seam tests (issue #4280) read as fixtures:
# a pure copy of what the bats harness already renders (plus the repo's
# nix-checks skill, which the Go check sandbox cannot reach by relative path),
# no test logic here.
# The names are pinned against cmd/launcher/internal/seamtest/fixtures.json by
# nix/checks/seam-fixtures.nix.
# There is deliberately no "code-comments" contract fixture: that policy was
# inlined into the prompts (#3505, file removed in 78f2611f), so no rendered
# artifact exists to copy.
{
  pkgs,
  batsHarness,
  dockerHarness,
  bwrapHarness,
  butlerHarness,
}:
let
  inherit (batsHarness) internals;
  # The registries box hands prompt assembly, so the Go golden test assembles
  # from the exact bytes the goldens were produced under.
  registryJson = pkgs.writeText "fragments-registry.json" (
    builtins.toJSON (import ../lib/fragments.nix)
  );
  promptContractRegistryJson = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).validateMarkers
  );
  researchVerdictsParity = import ./research-verdicts-parity.nix { inherit pkgs; };
  promptContractParityFixtures = pkgs.writeText "prompt-contract-parity-fixtures.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).parityFixtures
  );
  files = {
    "outcome-contract.md" = internals.outcomeContractFile;
    "comms-contract.md" = internals.commsContractFile;
    "check-contract.md" = internals.checkContractFile;
    "research-outcome-contract.md" = internals.researchOutcomeContractFile;
    "driver-preamble.sh" = internals.driverPreambleFile;
    "agent-paths-preamble.sh" = internals.agentPathsPreambleFile;
    "fragment-registry.sh" = internals.fragmentRegistryFile;
    "fragments-registry.json" = registryJson;
    "prompt-contract-registry.json" = promptContractRegistryJson;
    "prompt-contract-parity-fixtures.json" = promptContractParityFixtures;
    "research-verdicts-parity.json" = researchVerdictsParity;
    "launcher-run-input.json" = internals.runInputDocumentFile;
    "launcher-run-input-docker.json" = dockerHarness.internals.runInputDocumentFile;
    "launcher-run-input-bwrap.json" = bwrapHarness.internals.runInputDocumentFile;
    "launcher-run-input-butler.json" = butlerHarness.internals.runInputDocumentFile;
    "prompts" = internals.promptDir;
    "nix-checks-skill.md" = ../skills/nix-checks/SKILL.md;
  };
in
pkgs.runCommand "seam-fixtures" { passthru = { inherit files; }; } ''
  mkdir -p $out
  ${pkgs.lib.concatStringsSep "\n" (
    pkgs.lib.mapAttrsToList (name: path: "cp -r ${path} $out/${name}") files
  )}
''
