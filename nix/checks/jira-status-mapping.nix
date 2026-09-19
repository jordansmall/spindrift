# Eval-level pins for lib/jira-status-mapping.nix (issue #2539), mirroring
# cmd/launcher/internal/forge/jira/jira.go's ParseStatusMapping. No
# malformed-JSON check: builtins.fromJSON raises a builtin type error that
# builtins.tryEval cannot catch, so forcing it here would abort the whole
# checks-inbox aggregate instead of failing one assert.
{ pkgs, ... }:
let
  jsm = import ../../lib/jira-status-mapping.nix;
  inherit (pkgs.lib) assertMsg;
in
{
  jira-status-mapping-parse-empty-is-noop =
    let
      out = jsm.parse "";
    in
    assert assertMsg (out == { }) "parse \"\" must return an empty attrset";
    pkgs.runCommand "jira-status-mapping-parse-empty-is-noop" { } "touch $out";

  jira-status-mapping-parse-valid =
    let
      json = builtins.toJSON {
        inProgress = "In Progress";
        complete = "Done";
      };
      out = jsm.parse json;
    in
    assert assertMsg (
      out == {
        inProgress = "In Progress";
        complete = "Done";
      }
    ) "parse must return the expected attrset with both keys, values unchanged";
    pkgs.runCommand "jira-status-mapping-parse-valid" { } "touch $out";

  # tryEval exposes only success or failure, never the thrown message text, so
  # this cannot also pin that the message names the four valid states (see
  # nix/checks/drivers.nix's drivers-assert-shape-missing-attribute-throws).
  jira-status-mapping-parse-rejects-unknown-key =
    let
      badJSON = builtins.toJSON { bogus = "x"; };
      result = builtins.tryEval (jsm.parse badJSON);
    in
    assert assertMsg (!result.success) "parse must throw on an unknown key";
    pkgs.runCommand "jira-status-mapping-parse-rejects-unknown-key" { } "touch $out";

  jira-status-mapping-parse-valid-all-keys =
    let
      mapping = {
        dispatchable = "To Do";
        inProgress = "In Progress";
        complete = "Done";
        failed = "Failed";
      };
      out = jsm.parse (builtins.toJSON mapping);
    in
    assert assertMsg (out == mapping) "parse must return all four keys unchanged";
    pkgs.runCommand "jira-status-mapping-parse-valid-all-keys" { } "touch $out";

  # Go's json.Unmarshal of "null" into map[string]string leaves the map nil
  # with no error, so ParseStatusMapping("null") returns an empty mapping
  # rather than erroring.
  jira-status-mapping-parse-null-is-noop =
    let
      out = jsm.parse "null";
    in
    assert assertMsg (out == { }) "parse \"null\" must return an empty attrset";
    pkgs.runCommand "jira-status-mapping-parse-null-is-noop" { } "touch $out";

  # parse has to reject a non-object, non-null payload itself: letting one
  # reach builtins.attrNames raises a type error tryEval cannot catch.
  jira-status-mapping-parse-rejects-non-object =
    let
      arrayResult = builtins.tryEval (
        jsm.parse (
          builtins.toJSON [
            1
            2
          ]
        )
      );
      stringResult = builtins.tryEval (jsm.parse (builtins.toJSON "hi"));
    in
    assert assertMsg (!arrayResult.success) "parse must throw on a JSON array";
    assert assertMsg (!stringResult.success) "parse must throw on a bare JSON string";
    pkgs.runCommand "jira-status-mapping-parse-rejects-non-object" { } "touch $out";

  # Known divergence from the Go side (see lib/jira-status-mapping.nix's
  # header): validate checks keys only, not value types, so a non-string value
  # survives here even though Go's map[string]string unmarshal would reject
  # it. Pinned so a tightened check cannot change this without updating that
  # caveat too.
  jira-status-mapping-parse-allows-non-string-value =
    let
      out = jsm.parse (builtins.toJSON { complete = 5; });
    in
    assert assertMsg (
      out == { complete = 5; }
    ) "parse must pass a non-string value through unchanged (known Go divergence)";
    pkgs.runCommand "jira-status-mapping-parse-allows-non-string-value" { } "touch $out";
}
