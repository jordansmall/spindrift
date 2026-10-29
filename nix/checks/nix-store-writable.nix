# Eval-level pins for the nixStoreWritable / nixInBox coherence assert (issue
# #6136): a writable /nix/store needs the in-box nix the image bakes, so the
# pair nixInBox = false + nixStoreWritable = true must throw at eval time on
# every runner. `evaluates` forces only `.spindrift.drvPath`, which walks the
# assert chain without paying for a real build (see
# nix/checks/read-only-capability.nix).
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  inherit (pkgs.lib) assertMsg concatMap;
  runners = import ../../lib/runners/index.nix { };
  runtimes = concatMap (row: row.runtimes) runners.rows;
  # nixInBox / nixStoreWritable / runtime are mkHarness function args, not
  # schema `defaults`.
  evaluates =
    args:
    (builtins.tryEval (
      builtins.seq
        (import ../../lib/mkHarness.nix (
          {
            inherit nixpkgs system;
            packages = p: [ p.hello ];
          }
          // args
        )).spindrift.drvPath
        "reached"
    )).success;
in
{
  nix-store-writable-without-nix-in-box-throws =
    let
      accepted = builtins.filter (
        runtime:
        evaluates {
          inherit runtime;
          nixInBox = false;
          nixStoreWritable = true;
        }
      ) runtimes;
    in
    assert assertMsg (accepted == [ ])
      "mkHarness.nix must throw when nixStoreWritable is set with nixInBox = false, but accepted on runtime(s): ${builtins.concatStringsSep ", " accepted}";
    pkgs.runCommand "nix-store-writable-without-nix-in-box-throws" { } "touch $out";

  nix-store-writable-with-nix-in-box-does-not-throw =
    assert assertMsg (builtins.all (
      runtime:
      evaluates {
        inherit runtime;
        nixInBox = true;
        nixStoreWritable = true;
      }
    ) runtimes) "mkHarness.nix must not throw when nixStoreWritable is set with nixInBox = true";
    pkgs.runCommand "nix-store-writable-with-nix-in-box-does-not-throw" { } "touch $out";

  # The lean image (nixInBox = false) alone, with the store left read-only, is
  # the documented nix-free configuration.
  nix-in-box-off-alone-does-not-throw =
    assert assertMsg (builtins.all (
      runtime:
      evaluates {
        inherit runtime;
        nixInBox = false;
      }
    ) runtimes) "mkHarness.nix must not throw when nixInBox = false and nixStoreWritable is left unset";
    pkgs.runCommand "nix-in-box-off-alone-does-not-throw" { } "touch $out";
}
