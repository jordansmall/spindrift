# Single hand-typed anti-vacuity root (issue #2514) for every bump-sensitive
# expected-default-model assertion. Eleven places across the Nix checks used
# to hand-type the literal expected default model per agent independently --
# deliberately NOT derived from lib/env-schema.nix, so that a schema-default
# bump is caught by these checks instead of silently validating against
# itself. This file collapses those eleven inline literals down to ONE
# hand-typed source; nix/checks/image.nix and nix/checks/equivalence.nix
# import it directly instead of re-typing the values, and
# nix/checks/schema-drift.nix's default-model-fixture-schema-sync check
# proves this file itself hasn't drifted from lib/env-schema.nix.
#
# schemaDefaults' keys match lib/env-schema.nix's own attrset keys 1:1 (model,
# scoutModel, reviewModel, filerModel, workerModel), so a check can zip this
# attrset against the real schema key-for-key.
#
# dogfoodPins is kept as a SEPARATE attrset from schemaDefaults, not merged
# in, because nix/dogfood-defaults.nix's roster (rosterLib.defaultRoster
# { models = { filer = "claude-haiku-4-5-20251001"; }; }) pins only `filer`
# locally -- scout, reviewer, and worker are unmentioned in that roster and
# resolve through schemaDefaults instead. Keeping the two attrsets distinct
# lets a consumer tell, per key, whether an expected value traces back to a
# dogfood-local pin or to the schema's own baked-in default (issue #2514
# AC4).
#
# nix run .#regen will render this fixture into further consumer forms in a
# later slice (a bats fixture, launcher testdata, and a docs block) -- not
# implemented here.
{
  schemaDefaults = {
    model = "claude-sonnet-5";
    scoutModel = "claude-haiku-4-5-20251001";
    reviewModel = "claude-opus-5";
    filerModel = "";
    workerModel = "claude-sonnet-5";
  };
  dogfoodPins = {
    filer = "claude-haiku-4-5-20251001";
  };
}
