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
  unique = builtins.foldl' (acc: x: if builtins.elem x acc then acc else acc ++ [ x ]) [ ];
  builtinsCompat = import ./builtins-compat.nix;
  inherit (builtinsCompat) concatStrings mapAttrsToList escapeShellArg;
  # Shared by runArtifacts and buildArtifacts below (issue #2677 review fix)
  # so the FLAKE_LAUNCHER_ATTR string is defined once instead of duplicated
  # verbatim at both call sites.
  launcherCurrencyAttr = system: ".#packages.${system}.launcher-currency";
  # Shared by runArtifacts' bwrap/OCI branches and buildArtifacts' OCI branch
  # below (issue #2667 review fix) so the FLAKE_IMAGE_ATTR shape is defined
  # once instead of hand-assembled at each call site.
  flakeImageAttrFor = system: name: ".#packages.${system}.${name}";
  # Shared by runArtifacts' and buildArtifacts' bwrap branches (issue #2672
  # review fix) so the six build-time drv keys can never render differently
  # between the run and build documents.
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
  # One renderer used by both the shell and Go preamble families: iterates
  # over flakeOption schema entries and emits `[export ]VAR=${VAR:-<baked>}`
  # lines, shell-escaping each baked default via escapeShellArg so a value
  # containing quotes (e.g. a builtins.toJSON default) neither trips SC2140 nor
  # corrupts at runtime (issue #2234). A matching env var (or harness.env,
  # sourced by the wrapper) still wins at runtime.
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

  # The 8 baked /agent/* path fallbacks (lib/agent-paths.nix), rendered the
  # same `VAR=${VAR:-<baked>}` fallback-preserving way as
  # renderDefaultsPreamble above -- not an unconditional overwrite like
  # renderDriverMountPreamble/renderPreamble (lib/drivers/default.nix), whose
  # Driver-identity vars are fixed per Box invocation. These 9 vars must stay
  # overridable by an already-exported env var: existing bats fixtures rely
  # on overriding a subset of them, and a future path relocation should still
  # be able to override the baked default without editing nix.
  renderAgentPathsPreamble =
    agentPaths:
    concatStrings (
      mapAttrsToList (var: path: "${var}=\${${var}:-${escapeShellArg path}}\n") agentPaths
    );

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
      passwdFilePath,
      groupFilePath,
      # The bwrap-only build-time drv counterparts of the four run-time paths
      # above (issue #2672): `spindrift build` has no doc of its own -- it
      # runs against this SAME run document (see the OCI-branch comment
      # below), so the bwrap branch needs its build artifacts here too, not
      # only in buildArtifacts' own bwrap branch.
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
      # The host system and its Linux twin, bundled (issue #2770 slice 1) so
      # this function takes one systems-shaped param instead of two loose
      # system strings. See the FLAKE_LAUNCHER_ATTR comment below for why the
      # two fields diverge in what they render.
      systems,
      boxEnvVars,
      # Four capability signals resolved by lib/mkHarness.nix from the
      # backend descriptor registry (lib/backends/default.nix) for the
      # active CODE_FORGE/ISSUE_TRACKER pairing (issue #2527 slice 1), as
      # plain bools. Rendered below as the literal strings "true"/"false" --
      # Go's getenvArtifact returns map[string]string, and a later slice
      # compares against the literal string "true".
      hostMediatedRemote,
      outboxRelayCapable,
      inBoxUnreachableTracker,
      fullyLocal,
      # The facts resolved by lib/mkHarness.nix from mergedDefaults/
      # finalRoster (issue #2533, scoutProvisioned added by #3157): the
      # tracker/forge axis strings and the roster/review-loop bools a prior
      # slice deleted from in-box Go (cmd/launcher/internal/promptassembly/
      # gates_tracker.go and friends)
      # so promptassembly's Env can read them via CLI flags fed from
      # getenvArtifact instead of re-deriving them itself.
      trackerAxisRead,
      trackerAxisWrite,
      trackerAxisFiler,
      forgeBackend,
      filerEnabled,
      workerProvisioned,
      scoutProvisioned,
      reviewLoopInline,
      reviewLoopOrchestrator,
      # The bwrap-only nix.conf artifact (issue #2664): the ephemeral overlay
      # store's nix.conf, sourced from the same nixConfigFile derivation the
      # OCI image bakes in directly (so oci never needs a runtime artifact
      # for it). Defaults to "" so a non-nixInBox Consumer's runArtifacts
      # call renders the key present-but-empty rather than omitting it.
      nixConfigPath ? "",
      # The bwrap ephemeral-overlay-store knob (ADR 0042, issue #2665): the
      # sibling of nixConfigPath -- when nixConfigPath is truthy (nixInBox
      # on), this decides whether the /nix/store overlay bwrap.go mounts is
      # additionally writable, vs. a plain read-only bind. No `?` default:
      # unlike nixConfigPath (meaningless, so blanked to "" when nixInBox is
      # off), nixStoreWritable is a real Consumer knob independent of
      # nixInBox and must always render its true value -- the AND-gate with
      # NixConfigFile that makes it a no-op when nixInBox is off lives in
      # cmd/launcher/internal/runner/bwrap.go, not here.
      nixStoreWritable,
      # The bwrap-only nix.conf build artifact (issue #2672): the drv
      # counterpart of nixConfigPath above, defaulting to "" for the same
      # nixInBox-off reason.
      nixConfigDrv ? "",
      # The compiled BPF syscall-filter artifact (issue #2670 slice 3): unlike
      # nixConfigPath above, this is a bwrap-hardening concern orthogonal to
      # nix-in-box -- it always builds and always renders a real path, so no
      # `?` default and no on/off knob.
      syscallFilterPath,
      # The build-time drv counterpart of syscallFilterPath above (issue
      # #2672), same reasoning as agentFilesDrv et al.
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
          # The bwrap freshness dimension (issue #2667): the same two keys
          # the OCI branch below renders, populated with the bwrap agent
          # closure's own flake attr/output path so Probe() can compare
          # them without caring which runnerKind produced them.
          FLAKE_IMAGE_ATTR = flakeImageAttrFor systems.linux "agent-closure";
          IMAGE_TAG = agentClosurePath;
        }
        # The build-time drv counterparts (issue #2672): `spindrift build`
        # has no doc of its own -- cmdBuild reads this same run document, so
        # the bwrap branch must carry its own build artifacts the way the
        # OCI branch already does (IMAGE_DRV et al below), instead of
        # relying on buildArtifacts' bwrap branch, which only backs the
        # separate build wrapper script, not `build` run against this doc.
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
      # Unlike FLAKE_IMAGE_ATTR above (rendered against systems.linux, the
      # Linux twin used only for Linux-bound OCI artifacts), the launcher is
      # a plain per-host-system Go binary (built via hostPkgs.buildGoModule,
      # runs on every platform including bwrap/darwin), so this correctly
      # renders against systems.host -- the host's own system.
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

  # The Launcher input document's `artifacts` section for the `build`
  # wrapper: everything `build` needs to realize the image/closure. Replaces
  # the pre-#625 renderGoBuildPreamble.
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
      # The host system and its Linux twin, bundled into one systems-shaped
      # param the same way as runArtifacts above (issue #2770 slice 2) — see
      # its comment for the bundling rationale and the FLAKE_IMAGE_ATTR/
      # FLAKE_LAUNCHER_ATTR divergence.
      systems,
      # The bwrap-only nix.conf build artifact (issue #2664) -- see
      # runArtifacts' nixConfigPath comment above for why it defaults to "".
      nixConfigDrv ? "",
      # See runArtifacts' syscallFilterPath comment above -- unconditional,
      # no `?` default.
      syscallFilterDrv,
      # bwrap-only (issue #2966): bwrap.go's closureGeneration() derives the
      # store-DB snapshot dir name from IMAGE_TAG, so this must render the
      # same value runArtifacts' bwrap branch does or NewBwrapBuild and
      # NewBwrap disagree about where that snapshot lives.
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

  # Every artifact key runArtifacts/buildArtifacts can emit, across both
  # runnerKind branches, unioned and sorted — the allowed-artifact-keys set
  # nix/checks/schema-drift.nix's launcher-env-coverage check derives from
  # what actually gets rendered, instead of hand-maintaining a parallel list
  # that can silently drift from it (issue #810). Called with placeholder
  # args at eval time: only each function's output *keys* matter here, not
  # the values. IMAGE and GITHUB_OUTPUT are manual escape hatches read via
  # getenvArtifact's env-only fallback, never emitted by either function, so
  # they're added explicitly rather than derived. GITHUB_OUTPUT is GitHub
  # Actions' own ambient step-output file path (#2324), not spindrift
  # plumbing, but the same env-only-escape-hatch shape as IMAGE.
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
