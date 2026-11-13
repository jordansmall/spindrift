{
  description = "spindrift jira Connector: the Jira tracker read path, compiled to wasm";

  # Pinned for standalone use only. The in-tree checks call the entry point
  # with the checkout's own core and never fetch this input; a batteries root
  # passes its own core the same way.
  inputs.core.url = "github:jordansmall/spindrift?dir=core";

  outputs =
    { core, ... }:
    let
      entry = import ./.;
      connector = entry.lib.connector { inherit core; };
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
    in
    entry
    // {
      flakeModules.default = connector.flakeModule;
      packages = builtins.listToAttrs (
        map (system: {
          name = system;
          value.default = connector.package core.inputs.nixpkgs.legacyPackages.${system};
        }) systems
      );
      checks = core.lib.connectorChecks {
        inherit (core.inputs) nixpkgs;
        inherit connector systems;
        name = "jira";
        src = ./.;
      };
    };
}
