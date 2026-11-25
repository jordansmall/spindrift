# The opencode Connector as data: core turns it into the
# `spindrift.connectors.opencode` row. The native opencode Strategy stays
# authoritative: the row (./row.nix) is core's own row plus `module` (the
# wasm), and is neither `primary` nor `shadow` by default, so a Consumer opts
# into `shadow` or `primary` to run the Connector. The Driver's in-box half,
# credential rules and knobs are unchanged and stay in core.
{ core }:
{
  name = "opencode";
  driver = lib: import ./row.nix { inherit core lib; };
}
