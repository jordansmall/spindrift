# Pins for MERGE_GUARD_PATHS (ADR 0016, ADR 0062, issues #4949 and #4974): the
# shipped schema default stays unchanged, and the dogfood restates it plus the
# globs for behaviour-changing files that must merge by hand whatever their
# origin.
{
  pkgs,
  config,
  ...
}:
let
  inherit (pkgs.lib)
    assertMsg
    all
    concatStringsSep
    splitString
    ;
  schemaDefault = (import ../../lib/env-schema.nix).mergeGuardPaths.default;
  splitGlobs = splitString ",";
  dogfoodGlobs = splitGlobs config.spindrift.git.merge.guardPaths;
  # Deliberately a second copy of nix/dogfood-defaults.nix's list: a pin, so
  # dropping a glob there fails here instead of passing vacuously.
  added = [
    "templates/**"
    "skills/**"
    "fragments/**"
    "lib/chore-catalog.nix"
    "lib/env-schema.nix"
    "flake.nix"
    "nix/dogfood-defaults.nix"
    "lib/default-model-fixture.nix"
  ];
in
{
  # Extending the dogfood must not widen what every consumer gets by default.
  merge-guard-paths-schema-default-unchanged =
    assert assertMsg
      (schemaDefault == ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**")
      "lib/env-schema.nix mergeGuardPaths.default must stay the shipped default; extend the dogfood in nix/dogfood-defaults.nix instead, or update this pin if the shipped default is meant to change";
    pkgs.runCommand "merge-guard-paths-schema-default-unchanged" { } "touch $out";

  merge-guard-paths-dogfood-includes-added-globs =
    assert assertMsg (all (
      g: builtins.elem g dogfoodGlobs
    ) added) "the dogfood's git.merge.guardPaths must include each of: ${concatStringsSep ", " added}";
    pkgs.runCommand "merge-guard-paths-dogfood-includes-added-globs" { } "touch $out";

  # The setting replaces the whole default, so the dogfood must restate it.
  merge-guard-paths-dogfood-keeps-shipped-default =
    assert assertMsg (all (g: builtins.elem g dogfoodGlobs) (
      splitGlobs schemaDefault
    )) "the dogfood's git.merge.guardPaths must still contain every shipped-default glob";
    pkgs.runCommand "merge-guard-paths-dogfood-keeps-shipped-default" { } "touch $out";
}
