# Single source of truth for defaultRoster's per-agent schema-key/effort
# defaults (lib/roster.nix) -- issues #2386/#2437/#2506. `rosterDefaults`
# maps each default-roster agent name to its lib/env-schema.nix model key
# and its fixed default effort. `readSchemaDefaults` is the one
# schema-defaults reader both tolerance policies this repo needs go
# through: `strict = true` throws on a missing `.default` (the roster's
# four model keys are expected to carry one); `strict = false` falls back
# to `""` (lib/mkHarness.nix's generic sweep over every flakeOption-flagged
# schema entry, most of which have no model concept at all -- e.g.
# devShellName -- and can't guarantee a `.default`). Kept as a separate
# file (not folded into lib/env-schema.nix itself) because that file's
# returned attrset is iterated uniformly as "one entry per knob" by other
# generators (lib/renderers.nix's renderFlagTableGo throws on a non-knob
# key missing `group`, ...) -- adding a non-knob key there would break
# those iterations. roster.nix cannot import lib/mkHarness.nix for this
# either (mkHarness.nix already imports roster.nix, to resolve the
# legacy-knob-derived roster -- the reverse import would be circular), so
# this third file is what both lib/roster.nix and lib/mkHarness.nix import
# directly.
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
  };
  rosterModelKeys = lib.mapAttrs (_: v: v.schemaKey) rosterDefaults;
  # The one schema-defaults reader (issue #2506): `entries` is an attrset of
  # already-resolved schema entries (not schema keys), so every caller --
  # the roster's four model keys resolved through rosterModelKeys below, and
  # mkHarness's every flakeOption-flagged entry -- can hand it a uniform
  # shape.
  readSchemaDefaults =
    { strict }:
    entries:
    lib.mapAttrs (_: e: if strict then e.default else e.default or "") entries;
in
{
  inherit rosterDefaults rosterModelKeys readSchemaDefaults;
  schemaDefaults = readSchemaDefaults { strict = true; } (
    lib.mapAttrs (_: schemaKey: schema.${schemaKey}) rosterModelKeys
  );
}
