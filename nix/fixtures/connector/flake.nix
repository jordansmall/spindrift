{
  description = "spindrift fixture Connector: a template for third-party Connector subflakes";

  # Pinned for standalone use only. The in-tree checks call the entry point
  # with the checkout's own core and never fetch this input; a batteries root
  # passes its own core the same way.
  inputs.core.url = "github:jordansmall/spindrift?dir=core";

  outputs =
    { core, ... }:
    let
      entry = import ./.;
    in
    entry // { flakeModules.default = (entry.lib.connector { inherit core; }).flakeModule; };
}
