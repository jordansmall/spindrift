# The one hand-typed source of expected default models (issue #2514). The Nix
# checks import it rather than re-type the literals, and it is deliberately not
# derived from lib/env-schema.nix, so a schema-default bump fails those checks
# instead of validating against itself; nix/checks/schema-drift.nix proves it
# has not drifted. `nix run .#regen` renders the Go and docs copies.
{
  # Keys match lib/env-schema.nix's attrset keys 1:1, so a check can zip the
  # two key-for-key.
  schemaDefaults = {
    model = "claude-sonnet-5";
    scoutModel = "claude-haiku-4-5-20251001";
    reviewModel = "claude-opus-5";
    filerModel = "";
    workerModel = "claude-sonnet-5";
  };
  # Separate from schemaDefaults, not merged in (issue #2514 AC4):
  # nix/dogfood-defaults.nix pins these and leaves scout on the schema default,
  # so a consumer can tell per key whether a value comes from a local pin or
  # from the schema's own default. `model` is the coordinator, set through the
  # dogfood's `defaults`; the rest are roster entry names.
  dogfoodPins = {
    model = "claude-opus-5-5";
    filer = "claude-haiku-4-5-20251001";
    reviewer = "claude-opus-5-5";
    worker = "claude-sonnet-5-5";
  };
}
