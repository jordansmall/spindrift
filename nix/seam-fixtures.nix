# The rendered artifacts the Go seam tests (issue #4280) read as fixtures:
# a pure copy of files the bats harness already renders, no test logic here.
# The names are pinned against cmd/launcher/internal/seamtest/fixtures.json by
# nix/checks/seam-fixtures.nix.
# There is deliberately no "code-comments" contract fixture: that policy was
# inlined into the prompts (#3505, file removed in 78f2611f), so no rendered
# artifact exists to copy.
{ pkgs, batsHarness }:
let
  inherit (batsHarness) internals;
  files = {
    "outcome-contract.md" = internals.outcomeContractFile;
    "comms-contract.md" = internals.commsContractFile;
    "check-contract.md" = internals.checkContractFile;
    "research-outcome-contract.md" = internals.researchOutcomeContractFile;
    "driver-preamble.sh" = internals.driverPreambleFile;
    "agent-paths-preamble.sh" = internals.agentPathsPreambleFile;
    "fragment-registry.sh" = internals.fragmentRegistryFile;
    "launcher-run-input.json" = internals.runInputDocumentFile;
  };
in
pkgs.runCommand "seam-fixtures" { passthru = { inherit files; }; } ''
  mkdir -p $out
  ${pkgs.lib.concatStringsSep "\n" (
    pkgs.lib.mapAttrsToList (name: path: "cp ${path} $out/${name}") files
  )}
''
