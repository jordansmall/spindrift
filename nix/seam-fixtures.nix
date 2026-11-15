# The rendered artifacts the Go seam tests (issue #4280) read as fixtures:
# a pure copy of what the bats harness already renders (plus the repo's
# nix-checks skill, which the Go check sandbox cannot reach by relative path,
# and each Driver's baked agent-files tree),
# no test logic here.
# The names are pinned against cmd/launcher/internal/seamtest/fixtures.json by
# nix/checks/seam-fixtures.nix.
# There is deliberately no "code-comments" contract fixture: that policy was
# inlined into the prompts (#3505, file removed in 78f2611f), so no rendered
# artifact exists to copy.
# Store paths the fixtures name but do not carry (the run-input docs discard
# their artifacts' context) are rewritten to the zero-hash sentinel: a Box runs
# with sandbox=false and sees the whole store, CI's sandbox does not, so a real
# path passes in one and breaks in the other (issue #7082). A seam test that
# needs a present artifact supplies its own through its input document.
{
  pkgs,
  batsHarness,
  dockerHarness,
  bwrapHarness,
  butlerHarness,
  opencodeHarness,
}:
let
  inherit (batsHarness) internals;
  # The registries box hands prompt assembly, so the Go golden test assembles
  # from the exact bytes the goldens were produced under.
  registryJson = pkgs.writeText "fragments-registry.json" (
    builtins.toJSON (import ../lib/fragments.nix)
  );
  gateVocabularyJson = pkgs.writeText "gate-vocabulary.json" (
    builtins.toJSON (builtins.attrNames (import ../lib/gate-vocabulary.nix).gates)
  );
  promptContractRegistryJson = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).validateMarkers
  );
  forbiddenMarkersRegistryJson = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).forbiddenMarkers
  );
  researchVerdictsParity = import ./research-verdicts-parity.nix { inherit pkgs; };
  promptContractParityFixtures = pkgs.writeText "prompt-contract-parity-fixtures.json" (
    builtins.toJSON (import ../lib/prompt-contract.nix).parityFixtures
  );
  sentinelHash = "00000000000000000000000000000000";
  storePathRegex = "/nix/store/[0-9a-z]{32}-[0-9a-zA-Z+._?=-]+";
  files = {
    "outcome-contract.md" = internals.outcomeContractFile;
    "comms-contract.md" = internals.commsContractFile;
    "check-contract.md" = internals.checkContractFile;
    "research-outcome-contract.md" = internals.researchOutcomeContractFile;
    "driver-preamble.sh" = internals.driverPreambleFile;
    "agent-paths-preamble.sh" = internals.agentPathsPreambleFile;
    "fragments-registry.json" = registryJson;
    "forbidden-markers-registry.json" = forbiddenMarkersRegistryJson;
    "gate-vocabulary.json" = gateVocabularyJson;
    "prompt-contract-registry.json" = promptContractRegistryJson;
    "prompt-contract-parity-fixtures.json" = promptContractParityFixtures;
    "research-verdicts-parity.json" = researchVerdictsParity;
    "launcher-run-input.json" = internals.runInputDocumentFile;
    "launcher-run-input-docker.json" = dockerHarness.internals.runInputDocumentFile;
    "launcher-run-input-bwrap.json" = bwrapHarness.internals.runInputDocumentFile;
    "launcher-run-input-butler.json" = butlerHarness.internals.runInputDocumentFile;
    "prompts" = internals.promptDir;
    "agent-files-claude" = internals.homeLayoutDir;
    "agent-files-opencode" = opencodeHarness.internals.homeLayoutDir;
    "nix-checks-skill.md" = ../skills/nix-checks/SKILL.md;
  };
in
pkgs.runCommand "seam-fixtures"
  {
    passthru = {
      inherit files sentinelHash storePathRegex;
    };
  }
  ''
    mkdir -p $out
    ${pkgs.lib.concatStringsSep "\n" (
      pkgs.lib.mapAttrsToList (name: path: "cp -r ${path} $out/${name}") files
    )}
    chmod -R u+w $out
    grep -rhoaE '${storePathRegex}' $out | sort -u \
      | { grep -vxFf ${
        pkgs.closureInfo { rootPaths = builtins.attrValues files; }
      }/store-paths || true; } \
      | sed -E 's|^/nix/store/([0-9a-z]{32})-.*|s#/nix/store/\1-#/nix/store/${sentinelHash}-#g|' \
      >rewrite.sed
    grep -rlaZE '/nix/store/[0-9a-z]{32}-' $out | xargs -0r sed -i -f rewrite.sed
  ''
