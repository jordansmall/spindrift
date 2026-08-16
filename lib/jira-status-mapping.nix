# Eval-level parse of the JIRA_STATUS_MAPPING knob (lib/env-schema.nix's
# jiraStatusMapping, issue #2539): a JSON string mapping the four canonical
# Dispatch states to native Jira status names. The Go launcher parses the
# same knob at runtime (cmd/launcher/internal/forge/jira/jira.go's
# ParseStatusMapping) to drive TransitionState; this file must never diverge
# from that runtime counterpart's accepted keys or error wording.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without a locked nixpkgs (mirrors
# lib/prompt-inject.nix, lib/renderers.nix, and lib/research-verdicts.nix).
let
  # Mirrors jira.go's statusMappingKeys map keys exactly (lowerCamelCase).
  validKeys = [
    "dispatchable"
    "inProgress"
    "complete"
    "failed"
  ];

  # Validates a parsed JIRA_STATUS_MAPPING object against the same rule the
  # Go launcher enforces at runtime (jira.go's ParseStatusMapping): every key
  # must be one of validKeys. Throws on the first unknown key found (mirrors
  # ParseStatusMapping's error wording); returns `parsed` unchanged otherwise.
  validate =
    parsed:
    let
      keys = builtins.attrNames parsed;
      unknown = builtins.filter (k: !(builtins.elem k validKeys)) keys;
    in
    if unknown == [ ] then
      parsed
    else
      throw ''
        JIRA_STATUS_MAPPING: unknown key "${builtins.head unknown}" (want one of dispatchable, inProgress, complete, failed)'';
in
{
  inherit validKeys validate;

  # Parses the raw JIRA_STATUS_MAPPING knob string. The empty string (the
  # schema default) yields an empty mapping, mirroring ParseStatusMapping's
  # no-op on an empty string; any other value is parsed as JSON and its keys
  # validated, with a malformed value failing the build loudly (mirrors the
  # launcher's startup validation).
  parse = s: if s == "" then { } else validate (builtins.fromJSON s);
}
