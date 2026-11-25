# The connectorname Connector as data: its Driver row. Core turns this into the
# `spindrift.connectors.connectorname` row. The Box loads the Driver in-process
# only when the batteries flake's own build of this wasm is the row's `module`;
# mkHarness derives that mark, a row cannot set it.
{ core }:
{
  name = "connectorname";
  driver = import ./row.nix { inherit core; };
}
