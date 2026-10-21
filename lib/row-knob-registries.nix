# The registries lib/env-schema.nix folds row-owned knobs from, each paired
# with the repo directory holding its `<row.name>.nix` files (the `source`
# lib/row-knobs.nix reports collisions against) and `goPackageDir`, which maps
# a row name to the repo-relative Go package directory lib/row-knob-readers.nix
# generates that row's knob reader into. The Chore catalog is one: a row's
# knobs live in lib/chores/<name>.nix, which lib/chore-catalog.nix imports.
[
  {
    source = "lib/backends";
    rows = import ./backends/default.nix;
    goPackageDir = name: "cmd/launcher/internal/forge/${name}";
  }
  {
    source = "lib/chores";
    rows = (import ./chore-catalog.nix).chores;
    # A knob-bearing Chore's Go package is named after it, so its name must
    # not collide with an unrelated internal package; nix/checks/row-knobs.nix
    # fails eval on an existing dir its ownedChorePackages does not pin, so a
    # new knob-bearing Chore pins its name there once regen creates the dir.
    goPackageDir = name: "cmd/launcher/internal/${name}";
  }
  {
    source = "lib/runners";
    rows = import ./runners/default.nix;
    goPackageDir = name: "cmd/launcher/internal/runner/${name}";
  }
]
