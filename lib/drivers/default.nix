# The Driver registry (ADR 0009): one entry per in-box agent CLI, keyed by
# name, validated against a required-attribute list and rendered into the
# in-box preamble/function bodies here (issue #624) so per-Driver files (e.g.
# ./claude.nix) stay pure data. lib/mkHarness.nix selects an entry by its
# `driver` option (default "claude") via `entries`, calls the renderers below
# on it, and bakes the result into the image; the Go launcher selects the
# matching host-side strategy by the same name via DRIVER (see
# cmd/launcher/internal/driver). A parity test
# (cmd/launcher/internal/driver/parity_test.go) asserts the two registries'
# names never drift -- name-only by design (ADR 0009): each half now enforces
# its own entries' completeness independently.
{ lib }:
let
  # Every attribute a Driver entry must supply for the registry to validate
  # and render it. sessionCacheDirRelative is deliberately absent here: it's
  # optional (a Driver with no resumable session state omits it; see
  # lib/preambles.nix's renderDriverMountPreamble).
  requiredAttrs = [
    "name"
    "package"
    "bin"
    "flagsCommon"
    "skillsDirRelative"
    "outcomeExtractFnBody"
    "outcomeExtractNearMissFnBody"
    "sessionFlagsFnBody"
    "agentsJsonTemplate"
    "agentFilesTemplate"
  ];

  # Fails eval naming both the Driver and the missing attribute(s), so an
  # entry missing a required attribute dies at build time -- never a live Box.
  assertShape =
    driverName: entry:
    let
      missing = lib.filter (attr: !(entry ? ${attr})) requiredAttrs;
    in
    if missing == [ ] then
      entry
    else
      throw "Driver '${driverName}' is missing required attribute(s): ${lib.concatStringsSep ", " missing}";

  entries = {
    claude = assertShape "claude" (import ./claude.nix { inherit lib; });
    opencode = assertShape "opencode" (import ./opencode.nix { inherit lib; });
  };

  # The Driver's function definitions, shared verbatim between the image
  # preamble and the bats harness file (issue #433) so neither can drift from
  # the other.
  renderFunctions =
    driverEntry:
    "_driver_extract_outcome() {\n"
    + driverEntry.outcomeExtractFnBody
    + "}\n"
    + "_driver_extract_near_miss_outcome() {\n"
    + driverEntry.outcomeExtractNearMissFnBody
    + "}\n"
    + "_driver_session_flags() {\n"
    + driverEntry.sessionFlagsFnBody
    + "}\n";

  # The Driver's in-box half rendered into agent/entrypoint.sh's DRIVER_* vars
  # and function definitions (ADR 0009). /home/agent is the image's fixed
  # HOME (see lib/image.nix's passwdFile), so the skills dir is baked as an
  # absolute path rather than depending on $HOME at run time -- byte-identical
  # to what mkHarness.nix used to string-build inline for all three vars.
  # Renders driverEntry.envCommon (a Driver-specific attrset of env vars, e.g.
  # claude.nix's CLAUDE_CODE_DISABLE_BACKGROUND_TASKS, issue #2011) as one
  # `export KEY=value` line per entry -- `export`, unlike the plain DRIVER_*
  # assignments above, since the point is reaching a child process (claude,
  # via driver-exec/orchestrator's unmodified os/exec env inheritance), not
  # entrypoint.sh's own interpolation. Optional: a Driver entry that omits
  # envCommon renders no lines at all, so a Driver with nothing to export
  # (or a future one that never adds this attribute) needn't declare it.
  renderEnvCommon =
    driverEntry:
    lib.concatStrings (
      lib.mapAttrsToList (
        name: value:
        if builtins.match "[A-Za-z_][A-Za-z0-9_]*" name == null then
          throw "Driver envCommon key '${name}' is not a valid shell identifier"
        else
          "export ${name}=" + lib.escapeShellArg value + "\n"
      ) (driverEntry.envCommon or { })
    );

  renderPreamble =
    driverEntry:
    "DRIVER_NAME="
    + lib.escapeShellArg driverEntry.name
    + "\n"
    + "DRIVER_BIN="
    + lib.escapeShellArg driverEntry.bin
    + "\n"
    + "DRIVER_FLAGS_COMMON="
    + lib.escapeShellArg driverEntry.flagsCommon
    + "\n"
    + "DRIVER_SKILLS_DIR="
    + lib.escapeShellArg "/home/agent/${driverEntry.skillsDirRelative}"
    + "\n"
    + renderEnvCommon driverEntry
    + renderFunctions driverEntry;
in
{
  inherit
    entries
    assertShape
    requiredAttrs
    renderPreamble
    ;
}
