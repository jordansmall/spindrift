# The argument set `row-knobs.nix`'s `fold` and `collisions` take for a list of
# Connector rows: the static registries plus the Connectors' own, so
# lib/flakeModule.nix and nix/checks/connectors.nix fold one shape.
rows:
let
  staticRegistries = import ./row-knob-registries.nix;
  staticKnobKeys = builtins.concatMap (
    registry: builtins.concatMap (row: builtins.attrNames (row.knobs or { })) registry.rows
  ) staticRegistries;
in
{
  # `env-schema.nix` is already folded over the static registries; strip their
  # knobs so the fold reports a clash against the real core row file.
  shared = removeAttrs (import ./env-schema.nix) staticKnobKeys;
  sharedSource = "lib/env-schema.nix";
  registries = staticRegistries ++ [
    {
      sourceOf = name: "spindrift.connectors.${name}";
      inherit rows;
    }
  ];
}
