# Nix to bash preamble marshalling (issue #513): turns lib/env-schema.nix and
# driver-registry data into what the Go launcher and the wrapper scripts read;
# nix/checks/preambles.nix pins each renderer's output shape. Pure builtins
# only (no `pkgs.lib`) so a bare `nix eval` can test this file without a
# locked nixpkgs (mirrors lib/renderers.nix, issue #402).
let
  unique = builtins.foldl' (acc: x: if builtins.elem x acc then acc else acc ++ [ x ]) [ ];
  builtinsCompat = import ./builtins-compat.nix;
  inherit (builtinsCompat) concatStrings mapAttrsToList escapeShellArg;
  # Shared by runArtifacts and buildArtifacts so FLAKE_LAUNCHER_ATTR is
  # defined once (issue #2677 review fix).
  launcherCurrencyAttr = system: ".#packages.${system}.launcher-currency";
  # Shared by runArtifacts' bwrap/OCI branches and buildArtifacts' OCI branch
  # so the FLAKE_IMAGE_ATTR shape is assembled once (issue #2667 review fix).
  flakeImageAttrFor = system: name: ".#packages.${system}.${name}";
  # Shared by runArtifacts' and buildArtifacts' bwrap branches so the six
  # build-time drv keys can never render differently between the run and
  # build documents (issue #2672 review fix).
  bwrapDrvArtifacts =
    {
      agentFilesDrv,
      agentEnvDrv,
      passwdFileDrv,
      groupFileDrv,
      nixConfigDrv ? "",
      syscallFilterDrv,
    }:
    {
      AGENT_FILES_DRV = agentFilesDrv;
      AGENT_ENV_DRV = agentEnvDrv;
      PASSWD_FILE_DRV = passwdFileDrv;
      GROUP_FILE_DRV = groupFileDrv;
      NIX_CONFIG_FILE_DRV = nixConfigDrv;
      SYSCALL_FILTER_DRV = syscallFilterDrv;
    };
in
rec {
  # Shell-escapes each baked default via escapeShellArg so a value containing
  # quotes (e.g. a builtins.toJSON default) neither trips SC2140 nor corrupts
  # at runtime (issue #2234). A matching env var (or harness.env, sourced by
  # the wrapper) still wins at runtime.
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
          ${prefix}${entry.env}=''${${entry.env}:-${escapeShellArg value}}
        ''
      ) flakeOptionEntries
    );

  # The Go launcher reads BOX_ENV_VARS and builds its container-arg list from
  # it, so runOneOCI / runOneBwrap need no hand-enumerated forwarding lists.
  renderBoxEnvVarsList =
    schema:
    builtins.concatStringsSep " " (
      map (e: e.env) (builtins.filter (e: e.boxEnv or false) (builtins.attrValues schema))
    );

  # The Driver's in-box mount targets (ADR 0009) so the launcher's runner
  # adapters mount over the Driver's declared paths instead of a hardcoded
  # ".claude" literal. DRIVER_SESSION_CACHE_DIR is empty when the selected
  # Driver declares no session-state dir, and the launcher then mounts no
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

  # The baked /agent/* path fallbacks (lib/agent-paths.nix), rendered the
  # fallback-preserving `VAR=${VAR:-<baked>}` way rather than as an
  # unconditional overwrite: these vars must stay overridable by an
  # already-exported env var, because bats fixtures override a subset of them
  # and a path relocation should not need a nix edit.
  renderAgentPathsPreamble =
    agentPaths:
    concatStrings (
      mapAttrsToList (var: path: "${var}=\${${var}:-${escapeShellArg path}}\n") agentPaths
    );

  # The Launcher input document's `artifacts` section for the `run` wrapper
  # (ADR 0020, issue #625). OCI run also carries the build-time vars so
  # EnsureReady can build the image on demand when it is absent: the workflow
  # is `build` first, but `run` must still handle a missing image on any
  # machine.
  runArtifacts =
    {
      runnerKind,
      driverEntry,
      agentFilesPath,
      agentEnvPath,
      passwdFilePath,
      groupFilePath,
      # `spindrift build` has no doc of its own: it runs against this same run
      # document, so the bwrap branch needs its build-time drv counterparts
      # here too, not only in buildArtifacts' own bwrap branch (issue #2672).
      agentFilesDrv,
      agentEnvDrv,
      passwdFileDrv,
      groupFileDrv,
      agentClosurePath,
      prefetch,
      imagePath,
      imageHash,
      launcherCurrencyHash,
      imageName,
      runtime,
      imageDrv,
      nixBuilderImage,
      # The host system and its Linux twin, bundled (issue #2770 slice 1). See
      # the FLAKE_LAUNCHER_ATTR comment below for why the two fields diverge.
      systems,
      boxEnvVars,
      # Capability signals resolved by lib/mkHarness.nix from the backend
      # descriptor registry for the active CODE_FORGE/ISSUE_TRACKER pairing
      # (issue #2527 slice 1). Rendered below as the literal strings
      # "true"/"false": Go's getenvArtifact returns map[string]string, and
      # callers compare against the literal string "true".
      hostMediatedRemote,
      outboxRelayCapable,
      inBoxUnreachableTracker,
      fullyLocal,
      # Facts resolved by lib/mkHarness.nix from mergedDefaults/finalRoster
      # (issue #2533, scoutProvisioned added by #3157) so promptassembly's Env
      # reads them via CLI flags fed from getenvArtifact instead of re-deriving
      # them in the Box.
      trackerAxisRead,
      trackerAxisWrite,
      trackerAxisFiler,
      forgeBackend,
      filerEnabled,
      workerProvisioned,
      scoutProvisioned,
      reviewLoopInline,
      reviewLoopOrchestrator,
      # The bwrap-only nix.conf artifact (issue #2664), from the same
      # nixConfigFile derivation the OCI image bakes in directly. Defaults to
      # "" so a non-nixInBox Consumer renders the key present-but-empty rather
      # than omitting it.
      nixConfigPath ? "",
      # The bwrap ephemeral-overlay-store knob (ADR 0042, issue #2665). No `?`
      # default: unlike nixConfigPath, which is meaningless and so blanked to
      # "" when nixInBox is off, this is a real Consumer knob independent of
      # nixInBox and must always render its true value. The AND-gate with
      # NixConfigFile lives in cmd/launcher/internal/runner/bwrap.go, not here.
      nixStoreWritable,
      # The drv counterpart of nixConfigPath above (issue #2672), defaulting
      # to "" for the same nixInBox-off reason.
      nixConfigDrv ? "",
      # The compiled BPF syscall-filter artifact (issue #2670 slice 3). Unlike
      # nixConfigPath above, this bwrap hardening is orthogonal to nix-in-box:
      # it always builds, so no `?` default and no on/off knob.
      syscallFilterPath,
      # The build-time drv counterpart of syscallFilterPath above
      # (issue #2672), same reasoning as agentFilesDrv.
      syscallFilterDrv,
    }:
    (
      if runnerKind == "bwrap" then
        {
          RUNTIME = "bwrap";
          DRIVER = driverEntry.name;
          AGENT_FILES = agentFilesPath;
          AGENT_ENV = agentEnvPath;
          PASSWD_FILE = passwdFilePath;
          GROUP_FILE = groupFilePath;
          BAKED_PREFETCH = prefetch;
          NIX_CONFIG_FILE = nixConfigPath;
          NIX_STORE_WRITABLE = if nixStoreWritable then "true" else "false";
          SYSCALL_FILTER = syscallFilterPath;
          # The same two keys the OCI branch below renders, populated with the
          # bwrap agent closure's own flake attr and output path, so Probe()
          # can compare them without caring which runnerKind produced them
          # (issue #2667).
          FLAKE_IMAGE_ATTR = flakeImageAttrFor systems.linux "agent-closure";
          IMAGE_TAG = agentClosurePath;
        }
        # cmdBuild reads this same run document, so the bwrap branch carries
        # its own build artifacts the way the OCI branch does (IMAGE_DRV and
        # friends below). buildArtifacts' bwrap branch only backs the separate
        # build wrapper script, not `build` run against this doc (issue #2672).
        // bwrapDrvArtifacts {
          inherit
            agentFilesDrv
            agentEnvDrv
            passwdFileDrv
            groupFileDrv
            nixConfigDrv
            syscallFilterDrv
            ;
        }
      else
        {
          IMAGE_ARCHIVE = imagePath;
          IMAGE_TAG = "${imageName}:${imageHash}";
          RUNTIME = runtime;
          DRIVER = driverEntry.name;
          IMAGE_DRV = imageDrv;
          NIX_BUILDER_IMAGE = nixBuilderImage;
          NIX_VOLUME = "spindrift-nix";
          FLAKE_IMAGE_ATTR = flakeImageAttrFor systems.linux "agent-image";
        }
    )
    // {
      RUNNER_KIND = runnerKind;
      DRIVER_SKILLS_DIR = "/home/agent/${driverEntry.skillsDirRelative}";
      DRIVER_SESSION_CACHE_DIR =
        if driverEntry ? sessionCacheDirRelative then
          "/home/agent/${driverEntry.sessionCacheDirRelative}"
        else
          "";
      BOX_ENV_VARS = boxEnvVars;
      # Unlike FLAKE_IMAGE_ATTR above, rendered against systems.linux for
      # Linux-bound OCI artifacts, the launcher is a per-host-system Go binary
      # that runs on every platform including bwrap/darwin, so this renders
      # against systems.host.
      FLAKE_LAUNCHER_ATTR = launcherCurrencyAttr systems.host;
      LAUNCHER_CURRENCY_HASH = launcherCurrencyHash;
      HOST_MEDIATED_REMOTE = if hostMediatedRemote then "true" else "false";
      OUTBOX_RELAY_CAPABLE = if outboxRelayCapable then "true" else "false";
      IN_BOX_UNREACHABLE_TRACKER = if inBoxUnreachableTracker then "true" else "false";
      FULLY_LOCAL = if fullyLocal then "true" else "false";
      TRACKER_AXIS_READ = trackerAxisRead;
      TRACKER_AXIS_WRITE = trackerAxisWrite;
      TRACKER_AXIS_FILER = trackerAxisFiler;
      FORGE_BACKEND = forgeBackend;
      FILER_ENABLED = if filerEnabled then "true" else "false";
      WORKER_PROVISIONED = if workerProvisioned then "true" else "false";
      SCOUT_PROVISIONED = if scoutProvisioned then "true" else "false";
      REVIEW_LOOP_INLINE = if reviewLoopInline then "true" else "false";
      REVIEW_LOOP_ORCHESTRATOR = if reviewLoopOrchestrator then "true" else "false";
    };

  # The Launcher input document's `artifacts` section for the `build` wrapper
  # (ADR 0020, issue #625): everything `build` needs to realize the image or
  # closure.
  buildArtifacts =
    {
      runnerKind,
      agentFilesDrv,
      agentEnvDrv,
      passwdFileDrv,
      groupFileDrv,
      runtime,
      imagePath,
      imageHash,
      launcherCurrencyHash,
      imageName,
      imageDrv,
      nixBuilderImage,
      # Bundled the same way as runArtifacts above (issue #2770 slice 2). See
      # its comment for the FLAKE_IMAGE_ATTR/FLAKE_LAUNCHER_ATTR divergence.
      systems,
      # The bwrap-only nix.conf build artifact (issue #2664). See runArtifacts'
      # nixConfigPath comment above for why it defaults to "".
      nixConfigDrv ? "",
      # See runArtifacts' syscallFilterPath comment above: unconditional, so
      # no `?` default.
      syscallFilterDrv,
      # bwrap.go's closureGeneration() derives the store-DB snapshot dir name
      # from IMAGE_TAG, so this must render the same value runArtifacts' bwrap
      # branch does or NewBwrapBuild and NewBwrap disagree about where that
      # snapshot lives (issue #2966).
      agentClosurePath,
    }:
    (
      if runnerKind == "bwrap" then
        {
          RUNTIME = "bwrap";
          IMAGE_TAG = agentClosurePath;
        }
        // bwrapDrvArtifacts {
          inherit
            agentFilesDrv
            agentEnvDrv
            passwdFileDrv
            groupFileDrv
            nixConfigDrv
            syscallFilterDrv
            ;
        }
      else
        {
          RUNTIME = runtime;
          IMAGE_ARCHIVE = imagePath;
          IMAGE_TAG = "${imageName}:${imageHash}";
          IMAGE_DRV = imageDrv;
          NIX_BUILDER_IMAGE = nixBuilderImage;
          NIX_VOLUME = "spindrift-nix";
          FLAKE_IMAGE_ATTR = flakeImageAttrFor systems.linux "agent-image";
        }
    )
    // {
      RUNNER_KIND = runnerKind;
      FLAKE_LAUNCHER_ATTR = launcherCurrencyAttr systems.host;
      LAUNCHER_CURRENCY_HASH = launcherCurrencyHash;
    };

  # Lets nix/checks/schema-drift.nix derive the allowed artifact keys from
  # what actually renders instead of a parallel list that can silently drift
  # (issue #810). The placeholder args below are safe because only the output
  # keys matter. IMAGE and GITHUB_OUTPUT (#2324) are env-only escape hatches
  # neither function emits, so they are added by hand.
  documentArtifactKeys =
    let
      dummyDriverEntry = {
        name = "dummy";
        skillsDirRelative = "dummy";
        sessionCacheDirRelative = "dummy";
      };
      dummyRunArtifacts =
        runnerKind:
        runArtifacts {
          inherit runnerKind;
          driverEntry = dummyDriverEntry;
          agentFilesPath = "dummy";
          agentEnvPath = "dummy";
          passwdFilePath = "dummy";
          groupFilePath = "dummy";
          agentFilesDrv = "dummy";
          agentEnvDrv = "dummy";
          passwdFileDrv = "dummy";
          groupFileDrv = "dummy";
          agentClosurePath = "dummy";
          prefetch = "dummy";
          imagePath = "dummy";
          imageHash = "dummy";
          launcherCurrencyHash = "dummy";
          imageName = "dummy";
          runtime = "dummy";
          imageDrv = "dummy";
          nixBuilderImage = "dummy";
          systems = {
            host = "dummy";
            linux = "dummy";
          };
          boxEnvVars = "dummy";
          hostMediatedRemote = false;
          outboxRelayCapable = false;
          inBoxUnreachableTracker = false;
          fullyLocal = false;
          trackerAxisRead = "dummy";
          trackerAxisWrite = "dummy";
          trackerAxisFiler = "dummy";
          forgeBackend = "dummy";
          filerEnabled = false;
          workerProvisioned = false;
          scoutProvisioned = false;
          reviewLoopInline = false;
          reviewLoopOrchestrator = false;
          nixConfigPath = "dummy";
          nixConfigDrv = "dummy";
          nixStoreWritable = false;
          syscallFilterPath = "dummy";
          syscallFilterDrv = "dummy";
        };
      dummyBuildArtifacts =
        runnerKind:
        buildArtifacts {
          inherit runnerKind;
          agentFilesDrv = "dummy";
          agentEnvDrv = "dummy";
          passwdFileDrv = "dummy";
          groupFileDrv = "dummy";
          runtime = "dummy";
          imagePath = "dummy";
          imageHash = "dummy";
          launcherCurrencyHash = "dummy";
          imageName = "dummy";
          imageDrv = "dummy";
          nixBuilderImage = "dummy";
          systems = {
            host = "dummy";
            linux = "dummy";
          };
          nixConfigDrv = "dummy";
          syscallFilterDrv = "dummy";
          agentClosurePath = "dummy";
        };
      allKeys =
        builtins.concatMap (runnerKind: builtins.attrNames (dummyRunArtifacts runnerKind)) [
          "bwrap"
          "oci"
        ]
        ++ builtins.concatMap (runnerKind: builtins.attrNames (dummyBuildArtifacts runnerKind)) [
          "bwrap"
          "oci"
        ]
        ++ [
          "IMAGE"
          "GITHUB_OUTPUT"
        ];
    in
    builtins.sort builtins.lessThan (unique allKeys);

  # The Launcher input document (ADR 0020): a `settings` section of resolved
  # knob values keyed by env var name, and an `artifacts` section of
  # nix-computed plumbing. mkHarness.nix writes it to a store path and the
  # generated wrapper passes it via a single `--input` flag.
  renderInputDocumentJSON =
    { settings, artifacts }:
    builtins.toJSON {
      inherit settings artifacts;
    };
}
