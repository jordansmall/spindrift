# The registries lib/env-schema.nix folds row-owned knobs from, each paired
# with the repo directory holding its `<row.name>.nix` files (the `source`
# lib/row-knobs.nix reports collisions against) and `goPackageDir`, which maps
# a row name to the repo-relative Go package directory lib/row-knob-readers.nix
# generates that row's knob reader into.
[
  {
    source = "lib/backends";
    rows = import ./backends/default.nix;
    goPackageDir = name: "cmd/launcher/internal/forge/${name}";
  }
]
