# The pi Connector as data: core turns it into the `spindrift.connectors.pi`
# row. The row carries pi's Driver row (./row.nix): core's own facts plus the
# `module` wasm. The native Strategy stays authoritative; the module runs only
# when the row's `shadow` or `primary` option is on. It owns no env knobs; the
# pi Driver's knobs stay in core.
{ core }:
{
  name = "pi";
  driver = lib: import ./row.nix { inherit core lib; };
}
