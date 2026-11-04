# Eval-level pins for lib/jira-status-mapping.nix (issue #2539), mirroring
# cmd/launcher/internal/forge/jira/jira.go's ParseStatusMapping. No
# malformed-JSON check: builtins.fromJSON raises a builtin type error that
# builtins.tryEval cannot catch, so forcing it here would abort the whole
# checks-inbox aggregate instead of failing one assert. The error text is
# pinned by the shared corpus check, which compares jsm.error, not tryEval.
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

  # Same hand-mirroring for the object-value field list (jsm.validFields).
  fieldsMarker = "statusTargetFields = []string{";
  fieldErrMarker = "unknown field %q (want ";

  extractFields =
    src:
    let
      parts = splitString fieldsMarker src;
      firstQuoted =
        entry:
        let
          toks = splitString "\"" entry;
        in
        if builtins.length toks >= 3 then [ (builtins.elemAt toks 1) ] else [ ];
    in
    if builtins.length parts < 2 then
      [ ]
    else
      builtins.concatMap firstQuoted (
        splitString "," (builtins.head (splitString "}" (builtins.elemAt parts 1)))
      );

  extractErrorFields =
    src:
    let
      parts = splitString fieldErrMarker src;
    in
    if builtins.length parts < 2 then
      [ ]
    else
      splitString ", " (builtins.head (splitString ")" (builtins.elemAt parts 1)));

  checkFields =
    validFields: src:
    let
      goFields = extractFields src;
      errFields = extractErrorFields src;
      show = builtins.concatStringsSep ", ";
    in
    assert assertMsg (goFields != [ ]) (
      "jira.go statusTargetFields extraction yielded no fields: extractFields no longer matches "
      + "jira.go's shape — fix the extractor"
    );
    assert assertMsg (errFields != [ ]) (
      "jira.go unknown-field error extraction yielded no fields: extractErrorFields no longer matches "
      + "jira.go's shape — fix the extractor"
    );
    assert assertMsg (sortKeys goFields == sortKeys validFields) (
      "lib/jira-status-mapping.nix validFields [${show validFields}] drifted from "
      + "jira.go statusTargetFields [${show goFields}]"
    );
    assert assertMsg (sortKeys errFields == sortKeys goFields) (
      "jira.go's unknown-field error lists [${show errFields}] but statusTargetFields has [${show goFields}]"
    );
    true;

  fieldsLiteral = "statusTargetFields = []string{\"status\", \"resolution\"}";
  fieldsErrTail = "(want status, resolution)";
  fieldDriftCases = {
    field-added = [
      {
        from = fieldsLiteral;
        to = "statusTargetFields = []string{\"status\", \"resolution\", \"note\"}";
      }
      {
        from = fieldsErrTail;
        to = "(want status, resolution, note)";
      }
    ];
    field-renamed = [
      {
        from = fieldsLiteral;
        to = "statusTargetFields = []string{\"status\", \"resolutions\"}";
      }
      {
        from = fieldsErrTail;
        to = "(want status, resolutions)";
      }
    ];
    field-dropped = [
      {
        from = fieldsLiteral;
        to = "statusTargetFields = []string{\"status\"}";
      }
      {
        from = fieldsErrTail;
        to = "(want status)";
      }
    ];
    error-string-only = [
      {
        from = fieldsErrTail;
        to = "(want status, resolutions)";
      }
    ];
  };

  corpus = builtins.fromJSON (
    builtins.readFile ../../cmd/launcher/internal/forge/jira/testdata/status-mapping-cases.json
  );

  lifecycleCorpus = builtins.fromJSON (
    builtins.readFile ../../cmd/launcher/internal/forge/jira/testdata/lifecycle-cases.json
  );

  mapEntry = "\t\"ambiguous\":    forge.Ambiguous,\n";
  errTail = "inProgress, complete, failed, ambiguous)";
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
        to = "inProgress, complete, failed, ambiguous, blocked)";
      }
    ];
    key-renamed = [
      {
        from = "\"complete\":     forge.Complete";
        to = "\"completed\":    forge.Complete";
      }
      {
        from = errTail;
        to = "inProgress, completed, failed, ambiguous)";
      }
    ];
    key-dropped = [
      {
        from = mapEntry;
        to = "";
      }
      {
        from = errTail;
        to = "inProgress, complete, failed)";
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

  jira-status-mapping-fields-match-go-source =
    assert checkFields jsm.validFields jiraGoSrc;
    pkgs.runCommand "jira-status-mapping-fields-match-go-source" { } "touch $out";

  jira-status-mapping-fields-drift-regression =
    let
      names = builtins.attrNames fieldDriftCases;
      stale = builtins.filter (
        n: builtins.any (p: !pkgs.lib.hasInfix p.from jiraGoSrc) fieldDriftCases.${n}
      ) names;
      acceptedGo = builtins.filter (
        n: (builtins.tryEval (checkFields jsm.validFields (patchGoSrc fieldDriftCases.${n}))).success
      ) names;
      acceptedNix =
        pkgs.lib.optional (builtins.tryEval (checkFields (jsm.validFields ++ [ "note" ]) jiraGoSrc)).success
          "validFields-added";
      accepted = acceptedGo ++ acceptedNix;
    in
    assert assertMsg (stale == [ ])
      "jira-status-mapping-fields-drift-regression: a doctoring needle no longer matches jira.go for case(s) ${builtins.concatStringsSep ", " stale}";
    assert assertMsg (accepted == [ ])
      "jira-status-mapping-fields-drift-regression: checkFields accepted divergent source for case(s) ${builtins.concatStringsSep ", " accepted}";
    pkgs.runCommand "jira-status-mapping-fields-drift-regression" { } "touch $out";

  # The corpus is shared with the Go test TestParseStatusMapping_Corpus, so the
  # eval-time parse and the launcher's startup parse agree on text, not just on
  # accept/reject.
  jira-status-mapping-corpus-matches-go =
    let
      expected = row: if row.error == "" then null else row.error;
      mismatched = builtins.filter (row: jsm.error row.input != expected row) corpus;
    in
    assert assertMsg (corpus != [ ]) "jira-status-mapping-corpus-matches-go: the corpus is empty";
    assert assertMsg (mismatched == [ ])
      "jira-status-mapping-corpus-matches-go: lib/jira-status-mapping.nix's error diverges from the shared corpus for row(s) ${
        builtins.concatStringsSep ", " (map (r: r.name) mismatched)
      }";
    pkgs.runCommand "jira-status-mapping-corpus-matches-go" { } "touch $out";

  # Shared with the Go test TestCheckLifecycle_Corpus.
  jira-status-mapping-lifecycle-corpus-matches-go =
    let
      expected = row: if row.error == "" then null else row.error;
      mismatched = builtins.filter (
        row: jsm.lifecycleError row.lifecycle row.mapping != expected row
      ) lifecycleCorpus;
    in
    assert assertMsg (
      lifecycleCorpus != [ ]
    ) "jira-status-mapping-lifecycle-corpus-matches-go: the corpus is empty";
    assert assertMsg (mismatched == [ ])
      "jira-status-mapping-lifecycle-corpus-matches-go: lib/jira-status-mapping.nix's lifecycleError diverges from the shared corpus for row(s) ${
        builtins.concatStringsSep ", " (map (r: r.name) mismatched)
      }";
    pkgs.runCommand "jira-status-mapping-lifecycle-corpus-matches-go" { } "touch $out";

  jira-status-mapping-keys-reformat-regression =
    let
      oldLiteral =
        mapMarker
        + "\n\t\"dispatchable\": forge.Dispatchable,\n\t\"inProgress\":   forge.InProgress,\n"
        + "\t\"complete\":     forge.Complete,\n\t\"failed\":       forge.Failed,\n"
        + "\t\"ambiguous\":    forge.Ambiguous,\n}";
      newLiteral =
        mapMarker
        + "\"dispatchable\": forge.Dispatchable, \"inProgress\": forge.InProgress, "
        + "\"complete\": forge.Complete, \"failed\": forge.Failed, \"ambiguous\": forge.Ambiguous}";
      doctored = builtins.replaceStrings [ oldLiteral ] [ newLiteral ] jiraGoSrc;
    in
    assert assertMsg (doctored != jiraGoSrc)
      "jira-status-mapping-keys-reformat-regression: the map literal no longer matches the doctoring pattern; update oldLiteral";
    assert assertMsg (
      sortKeys (extractMapKeys doctored) == sortKeys jsm.validKeys
    ) "extractMapKeys must still find all five keys in a one-line map literal";
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
  # this cannot also pin that the message names the five valid states (see
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
        ambiguous = "Needs Spec";
      };
      out = jsm.parse (builtins.toJSON mapping);
    in
    assert assertMsg (out == mapping) "parse must return all five keys unchanged";
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

  # A value is a status name, null, or a {status, resolution} object; any
  # other JSON type fails at launcher startup.
  jira-status-mapping-parse-rejects-bad-value-type =
    let
      rejects = v: !(builtins.tryEval (jsm.parse (builtins.toJSON v))).success;
    in
    assert assertMsg (rejects { complete = 5; }) "parse must throw on a number value";
    assert assertMsg (rejects { complete = true; }) "parse must throw on a boolean value";
    assert assertMsg (rejects { complete = [ "x" ]; }) "parse must throw on an array value";
    pkgs.runCommand "jira-status-mapping-parse-rejects-bad-value-type" { } "touch $out";

  jira-status-mapping-parse-object-value =
    let
      rejects = v: !(builtins.tryEval (jsm.parse (builtins.toJSON { complete = v; }))).success;
      full = {
        status = "Done";
        resolution = "Fixed";
      };
    in
    assert assertMsg (
      jsm.parse (builtins.toJSON { complete = full; }) == { complete = full; }
    ) "parse must accept a {status, resolution} object";
    assert assertMsg (!rejects { status = "Done"; }) "parse must accept an object with only a status";
    assert assertMsg (rejects (full // { note = "x"; })) "parse must throw on an unknown field";
    assert assertMsg (rejects { resolution = "Fixed"; }) "parse must throw on a missing status";
    assert assertMsg (rejects (full // { status = ""; })) "parse must throw on an empty status";
    assert assertMsg (rejects (full // { resolution = ""; })) "parse must throw on an empty resolution";
    assert assertMsg (rejects (full // { status = 5; })) "parse must throw on a non-string status";
    assert assertMsg (rejects (
      full // { resolution = true; }
    )) "parse must throw on a non-string resolution";
    assert assertMsg (rejects (full // { resolution = null; })) "parse must throw on a null resolution";
    pkgs.runCommand "jira-status-mapping-parse-object-value" { } "touch $out";

  # Go leaves a null value as an empty status without error.
  jira-status-mapping-parse-allows-null-value =
    let
      out = jsm.parse (builtins.toJSON { complete = null; });
    in
    assert assertMsg (out == { complete = null; }) "parse must pass a null value through unchanged";
    pkgs.runCommand "jira-status-mapping-parse-allows-null-value" { } "touch $out";

  # Mirrors ParseStatusMapping's sharing rule: only failed and ambiguous may map
  # to one status (compared ASCII-case-insensitively); an empty or null value
  # maps to no status, so it never collides.
  jira-status-mapping-parse-status-sharing =
    let
      accepts = v: (builtins.tryEval (builtins.deepSeq (jsm.parse (builtins.toJSON v)) true)).success;
    in
    assert assertMsg (accepts { ambiguous = "Needs Spec"; }) "parse must accept the ambiguous key";
    assert assertMsg (accepts {
      failed = "Blocked";
      ambiguous = "bLOCKED";
    }) "parse must accept failed and ambiguous sharing a status, case-folded";
    assert assertMsg (
      !accepts {
        dispatchable = "To Do";
        inProgress = "to do";
      }
    ) "parse must reject dispatchable and inProgress sharing a status";
    assert assertMsg (
      !accepts {
        ambiguous = "X";
        failed = "X";
        complete = "x";
      }
    ) "parse must reject complete sharing a status with failed and ambiguous";
    assert assertMsg (accepts {
      complete = null;
      failed = null;
    }) "parse must not collide two null values";
    assert assertMsg (accepts {
      complete = "";
      failed = "";
    }) "parse must not collide two empty values";
    pkgs.runCommand "jira-status-mapping-parse-status-sharing" { } "touch $out";
}
