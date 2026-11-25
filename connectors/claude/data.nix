# The claude Connector as data: core turns it into the
# `spindrift.connectors.claude` row. The row carries claude's Driver row
# (./row.nix): core's own facts plus the `module` wasm. The claude Driver has
# no native Strategy (issue #6799): this Connector is the Driver for every
# Consumer, so `primary` and `shadow` are refused on this row. It owns no env
# knobs yet; the claude Driver's knobs stay in core.
{ core }:
{
  name = "claude";
  driver = lib: import ./row.nix { inherit core lib; };
}
