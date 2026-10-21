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
    filter
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
    "lib/backends/**"
    "lib/chores/**"
    "lib/runners/**"
    "lib/row-knobs.nix"
    "lib/row-knob-registries.nix"
    "flake.nix"
    "nix/dogfood-defaults.nix"
    "lib/default-model-fixture.nix"
  ];
  rowKnobSources = map (r: r.source) (import ../../lib/row-knob-registries.nix);
  uncoveredRowKnobSources = filter (
    s: !(builtins.elem "${s}/**" dogfoodGlobs && builtins.elem "${s}/**" added)
  ) rowKnobSources;
in
{
  # Extending the dogfood must not widen what every consumer gets by default.
  merge-guard-paths-schema-default-unchanged =
    assert assertMsg
      (schemaDefault == ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**,.pi/**")
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

  # A row-knob registry's rows set env knobs (ADR 0068), so a new registry's
  # directory must merge by hand like the existing ones (ADR 0062). Only each
  # `source` dir is checked: a row importing outside it (lib/chore-catalog.nix)
  # is guarded by its own glob above, not seen here; lib/row-knob-fixture.nix
  # is deliberately not a registry.
  merge-guard-paths-dogfood-covers-row-knob-registries =
    assert assertMsg (uncoveredRowKnobSources == [ ])
      "lib/row-knob-registries.nix sources not guarded: ${concatStringsSep ", " uncoveredRowKnobSources}; add `<source>/**` to mergeGuardPaths in nix/dogfood-defaults.nix and to the `added` pin in nix/checks/merge-guard-paths.nix";
    pkgs.runCommand "merge-guard-paths-dogfood-covers-row-knob-registries" { } "touch $out";
}
