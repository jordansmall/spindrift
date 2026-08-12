# Eval-level checks for lib/roster.nix's normalizeRoster (issue #2152 slice
# A): pins name-validation, duplicate-name rejection, and the promptFile
# default-injection contract before any Driver ever consumes a roster.
{
  pkgs,
  nixpkgs,
  system,
  ...
}:
let
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib) assertMsg;
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
    assert assertMsg (
      !result.success
    ) "normalizeRoster must throw on an entry that omits name";
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
    assert assertMsg (
      result.success
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
    assert assertMsg (
      !result.success
    ) "normalizeRoster must throw when two entries share a name";
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
    assert assertMsg (
      result.success
    ) "normalizeRoster must not throw on an entry omitting promptFile";
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
    assert assertMsg (
      result.success
    ) "normalizeRoster must not throw on an entry with explicit promptFile";
    assert assertMsg ((builtins.elemAt result.value 0).promptFile == "custom-scout.md")
      "normalizeRoster must preserve an explicit promptFile, got: ${builtins.toJSON result.value}";
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
    assert assertMsg (
      result.success
    ) "normalizeRoster must not throw on an empty roster";
    assert assertMsg (
      result.value == [ ]
    ) "normalizeRoster [] must return [], got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-allows-empty" { } "touch $out";

  # Issue #2386: defaultRoster ships a fixed default `effort` per agent
  # (scout=medium, reviewer=high, filer=medium, worker=high) as a literal on
  # each entry.
  roster-default-roster-ships-effort-defaults =
    let
      roster = rosterLib.defaultRoster {
        scoutModel = "m";
        reviewModel = "m";
        filerModel = "m";
        workerModel = "m";
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      expected = {
        scout = "medium";
        reviewer = "high";
        filer = "medium";
        worker = "high";
      };
      mismatches = builtins.filter (n: (byName n).effort != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "defaultRoster must ship the fixed default effort per agent (scout=medium, reviewer=high, filer=medium, worker=high), mismatched: ${builtins.toJSON mismatches}";
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
  # four current defaults so the two can never silently drift.
  roster-schema-defaults-helper-matches-env-schema =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      schema = import ../../lib/env-schema.nix;
      expected = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      mismatches = builtins.filter (n: helper.schemaDefaults.${n} != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "lib/roster-schema-defaults.nix schemaDefaults must match lib/env-schema.nix's four current defaults, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-schema-defaults-helper-matches-env-schema" { } "touch $out";

  # Issue #2437: lib/roster-schema-defaults.nix's throw-on-missing
  # schemaDefaults and lib/mkHarness.nix's independently-computed, `or
  # ""`-tolerant generic schemaDefaults are two separate derivations over
  # the same lib/env-schema.nix. A schema-key rename or a changed `.default`
  # could be caught by one (the roster helper throws on a missing key) and
  # silently swallowed by the other (mkHarness falls back to ""), or vice
  # versa, with nothing today pinning the two to agree. Import
  # lib/mkHarness.nix directly (same pattern as equivalence.nix's
  # mkharness-rejects-unknown-key) so this pins mkHarness's actual computed
  # `schemaDefaults`, not a local reimplementation of its one-liner, and
  # assert the roster helper's four per-agent defaults match mkHarness's
  # generic defaults for the same underlying schema keys, looked up via
  # rosterModelKeys rather than a third hardcoded mapping.
  roster-schema-defaults-matches-mkharness-schema-defaults =
    let
      rosterHelper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      harness = import ../../lib/mkHarness.nix { inherit nixpkgs system; };
      mismatches = builtins.filter (
        n: rosterHelper.schemaDefaults.${n} != harness.schemaDefaults.${rosterHelper.rosterModelKeys.${n}}
      ) [ "scout" "reviewer" "filer" "worker" ];
    in
    assert assertMsg (mismatches == [ ])
      "lib/roster-schema-defaults.nix schemaDefaults must match lib/mkHarness.nix's independently-computed generic schemaDefaults for the same schema keys, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-schema-defaults-matches-mkharness-schema-defaults" { } "touch $out";
}
