# opencode's Driver row: core's own row plus the wasm that is opencode's host
# half. `module` is a function of pkgs so the host and the Box each build it
# with their own pkgs.
{ core, lib }:
(core.lib.driverEntries { inherit lib; }).opencode
// {
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/opencode.wasm";
}
