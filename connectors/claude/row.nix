# claude's Driver row: core's own row plus the wasm that replays a Pass log in
# shadow mode. `module` is lazy (a function of pkgs), so the wasm is built only
# for a Harness whose row sets `shadow`.
{ core, lib }:
(core.lib.driverEntries { inherit lib; }).claude
// {
  module = pkgs: "${import ./package.nix { inherit pkgs core; }}/claude.wasm";
}
