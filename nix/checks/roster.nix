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

  # Issue #2426: defaultRoster's models attrset sets a named agent's model
  # and leaves every unmentioned agent at its existing empty-model default.
  roster-default-roster-models-by-name =
    let
      roster = rosterLib.defaultRoster {
        models = {
          filer = "claude-haiku-4-5-20251001";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "filer").model == "claude-haiku-4-5-20251001")
      "defaultRoster models.filer must set the filer entry's model, got: ${builtins.toJSON (byName "filer").model}";
    assert assertMsg ((byName "scout").model == "")
      "defaultRoster must leave an unmentioned name's model empty, got: ${builtins.toJSON (byName "scout").model}";
    assert assertMsg ((byName "reviewer").model == "")
      "defaultRoster must leave an unmentioned name's model empty, got: ${builtins.toJSON (byName "reviewer").model}";
    assert assertMsg ((byName "worker").model == "")
      "defaultRoster must leave an unmentioned name's model empty, got: ${builtins.toJSON (byName "worker").model}";
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
}
