# The in-repo Connectors' fragment contributions (issue #6857) in the shape
# lib/fragment-paths.nix's `withConnectors` takes, for the shipped-tree checks
# and the bats harness; a consumer flake gets them via lib/flakeModule.nix.
# Sorted by name, the module system's attr order, so a direct mkHarness call
# mirroring the dogfood flake gets the identical list.
builtins.sort (a: b: a.name < b.name) (
  builtins.filter (f: f != null) (
    map (dir: ((import dir).lib.connector { core = import ../core; }).fragments or null) (
      import ./connector-dirs.nix
    )
  )
)
