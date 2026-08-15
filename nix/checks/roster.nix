# Eval-level checks for lib/roster.nix's normalizeRoster (issue #2152 slice
# A): pins name-validation, duplicate-name rejection, and the promptFile
# default-injection contract before any Driver ever consumes a roster.
{
  pkgs,
  ...
}:
let
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib) assertMsg mapAttrs;
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

  # Issue #2506: lib/roster-schema-defaults.nix's rosterDefaults table is the
  # roster's single root -- each default-roster agent name maps to its
  # lib/env-schema.nix schemaKey and its fixed default effort (issue #2386).
  roster-schema-defaults-exports-roster-defaults =
    let
      rosterDefaults =
        (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
      expected = {
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
      mismatches = builtins.filter (n: rosterDefaults.${n} != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg
      (
        builtins.attrNames rosterDefaults == [
          "filer"
          "reviewer"
          "scout"
          "worker"
        ]
      )
      "rosterDefaults must have exactly the four keys scout/reviewer/filer/worker, got: ${builtins.toJSON (builtins.attrNames rosterDefaults)}";
    assert assertMsg (mismatches == [ ])
      "rosterDefaults entries must match the expected schemaKey/effort per agent, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-schema-defaults-exports-roster-defaults" { } "touch $out";

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
          filer = "claude-haiku-4-5-20251001";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      # Deliberate carve-out (issue #2506 AC5): reads the schema directly
      # rather than through readSchemaDefaults, so this pin can't go
      # vacuous by comparing the helper under test against itself.
      schema = import ../../lib/env-schema.nix;
    in
    assert assertMsg ((byName "filer").model == "claude-haiku-4-5-20251001")
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
