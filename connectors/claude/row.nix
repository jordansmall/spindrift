# claude's Driver row: core's own row plus the wasm that is claude's Driver.
# mkHarness always renders this row. `module` is a function of pkgs so the host
# and the Box each build it with their own pkgs.
{ core, lib }:
(core.lib.driverEntries { inherit lib; }).claude
// {
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/claude.wasm";
}
