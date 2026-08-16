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
    "argvShape"
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

  # The 6 argv-assembly slot names a Driver's argvShape.order must place,
  # exactly once each (ADR 0009, issue #2534): the Go/bash side (a sibling
  # slice) walks this list to assemble the CLI invocation.
  argvOrderSlots = [
    "prompt"
    "model"
    "agents"
    "session"
    "driverFlags"
    "effort"
  ];

  # assertShape (above) only checks that a Driver entry carries an argvShape
  # attribute at all -- this validates its internal structure, throwing
  # naming both the Driver and every problem found (not just the first) so a
  # malformed entry dies at build time with a complete diagnosis in one pass.
  assertArgvShape =
    driverName: entry:
    let
      shape = entry.argvShape;
      promptStyle = shape.promptStyle or null;
      promptStyleValid = promptStyle == "flag" || promptStyle == "positional";
      promptFlagOk =
        promptStyle != "flag"
        || (shape ? promptFlag && builtins.isString shape.promptFlag && shape.promptFlag != "");
      modelFlagOk = shape ? modelFlag && builtins.isString shape.modelFlag && shape.modelFlag != "";
      modelOmitEmptyOk = shape ? modelOmitEmpty && builtins.isBool shape.modelOmitEmpty;
      # A nullable slot (issue #2534): absent entirely is the valid "no
      # --agents equivalent" case (opencode), so only a *present-but-empty*
      # value is a violation.
      agentsFlagOk = !(shape ? agentsFlag) || (builtins.isString shape.agentsFlag && shape.agentsFlag != "");
      effortFlagOk = shape ? effortFlag && builtins.isString shape.effortFlag && shape.effortFlag != "";
      order = shape.order or null;
      orderIsList = builtins.isList order;
      # order's own permutation set tracks agentsFlag's nullability: a Driver
      # with no --agents equivalent has no "agents" position to place either,
      # so its order omits that slot name the same way its argvShape omits
      # agentsFlag itself (mirrors opencode.nix's 5-slot order vs. claude.nix's
      # 6-slot order).
      expectedSlots = if shape ? agentsFlag then argvOrderSlots else lib.filter (s: s != "agents") argvOrderSlots;
      missingSlots = if orderIsList then lib.filter (s: !(builtins.elem s order)) expectedSlots else expectedSlots;
      extraSlots =
        if orderIsList then lib.unique (lib.filter (s: !(builtins.elem s expectedSlots)) order) else [ ];
      duplicateSlots =
        if orderIsList then
          lib.filter (s: (lib.count (x: x == s) order) > 1) (lib.unique order)
        else
          [ ];
      orderOk = orderIsList && missingSlots == [ ] && extraSlots == [ ] && duplicateSlots == [ ];

      errors =
        lib.optional (
          !promptStyleValid
        ) ''argvShape.promptStyle must be "flag" or "positional", got: ${builtins.toJSON promptStyle}''
        ++ lib.optional (
          !promptFlagOk
        ) "argvShape.promptFlag must be a non-empty string when promptStyle is \"flag\""
        ++ lib.optional (!modelFlagOk) "argvShape.modelFlag must be a non-empty string"
        ++ lib.optional (!modelOmitEmptyOk) "argvShape.modelOmitEmpty must be a bool"
        ++ lib.optional (
          !agentsFlagOk
        ) "argvShape.agentsFlag must be a non-empty string when present (omit it entirely for a Driver with no --agents equivalent)"
        ++ lib.optional (!effortFlagOk) "argvShape.effortFlag must be a non-empty string"
        ++ lib.optional (!orderOk) (
          if !orderIsList then
            "argvShape.order must be a list, got: ${builtins.toJSON order}"
          else
            "argvShape.order must contain each of ${lib.concatStringsSep ", " expectedSlots} exactly once"
            + lib.optionalString (missingSlots != [ ]) "; missing: ${lib.concatStringsSep ", " missingSlots}"
            + lib.optionalString (extraSlots != [ ]) "; extra/unknown: ${lib.concatStringsSep ", " extraSlots}"
            + lib.optionalString (duplicateSlots != [ ]) "; duplicated: ${lib.concatStringsSep ", " duplicateSlots}"
        );
    in
    if errors == [ ] then
      entry
    else
      throw "Driver '${driverName}' has an invalid argvShape: ${lib.concatStringsSep "; " errors}";

  entries = {
    claude = assertArgvShape "claude" (assertShape "claude" (import ./claude.nix { inherit lib; }));
    opencode = assertArgvShape "opencode" (assertShape "opencode" (import ./opencode.nix { inherit lib; }));
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

  # The Driver's argv assembly shape (ADR 0009, issue #2534) rendered into
  # DRIVER_ARGV_* vars the Go/bash side (a sibling slice) assembles the CLI
  # invocation from. DRIVER_ARGV_MODEL_OMIT_EMPTY and DRIVER_ARGV_AGENTS_FLAG
  # follow the same bare-flag/optional-attr conventions as flagsCommon's
  # --devshell and DRIVER_AGENT_FILES_DIR above: a false/absent value renders
  # no line at all, never an empty-string assignment.
  renderArgvShape =
    driverEntry:
    let
      shape = driverEntry.argvShape;
    in
    "DRIVER_ARGV_PROMPT_STYLE="
    + lib.escapeShellArg shape.promptStyle
    + "\n"
    + lib.optionalString (shape ? promptFlag) (
      "DRIVER_ARGV_PROMPT_FLAG=" + lib.escapeShellArg shape.promptFlag + "\n"
    )
    + "DRIVER_ARGV_MODEL_FLAG="
    + lib.escapeShellArg shape.modelFlag
    + "\n"
    + lib.optionalString shape.modelOmitEmpty "DRIVER_ARGV_MODEL_OMIT_EMPTY=1\n"
    + lib.optionalString (shape ? agentsFlag) (
      "DRIVER_ARGV_AGENTS_FLAG=" + lib.escapeShellArg shape.agentsFlag + "\n"
    )
    + "DRIVER_ARGV_EFFORT_FLAG="
    + lib.escapeShellArg shape.effortFlag
    + "\n"
    + "DRIVER_ARGV_ORDER="
    + lib.escapeShellArg (lib.concatStringsSep " " shape.order)
    + "\n";

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
    # Optional, like sessionCacheDirRelative (see requiredAttrs comment
    # above): rendered only when the Driver entry declares
    # agentFilesDirRelative (currently opencode.nix only), so
    # agent/entrypoint.sh's DRIVER_AGENT_FILES_DIR-gated file-rewrite loop
    # (issue #2153) stays a true no-op -- the var unset, not empty -- for a
    # Driver (claude) whose subagents don't ride on-disk files.
    + lib.optionalString (driverEntry ? agentFilesDirRelative) (
      "DRIVER_AGENT_FILES_DIR="
      + lib.escapeShellArg "/home/agent/${driverEntry.agentFilesDirRelative}"
      + "\n"
    )
    + renderEnvCommon driverEntry
    + renderArgvShape driverEntry
    + renderFunctions driverEntry;
in
{
  inherit
    entries
    assertShape
    assertArgvShape
    requiredAttrs
    renderPreamble
    ;
}
