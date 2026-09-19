# These defaults live outside lib/env-schema.nix because other generators
# iterate that file's attrset as one entry per knob (lib/renderers.nix's
# renderFlagTableGo throws on a non-knob key missing `group`), and outside
# lib/mkHarness.nix because mkHarness already imports roster.nix, so the
# reverse import would be circular. Issues #2386/#2437/#2506.
{ lib }:
let
  schema = import ./env-schema.nix;
  rosterDefaults = {
    scout = {
      schemaKey = "scoutModel";
      effort = "medium";
    };
    reviewer = {
      schemaKey = "reviewModel";
      effort = "high";
    };
    filer = {
      schemaKey = "filerModel";
      effort = "medium";
    };
    worker = {
      schemaKey = "workerModel";
      effort = "high";
    };
    # review-axis has no schema key of its own: lib/roster.nix's defaultRoster
    # resolves its model by tracking the reviewer entry. reviewModel here only
    # keeps this table's key column consistent and keeps nix/checks/roster.nix's
    # env-schema pin total. Issue #3447, effort per ADR 0049.
    "review-axis" = {
      schemaKey = "reviewModel";
      effort = "high";
    };
  };
  rosterModelKeys = lib.mapAttrs (_: v: v.schemaKey) rosterDefaults;
  # `entries` holds already-resolved schema entries, not schema keys, so both
  # callers hand it one shape. strict = false exists for mkHarness's sweep over
  # every flakeOption-flagged entry, most of which have no model concept and so
  # carry no `.default`. Issue #2506.
  readSchemaDefaults =
    { strict }:
    entries:
    lib.mapAttrs (
      n: e:
      if strict then
        e.default
          or (throw "readSchemaDefaults: entry '${n}' is missing .default (strict mode expects every entry to carry one)")
      else
        e.default or ""
    ) entries;
in
{
  inherit rosterDefaults rosterModelKeys readSchemaDefaults;
  schemaDefaults = readSchemaDefaults { strict = true; } (
    lib.mapAttrs (_: schemaKey: schema.${schemaKey}) rosterModelKeys
  );
}
