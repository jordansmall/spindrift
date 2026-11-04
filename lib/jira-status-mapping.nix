# Eval-level parse of the JIRA_STATUS_MAPPING knob (the jira row's
# jiraStatusMapping in lib/backends/jira.nix, issue #2539). cmd/launcher/internal/forge/jira/jira.go's
# ParseStatusMapping parses the same knob at runtime, so the accepted shapes
# and error wording here must not diverge from it; nix/checks/jira-status-mapping.nix
# runs the shared corpus (jira/testdata/status-mapping-cases.json) against
# `error`. The one difference is malformed JSON: builtins.fromJSON aborts
# uncatchably, so only the Go side words that error. Pure builtins only, no
# pkgs.lib, so a bare `nix eval` tests this file without a locked nixpkgs.
let
  # Must match jira.go's statusMappingKeys keys, in lowerCamelCase;
  # nix/checks/jira-status-mapping.nix's jira-status-mapping-keys-match-go-source
  # enforces it.
  validKeys = [
    "dispatchable"
    "inProgress"
    "complete"
    "failed"
  ];

  # Must match jira.go's statusTargetFields; the same check enforces it.
  validFields = [
    "status"
    "resolution"
  ];

  # builtins.typeOf names as the JSON type names jira.go's jsonTypeName reports.
  jsonType = {
    int = "number";
    float = "number";
    bool = "bool";
    set = "object";
    list = "array";
    null = "null";
    string = "string";
  };

  firstNonNull =
    errs:
    let
      found = builtins.filter (e: e != null) errs;
    in
    if found == [ ] then null else builtins.head found;

  fieldError =
    key: obj: name:
    let
      v = obj.${name};
    in
    if !(builtins.elem name validFields) then
      ''JIRA_STATUS_MAPPING: key "${key}": unknown field "${name}" (want ${builtins.concatStringsSep ", " validFields})''
    else if !(builtins.isString v) then
      ''JIRA_STATUS_MAPPING: key "${key}": field "${name}" must be a string, got ${jsonType.${builtins.typeOf v}}''
    else
      null;

  # Mirrors parseStatusTarget: fields in sorted order (attrNames sorts, as
  # slices.Sorted does), then status, then resolution.
  objectError =
    key: obj:
    firstNonNull (
      map (fieldError key obj) (builtins.attrNames obj)
      ++ [
        (
          if (obj.status or "") == "" then
            ''JIRA_STATUS_MAPPING: key "${key}": "status" must be a non-empty string''
          else
            null
        )
        (
          if obj ? resolution && obj.resolution == "" then
            ''JIRA_STATUS_MAPPING: key "${key}": "resolution" must be a non-empty string''
          else
            null
        )
      ]
    );

  keyError =
    parsed: key:
    let
      v = parsed.${key};
    in
    if !(builtins.elem key validKeys) then
      ''JIRA_STATUS_MAPPING: unknown key "${key}" (want one of ${builtins.concatStringsSep ", " validKeys})''
    else if v == null || builtins.isString v then
      null
    else if builtins.isAttrs v then
      objectError key v
    else
      ''JIRA_STATUS_MAPPING: key "${key}": expected a status name or a {"status", "resolution"} object, got ${jsonType.${builtins.typeOf v}}'';

  decode = s: if s == "" then null else builtins.fromJSON s;

  # The message ParseStatusMapping would fail with for a decoded value, or null
  # when it is valid.
  valueError =
    value:
    # json.Unmarshal of "null" into Go's `any` yields nil with no error, so
    # ParseStatusMapping("null") is an empty mapping; catch it before
    # attrNames aborts on it.
    if value == null then
      null
    else if !(builtins.isAttrs value) then
      "JIRA_STATUS_MAPPING: expected a JSON object, got ${jsonType.${builtins.typeOf value}}"
    else
      firstNonNull (map (keyError value) (builtins.attrNames value));

  # Pure so the corpus check can compare text; tryEval hides a throw's.
  error = s: valueError (decode s);
in
{
  inherit validKeys validFields error;

  # The empty string is the schema default and yields an empty mapping, as
  # ParseStatusMapping does. A malformed value fails the build loudly, the way
  # the launcher fails at startup.
  parse =
    s:
    let
      value = decode s;
      e = valueError value;
    in
    if e != null then
      throw e
    else if value == null then
      { }
    else
      value;
}
