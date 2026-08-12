# Single source of truth for defaultRoster's per-agent model schema
# defaults (lib/roster.nix) -- issue #2437. Deliberately narrow and
# throw-on-missing (the four roster model keys are all asserted to carry
# an explicit `.default` in lib/env-schema.nix), unlike
# lib/mkHarness.nix's generic, `or ""`-tolerant schemaDefaults, which
# spans every flakeOption-flagged schema entry -- most of which have no
# model concept at all (e.g. devShellName) and can't guarantee a
# `.default`. Kept as a separate file (not folded into lib/env-schema.nix
# itself) because that file's returned attrset is iterated uniformly as
# "one entry per knob" by other generators (lib/renderers.nix, the
# harness.env.example generator, ...) -- adding a non-knob key there
# would corrupt those iterations. mkHarness.nix cannot import roster.nix
# for this either (lib/mkHarness.nix:316 already imports roster.nix ->
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
