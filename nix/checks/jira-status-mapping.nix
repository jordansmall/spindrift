# Eval-level pins for lib/jira-status-mapping.nix (issue #2539), mirroring
# cmd/launcher/internal/forge/jira/jira.go's ParseStatusMapping. No
# malformed-JSON check: builtins.fromJSON raises a builtin type error that
# builtins.tryEval cannot catch, so forcing it here would abort the whole
# checks-inbox aggregate instead of failing one assert.
{ pkgs, ... }:
let
  jsm = import ../../lib/jira-status-mapping.nix;
  inherit (pkgs.lib) assertMsg splitString;

  # The key list is hand-mirrored from jira.go, the runtime source of truth
  # (issue #2645). Extraction scans spans, not lines, so a gofmt reflow of the
  # map literal does not break it. A missing marker yields [ ] rather than
  # indexing past the split: builtins.elemAt out of bounds is not catchable by
  # tryEval and would abort the whole checks-inbox aggregate.
  jiraGoSrc = builtins.readFile ../../cmd/launcher/internal/forge/jira/jira.go;

  mapMarker = "statusMappingKeys = map[string]forge.DispatchState{";
  errMarker = "JIRA_STATUS_MAPPING: unknown key %q (want one of ";

  extractMapKeys =
    src:
    let
      parts = splitString mapMarker src;
      span = builtins.head (splitString "}" (builtins.elemAt parts 1));
      firstQuoted =
        entry:
        let
          toks = splitString "\"" entry;
        in
        if builtins.length toks >= 3 then [ (builtins.elemAt toks 1) ] else [ ];
    in
    if builtins.length parts < 2 then [ ] else builtins.concatMap firstQuoted (splitString "," span);

  extractErrorKeys =
    src:
    let
      parts = splitString errMarker src;
    in
    if builtins.length parts < 2 then
      [ ]
    else
      splitString ", " (builtins.head (splitString ")" (builtins.elemAt parts 1)));

  sortKeys = builtins.sort builtins.lessThan;

  checkKeys =
    validKeys: src:
    let
      mapKeys = extractMapKeys src;
      errKeys = extractErrorKeys src;
      show = builtins.concatStringsSep ", ";
    in
    assert assertMsg (mapKeys != [ ]) (
      "jira.go statusMappingKeys extraction yielded no keys: extractMapKeys no longer matches "
      + "jira.go's shape — fix the extractor"
    );
    assert assertMsg (errKeys != [ ]) (
      "jira.go unknown-key error extraction yielded no keys: extractErrorKeys no longer matches "
      + "jira.go's shape — fix the extractor"
    );
    assert assertMsg (sortKeys mapKeys == sortKeys validKeys) (
      "lib/jira-status-mapping.nix validKeys [${show validKeys}] drifted from "
      + "jira.go statusMappingKeys [${show mapKeys}]"
    );
    assert assertMsg (sortKeys errKeys == sortKeys mapKeys) (
      "jira.go's unknown-key error lists [${show errKeys}] but statusMappingKeys has [${show mapKeys}]"
    );
    true;

  mapEntry = "\t\"failed\":       forge.Failed,\n";
  errTail = "inProgress, complete, failed)";
  patchGoSrc = pairs: builtins.replaceStrings (map (p: p.from) pairs) (map (p: p.to) pairs) jiraGoSrc;

  # By construction the map and error string are rewritten together (except
  # error-string-only), so the error list stays consistent with the map and
  # only the validKeys comparison diverges.
  driftCases = {
    key-added = [
      {
        from = mapEntry;
        to = mapEntry + "\t\"blocked\":      forge.Failed,\n";
      }
      {
        from = errTail;
        to = "inProgress, complete, failed, blocked)";
      }
    ];
    key-renamed = [
      {
        from = "\"complete\":     forge.Complete";
        to = "\"completed\":    forge.Complete";
      }
      {
        from = errTail;
        to = "inProgress, completed, failed)";
      }
    ];
    key-dropped = [
      {
        from = mapEntry;
        to = "";
      }
      {
        from = errTail;
        to = "inProgress, complete)";
      }
    ];
    error-string-only = [
      {
        from = "dispatchable, inProgress, complete";
        to = "dispatchable, in-progress, complete";
      }
    ];
  };
in
{
  jira-status-mapping-keys-match-go-source =
    assert checkKeys jsm.validKeys jiraGoSrc;
    pkgs.runCommand "jira-status-mapping-keys-match-go-source" { } "touch $out";

  jira-status-mapping-keys-reformat-regression =
    let
      oldLiteral =
        mapMarker
        + "\n\t\"dispatchable\": forge.Dispatchable,\n\t\"inProgress\":   forge.InProgress,\n"
        + "\t\"complete\":     forge.Complete,\n\t\"failed\":       forge.Failed,\n}";
      newLiteral =
        mapMarker
        + "\"dispatchable\": forge.Dispatchable, \"inProgress\": forge.InProgress, "
        + "\"complete\": forge.Complete, \"failed\": forge.Failed}";
      doctored = builtins.replaceStrings [ oldLiteral ] [ newLiteral ] jiraGoSrc;
    in
    assert assertMsg (doctored != jiraGoSrc)
      "jira-status-mapping-keys-reformat-regression: the map literal no longer matches the doctoring pattern; update oldLiteral";
    assert assertMsg (
      sortKeys (extractMapKeys doctored) == sortKeys jsm.validKeys
    ) "extractMapKeys must still find all four keys in a one-line map literal";
    assert assertMsg (builtins.tryEval (checkKeys jsm.validKeys doctored)).success
      "checkKeys must accept a gofmt-legal reflow of the map literal";
    pkgs.runCommand "jira-status-mapping-keys-reformat-regression" { } "touch $out";

  jira-status-mapping-keys-drift-regression =
    let
      names = builtins.attrNames driftCases;
      stale = builtins.filter (
        n: builtins.any (p: !pkgs.lib.hasInfix p.from jiraGoSrc) driftCases.${n}
      ) names;
      acceptedGo = builtins.filter (
        n: (builtins.tryEval (checkKeys jsm.validKeys (patchGoSrc driftCases.${n}))).success
      ) names;
      acceptedNix =
        pkgs.lib.optional (builtins.tryEval (checkKeys (jsm.validKeys ++ [ "blocked" ]) jiraGoSrc)).success
          "validKeys-added";
      accepted = acceptedGo ++ acceptedNix;
    in
    assert assertMsg (stale == [ ])
      "jira-status-mapping-keys-drift-regression: a doctoring needle no longer matches jira.go for case(s) ${builtins.concatStringsSep ", " stale}";
    assert assertMsg (accepted == [ ])
      "jira-status-mapping-keys-drift-regression: checkKeys accepted divergent source for case(s) ${builtins.concatStringsSep ", " accepted}";
    pkgs.runCommand "jira-status-mapping-keys-drift-regression" { } "touch $out";

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
