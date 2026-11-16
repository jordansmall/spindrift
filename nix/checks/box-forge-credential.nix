# Issue #7140: BOX_FORGE_CREDENTIAL=none is an opt-in for a read-only Box and
# is meaningless under read-write, where the Box writes with its own token.
# lib/mkHarness.nix throws at eval time for that pairing; these cases go
# through the real mkHarness.nix entry point (see read-only-capability.nix).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg;
  inherit (import ../../lib/builtins-compat.nix) hasInfix;

  mkHarnessWith =
    defaults:
    (import ../../lib/mkHarness.nix {
      inherit nixpkgs system;
      packages = p: [ p.hello ];
      inherit defaults;
    });
  evals =
    defaults:
    (builtins.tryEval (builtins.seq (mkHarnessWith defaults).spindrift.drvPath "reached")).success;
in
{
  box-forge-credential-none-rejects-read-write =
    assert assertMsg
      (
        !evals {
          boxForgeAndIssueAccess = "read-write";
          boxForgeCredential = "none";
        }
      )
      "mkHarness.nix must throw when BOX_FORGE_CREDENTIAL=none and BOX_FORGE_AND_ISSUE_ACCESS=read-write";
    # tryEval discards the message, so pin its stable name in the source.
    assert assertMsg
      (hasInfix "spindrift: BOX_FORGE_CREDENTIAL=none requires BOX_FORGE_AND_ISSUE_ACCESS=read-only" (
        builtins.readFile ../../lib/mkHarness.nix
      ))
      "lib/mkHarness.nix lost the named BOX_FORGE_CREDENTIAL=none error";
    pkgs.runCommand "box-forge-credential-none-rejects-read-write" { } "touch $out";

  box-forge-credential-none-accepts-read-only =
    assert assertMsg
      (evals {
        boxForgeAndIssueAccess = "read-only";
        boxForgeCredential = "none";
      })
      "mkHarness.nix must not throw when BOX_FORGE_CREDENTIAL=none and BOX_FORGE_AND_ISSUE_ACCESS=read-only";
    pkgs.runCommand "box-forge-credential-none-accepts-read-only" { } "touch $out";

  box-forge-credential-forwarded-accepts-both-access-modes =
    assert assertMsg
      (
        evals {
          boxForgeAndIssueAccess = "read-write";
          boxForgeCredential = "forwarded";
        }
        && evals { boxForgeCredential = "forwarded"; }
        && evals { }
      )
      "mkHarness.nix must not throw for BOX_FORGE_CREDENTIAL=forwarded or unset under either access mode";
    pkgs.runCommand "box-forge-credential-forwarded-accepts-both-access-modes" { } "touch $out";
}
