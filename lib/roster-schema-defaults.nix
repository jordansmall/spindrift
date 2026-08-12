# Single source of truth for defaultRoster's per-agent model schema
# defaults (lib/roster.nix) -- issue #2437. Deliberately narrow and
# throw-on-missing (the four roster model keys are expected to carry an
# explicit `.default` in lib/env-schema.nix; nothing asserts this up
# front, a key missing one just throws on attribute access here), unlike
# lib/mkHarness.nix's generic, `or ""`-tolerant schemaDefaults, which
# spans every flakeOption-flagged schema entry -- most of which have no
# model concept at all (e.g. devShellName) and can't guarantee a
# `.default`. Kept as a separate file (not folded into lib/env-schema.nix
# itself) because that file's returned attrset is iterated uniformly as
# "one entry per knob" by other generators (lib/renderers.nix's
# renderFlagTableGo throws on a non-knob key missing `group`, ...) --
# adding a non-knob key there would break those iterations. roster.nix
# cannot import mkHarness.nix
# for this either (mkHarness.nix already imports roster.nix, to resolve
# the legacy-knob-derived roster -- the reverse import would be
# circular), so this third file is what lib/roster.nix imports directly.
# lib/mkHarness.nix deliberately does NOT reuse this helper -- it keeps its
# own separate generic, `or ""`-tolerant schemaDefaults derivation instead
# (see mkHarness.nix's own comment for why); nix/checks/roster.nix pins the
# two derivations to agree.
{ lib }:
let
  schema = import ./env-schema.nix;
  # roster-entry name -> lib/env-schema.nix key
  rosterModelKeys = {
    scout = "scoutModel";
    reviewer = "reviewModel";
    filer = "filerModel";
    worker = "workerModel";
  };
in
{
  inherit rosterModelKeys;
  schemaDefaults = lib.mapAttrs (_: schemaKey: schema.${schemaKey}.default) rosterModelKeys;
}
