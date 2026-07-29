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
}
