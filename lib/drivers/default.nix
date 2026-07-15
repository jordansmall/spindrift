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
    "sessionFlagsFnBody"
    "agentsJsonTemplate"
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
  };

  # The Driver's function definitions, shared verbatim between the image
  # preamble and the bats harness file (issue #433) so neither can drift from
  # the other.
  renderFunctions =
    driverEntry:
    "_driver_extract_outcome() {\n"
    + driverEntry.outcomeExtractFnBody
    + "}\n"
    + "_driver_session_flags() {\n"
    + driverEntry.sessionFlagsFnBody
    + "}\n";

  # The Driver's in-box half rendered into agent/entrypoint.sh's DRIVER_* vars
  # and function definitions (ADR 0009). /home/agent is the image's fixed
  # HOME (see lib/image.nix's passwdFile), so the skills dir is baked as an
  # absolute path rather than depending on $HOME at run time. Each var keeps
  # the `${VAR:-<baked>}` shape agent/entrypoint.sh's own copy used to
  # hand-write (issue #624 kills that hand-written copy, not the shape
  # itself) so a real Box -- where nothing ever sets DRIVER_BIN/
  # DRIVER_FLAGS_COMMON/DRIVER_SKILLS_DIR in its environment -- always runs
  # the baked value, while a bats fixture can still redirect DRIVER_SKILLS_DIR
  # at a writable test directory the same way it always could.
  renderPreamble =
    driverEntry:
    ''
      DRIVER_BIN="''${DRIVER_BIN:-${driverEntry.bin}}"
      DRIVER_FLAGS_COMMON="''${DRIVER_FLAGS_COMMON:-${driverEntry.flagsCommon}}"
      DRIVER_SKILLS_DIR="''${DRIVER_SKILLS_DIR:-/home/agent/${driverEntry.skillsDirRelative}}"
    ''
    + renderFunctions driverEntry;
in
{
  inherit
    entries
    assertShape
    requiredAttrs
    renderFunctions
    renderPreamble
    ;
}
