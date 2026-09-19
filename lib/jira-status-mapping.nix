# Eval-level parse of the JIRA_STATUS_MAPPING knob (lib/env-schema.nix's
# jiraStatusMapping, issue #2539). cmd/launcher/internal/forge/jira/jira.go's
# ParseStatusMapping parses the same knob at runtime, so the accepted keys and
# error wording here must not diverge from it. Pure builtins only, no pkgs.lib,
# so a bare `nix eval` tests this file without a locked nixpkgs.
let
  # These must match the keys of jira.go's statusMappingKeys map exactly,
  # in lowerCamelCase.
  validKeys = [
    "dispatchable"
    "inProgress"
    "complete"
    "failed"
  ];

  # This mirrors jira.go's ParseStatusMapping, which throws on the first
  # unknown key with the same error wording.
  validate =
    parsed:
    let
      keys = builtins.attrNames parsed;
      unknown = builtins.filter (k: !(builtins.elem k validKeys)) keys;
    in
    if unknown == [ ] then
      parsed
    else
      throw ''JIRA_STATUS_MAPPING: unknown key "${builtins.head unknown}" (want one of ${builtins.concatStringsSep ", " validKeys})'';
in
{
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
