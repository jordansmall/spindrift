# Nix→bash preamble marshalling (issue #513): turns lib/env-schema.nix and
# driver-registry data into the exported bash the Go launcher (cmd/launcher)
# reads and the spindrift/build wrapper scripts bake in. lib/mkHarness.nix
# imports this file and concatenates the results at the exact call sites the
# inline definitions used to occupy; nix/checks/preambles.nix pins each
# renderer's output shape.
#
# Pure builtins only (no `pkgs.lib`): keeps this file evaluable and unit-
# testable with a bare `nix eval`, without needing a locked nixpkgs (mirrors
# lib/renderers.nix, issue #402).
let
  concatStrings = builtins.concatStringsSep "";
  mapAttrsToList = f: attrs: map (n: f n attrs.${n}) (builtins.attrNames attrs);
  # Matches `lib.escapeShellArg` byte for byte without depending on pkgs.lib:
  # a string of only shell-safe characters passes through unquoted; anything
  # else gets single-quote-wrapped, with embedded `'` escaped as `'\''`.
  escapeShellArg =
    arg:
    let
      string = builtins.toString arg;
    in
    if builtins.match "[[:alnum:],._+:@%/-]+" string == null then
      "'" + builtins.replaceStrings [ "'" ] [ "'\\''" ] string + "'"
    else
      string;
in
rec {
  # One renderer used by both the shell and Go preamble families: iterates
  # over flakeOption schema entries and emits `[export ]VAR="${VAR:-<baked>}"`
  # lines. A matching env var (or harness.env, sourced by the wrapper) still
  # wins at runtime.
  renderDefaultsPreamble =
    {
      export ? false,
      flakeOptionEntries,
      mergedDefaults,
    }:
    concatStrings (
      mapAttrsToList (
        key: entry:
        let
          value = mergedDefaults.${key};
          prefix = if export then "export " else "";
        in
        ''
          ${prefix}${entry.env}="''${${entry.env}:-${toString value}}"
        ''
      ) flakeOptionEntries
    );

  # Space-separated list of env var names forwarded from the host into the
  # Box, derived from schema boxEnv=true entries. The Go launcher reads
  # BOX_ENV_VARS and builds its container-arg list from it, eliminating the
  # hand-enumerated forwarding lists in runOneOCI / runOneBwrap.
  renderBoxEnvVarsList =
    schema:
    builtins.concatStringsSep " " (
      map (e: e.env) (builtins.filter (e: e.boxEnv or false) (builtins.attrValues schema))
    );

  # BOX_ENV_VARS exported for the Go binary.
  renderBoxEnvVarsPreamble = schema: ''
    export BOX_ENV_VARS="${renderBoxEnvVarsList schema}"
  '';

  # The Driver's in-box mount targets (ADR 0009), exported for the Go
  # launcher's runner adapters (cmd/launcher/internal/runner) so they mount
  # over the Driver's declared paths instead of a hardcoded ".claude"
  # literal. DRIVER_SESSION_CACHE_DIR is empty when the selected Driver
  # declares no session-state dir, in which case the launcher mounts no
  # driver cache on either backend.
  renderDriverMountPreamble =
    driverEntry:
    "export DRIVER_SKILLS_DIR="
    + escapeShellArg "/home/agent/${driverEntry.skillsDirRelative}"
    + "\n"
    + "export DRIVER_SESSION_CACHE_DIR="
    + escapeShellArg (
      if driverEntry ? sessionCacheDirRelative then
        "/home/agent/${driverEntry.sessionCacheDirRelative}"
      else
        ""
    )
    + "\n";

  # Exported preamble for the Go launcher's `run` wrapper: everything `run`
  # needs at sandbox dispatch time. `export` is required so the exec'd binary
  # inherits each value; plain assignments would be shell-only. OCI run also
  # bakes the build-time vars so EnsureReady can build the image on demand
  # when it is absent — the workflow is `build` first, but `run` must handle
  # a missing image gracefully on any machine.
  renderGoRunPreamble =
    {
      runnerKind,
      driverEntry,
      agentFilesPath,
      agentEnvPath,
      prefetch,
      imagePath,
      imageHash,
      runtime,
      imageDrv,
      nixBuilderImage,
      linuxSystem,
    }:
    if runnerKind == "bwrap" then
      ''
        export RUNTIME="bwrap"
        export DRIVER="${driverEntry.name}"
        export AGENT_FILES="${agentFilesPath}"
        export AGENT_ENV="${agentEnvPath}"
        BAKED_PREFETCH=${escapeShellArg prefetch}
        export BAKED_PREFETCH
      ''
      + renderDriverMountPreamble driverEntry
    else
      ''
        export IMAGE_ARCHIVE="${imagePath}"
        export IMAGE_TAG="spindrift:${imageHash}"
        export RUNTIME="${runtime}"
        export DRIVER="${driverEntry.name}"
        export IMAGE_DRV="${imageDrv}"
        export NIX_BUILDER_IMAGE="${nixBuilderImage}"
        export NIX_VOLUME="spindrift-nix"
        export FLAKE_IMAGE_ATTR=".#packages.${linuxSystem}.agent-image"
      ''
      + renderDriverMountPreamble driverEntry;

  # Exported preamble for the Go launcher's `build` wrapper: everything
  # `build` needs to realize the image/closure.
  renderGoBuildPreamble =
    {
      runnerKind,
      agentFilesDrv,
      agentEnvDrv,
      runtime,
      imagePath,
      imageHash,
      imageDrv,
      nixBuilderImage,
      linuxSystem,
    }:
    if runnerKind == "bwrap" then
      ''
        export RUNTIME="bwrap"
        export AGENT_FILES_DRV="${agentFilesDrv}"
        export AGENT_ENV_DRV="${agentEnvDrv}"
      ''
    else
      ''
        export RUNTIME="${runtime}"
        export IMAGE_ARCHIVE="${imagePath}"
        export IMAGE_TAG="spindrift:${imageHash}"
        export IMAGE_DRV="${imageDrv}"
        export NIX_BUILDER_IMAGE="${nixBuilderImage}"
        export NIX_VOLUME="spindrift-nix"
        export FLAKE_IMAGE_ATTR=".#packages.${linuxSystem}.agent-image"
      '';
}
