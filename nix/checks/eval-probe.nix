# Eval-level pin for nix/eval-probe.nix (issue #5864). The probe lives under
# legacyPackages, which `nix flake check` never evaluates, so a rename of any
# attr the probe forces would break it silently. The assertion forces every
# element of `one`: the list is lazy, so a bare isList would not reach them.
# Forcing `one` costs one lean harness instance (~195k calls), cheap enough to
# stay out of evalHeavyCheckNames.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  probe = import ../eval-probe.nix { inherit nixpkgs system; };
  inherit (pkgs.lib) assertMsg;
in
{
  eval-probe-evaluates =
    assert assertMsg (builtins.all builtins.isString probe.one)
      "nix/eval-probe.nix `one` must evaluate, every forced element a string";
    pkgs.runCommand "eval-probe-evaluates" { } "touch $out";
}
