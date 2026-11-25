# The first-party Connectors' own knob declarations, read straight from their
# registration so a generated artifact never restates a Connector's env name or
# doc. A Connector registered as data (lib/connector-data.nix) is read from its
# `data`; one that still ships a flake-parts module is applied to `{ inherit
# lib; }`. Connector knobs are `flakeOption = false`, so they never enter
# lib/env-schema.nix. Only Connectors that declare a `backend` row are kept.
#
# Laziness protects the stub core: neither reader forces it. A data reader never
# forces `module`, and a module's uses of the core (`imports` and the
# `backend.module` lambda) are not forced either, since `filterAttrs` only tests
# `backend` presence and `module` is never applied. Forcing either would break
# against the `{ }` core.
{ lib }:
let
  stubCore = { };
  connectorData = import ../lib/connector-data.nix;
  # One entry per Connector: { <name> = { knobs; backend; ... }; }.
  connectorRows = map (
    dir: connectorData.rowsOf { inherit lib; } ((import dir).lib.connector { core = stubCore; })
  ) (import ./connector-dirs.nix);
  rows = lib.foldl' (acc: r: acc // r) { } connectorRows;
  backendRows = lib.filterAttrs (_: r: r ? backend) rows;
in
{
  # Connector name -> its module row (`{ knobs; backend; ... }`), backend Connectors only.
  rows = backendRows;
}
