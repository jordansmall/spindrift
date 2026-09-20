# Two checks (nix/checks/schema-drift.nix's regen-app-wiring, issue #3128, and
# nix/checks/promptassembly.nix's regen-goldens-app-wiring, issue #2951) guard
# the identical shape: flake.nix's top-level apps.<name> must resolve to the
# same derivation this check itself built. Each `*Reason` is a sentence
# fragment -- what breaks, in the caller's own words -- that the shared
# diagnostic tail continues, so it takes no trailing punctuation.
# nix/checks/equivalence.nix's dogfood-bwrap-app-wiring is deliberately not a
# caller: its program path comes from a fixture, not from a derivation the
# check itself built.
{ pkgs }:
{
  name,
  apps,
  package,
  exposedReason,
  sameEnvReason,
}:
let
  inherit (pkgs.lib) assertMsg;
  expectedProgram = "${package}/bin/${name}";
in
assert assertMsg (builtins.hasAttr name apps)
  "${exposedReason}, got top-level app names: ${builtins.toJSON (builtins.attrNames apps)}";
assert assertMsg (
  apps.${name}.type == "app"
) "flake.nix's top-level apps.${name} must be a real app, got: ${builtins.toJSON apps.${name}}";
assert assertMsg (
  apps.${name}.program == expectedProgram
) "${sameEnvReason}: ${apps.${name}.program} != ${expectedProgram}";
pkgs.runCommand "${name}-app-wiring" { } ''
  [ -x ${expectedProgram} ]
  touch $out
''
