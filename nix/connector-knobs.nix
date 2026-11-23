# The first-party Connectors' own knob declarations, read straight from their
# modules so a generated artifact never restates a Connector's env name or doc.
# Connector knobs are `flakeOption = false`, so they never enter
# lib/env-schema.nix. Only Connectors that declare a `backend` row are kept.
#
# Laziness protects the stub core: the module's uses of it (`imports` and the
# `backend.module` lambda) are never forced here, since `filterAttrs` only tests
# `backend` presence and `module` is never applied. Forcing either would break
# against the `{ }` core.
{ lib }:
let
  stubCore = { };
  modules = map (
    dir: ((import dir).lib.connector { core = stubCore; }).flakeModule { inherit lib; }
  ) (import ./connector-dirs.nix);
  # One entry per Connector module: { <name> = { knobs; backend; ... }; }.
  rows = lib.foldl' (acc: m: acc // (m.spindrift.connectors or { })) { } modules;
  backendRows = lib.filterAttrs (_: r: r ? backend) rows;
in
{
  # Every backend Connector's knob entries, keyed by knob name.
  knobs = lib.foldl' (acc: r: acc // (r.knobs or { })) { } (lib.attrValues backendRows);
  # backend name -> env var of the knob named by `backend.credential`.
  credentialEnv = lib.mapAttrs (_: r: r.knobs.${r.backend.credential}.env) backendRows;
}
