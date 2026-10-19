# Eval-level parse of the JIRA_STATUS_MAPPING knob (the jira row's
# jiraStatusMapping in lib/backends/jira.nix, issue #2539). cmd/launcher/internal/forge/jira/jira.go's
# ParseStatusMapping parses the same knob at runtime, so the accepted keys and
# error wording here must not diverge from it. The one exception is a
# non-string value: Go's text there comes from encoding/json, so only its
# `parse JIRA_STATUS_MAPPING:` prefix and JSON type name carry over. Pure
# builtins only, no pkgs.lib, so a bare `nix eval` tests this file without a
# locked nixpkgs.
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

  # builtins.typeOf names as encoding/json's error text names them.
  jsonType = {
    int = "number";
    float = "number";
    bool = "bool";
    set = "object";
    list = "array";
  };

  # This mirrors jira.go's ParseStatusMapping: a value that is neither a string
  # nor null throws first (Go's map[string]string decode fails before its key
  # loop), then the first unknown key, with Go's exact unknown-key wording.
  validate =
    parsed:
    let
      keys = builtins.attrNames parsed;
      badValue = builtins.filter (
        k:
        let
          v = parsed.${k};
        in
        !(builtins.isString v || v == null)
      ) keys;
      unknown = builtins.filter (k: !(builtins.elem k validKeys)) keys;
    in
    if badValue != [ ] then
      let
        k = builtins.head badValue;
      in
      throw ''parse JIRA_STATUS_MAPPING: key "${k}": expected a JSON string or null, got ${
        jsonType.${builtins.typeOf parsed.${k}}
      }''
    else if unknown == [ ] then
      parsed
    else
      throw ''JIRA_STATUS_MAPPING: unknown key "${builtins.head unknown}" (want one of ${builtins.concatStringsSep ", " validKeys})'';
in
{
  inherit validKeys;

  # The empty string is the schema default and yields an empty mapping, as
  # ParseStatusMapping does. A malformed value fails the build loudly, the way
  # the launcher fails at startup.
  parse =
    s:
    if s == "" then
      { }
    else
      let
        value = builtins.fromJSON s;
      in
      # json.Unmarshal of "null" into Go's map[string]string leaves it nil with
      # no error, so ParseStatusMapping("null") returns an empty mapping. Catch
      # null here, before validate's attrNames aborts on it.
      if value == null then
        { }
      else if !(builtins.isAttrs value) then
        throw "JIRA_STATUS_MAPPING: expected a JSON object, got ${builtins.typeOf value}"
      else
        validate value;
}
