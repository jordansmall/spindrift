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
{
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

  # The Launcher input document's `artifacts` section for the `run` wrapper
  # (ADR 0020): everything `run` needs at sandbox dispatch time, as a plain
  # attrset instead of exported bash — mkHarness.nix renders it to JSON
  # alongside the `settings` section (renderInputDocumentJSON below). OCI run
  # also carries the build-time vars so EnsureReady can build the image on
  # demand when it is absent — the workflow is `build` first, but `run` must
  # handle a missing image gracefully on any machine. Replaces the pre-#625
  # renderGoRunPreamble, which exported the same values as bash env.
  runArtifacts =
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
      boxEnvVars,
    }:
    (
      if runnerKind == "bwrap" then
        {
          RUNTIME = "bwrap";
          DRIVER = driverEntry.name;
          AGENT_FILES = agentFilesPath;
          AGENT_ENV = agentEnvPath;
          BAKED_PREFETCH = prefetch;
        }
      else
        {
          IMAGE_ARCHIVE = imagePath;
          IMAGE_TAG = "spindrift:${imageHash}";
          RUNTIME = runtime;
          DRIVER = driverEntry.name;
          IMAGE_DRV = imageDrv;
          NIX_BUILDER_IMAGE = nixBuilderImage;
          NIX_VOLUME = "spindrift-nix";
          FLAKE_IMAGE_ATTR = ".#packages.${linuxSystem}.agent-image";
        }
    )
    // {
      DRIVER_SKILLS_DIR = "/home/agent/${driverEntry.skillsDirRelative}";
      DRIVER_SESSION_CACHE_DIR =
        if driverEntry ? sessionCacheDirRelative then
          "/home/agent/${driverEntry.sessionCacheDirRelative}"
        else
          "";
      BOX_ENV_VARS = boxEnvVars;
    };

  # The Launcher input document's `artifacts` section for the `build`
  # wrapper: everything `build` needs to realize the image/closure. Replaces
  # the pre-#625 renderGoBuildPreamble.
  buildArtifacts =
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
      {
        RUNTIME = "bwrap";
        AGENT_FILES_DRV = agentFilesDrv;
        AGENT_ENV_DRV = agentEnvDrv;
      }
    else
      {
        RUNTIME = runtime;
        IMAGE_ARCHIVE = imagePath;
        IMAGE_TAG = "spindrift:${imageHash}";
        IMAGE_DRV = imageDrv;
        NIX_BUILDER_IMAGE = nixBuilderImage;
        NIX_VOLUME = "spindrift-nix";
        FLAKE_IMAGE_ATTR = ".#packages.${linuxSystem}.agent-image";
      };

  # The Launcher input document (ADR 0020): a JSON object with a `settings`
  # section (resolved knob values, env-var-name keyed — the Consumer flake's
  # voice) and an `artifacts` section (nix-computed plumbing, from
  # runArtifacts/buildArtifacts above). mkHarness.nix writes this to a store
  # path and the generated wrapper passes it via a single `--input` flag,
  # instead of the per-var env exports the pre-#625 preambles emitted.
  renderInputDocumentJSON =
    { settings, artifacts }:
    builtins.toJSON {
      inherit settings artifacts;
    };
}
