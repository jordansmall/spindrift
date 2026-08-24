# Eval-level checks for lib/roster.nix's normalizeRoster (issue #2152 slice
# A): pins name-validation, duplicate-name rejection, and the promptFile
# default-injection contract before any Driver ever consumes a roster.
{
  pkgs,
  ...
}:
let
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  inherit (pkgs.lib) assertMsg mapAttrs;
  # Shared by the roster-default-roster-by-name-* checks below (issue #2560,
  # non-blocking review finding): pulls a named entry out of a roster, same
  # shape as equivalence.nix's modelOf but returning the whole entry since
  # callers here read both .model and .effort off it.
  entryFor = name: roster: builtins.head (builtins.filter (e: e.name == name) roster);
in
{
  roster-normalize-rejects-invalid-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "Bad_Name";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "normalizeRoster must throw on a name that isn't lowercase-alnum-dash";
    pkgs.runCommand "roster-normalize-rejects-invalid-name" { } "touch $out";

  roster-normalize-rejects-missing-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw on an entry that omits name";
    pkgs.runCommand "roster-normalize-rejects-missing-name" { } "touch $out";

  roster-normalize-accepts-valid-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout-2";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success
    ) "normalizeRoster must not throw on a valid lowercase-alnum-dash name";
    pkgs.runCommand "roster-normalize-accepts-valid-name" { } "touch $out";

  roster-normalize-rejects-duplicate-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m1";
              mode = "subagent";
              description = "d1";
              tools = [ ];
            }
            {
              name = "scout";
              model = "m2";
              mode = "subagent";
              description = "d2";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw when two entries share a name";
    pkgs.runCommand "roster-normalize-rejects-duplicate-name" { } "touch $out";

  roster-normalize-injects-promptfile-default =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success) "normalizeRoster must not throw on an entry omitting promptFile";
    assert assertMsg ((builtins.elemAt result.value 0).promptFile == "scout-prompt.md")
      "normalizeRoster must inject promptFile as <name>-prompt.md when omitted, got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-injects-promptfile-default" { } "touch $out";

  roster-normalize-preserves-promptfile =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              promptFile = "custom-scout.md";
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success
    ) "normalizeRoster must not throw on an entry with explicit promptFile";
    assert assertMsg (
      (builtins.elemAt result.value 0).promptFile == "custom-scout.md"
    ) "normalizeRoster must preserve an explicit promptFile, got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-preserves-promptfile" { } "touch $out";

  roster-normalize-allows-empty =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [ ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success) "normalizeRoster must not throw on an empty roster";
    assert assertMsg (
      result.value == [ ]
    ) "normalizeRoster [] must return [], got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-allows-empty" { } "touch $out";

  # Issue #2506: readSchemaDefaults' `strict` flag must actually discriminate
  # -- `strict = true` throws on an entry missing `.default` (the contract
  # the roster's four schemaDefaults callers depend on). Without this
  # fixture, a reader that ignored `strict` and always fell back to `or ""`
  # would still pass every other check in the repo.
  roster-schema-defaults-strict-throws-on-missing-default =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      result = builtins.tryEval (
        let
          r = helper.readSchemaDefaults { strict = true; } { missing = { }; };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "readSchemaDefaults { strict = true; } must throw on an entry missing .default";
    pkgs.runCommand "roster-schema-defaults-strict-throws-on-missing-default" { } "touch $out";

  # Issue #2506: the other half of the same contract -- `strict = false`
  # must fall back to `""` on an entry missing `.default` instead of
  # throwing, the tolerance mkHarness's flakeOption sweep depends on since
  # most flakeOption-flagged schema entries carry no model concept at all.
  roster-schema-defaults-tolerant-falls-back-on-missing-default =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      result = builtins.tryEval (
        let
          r = helper.readSchemaDefaults { strict = false; } { missing = { }; };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success
    ) "readSchemaDefaults { strict = false; } must not throw on an entry missing .default";
    assert assertMsg (result.value.missing == "")
      "readSchemaDefaults { strict = false; } must fall back to \"\" on an entry missing .default, got: ${builtins.toJSON result.value.missing}";
    pkgs.runCommand "roster-schema-defaults-tolerant-falls-back-on-missing-default" { } "touch $out";

  # Issue #2386: defaultRoster ships a fixed default `effort` per agent,
  # looked up per name from rosterDefaults (issue #2506) rather than a
  # literal on each entry.
  roster-default-roster-ships-effort-defaults =
    let
      roster = rosterLib.defaultRoster {
        scoutModel = "m";
        reviewModel = "m";
        filerModel = "m";
        workerModel = "m";
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      expected =
        mapAttrs (_: v: v.effort)
          (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
      mismatches = builtins.filter (n: (byName n).effort != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "defaultRoster must ship the fixed default effort per agent from rosterDefaults (expected: ${builtins.toJSON expected}), mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-default-roster-ships-effort-defaults" { } "touch $out";

  # Issue #2426: defaultRoster's models attrset sets a named agent's model.
  # Issue #2434: every unmentioned agent instead inherits its
  # lib/env-schema.nix default (filer's own schema default stays empty, so
  # naming a different agent in `models` doesn't accidentally provision it).
  roster-default-roster-models-by-name =
    let
      roster = rosterLib.defaultRoster {
        models = {
          filer = defaultModelFixture.dogfoodPins.filer;
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      # Deliberate carve-out (issue #2506 AC5): reads the schema directly
      # rather than through readSchemaDefaults, so this pin can't go
      # vacuous by comparing the helper under test against itself.
      schema = import ../../lib/env-schema.nix;
    in
    assert assertMsg ((byName "filer").model == defaultModelFixture.dogfoodPins.filer)
      "defaultRoster models.filer must set the filer entry's model, got: ${builtins.toJSON (byName "filer").model}";
    assert assertMsg ((byName "scout").model == schema.scoutModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "scout").model}";
    assert assertMsg ((byName "reviewer").model == schema.reviewModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "reviewer").model}";
    assert assertMsg ((byName "worker").model == schema.workerModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "worker").model}";
    pkgs.runCommand "roster-default-roster-models-by-name" { } "touch $out";

  roster-default-roster-rejects-unknown-model-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            models = {
              typo-agent = "m";
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when models names an agent absent from the roster";
    pkgs.runCommand "roster-default-roster-rejects-unknown-model-name" { } "touch $out";

  # Issue #2426: when both the legacy per-agent knob and models name the same
  # agent, models wins -- the higher-precedence source, per lib/roster.nix's
  # modelFor.
  roster-default-roster-models-overrides-legacy =
    let
      roster = rosterLib.defaultRoster {
        filerModel = "legacy-model";
        models = {
          filer = "models-model";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "filer").model == "models-model")
      "defaultRoster models.filer must win over a same-named legacy filerModel, got: ${builtins.toJSON (byName "filer").model}";
    pkgs.runCommand "roster-default-roster-models-overrides-legacy" { } "touch $out";

  # Issue #2434: an explicitly supplied legacy positional argument still
  # wins over the schema default -- the sentinel-`null` default on
  # scoutModel/reviewModel/filerModel/workerModel only defers to the schema
  # when the caller truly supplied nothing, never when it supplied a value.
  roster-default-roster-legacy-wins-over-schema-default =
    let
      roster = rosterLib.defaultRoster { scoutModel = "explicit-legacy"; };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "explicit-legacy")
      "defaultRoster must let an explicitly supplied legacy scoutModel win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-legacy-wins-over-schema-default" { } "touch $out";

  # Issue #2434 (was #392): an explicit empty string on a legacy positional
  # knob is itself a supplied value, not "not supplied" -- it must keep
  # opting the entry out, the same rung mkHarness.nix's deprecated
  # settings.*Model resolution relies on, even though the name is now
  # eligible to inherit a non-empty schema default.
  roster-default-roster-legacy-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster { scoutModel = ""; };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "")
      "defaultRoster must let an explicit empty legacy scoutModel opt-out win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-legacy-explicit-empty-opts-out" { } "touch $out";

  # Issue #2434: models.<name> = "" is the explicit opt-out (#392) and must
  # keep dropping that entry's model even though the name is now eligible
  # to inherit a non-empty schema default.
  roster-default-roster-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster {
        models = {
          scout = "";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "")
      "defaultRoster must let an explicit models.scout = \"\" opt-out win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-explicit-empty-opts-out" { } "touch $out";

  # Issue #2434: an agent unmentioned in `models` and with no legacy
  # positional argument supplied inherits its model from
  # lib/env-schema.nix's default -- the same default mkHarness's no-roster
  # fallback resolves through `mergedDefaults`.
  roster-default-roster-inherits-schema-default =
    let
      roster = rosterLib.defaultRoster { };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      # Deliberate carve-out (issue #2506 AC5), same rationale as
      # roster-default-roster-models-by-name above: reads the schema
      # directly instead of through readSchemaDefaults.
      schema = import ../../lib/env-schema.nix;
      expected = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      mismatches = builtins.filter (n: (byName n).model != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "defaultRoster {} must inherit each unmentioned agent's model from lib/env-schema.nix's default, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-default-roster-inherits-schema-default" { } "touch $out";

  # Issue #2560: byName.<name>.model sets a named agent's model, the same as
  # `models.<name>` but nested under a per-agent map that can also carry
  # `effort` alongside `model`.
  roster-default-roster-by-name-sets-model =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          filer = {
            model = defaultModelFixture.dogfoodPins.filer;
          };
        };
      };
    in
    assert assertMsg (
      (entryFor "filer" roster).model == defaultModelFixture.dogfoodPins.filer
    ) "defaultRoster byName.filer.model must set the filer entry's model, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-sets-model" { } "touch $out";

  # Issue #2560: byName.<name>.effort overrides that agent's default effort
  # (from rosterDefaults) without disturbing its model.
  roster-default-roster-by-name-sets-effort =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          reviewer = {
            effort = "high";
          };
        };
      };
      schema = import ../../lib/env-schema.nix;
    in
    assert assertMsg ((entryFor "reviewer" roster).effort == "high")
      "defaultRoster byName.reviewer.effort must override the reviewer entry's effort, got: ${builtins.toJSON (entryFor "reviewer" roster).effort}";
    assert assertMsg ((entryFor "reviewer" roster).model == schema.reviewModel.default)
      "defaultRoster byName.reviewer.effort must not disturb the reviewer entry's model, got: ${builtins.toJSON (entryFor "reviewer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-sets-effort" { } "touch $out";

  # Issue #2560: models.<name> is the higher-precedence shorthand and must
  # win over byName.<name>.model when both name the same agent.
  roster-default-roster-models-overrides-by-name =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          filer = {
            model = "byname-model";
          };
        };
        models = {
          filer = "models-model";
        };
      };
    in
    assert assertMsg ((entryFor "filer" roster).model == "models-model")
      "defaultRoster models.filer must win over a same-named byName.filer.model, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-models-overrides-by-name" { } "touch $out";

  # Issue #2560: byName.<name>.model wins over a same-named legacy positional
  # knob (e.g. filerModel) -- byName sits between models and the legacy
  # knobs in the precedence chain.
  roster-default-roster-by-name-overrides-legacy =
    let
      roster = rosterLib.defaultRoster {
        filerModel = "legacy-model";
        byName = {
          filer = {
            model = "byname-model";
          };
        };
      };
    in
    assert assertMsg ((entryFor "filer" roster).model == "byname-model")
      "defaultRoster byName.filer.model must win over a same-named legacy filerModel, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-overrides-legacy" { } "touch $out";

  # Issue #2560: an unknown top-level key in byName must throw, mirroring
  # models' unknownNames guard.
  roster-default-roster-rejects-unknown-by-name-key =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName = {
              typo-agent = {
                model = "m";
              };
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when byName names an agent absent from the roster";
    pkgs.runCommand "roster-default-roster-rejects-unknown-by-name-key" { } "touch $out";

  # Issue #2560: byName is a closed attrset -- only `model` and `effort` are
  # accepted per agent. mode/tools/prompt etc. must throw, since those stay
  # roster-only.
  roster-default-roster-rejects-unknown-by-name-field =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName = {
              filer = {
                mode = "x";
              };
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when a byName.<name> value carries a field other than model/effort";
    pkgs.runCommand "roster-default-roster-rejects-unknown-by-name-field" { } "touch $out";

  # Issue #2560: byName.<name>.model = "" is the explicit opt-out (#392) and
  # must win over the schema default, mirroring
  # roster-default-roster-explicit-empty-opts-out for models.
  roster-default-roster-by-name-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          scout = {
            model = "";
          };
        };
      };
    in
    assert assertMsg ((entryFor "scout" roster).model == "")
      "defaultRoster must let an explicit byName.scout.model = \"\" opt-out win over the schema default, got: ${builtins.toJSON (entryFor "scout" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-explicit-empty-opts-out" { } "touch $out";

  # Issue #2560 (non-blocking review finding): unlike model = "", effort = ""
  # is not a documented opt-out -- it's accepted and silently overrides
  # defaultRoster's own per-agent default effort with an empty string
  # (docs/reference.md's "Subagent roster" section). Pins that it's accepted
  # (doesn't throw) and that it produces exactly "", not the schema default,
  # so a future change can't silently make it fall back to the default
  # instead.
  roster-default-roster-by-name-explicit-empty-effort-overrides-default =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          reviewer = {
            effort = "";
          };
        };
      };
    in
    assert assertMsg ((entryFor "reviewer" roster).effort == "")
      "defaultRoster must let an explicit byName.reviewer.effort = \"\" override the schema default effort with an empty string (not fall back to it), got: ${builtins.toJSON (entryFor "reviewer" roster).effort}";
    pkgs.runCommand "roster-default-roster-by-name-explicit-empty-effort-overrides-default" { }
      "touch $out";

  # Issue #2560 (non-blocking review finding): defaultRoster's unknown-field
  # scan calls builtins.attrNames on each byName.<name> value. On the raw
  # rosterLib/mkHarness call path (unlike the flakeModule path, which is
  # type-guarded by a types.submodule option), nothing stops a caller from
  # passing a non-attrset there, e.g. byName.filer = "oops". Pins that this
  # throws (rather than propagating whatever error builtins.attrNames itself
  # produces) so a future refactor can't silently make this path stop
  # throwing entirely.
  roster-default-roster-by-name-non-attrset-value-throws =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName.filer = "oops";
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "defaultRoster must throw when byName.<name> is not an attribute set (e.g. byName.filer = \"oops\")";
    pkgs.runCommand "roster-default-roster-by-name-non-attrset-value-throws" { } "touch $out";

  # Issue #2437: lib/roster-schema-defaults.nix is the single source of
  # truth for defaultRoster's roster-name -> schema-key model defaults.
  # Pin its schemaDefaults output directly against lib/env-schema.nix's
  # four current defaults so the two can never silently drift. `expected`
  # below mirrors roster-default-roster-inherits-schema-default's mapping
  # on purpose: that check pins defaultRoster's output, this one pins the
  # helper's output one level down.
  roster-schema-defaults-helper-matches-env-schema =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      # Same carve-out as above: a direct schema read, not through
      # readSchemaDefaults, so this pin can't go vacuous.
      schema = import ../../lib/env-schema.nix;
      expected = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      mismatches = builtins.filter (n: helper.schemaDefaults.${n} != expected.${n}) (
        builtins.attrNames helper.rosterModelKeys
      );
    in
    assert assertMsg (mismatches == [ ])
      "lib/roster-schema-defaults.nix schemaDefaults must match lib/env-schema.nix's four current defaults, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-schema-defaults-helper-matches-env-schema" { } "touch $out";
}
