# The Driver registry (ADR 0009): one entry per in-box agent CLI. Rendering
# lives here (issue #624) so per-Driver files like ./claude.nix stay pure data.
# The Go launcher keeps a matching host-side registry keyed by the same names;
# cmd/launcher/internal/driver/parity_test.go asserts the two never drift on
# names only, because each half validates its own entries independently.
{ lib }:
let
  # sessionCacheDirRelative is deliberately absent: a Driver with no resumable
  # session state omits it (see lib/preambles.nix's renderDriverMountPreamble).
  # When present, assertShape checks its shape: a non-empty relative path with
  # no empty, "." or ".." segment (so no leading/trailing/doubled slash either).
  requiredAttrs = [
    "name"
    "package"
    "bin"
    "flagsCommon"
    "skillsDirRelative"
    "agentsJsonTemplate"
    "agentFilesTemplate"
    "argvShape"
  ];

  # Names both the Driver and every missing attribute, so an incomplete entry
  # dies at build time rather than in a live Box.
  assertShape =
    driverName: entry:
    let
      missing = lib.filter (attr: !(entry ? ${attr})) requiredAttrs;
      # isString is checked first so a non-string value short-circuits the
      # segment check rather than throwing a type error out of splitString.
      # Keep the path canonical: homelayout.PopulateHome cleans it before its
      # chmod-skip comparison, but a "." segment ("./x", "x/.") is still a
      # redundant spelling, refused here like a stray slash.
      sessionCacheDirRelativeBad =
        entry ? sessionCacheDirRelative
        && !(
          builtins.isString entry.sessionCacheDirRelative
          && !(lib.any (
            seg:
            lib.elem seg [
              ""
              "."
              ".."
            ]
          ) (lib.splitString "/" entry.sessionCacheDirRelative))
        );
      errors =
        lib.optional (
          missing != [ ]
        ) "Driver '${driverName}' is missing required attribute(s): ${lib.concatStringsSep ", " missing}"
        ++ lib.optional sessionCacheDirRelativeBad "Driver '${driverName}' has an invalid sessionCacheDirRelative (must be a non-empty relative path with no empty, \".\" or \"..\" segment -- i.e. no leading/trailing or doubled slash), got: ${
          if builtins.isString entry.sessionCacheDirRelative then
            lib.generators.toPretty { } entry.sessionCacheDirRelative
          else
            "a ${builtins.typeOf entry.sessionCacheDirRelative}"
        }";
    in
    if errors != [ ] then throw (lib.concatStringsSep "; " errors) else entry;

  # Each slot must appear in a Driver's argvShape.order exactly once (ADR 0009,
  # issue #2534); the Go/bash side walks that order to assemble the CLI call.
  argvOrderSlots = [
    "prompt"
    "model"
    "agents"
    "session"
    "driverFlags"
    "effort"
  ];

  # assertShape only checks that argvShape exists; this validates its structure.
  # It reports every problem found, not just the first, so one build gives a
  # complete diagnosis.
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
      # A nullable slot (issue #2534): absent means the Driver has no --agents
      # equivalent (opencode), so only a present-but-empty value is a violation.
      agentsFlagOk =
        !(shape ? agentsFlag) || (builtins.isString shape.agentsFlag && shape.agentsFlag != "");
      effortFlagOk = shape ? effortFlag && builtins.isString shape.effortFlag && shape.effortFlag != "";
      order = shape.order or null;
      orderIsList = builtins.isList order;
      # The expected set tracks agentsFlag's nullability: a Driver with no
      # --agents equivalent has no "agents" position to place, so its order
      # omits that slot too (opencode.nix has 5, claude.nix has 6).
      expectedSlots =
        if shape ? agentsFlag then argvOrderSlots else lib.filter (s: s != "agents") argvOrderSlots;
      missingSlots =
        if orderIsList then lib.filter (s: !(builtins.elem s order)) expectedSlots else expectedSlots;
      extraSlots =
        if orderIsList then lib.unique (lib.filter (s: !(builtins.elem s expectedSlots)) order) else [ ];
      duplicateSlots =
        if orderIsList then lib.filter (s: (lib.count (x: x == s) order) > 1) (lib.unique order) else [ ];
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
        ++
          lib.optional (!agentsFlagOk)
            "argvShape.agentsFlag must be a non-empty string when present (omit it entirely for a Driver with no --agents equivalent)"
        ++ lib.optional (!effortFlagOk) "argvShape.effortFlag must be a non-empty string"
        ++ lib.optional (!orderOk) (
          if !orderIsList then
            "argvShape.order must be a list, got: ${builtins.toJSON order}"
          else
            "argvShape.order must contain each of ${lib.concatStringsSep ", " expectedSlots} exactly once"
            + lib.optionalString (missingSlots != [ ]) "; missing: ${lib.concatStringsSep ", " missingSlots}"
            + lib.optionalString (extraSlots != [ ]) "; extra/unknown: ${lib.concatStringsSep ", " extraSlots}"
            + lib.optionalString (
              duplicateSlots != [ ]
            ) "; duplicated: ${lib.concatStringsSep ", " duplicateSlots}"
        );
    in
    if errors == [ ] then
      entry
    else
      throw "Driver '${driverName}' has an invalid argvShape: ${lib.concatStringsSep "; " errors}";

  entries = {
    claude = assertArgvShape "claude" (assertShape "claude" (import ./claude.nix { inherit lib; }));
    opencode = assertArgvShape "opencode" (
      assertShape "opencode" (import ./opencode.nix { inherit lib; })
    );
  };

  # Names rendered into the preamble are interpolated into shell, so each must
  # be a plain identifier; `what` labels the offending field in the error.
  assertShellIdent =
    what: name:
    if builtins.match "[A-Za-z_][A-Za-z0-9_]*" name == null then
      throw "Driver ${what} '${name}' is not a valid shell identifier"
    else
      name;

  # envCommon (issue #2011) renders as `export`, unlike the plain DRIVER_*
  # assignments, because the value has to reach a child process (claude, via
  # driver-exec's env inheritance) rather than entrypoint.sh's own
  # interpolation. A Driver that omits envCommon renders no lines at all.
  renderEnvCommon =
    driverEntry:
    lib.concatStrings (
      lib.mapAttrsToList (
        name: value: "export ${assertShellIdent "envCommon key" name}=" + lib.escapeShellArg value + "\n"
      ) (driverEntry.envCommon or { })
    );

  # bashTimeoutEnv (issue #4409) is plain data, not an export: the Consumer's
  # timeout value is only known at run time, so the box program
  # (cmd/launcher/box) exports it under each name listed here. Absent renders
  # no line, so a Driver with no such env vars (opencode) ignores the knob
  # without any branch on its name.
  renderBashTimeoutEnv =
    driverEntry:
    lib.optionalString (driverEntry ? bashTimeoutEnv) (
      "DRIVER_BASH_TIMEOUT_ENV="
      + lib.escapeShellArg (
        lib.concatStringsSep " " (map (assertShellIdent "bashTimeoutEnv name") driverEntry.bashTimeoutEnv)
      )
      + "\n"
    );

  # The argv shape comes from ADR 0009 and issue #2534.
  # DRIVER_ARGV_MODEL_OMIT_EMPTY and DRIVER_ARGV_AGENTS_FLAG follow the same
  # convention as DRIVER_AGENT_FILES_DIR below: a false or absent value renders
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

  # /home/agent is the image's fixed HOME (lib/image.nix's passwdFile), so these
  # paths bake in as absolute rather than depending on $HOME at run time.
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
    # renderPreamble emits this var only when the entry declares
    # agentFilesDirRelative (opencode only), leaving it unset rather than empty
    # so promptassembly's file-rewrite loop (issue #2153) is a true no-op
    # for claude, whose subagents use no on-disk files.
    + lib.optionalString (driverEntry ? agentFilesDirRelative) (
      "DRIVER_AGENT_FILES_DIR="
      + lib.escapeShellArg "/home/agent/${driverEntry.agentFilesDirRelative}"
      + "\n"
    )
    # Symmetric with DRIVER_AGENT_FILES_DIR above (issue #2843): the var stays
    # unset, not empty, for opencode, which has no resumable session state.
    # lib/preambles.nix's renderDriverMountPreamble renders a same-named var for
    # the host-side launcher process, a separate consumer.
    + lib.optionalString (driverEntry ? sessionCacheDirRelative) (
      "DRIVER_SESSION_CACHE_DIR="
      + lib.escapeShellArg "/home/agent/${driverEntry.sessionCacheDirRelative}"
      + "\n"
    )
    + renderEnvCommon driverEntry
    + renderBashTimeoutEnv driverEntry
    + renderArgvShape driverEntry;
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
