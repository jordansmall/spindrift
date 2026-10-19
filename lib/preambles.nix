# Nix to bash preamble marshalling (issue #513): turns lib/env-schema.nix and
# driver-registry data into what the Go launcher and the wrapper scripts read;
# nix/checks/preambles.nix pins each renderer's output shape. Pure builtins
# only (no `pkgs.lib`) so a bare `nix eval` can test this file without a
# locked nixpkgs (mirrors lib/renderers.nix, issue #402).
let
  unique = builtins.foldl' (acc: x: if builtins.elem x acc then acc else acc ++ [ x ]) [ ];
  builtinsCompat = import ./builtins-compat.nix;
  inherit (builtinsCompat) concatStrings mapAttrsToList escapeShellArg;
  # The value handed to each row as `flakeImageAttr`, so the attr shape is
  # assembled once (issue #2667 review fix) while each row renders the key.
  flakeImageAttrFor = system: name: ".#packages.${system}.${name}";
  runnerRows = (import ./runners/index.nix { }).rows;
in
rec {
  # Shell-escapes each baked default via escapeShellArg so a value containing
  # quotes (e.g. a builtins.toJSON default) neither trips SC2140 nor corrupts
  # at runtime (issue #2234). A matching env var (or harness.env, sourced by
  # the wrapper) still wins at runtime. Bool knobs use the colon-less `-`:
  # set-but-empty is their explicit-off encoding (as in the launcher), and
  # `:-` would re-default it to on (issue #4290).
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
          op = if (entry.kind or null) == "bool" then "-" else ":-";
        in
        ''
          ${prefix}${entry.env}=''${${entry.env}${op}${escapeShellArg value}}
        ''
      ) flakeOptionEntries
    );

  # The Go launcher reads BOX_ENV_VARS and builds the Box's env from it
  # (buildBoxEnv in cmd/launcher/internal/dispatch), so neither runner
  # backend needs a hand-enumerated forwarding list.
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

  # The Launcher input document's `artifacts` section of the run input
  # (ADR 0020, issue #625). The keys only one runner family renders come from
  # that family's row (lib/runners, `runArtifacts`, ADR 0069 decision 7), so
  # this function names no family; `...` carries the row-only inputs through,
  # so callers use `runArtifacts` below, which rejects the unknown names.
  renderRunArtifacts =
    {
      # The runner row (lib/runners): its name and artifact (which Box artifact
      # the family boots) become RUNNER_KIND and BOX_ARTIFACT, and its
      # `runArtifacts` function renders the family's own keys.
      row,
      driverEntry,
      launcherCurrencyHash,
      imageAttrName,
      runtime,
      # The host system and its Linux twin, bundled (issue #2770 slice 1). See
      # the FLAKE_LAUNCHER_ATTR comment below for why the two fields diverge.
      systems,
      boxEnvVars,
      # Capability signals resolved by lib/mkHarness.nix from the backend
      # descriptor registry for the active CODE_FORGE/ISSUE_TRACKER pairing
      # (issue #2527 slice 1). Rendered below as the literal strings
      # "true"/"false": inputdoc.Document.Artifacts is a map[string]string,
      # and the launcher compares docArtifact(...) against the literal "true".
      hostMediatedRemote,
      outboxRelayCapable,
      inBoxUnreachableTracker,
      fullyLocal,
      # Facts resolved by lib/mkHarness.nix from mergedDefaults/finalRoster
      # (issue #2533, scoutProvisioned added by #3157). The launcher reads
      # them from the document's artifacts (deriving them host-side when a key
      # is missing or the CODE_FORGE/ISSUE_TRACKER pairing mismatches) and
      # forwards them as BOX_* env; promptassembly.EnvFromEnviron reads that
      # env in the Box instead of re-deriving them there.
      trackerAxisRead,
      trackerAxisWrite,
      trackerAxisFiler,
      forgeBackend,
      filerEnabled,
      workerProvisioned,
      scoutProvisioned,
      # Space-joined stems of the image's chores/*.md (issue #3905), so
      # butlerPreflight rejects a Chore with no prompt before claiming it.
      choreCatalog,
      # JSON {roster entry name: model} and the Driver CLI package version
      # (issue #4783), stamped onto each Pass log's dispatch_start.
      roleModels,
      driverVersion,
      ...
    }@args:
    row.runArtifacts (args // { flakeImageAttr = flakeImageAttrFor systems.linux imageAttrName; })
    // {
      RUNNER_KIND = row.name;
      BOX_ARTIFACT = row.artifact;
      # forRuntime picks the bwrap row only for runtime=bwrap, so this is the
      # literal "bwrap" there and the chosen runtime on the OCI family.
      RUNTIME = runtime;
      DRIVER = driverEntry.name;
      DRIVER_SKILLS_DIR = "/home/agent/${driverEntry.skillsDirRelative}";
      DRIVER_SESSION_CACHE_DIR =
        if driverEntry ? sessionCacheDirRelative then
          "/home/agent/${driverEntry.sessionCacheDirRelative}"
        else
          "";
      BOX_ENV_VARS = boxEnvVars;
      # Unlike the rows' FLAKE_IMAGE_ATTR, rendered against systems.linux for
      # Linux-bound OCI artifacts, the launcher is a per-host-system Go binary
      # that runs on every platform including bwrap/darwin, so this renders
      # against systems.host.
      FLAKE_LAUNCHER_ATTR = ".#packages.${systems.host}.launcher-currency";
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
      CHORE_CATALOG = choreCatalog;
      ROLE_MODELS = roleModels;
      DRIVER_VERSION = driverVersion;
    };

  # Restores the unknown-argument error that `...` above would swallow: every
  # name must be a formal of the trailer or of some row's `runArtifacts`
  # (mkHarness passes the union of all rows' inputs, whichever row it picked).
  # `flakeImageAttr` is a row formal but derived above from systems.linux and
  # imageAttrName, so a caller-passed one would be silently overwritten.
  runArtifacts =
    let
      withoutDerived = fns: removeAttrs fns [ "flakeImageAttr" ];
      sharedInputs = withoutDerived (
        builtins.foldl' (
          acc: row: acc // builtins.functionArgs row.runArtifacts
        ) (builtins.functionArgs renderRunArtifacts) runnerRows
      );
    in
    args:
    let
      allowed = sharedInputs // withoutDerived (builtins.functionArgs args.row.runArtifacts);
      unknown = builtins.filter (n: !(allowed ? ${n})) (builtins.attrNames args);
    in
    if unknown != [ ] then
      throw "preambles.runArtifacts: unknown argument(s) ${builtins.concatStringsSep ", " unknown}: neither the shared trailer nor any runner row (lib/runners) takes them (flakeImageAttr is derived from systems.linux and imageAttrName, never passed)"
    else if !(builtins.elem args.runtime args.row.runtimes) then
      throw "preambles.runArtifacts: runtime ${args.runtime} is not one of runner row ${args.row.name}'s runtimes (${builtins.concatStringsSep ", " args.row.runtimes}); the document would name a family and a runtime that contradict each other"
    else
      renderRunArtifacts args;

  # Placeholder arguments for deriving the artifact keys that render: only the
  # output keys matter, so every value is a dummy. Exposed so
  # nix/checks/schema-drift.nix can render a stub row without a parallel copy.
  dummyRunArtifactArgs = {
    driverEntry = {
      name = "dummy";
      skillsDirRelative = "dummy";
      sessionCacheDirRelative = "dummy";
    };
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
    imageAttrName = "dummy";
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
    choreCatalog = "dummy";
    roleModels = "dummy";
    driverVersion = "dummy";
    nixConfigPath = "dummy";
    nixConfigDrv = "dummy";
    nixStoreWritable = false;
    syscallFilterPath = "dummy";
    syscallFilterDrv = "dummy";
  };

  # The run-document keys one row renders by itself (without the shared
  # trailer), for checks that compare a row against the whole document.
  rowArtifactKeys =
    row: builtins.attrNames (row.runArtifacts (dummyRunArtifactArgs // { flakeImageAttr = "dummy"; }));

  # Lets nix/checks/schema-drift.nix derive the allowed artifact keys from
  # what actually renders instead of a parallel list that can silently drift
  # (issue #810). IMAGE and GITHUB_OUTPUT (#2324) are env-only escape hatches
  # runArtifacts does not emit, so they are added by hand.
  documentArtifactKeysFor =
    rows:
    builtins.sort builtins.lessThan (
      unique (
        builtins.concatMap (
          row:
          builtins.attrNames (
            runArtifacts (
              dummyRunArtifactArgs
              // {
                inherit row;
                runtime = builtins.head row.runtimes;
              }
            )
          )
        ) rows
        ++ [
          "IMAGE"
          "GITHUB_OUTPUT"
        ]
      )
    );

  documentArtifactKeys = documentArtifactKeysFor runnerRows;

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
