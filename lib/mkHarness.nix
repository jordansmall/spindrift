# The engine. A pure function a Consumer flake calls with its own locked
# `nixpkgs` input and `system`; returns the agent image plus the `spindrift`
# CLI (as both `packages.spindrift` and `apps.default`).
#
# Takes the locked *input* rather than a pre-built `pkgs` so it can map a darwin
# `system` to its Linux twin and re-instantiate for the OCI image, keeping the
# agent's toolchain and the Consumer's dev shell from one pin (ADR 0002). The
# image is target-agnostic: REPO_SLUG, auth, and commit identity stay runtime
# env, never Nix options (ADR 0001).
{
  nixpkgs,
  system,
  overlays ? [ ],
  config ? { },
  # Project-specific tools baked into the image on top of the harness plumbing,
  # as a function of the (Linux) pkgs — the Consumer's language/toolchain surface.
  packages ? (_pkgs: [ ]),
  # Optional shell snippet the entrypoint runs after cloning, to warm toolchain
  # caches (e.g. fetch pinned deps). Baked into the image; default is a no-op.
  prefetch ? "",
  # The agent prompt template, a Consumer-owned artifact. Baked into the image
  # at /agent/prompts (see agentFiles); changing it requires an image rebuild.
  # SPINDRIFT_PROMPT_DIR mounts an override directory at runtime for zero-rebuild
  # iteration (the Go launcher mounts it in cmd/launcher/internal/runner).
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  # Subagent system prompts. Defaults ship with the harness; Consumers can
  # override via the `prompt` directory mechanism (SPINDRIFT_PROMPT_DIR).
  scoutPrompt ? builtins.readFile ../templates/default/prompts/scout-prompt.md,
  reviewPrompt ? builtins.readFile ../templates/default/prompts/review-prompt.md,
  # Opt-in: provisioned only when filerModel is non-empty (see agentsJsonTemplate).
  filerPrompt ? builtins.readFile ../templates/default/prompts/filer-prompt.md,
  conflictResolvePrompt ? builtins.readFile ../templates/default/prompts/conflict-resolve-prompt.md,
  # Driven instead of `prompt` on a fix box (FIX_PASS>0, ADR: selfHeal/runFix
  # in cmd/launcher): the branch is already checked out, so this warm-fix
  # prompt skips scout/implement-from-scratch and goes straight to
  # check/fix/commit/push/watch-CI.
  fixPrompt ? builtins.readFile ../templates/default/prompts/fix-prompt.md,
  # The Conditional fragment registry (issue #622, CONTEXT.md): rows of
  # (gate, fragment, var) the entrypoint's single fragment loop and its
  # `_subst` substitution allowlist are both rendered from. Not
  # Consumer-tunable like `prompt`/`scoutPrompt`/etc above (see
  # fragmentsSourceDir below); overridable here only for the bats
  # fixture-row test proving a new row needs no entrypoint edit.
  fragments ? import ./fragments.nix,
  # Skill files baked into the image at /home/agent/.claude/skills so the
  # headless agent can invoke them without a runtime mount. Each element is
  # either a path/derivation (copied under its basename), or a
  # { name; src; } content entry (issue #597) baked under the given name by
  # re-realizing src with the image's own Linux pkgs — never a consumer host
  # derivation, which would tag the image's drvPath with the host system.
  # SPINDRIFT_SKILLS_DIR at runtime mounts over the same path and takes
  # precedence, shadowing all baked skills.
  skills ? [ ],
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default) or "docker".
  runtime ? "podman",
  # The agent CLI Driver (ADR 0009): a build-time choice selecting one entry
  # from the lib/drivers/ registry, baked into the image (in-box half) and
  # threaded to the Go launcher as DRIVER (host-side half). "claude" is the
  # only Driver today.
  driver ? "claude",
  # Fallback Linux builder for when the host can't realize the Linux image itself
  # (the stock-mac case). Fully qualified so podman needs no default registry.
  # Pinned by manifest-list digest for reproducibility and supply-chain safety —
  # this container runs with the consumer tree bind-mounted read-write, so a
  # silently-updated :latest would be a code-execution vector.
  # To bump: pull the image, run `podman image inspect --format '{{.RepoDigests}}' nixos/nix`,
  # and update the digest here and in README.md.
  nixBuilderImage ? "docker.io/nixos/nix@sha256:bf1d938835ab96312f098fa6c2e9cab367728e0aad0646ee3e02a787c80d8fb8",
  # Bake a usable nix into the box (binary + a registered store DB + a
  # single-user, sandbox-off nix.conf) so `nix flake check` and `nix develop`
  # run inside the unprivileged throwaway container. On by default — this is the
  # nix-centric baseline every box gets; set to false for a lean, nix-free image.
  nixInBox ? true,
  # Self-test mode (ADR 0018, issue #469): makes the /nix/store DIRECTORY
  # (not its existing contents, which stay root-owned and immutable) writable
  # by the agent uid in the built OCI image, so a `nix flake check` run inside
  # the Box can substitute/build new store paths instead of hitting EACCES.
  # New paths land in the container's ephemeral copy-on-write layer and die
  # with the Box — the image and any shared volumes are never mutated. Off by
  # default: this trades hermeticity for in-box feedback, so the entrypoint
  # prints a loud warning when it is enabled. OCI-runner only; the bwrap
  # runner keeps its read-only store bind.
  nixStoreWritable ? false,
  # Extra derivations whose closures are baked into the image contents and,
  # when nixInBox is on, registered in the store DB alongside the runtime
  # closure — so in-box nix sees them as already present instead of
  # cold-substituting the world on every Box. A function of the (Linux) pkgs,
  # like `packages`, so Consumer-supplied derivations stay correct on a
  # darwin host. A generic Consumer knob, not a spindrift special case
  # (issue #469).
  extraClosures ? (_pkgs: [ ]),
  # Short git revision injected into the binary via ldflags for `spindrift --version`.
  # Callers pass self.shortRev or self.rev; defaults to "unknown" for impure builds.
  revision ? "unknown",
}:
let
  # OCI images are Linux-only. Map the Consumer's (possibly darwin) system to
  # its Linux twin for the image.
  linuxSystem =
    {
      "aarch64-darwin" = "aarch64-linux";
      "x86_64-darwin" = "x86_64-linux";
      "aarch64-linux" = "aarch64-linux";
      "x86_64-linux" = "x86_64-linux";
    }
    .${system};

  mergedConfig = {
    allowUnfree = true;
  }
  // config;

  # Image toolset: the Consumer's locked nixpkgs, re-instantiated for Linux.
  pkgs = import nixpkgs {
    system = linuxSystem;
    inherit overlays;
    config = mergedConfig;
  };

  # Host toolset: the launcher commands run on the Consumer's own system. Takes
  # the same overlays as the image so the tools pinned into the launchers
  # (gh/git/coreutils via runtimeInputs) can be overridden consistently.
  hostPkgs = import nixpkgs {
    inherit system overlays;
    config = mergedConfig;
  };

  inherit (pkgs) lib;

  # Single source of truth for every runtime knob — name mapping, defaults, scope.
  # Generators below derive all per-knob output from this registry; no per-knob
  # lines appear anywhere else in this file.
  schema = import ./env-schema.nix;

  # Section taxonomy and man-page renderer, shared with flakeModule.nix and the
  # nix/checks/schema-drift.nix guards so none of them can drift from each
  # other (issue #461).
  renderers = import ./renderers.nix;

  # Nix→bash preamble marshalling shared by the entrypoint and the Go
  # launcher wrappers below (issue #513); nix/checks/preambles.nix pins each
  # renderer's output shape.
  preambles = import ./preambles.nix;

  # Marker-delimited slicing/injection primitives (issue #512);
  # nix/checks/prompt-inject.nix pins each primitive's behavior.
  promptInject = import ./prompt-inject.nix;
  inherit (promptInject) sliceBetween sliceFromMarker injectSection;

  # issue-prompt.md is the single source every shared block below is sliced
  # from — read once so each slice sees the identical text.
  issuePromptSource = builtins.readFile ../templates/default/prompts/issue-prompt.md;

  # The conditional prompt steps (skill preamble, FILE ISSUES, AUTO-FORMAT,
  # AUTO-LINT, CI FAILURE) live as fragment files under the prompts directory
  # rather than heredocs in agent/entrypoint.sh (issue #463): not
  # Consumer-tunable like `prompt`/`scoutPrompt`/etc above, so baked from this
  # fixed source into every image the same way, under /agent/prompts/fragments
  # -- a SPINDRIFT_PROMPT_DIR override supplies its own fragment for whichever
  # knob it enables, exactly as it already must supply filer-prompt.md.
  fragmentsSourceDir = ../templates/default/prompts/fragments;

  # The SPINDRIFT_OUTCOME contract (the LAND THE CHANGE / WATCH CI / OUTCOME /
  # IF BLOCKED sections) is harness-owned (issue #419): a Consumer `prompt`
  # that drops it would ship an agent that never emits the outcome line, so
  # the launcher never learns the PR and the merge/takeover silently never
  # happens. Sliced from the default prompt's own heading rather than
  # duplicated into a second file, so the injected block and the default
  # prompt's sections cannot drift apart — same source, same bytes.
  outcomeContractMarker = "# LAND THE CHANGE";
  outcomeContract = sliceFromMarker outcomeContractMarker issuePromptSource;

  injectOutcomeContract = injectSection outcomeContractMarker outcomeContract;

  # COMMS and CHECK/COMMIT are the other two blocks fix-prompt.md used to
  # hand-copy from issue-prompt.md (issue #455): sliced the same way as the
  # outcome contract above, so fix-prompt.md's default template can drop
  # them entirely and receive the byte-identical section at bake/run time
  # instead. COMMS runs from its own heading up to SCOUT (issue-prompt-only —
  # the fix prompt runs FIX in its place); CHECK/COMMIT runs from CHECK up to
  # REVIEW (also issue-prompt-only — a fix pass has no review step).
  commsMarker = "# COMMS";
  commsBlock = sliceBetween commsMarker "# SCOUT" issuePromptSource;
  checkMarker = "# CHECK";
  checkBlock = sliceBetween checkMarker "# REVIEW" issuePromptSource;

  injectComms = injectSection commsMarker commsBlock;
  injectCheckCommit = injectSection checkMarker checkBlock;

  # fix-prompt.md's full shared-block treatment (issue #455): COMMS, then
  # CHECK/COMMIT, then the outcome contract, applied in that order so a
  # fix prompt missing all three ends up with them in the same order
  # issue-prompt.md carries them — mirrors the injection order in
  # agent/entrypoint.sh so the baked and mounted-override cases agree.
  injectFixSharedBlocks =
    promptText: injectOutcomeContract (injectCheckCommit (injectComms promptText));

  # The Driver registry (ADR 0009); driverEntry is the selected Driver's
  # in-box half — invocation binary/flags, agent-config rendering, skill
  # wiring, and outcome extraction — baked into the image below.
  driverRegistry = import ./drivers/default.nix { inherit lib; };
  driverEntry =
    driverRegistry.${driver}
      or (throw "mkHarness: unknown driver '${driver}'; known drivers: ${lib.concatStringsSep ", " (lib.attrNames driverRegistry)}");

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults derived from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those again at runtime.
  schemaDefaults = lib.mapAttrs (_: e: e.default or "") flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # Unknown defaults keys are caught at eval time — a typo like `basebranch`
  # would otherwise be silently ignored, never baked, never surfaced.
  unknownDefaultKeys = lib.filter (k: !(lib.hasAttr k flakeOptionEntries)) (lib.attrNames defaults);

  # --agents JSON, rendered by the selected Driver (ADR 0009) so a future
  # Driver with a different agent-config shape (e.g. opencode's agents/*.md)
  # can supply its own renderer without touching mkHarness.
  agentsJsonTemplate = driverEntry.agentsJsonTemplate {
    scoutModel = mergedDefaults.scoutModel or "";
    reviewModel = mergedDefaults.reviewModel or "";
    filerModel = mergedDefaults.filerModel or "";
  };

  # The Driver's registry-rendered function definitions, shared between the
  # image preamble and the bats harness file (issue #433) so neither can drift
  # from the other.
  driverFunctionDefs =
    "_driver_extract_outcome() {\n"
    + driverEntry.outcomeExtractFnBody
    + "}\n"
    + "_driver_session_flags() {\n"
    + driverEntry.sessionFlagsFnBody
    + "}\n";

  # The Driver's in-box half, rendered into agent/entrypoint.sh's
  # ${DRIVER_*:-<default>} vars and the Driver function definitions
  # (ADR 0009). /home/agent is the image's fixed HOME (see passwdFile
  # below), so the skills dir is baked as an absolute path rather than
  # depending on $HOME at run time.
  driverPreamble =
    "DRIVER_BIN="
    + lib.escapeShellArg driverEntry.bin
    + "\n"
    + "DRIVER_FLAGS_COMMON="
    + lib.escapeShellArg driverEntry.flagsCommon
    + "\n"
    + "DRIVER_SKILLS_DIR="
    + lib.escapeShellArg "/home/agent/${driverEntry.skillsDirRelative}"
    + "\n"
    + driverFunctionDefs;

  # The Conditional fragment registry (issue #622, CONTEXT.md), rendered into
  # agent/entrypoint.sh's single fragment loop input and `_subst`
  # substitution allowlist: a bash array of "gate|fragment|var" rows, plus a
  # space-separated list of every var an envsubst call must know about (each
  # row's own var, plus any extraSubstVars a fragment's body interpolates).
  # entrypoint.sh's loop and `_subst` are both generic over this data — a new
  # row needs no entrypoint edit. Shared between the image preamble and the
  # bats harness file the same way driverPreamble/driverFunctionsFile are
  # (issue #433), so neither can drift from the other.
  fragmentRegistryRows = map (row: "${row.gate}|${row.fragment}|${row.var}") fragments;
  fragmentSubstVars = lib.concatMap (row: [ row.var ] ++ (row.extraSubstVars or [ ])) fragments;
  fragmentRegistryPreamble =
    "_FRAGMENT_ROWS=(\n"
    + lib.concatMapStrings (row: "  " + lib.escapeShellArg row + "\n") fragmentRegistryRows
    + ")\n"
    + "_FRAGMENT_SUBST_VARS=(\n"
    + lib.concatMapStrings (v: "  " + lib.escapeShellArg v + "\n") fragmentSubstVars
    + ")\n";

  # Version sourced from the release-please manifest so mkHarness always tracks
  # the bot-maintained source of truth (ADR-0010).
  spindriftVersion = (builtins.fromJSON (builtins.readFile ../.release-please-manifest.json)).".";

  # In-box heartbeat filter: reuses the #182 heartbeat parser as a CLI binary
  # so the entrypoint can pipe claude's stream-json output through it without
  # modifying the raw capture channel. Built for Linux (pkgs, not hostPkgs).
  # Goes through the Driver seam (driver.New("claude").NewHeartbeatWriter,
  # ADR 0009 / issue #620) rather than a heartbeat package directly.
  #
  # INVARIANT: the agent image drvPath must not change when host-side launcher
  # code outside this binary's import closure is modified (e.g. test-only
  # launcher commits). The fileset is intentionally tight: go.mod,
  # spindrift-heartbeat-filter, internal/driver, internal/driver/claude (the
  # claude Driver's heartbeat/transcript/classify/usage parsing),
  # internal/usage (Driver-agnostic report types), and internal/logscan
  # (claude's log-scan helper) only, with *_test.go excluded. If a new import
  # is added outside this closure the build fails loudly (missing package) —
  # that is the intended failure mode (#474).
  heartbeatFilterBin = pkgs.buildGoModule {
    pname = "spindrift-heartbeat-filter";
    version = spindriftVersion;
    src = lib.fileset.toSource {
      root = ../cmd/launcher;
      fileset = lib.fileset.unions [
        ../cmd/launcher/go.mod
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/spindrift-heartbeat-filter)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver/claude)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/usage)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/logscan)
      ];
    };
    vendorHash = null;
    subPackages = [ "spindrift-heartbeat-filter" ];
    meta.license = lib.licenses.mit;
  };

  # The harness plumbing package set, agent environment, agent files,
  # passwd/group files, and the layered OCI image build itself — extracted to
  # lib/image.nix (issue #514) as a pure code move; the image derivation must
  # stay byte-identical, so every value the module needs is threaded in
  # exactly as it was computed here.
  imageModule = import ./image.nix {
    inherit
      pkgs
      lib
      packages
      prefetch
      nixInBox
      nixStoreWritable
      extraClosures
      driverEntry
      heartbeatFilterBin
      agentsJsonTemplate
      driverPreamble
      fragmentRegistryPreamble
      prompt
      scoutPrompt
      reviewPrompt
      filerPrompt
      conflictResolvePrompt
      fixPrompt
      outcomeContract
      commsBlock
      checkBlock
      injectOutcomeContract
      injectFixSharedBlocks
      fragmentsSourceDir
      skills
      ;
    entrypointDefaultsPreamble = renderDefaultsPreamble { };
  };
  inherit (imageModule) image agentEnv agentFiles;

  # The canonical outcome contract as a host store path, so checks can diff
  # it against what a Consumer prompt lacking the contract gets injected with
  # — proof the two cannot drift apart (issue #419).
  outcomeContractFile = hostPkgs.writeText "outcome-contract.md" outcomeContract;

  # The COMMS and CHECK/COMMIT blocks as host store paths, for the same
  # drift-proof reason (issue #455).
  commsContractFile = hostPkgs.writeText "comms-contract.md" commsBlock;
  checkContractFile = hostPkgs.writeText "check-contract.md" checkBlock;

  # The Driver's function definitions as a host store-path file.  The bats
  # harness prepends this before exec-ing the entrypoint (issue #433) so tests
  # exercise the same registry-rendered bodies that mkHarness bakes into the
  # image — not any hand-copied duplicates in the entrypoint itself.
  driverFunctionsFile = hostPkgs.writeText "driver-functions.sh" driverFunctionDefs;

  # The Conditional fragment registry as a host store-path file (issue #622,
  # mirrors driverFunctionsFile above). The bats harness prepends this before
  # exec-ing the entrypoint so tests exercise the same registry-rendered loop
  # input and substitution allowlist that mkHarness bakes into the image.
  fragmentRegistryFile = hostPkgs.writeText "fragment-registry.sh" fragmentRegistryPreamble;

  # The rendered prompt directory as a host store path (native-buildable on
  # darwin, so it needs no Linux builder). The prompt is normally baked into
  # the image via agentFiles; this output exists so tests can assert it is NOT
  # bind-mounted by default, and so SPINDRIFT_PROMPT_DIR can point to it.
  promptDir = hostPkgs.runCommand "prompt-dir" { } ''
    mkdir -p $out
    cp ${hostPkgs.writeText "issue-prompt.md" (injectOutcomeContract prompt)} $out/issue-prompt.md
    cp ${hostPkgs.writeText "scout-prompt.md" scoutPrompt} $out/scout-prompt.md
    cp ${hostPkgs.writeText "review-prompt.md" reviewPrompt} $out/review-prompt.md
    cp ${hostPkgs.writeText "filer-prompt.md" filerPrompt} $out/filer-prompt.md
    cp ${hostPkgs.writeText "conflict-resolve-prompt.md" conflictResolvePrompt} $out/conflict-resolve-prompt.md
    cp ${hostPkgs.writeText "fix-prompt.md" (injectFixSharedBlocks fixPrompt)} $out/fix-prompt.md
    cp -r ${fragmentsSourceDir} $out/fragments
  '';

  # The baked-skills directory as a host store path (native-buildable on
  # darwin), laid out exactly as lib/image.nix bakes it: each skill is a
  # `<name>/SKILL.md` directory (Claude Code discovers skills only as
  # directories). A { name; src; } content entry (issue #597) is realized with
  # hostPkgs here — this directory is a host-only test artifact, never an input
  # to the (Linux) image itself, so it carries no host-independence requirement.
  skillsDir = hostPkgs.runCommand "skills-dir" { } (
    if skills == [ ] then
      "mkdir -p $out"
    else
      ''
        mkdir -p $out
        ${lib.concatMapStrings (
          f:
          if builtins.isAttrs f && !(lib.isDerivation f) then
            ''
              mkdir -p $out/${f.name}
              cp ${hostPkgs.writeText "SKILL.md" f.src} $out/${f.name}/SKILL.md
            ''
          else
            ''
              cp -r ${f} $out/${if lib.isDerivation f then f.name else builtins.baseNameOf f}
            ''
        ) skills}
      ''
  );

  # The image's store path as PLAIN TEXT (context discarded), so the launcher
  # commands embed the exact Linux image path WITHOUT taking a build-time
  # dependency on it. That lets `build`/`run` — and `nix flake check` — build
  # natively on darwin, while realizing the image stays an explicit, Linux-only
  # `nix build .#agent-image`.
  imagePath = builtins.unsafeDiscardStringContext (toString image);

  # The 32-char nix store hash extracted from imagePath. Nix store paths are
  # always `/nix/store/<32-char-base32-hash>-<name>`, so characters 11–42
  # (0-indexed) are the hash. Used as the content-hash image tag so that a
  # changed flake produces a new hash → the old tag is absent → run rebuilds.
  imageHash = builtins.substring 11 32 imagePath;

  # The image's `.drv` path, also context-discarded. `build` realizes this with
  # `nix build "<drv>^*"` before loading, so a fresh machine builds the image
  # instead of failing on an unrealized path — while discarding the context
  # keeps `nix flake check` and the launcher builds off any Linux build. Reading
  # `.drvPath` instantiates the derivation at eval time, so the .drv exists in
  # the store by the time `build` runs; only realizing it needs a Linux builder.
  imageDrv = builtins.unsafeDiscardStringContext image.drvPath;

  # bwrap runner: store paths for the agent files and env, context-discarded so
  # the launcher commands embed the exact paths without a build-time dependency.
  # Reading `.drvPath` instantiates each derivation at eval time (creating the
  # .drv file) but does not realize the output — `bwrap build` does that.
  agentFilesPath = builtins.unsafeDiscardStringContext (toString agentFiles);
  agentFilesDrv = builtins.unsafeDiscardStringContext agentFiles.drvPath;
  agentEnvPath = builtins.unsafeDiscardStringContext (toString agentEnv);
  agentEnvDrv = builtins.unsafeDiscardStringContext agentEnv.drvPath;

  # runnerKind collapses the runtime knob to the two adapter families the
  # launcher knows: "bwrap" (daemonless) or "oci" (podman/docker).
  runnerKind = if runtime == "bwrap" then "bwrap" else "oci";

  # One renderer used by both the shell and Go preamble families: iterates over
  # flakeOption schema entries and emits `[export ]VAR="${VAR:-<baked>}"` lines.
  # A matching env var (or harness.env, sourced by the wrapper) still wins at runtime.
  renderDefaultsPreamble =
    args: preambles.renderDefaultsPreamble (args // { inherit flakeOptionEntries mergedDefaults; });

  # Exported preambles for the Go launcher wrappers. `export` is required so
  # the exec'd binary inherits each value; plain assignments would be
  # shell-only. Two vars cover both adapter families:
  #
  #   goRunPreamble  — everything `run` needs at sandbox dispatch time.
  #   goBuildPreamble — everything `build` needs to realize the image/closure.

  goRunPreamble = preambles.renderGoRunPreamble {
    inherit
      runnerKind
      driverEntry
      agentFilesPath
      agentEnvPath
      prefetch
      imagePath
      imageHash
      runtime
      imageDrv
      nixBuilderImage
      linuxSystem
      ;
  };

  goBuildPreamble = preambles.renderGoBuildPreamble {
    inherit
      runnerKind
      agentFilesDrv
      agentEnvDrv
      runtime
      imagePath
      imageHash
      imageDrv
      nixBuilderImage
      linuxSystem
      ;
  };

  # Exported run defaults for the Go launcher wrapper.
  goRunDefaultsPreamble = renderDefaultsPreamble { export = true; };

  # BOX_ENV_VARS exported for the Go binary: the list of env vars it must forward
  # into each container, derived from schema boxEnv=true entries.
  boxEnvVarsPreamble = preambles.renderBoxEnvVarsPreamble schema;

  # buildGoModule's checkPhase runs `go test` from within its src, so docs/
  # must sit alongside cmd/launcher there too, mirroring the repo layout, for
  # TestReferenceDocLabelSnippetMatchesTriageDefaults's ../../docs/reference.md
  # path to resolve (#611).
  launcherSrc = hostPkgs.runCommand "launcher-src" { } ''
    mkdir -p $out/cmd/launcher
    cp -r ${../cmd/launcher}/. $out/cmd/launcher/
    cp -r ${../docs} $out/docs
  '';

  # The Go launcher binary, built hermetically by buildGoModule.
  #
  # vendorHash policy:
  #   null  — stdlib-only; no go.sum / vendor dir required. Keep null as long
  #           as cmd/launcher/go.mod has no external dependencies.
  #   "<hash>" — when the first external dep is added, run:
  #               nix build --impure --expr \
  #                 '(import <nixpkgs> {}).buildGoModule { pname="x"; version="0"; \
  #                  src = ./cmd/launcher; \
  #                  vendorHash = (import <nixpkgs> {}).lib.fakeHash; }'
  #             and replace lib.fakeHash with the hash Nix reports in the
  #             error output. Commit go.sum and the updated vendorHash together.
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = spindriftVersion;
    src = launcherSrc;
    modRoot = "cmd/launcher";
    vendorHash = null;
    subPackages = [ "." ]; # build only the launcher; heartbeat-filter is in-box only
    ldflags = [
      "-X main.version=${spindriftVersion}"
      "-X main.revision=${revision}"
    ];
    meta.license = lib.licenses.mit;
  };

  # Single-verb wrapper execing `launcher build`. The `apps.build`/
  # `packages.build` flake outputs that once forwarded to this were removed
  # in issue #613; this derivation lives on, off the flake surface, only as
  # a bats/equivalence test fixture for the build-time preamble baking.
  build =
    (hostPkgs.writeShellApplication {
      name = "build";
      runtimeInputs = [ hostPkgs.coreutils ];
      text = goBuildPreamble + ''
        exec ${launcherBin}/bin/launcher build
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  # Shared shell body used by both the spindrift CLI and the `run` test fixture.
  # Bakes nix-computed config into env vars, sources harness.env for runtime
  # overrides, then execs the Go binary (ADR 0007).
  runShellBody =
    goRunPreamble
    + goRunDefaultsPreamble
    + boxEnvVarsPreamble
    + ''
      # Config + secrets (gitignored), read from $PWD since the harness is a
      # store path with no working tree. `set -a` overrides the baked defaults.
      if [ -f "$PWD/harness.env" ]; then
        set -a
        # shellcheck disable=SC1091
        . "$PWD/harness.env"
        set +a
      fi
      # Commit identity: explicit override wins, else inherit the host git config.
      GIT_USER_NAME="''${GIT_USER_NAME:-$(git config --get user.name 2>/dev/null || true)}"
      GIT_USER_EMAIL="''${GIT_USER_EMAIL:-$(git config --get user.email 2>/dev/null || true)}"
      export GIT_USER_NAME GIT_USER_EMAIL
    '';

  # Roff man page rendered from the schema so `man spindrift` carries the full
  # flag reference while `spindrift --help` stays concise.
  manpageRoff = renderers.renderManpageRoff schema spindriftVersion;

  manpage = hostPkgs.runCommand "spindrift-manpage" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.1" manpageRoff} \
      "$out/share/man/man1/spindrift.1"
  '';

  # Bash completion script rendered from the schema (issue #551), same
  # build-time-only pattern as the man page: no committed copy, out of
  # `nix run .#regen`, coverage-guarded by nix/checks/schema-drift.nix.
  bashCompletionScript = renderers.renderBashCompletion schema;

  bashCompletion = hostPkgs.runCommand "spindrift-bash-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift-completion.bash" bashCompletionScript} \
      "$out/share/bash-completion/completions/spindrift"
  '';

  # Fish completion script rendered from the schema (issue #553), same
  # build-time-only pattern as the bash completion above: no committed copy,
  # out of `nix run .#regen`, coverage-guarded by nix/checks/schema-drift.nix.
  fishCompletionScript = renderers.renderFishCompletion schema;

  fishCompletion = hostPkgs.runCommand "spindrift-fish-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.fish" fishCompletionScript} \
      "$out/share/fish/vendor_completions.d/spindrift.fish"
  '';

  # Zsh completion script rendered from the schema (issue #552), same
  # build-time-only pattern as the bash completion and man page: no
  # committed copy, out of `nix run .#regen`, coverage-guarded by
  # nix/checks/schema-drift.nix.
  zshCompletionScript = renderers.renderZshCompletion schema;

  zshCompletion = hostPkgs.runCommand "spindrift-zsh-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "_spindrift" zshCompletionScript} \
      "$out/share/zsh/site-functions/_spindrift"
  '';

  # The spindrift CLI: bakes nix-computed config as env vars and execs the Go
  # launcher. Exposed as packages.spindrift, apps.default, and in devShells.
  # The man page is joined into the same output so `man spindrift` resolves
  # from the dev shell (nixpkgs adds share/man to MANPATH) and on install.
  spindriftBin =
    (hostPkgs.writeShellApplication {
      name = "spindrift";
      runtimeInputs = with hostPkgs; [
        gh
        git
        coreutils
      ];
      text = runShellBody + ''
        exec ${launcherBin}/bin/launcher "$@"
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  spindrift = hostPkgs.symlinkJoin {
    name = "spindrift";
    paths = [
      spindriftBin
      manpage
      bashCompletion
      fishCompletion
      zshCompletion
    ];
    meta.license = lib.licenses.mit;
  };

  # Single-verb wrapper execing `launcher dispatch`. The `apps.run`/
  # `packages.run` flake outputs that once forwarded to this were removed
  # in issue #613; this derivation lives on, off the flake surface, only as
  # a bats/equivalence test fixture for the dispatch-time preamble baking.
  run =
    (hostPkgs.writeShellApplication {
      name = "run";
      runtimeInputs = with hostPkgs; [
        gh
        git
        coreutils
      ];
      text = runShellBody + ''
        exec ${launcherBin}/bin/launcher dispatch "$@"
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  # Realizing the Linux image on darwin needs a Linux builder, so only offer it
  # as a package where it can actually build; the launcher commands (which merely
  # reference its path) are always available. `nix flake check` on darwin thus
  # never forces a Linux build.
  isLinux = system == linuxSystem;
in
if unknownDefaultKeys != [ ] then
  throw "mkHarness: unknown defaults key(s): ${lib.concatStringsSep ", " unknownDefaultKeys}; valid keys: ${lib.concatStringsSep ", " (lib.attrNames flakeOptionEntries)}"
else
  {
    inherit
      image
      agentEnv
      agentFiles
      build
      run
      spindrift
      manpage
      bashCompletion
      fishCompletion
      zshCompletion
      imagePath
      promptDir
      skillsDir
      outcomeContractFile
      commsContractFile
      checkContractFile
      driverFunctionsFile
      fragmentRegistryFile
      heartbeatFilterBin
      driverEntry
      ;

    packages = {
      inherit spindrift;
      spindrift-manpage = manpage;
      spindrift-bash-completion = bashCompletion;
      spindrift-fish-completion = fishCompletion;
      spindrift-zsh-completion = zshCompletion;
    }
    # The OCI image is not relevant for the bwrap runner (no image build/load).
    // lib.optionalAttrs (isLinux && runtime != "bwrap") { agent-image = image; };

    # apps.default (`nix run .`) is the sole app output: the spindrift CLI.
    # The `build`/`run` app-style aliases were removed (issue #613); the
    # `build`/`run` derivations themselves live on as bats/equivalence test
    # fixtures (see `inherit build run` above), just off the flake surface.
    apps.default = {
      type = "app";
      program = "${spindrift}/bin/spindrift";
    };
  }
