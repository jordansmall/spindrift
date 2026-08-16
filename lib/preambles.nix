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
  # Driver-identity vars are fixed per Box invocation. These 8 vars must stay
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
      prefetch,
      imagePath,
      imageHash,
      imageName,
      runtime,
      imageDrv,
      nixBuilderImage,
      linuxSystem,
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
      # Eight facts resolved by lib/mkHarness.nix from mergedDefaults/
      # finalRoster (issue #2533): the tracker/forge axis strings and the
      # roster/review-loop bools a prior slice deleted from in-box Go
      # (cmd/launcher/internal/promptassembly/gates_tracker.go and friends)
      # so promptassembly's Env can read them via CLI flags fed from
      # getenvArtifact instead of re-deriving them itself.
      trackerAxisRead,
      trackerAxisWrite,
      trackerAxisFiler,
      forgeBackend,
      filerEnabled,
      workerProvisioned,
      reviewLoopInline,
      reviewLoopOrchestrator,
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
          IMAGE_TAG = "${imageName}:${imageHash}";
          RUNTIME = runtime;
          DRIVER = driverEntry.name;
          IMAGE_DRV = imageDrv;
          NIX_BUILDER_IMAGE = nixBuilderImage;
          NIX_VOLUME = "spindrift-nix";
          FLAKE_IMAGE_ATTR = ".#packages.${linuxSystem}.agent-image";
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
      runtime,
      imagePath,
      imageHash,
      imageName,
      imageDrv,
      nixBuilderImage,
      linuxSystem,
    }:
    (
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
          IMAGE_TAG = "${imageName}:${imageHash}";
          IMAGE_DRV = imageDrv;
          NIX_BUILDER_IMAGE = nixBuilderImage;
          NIX_VOLUME = "spindrift-nix";
          FLAKE_IMAGE_ATTR = ".#packages.${linuxSystem}.agent-image";
        }
    )
    // {
      RUNNER_KIND = runnerKind;
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
          prefetch = "dummy";
          imagePath = "dummy";
          imageHash = "dummy";
          imageName = "dummy";
          runtime = "dummy";
          imageDrv = "dummy";
          nixBuilderImage = "dummy";
          linuxSystem = "dummy";
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
          reviewLoopInline = false;
          reviewLoopOrchestrator = false;
        };
      dummyBuildArtifacts =
        runnerKind:
        buildArtifacts {
          inherit runnerKind;
          agentFilesDrv = "dummy";
          agentEnvDrv = "dummy";
          runtime = "dummy";
          imagePath = "dummy";
          imageHash = "dummy";
          imageName = "dummy";
          imageDrv = "dummy";
          nixBuilderImage = "dummy";
          linuxSystem = "dummy";
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
