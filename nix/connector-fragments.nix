# The fragment contributions of the in-repo Connectors (issue #6857), in the
# `{ name; dir; shipped; }` shape lib/fragment-paths.nix's `withConnectors`
# takes: the root-level list the shipped-tree checks and the bats harness use,
# where a consumer flake gets the same list through lib/flakeModule.nix. Sorted
# by name, the module system's attr order, so a direct mkHarness call mirroring
# the dogfood flake gets the identical list.
builtins.sort (a: b: a.name < b.name) (
  builtins.filter (f: f != null) (
    map (dir: ((import dir).lib.connector { core = import ../core; }).fragments or null) (
      import ./connector-dirs.nix
    )
  )
)
