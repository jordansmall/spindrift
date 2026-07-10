# The engine. A pure function a Consumer flake calls with its own locked
# `nixpkgs` input and `system`; returns the agent image plus nix-built `build`
# and `run` commands (as both `packages.*` and `apps.*`).
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
  # Skill files baked into the image at /home/agent/.claude/skills so the
  # headless agent can invoke them without a runtime mount. Each element must
  # be a path to a skill file; the file is copied under its basename.
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

  # Drop a leading `#!...` line so a complete, standalone-runnable script can be
  # fed to writeShellApplication as its body (it supplies its own shebang).
  stripShebang =
    text:
    let
      lines = lib.splitString "\n" text;
    in
    if lines != [ ] && lib.hasPrefix "#!" (builtins.head lines) then
      lib.concatStringsSep "\n" (builtins.tail lines)
    else
      text;

  # Single source of truth for every runtime knob — name mapping, defaults, scope.
  # Generators below derive all per-knob output from this registry; no per-knob
  # lines appear anywhere else in this file.
  schema = import ./env-schema.nix;

  # Section taxonomy and man-page renderer, shared with flakeModule.nix and the
  # nix/checks/schema-drift.nix guards so none of them can drift from each
  # other (issue #461).
  renderers = import ./renderers.nix;

  # issue-prompt.md is the single source every shared block below is sliced
  # from — read once so each slice sees the identical text.
  issuePromptSource = builtins.readFile ../templates/default/prompts/issue-prompt.md;

  # Slices `text` from `startMarker` (inclusive) up to `endMarker`
  # (exclusive), asserting each marker appears exactly once — the same
  # single-occurrence guarantee the outcome-contract slice below relies on,
  # so a heading collision fails loudly at eval time instead of silently
  # slicing the wrong span.
  sliceBetween =
    startMarker: endMarker: text:
    let
      afterStartParts = lib.splitString startMarker text;
    in
    assert lib.assertMsg (
      builtins.length afterStartParts == 2
    ) "mkHarness: source must contain start marker '${startMarker}' exactly once";
    let
      afterStart = startMarker + builtins.elemAt afterStartParts 1;
      spanParts = lib.splitString endMarker afterStart;
    in
    assert lib.assertMsg (builtins.length spanParts == 2)
      "mkHarness: source must contain end marker '${endMarker}' exactly once after start marker '${startMarker}'";
    builtins.elemAt spanParts 0;

  # A sliced shared block (below) already ends with the blank line that
  # separated it from the next heading in issue-prompt.md, so chaining two of
  # them back to back — as #455's fix-prompt.md composition does — must not
  # double that blank line up. Strips one, if present; a no-op on text that
  # ends with a single "\n" (e.g. a plain Consumer `prompt` string).
  trimTrailingBlankLine = s: if lib.hasSuffix "\n\n" s then lib.removeSuffix "\n" s else s;

  # Appends `block` to `promptText` unless it already contains `marker` (the
  # default prompt's own copy, or a Consumer prompt that kept it) — so
  # injection is idempotent. Generic so #455 can reuse the #419 idiom for the
  # COMMS and CHECK/COMMIT blocks below, not just the outcome contract.
  injectSection =
    marker: block: promptText:
    if lib.hasInfix marker promptText then
      promptText
    else
      lib.removeSuffix "\n" (trimTrailingBlankLine promptText) + "\n\n" + block;

  # The SPINDRIFT_OUTCOME contract (the LAND THE CHANGE / WATCH CI / OUTCOME /
  # IF BLOCKED sections) is harness-owned (issue #419): a Consumer `prompt`
  # that drops it would ship an agent that never emits the outcome line, so
  # the launcher never learns the PR and the merge/takeover silently never
  # happens. Sliced from the default prompt's own heading rather than
  # duplicated into a second file, so the injected block and the default
  # prompt's sections cannot drift apart — same source, same bytes.
  outcomeContractMarker = "# LAND THE CHANGE";
  outcomeContract =
    let
      parts = lib.splitString outcomeContractMarker issuePromptSource;
    in
    assert lib.assertMsg (builtins.length parts == 2)
      "mkHarness: templates/default/prompts/issue-prompt.md must contain the outcome-contract marker '${outcomeContractMarker}' exactly once";
    outcomeContractMarker + builtins.elemAt parts 1;

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

  # Version sourced from the release-please manifest so mkHarness always tracks
  # the bot-maintained source of truth (ADR-0010).
  spindriftVersion = (builtins.fromJSON (builtins.readFile ../.release-please-manifest.json)).".";

  # In-box heartbeat filter: reuses the #182 heartbeat parser as a CLI binary
  # so the entrypoint can pipe claude's stream-json output through it without
  # modifying the raw capture channel. Built for Linux (pkgs, not hostPkgs).
  #
  # INVARIANT: the agent image drvPath must not change when host-side launcher
  # code outside this binary's import closure is modified (e.g. test-only
  # launcher commits). The fileset is intentionally tight: go.mod,
  # spindrift-heartbeat-filter, internal/heartbeat, and internal/claudetranscript
  # (heartbeat's transcript-parse import) only, with *_test.go excluded. If a
  # new import is added outside this closure the build fails loudly (missing
  # package) — that is the intended failure mode (#474).
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
        ) ../cmd/launcher/internal/heartbeat)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/claudetranscript)
      ];
    };
    vendorHash = null;
    subPackages = [ "spindrift-heartbeat-filter" ];
    meta.license = lib.licenses.mit;
  };

  # Plumbing every agent needs regardless of language: a shell, the VCS + GitHub
  # CLIs, the selected Driver's binary, CA certs, and the unix tools the
  # entrypoint relies on.
  harnessPackages =
    (with pkgs; [
      bashInteractive
      coreutils
      gnugrep
      gnused
      findutils
      gettext # envsubst, used by agent/entrypoint.sh
      jq # extracts the outcome line from the agent's stream-json transcript
      git
      gh
      (driverEntry.package pkgs)
      cacert
      heartbeatFilterBin # in-box heartbeat filter (#183)
    ])
    # The nix CLI is included by default so `nix flake check` / `nix develop`
    # work inside the box. Omitted only when the Consumer opts into the lean image.
    ++ lib.optional nixInBox pkgs.nix;

  agentEnv = pkgs.buildEnv {
    name = "agent-env";
    paths = harnessPackages ++ packages pkgs;
    pathsToLink = [
      "/bin"
      "/lib"
      "/etc"
      "/share"
      "/include"
    ];
  };

  # The in-container entrypoint, via writeShellApplication so shellcheck runs at
  # build time and its tools are pinned. Built for Linux. The source stays a
  # complete, standalone script (the bats harness prepends driverFunctionsFile
  # before exec-ing it) so its shebang is stripped before it becomes this
  # derivation's body.
  entrypoint = pkgs.writeShellApplication {
    name = "entrypoint";
    runtimeInputs = with pkgs; [
      git
      gh
      (driverEntry.package pkgs)
      gettext # envsubst
      coreutils
      jq # extracts the outcome from the stream-json transcript
      heartbeatFilterBin # in-box heartbeat view (#183)
    ];
    # Prepend the schema-derived defaults block so the entrypoint carries the
    # baked values without hardcoding them in the source script.
    # AGENTS_JSON_TEMPLATE is baked as a fixed value (not a :-default) because it
    # is derived from the configured models, not a standalone knob.
    text =
      "AGENTS_JSON_TEMPLATE="
      + lib.escapeShellArg agentsJsonTemplate
      + "\n"
      + driverPreamble
      + renderDefaultsPreamble { }
      + stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is baked in alongside the
  # entrypoint (not a host-path mount) so the Box is self-contained: a macOS
  # podman machine cannot bind-mount the host /nix/store into its Linux VM.
  # SPINDRIFT_PROMPT_DIR still mounts an override dir for zero-rebuild iteration
  # (the Go launcher mounts it in cmd/launcher/internal/runner).
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent/prompts
    # Pre-create the driver-cache mountpoint so podman reuses the agent-owned
    # directory instead of fabricating root-owned parents (issue #447).
    mkdir -p $out/home/agent/.claude/projects
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    # A sibling of prompts/, not inside it, so a SPINDRIFT_PROMPT_DIR mount
    # (which shadows only /agent/prompts) never hides it from the entrypoint
    # (issue #420).
    cp ${pkgs.writeText "outcome-contract.md" outcomeContract} $out/agent/outcome-contract.md
    cp ${pkgs.writeText "comms-contract.md" commsBlock} $out/agent/comms-contract.md
    cp ${pkgs.writeText "check-contract.md" checkBlock} $out/agent/check-contract.md
    cp ${pkgs.writeText "issue-prompt.md" (injectOutcomeContract prompt)} $out/agent/prompts/issue-prompt.md
    cp ${pkgs.writeText "scout-prompt.md" scoutPrompt} $out/agent/prompts/scout-prompt.md
    cp ${pkgs.writeText "review-prompt.md" reviewPrompt} $out/agent/prompts/review-prompt.md
    cp ${pkgs.writeText "filer-prompt.md" filerPrompt} $out/agent/prompts/filer-prompt.md
    cp ${pkgs.writeText "conflict-resolve-prompt.md" conflictResolvePrompt} $out/agent/prompts/conflict-resolve-prompt.md
    cp ${pkgs.writeText "fix-prompt.md" (injectFixSharedBlocks fixPrompt)} $out/agent/prompts/fix-prompt.md
    ${lib.optionalString (skills != [ ]) ''
      mkdir -p $out/home/agent/.claude/skills
      ${lib.concatMapStrings (f: ''
        cp ${f} $out/home/agent/.claude/skills/${
          if lib.isDerivation f then f.name else builtins.baseNameOf f
        }
      '') skills}
    ''}
  '';

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
  '';

  # The baked-skills directory as a host store path (native-buildable on
  # darwin). Each skill file is copied under its basename. When skills is
  # empty this derivation is an empty directory.
  skillsDir = hostPkgs.runCommand "skills-dir" { } (
    if skills == [ ] then
      "mkdir -p $out"
    else
      ''
        mkdir -p $out
        ${lib.concatMapStrings (f: ''
          cp ${f} $out/${if lib.isDerivation f then f.name else builtins.baseNameOf f}
        '') skills}
      ''
  );

  # A non-root `agent` user (uid/gid 1000). Claude Code refuses
  # --dangerously-skip-permissions under root/sudo, and the Box relies on that
  # flag; since the container itself IS the isolation boundary, running as an
  # unprivileged in-container user costs nothing and satisfies the check.
  passwdFile = pkgs.writeText "passwd" ''
    root:x:0:0:root:/root:/bin/bash
    agent:x:1000:1000:agent:/home/agent:/bin/bash
  '';
  groupFile = pkgs.writeText "group" ''
    root:x:0:
    agent:x:1000:
  '';

  # Evaluated once so the image's contents, closure registration, and Env
  # marker below all see the identical set of extra derivations.
  extraClosurePaths = extraClosures pkgs;

  image = pkgs.dockerTools.buildLayeredImage {
    name = "spindrift";
    tag = "latest";
    contents = [
      agentEnv
      agentFiles
    ]
    ++ extraClosurePaths;
    extraCommands = ''
      mkdir -p tmp home/agent work etc
      chmod 1777 tmp
      cp ${passwdFile} etc/passwd
      cp ${groupFile} etc/group
    ''
    # Make nix operable in an unprivileged throwaway container: a single-user,
    # sandbox-off nix.conf and a store DB registered from the baked closure, so
    # `nix flake check` reuses the image's store instead of treating it as empty.
    + lib.optionalString nixInBox ''
      mkdir -p etc/nix nix/var/nix/db nix/var/nix/gcroots nix/var/nix/profiles nix/var/nix/temproots nix/var/log/nix
      printf '%s\n' \
        'experimental-features = nix-command flakes' \
        'sandbox = false' \
        'filter-syscalls = false' > etc/nix/nix.conf
      export NIX_REMOTE="local?root=$PWD"
      # buildPackages.nix runs at image-build time on the builder host;
      # pkgs.nix (above) is what gets baked into the container's PATH.
      ${pkgs.buildPackages.nix}/bin/nix-store --load-db < ${
        pkgs.closureInfo {
          rootPaths = [
            agentEnv
            agentFiles
          ]
          ++ extraClosurePaths;
        }
      }/registration
    '';
    # chown must be recorded in the image layer, so it runs under fakeroot after
    # the tree is staged. HOME and the clone dir must be writable by the agent.
    # nix/var is also chowned so uid 1000 can lock the SQLite store DB and
    # write gcroots/profiles when nix commands run inside the container.
    fakeRootCommands = ''
      chown -R 1000:1000 home/agent work
    ''
    + lib.optionalString nixInBox ''
      chown -R 1000:1000 nix/var
    ''
    # Non-recursive: only the store directory itself becomes agent-writable,
    # so existing baked paths stay root-owned and immutable (self-test mode,
    # ADR 0018).
    + lib.optionalString nixStoreWritable ''
      chown 1000:1000 nix/store
    '';
    config = {
      Entrypoint = [ "/bin/bash" ];
      User = "agent";
      WorkingDir = "/";
      Env = [
        "PATH=/bin"
        "HOME=/home/agent"
        "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
        "GIT_SSL_CAINFO=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
        "PKG_CONFIG_PATH=/lib/pkgconfig"
        "PREFETCH=${prefetch}"
        "NIX_STORE_WRITABLE=${lib.boolToString nixStoreWritable}"
      ];
    };
  };

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
    {
      export ? false,
    }:
    lib.concatStrings (
      lib.mapAttrsToList (
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

  # Space-separated list of env var names forwarded from the host into the Box,
  # derived from schema boxEnv=true entries.  The Go launcher reads BOX_ENV_VARS
  # and builds its container-arg list from it, eliminating the hand-enumerated
  # forwarding lists in runOneOCI / runOneBwrap.
  boxEnvVarsList = lib.concatStringsSep " " (
    map (e: e.env) (lib.filter (e: e.boxEnv or false) (lib.attrValues schema))
  );

  # Exported preambles for the Go launcher wrappers. `export` is required so
  # the exec'd binary inherits each value; plain assignments would be
  # shell-only. Two vars cover both adapter families:
  #
  #   goRunPreamble  — everything `run` needs at sandbox dispatch time.
  #   goBuildPreamble — everything `build` needs to realize the image/closure.

  goRunPreamble =
    if runnerKind == "bwrap" then
      ''
        export RUNTIME="bwrap"
        export DRIVER="${driverEntry.name}"
        export AGENT_FILES="${agentFilesPath}"
        export AGENT_ENV="${agentEnvPath}"
        BAKED_PREFETCH=${lib.escapeShellArg prefetch}
        export BAKED_PREFETCH
      ''
    else
      # OCI run also bakes the build-time vars so EnsureReady can build the
      # image on demand when it is absent — the workflow is `build` first, but
      # `run` must handle a missing image gracefully on any machine.
      ''
        export IMAGE_ARCHIVE="${imagePath}"
        export IMAGE_TAG="spindrift:${imageHash}"
        export RUNTIME="${runtime}"
        export DRIVER="${driverEntry.name}"
        export IMAGE_DRV="${imageDrv}"
        export NIX_BUILDER_IMAGE="${nixBuilderImage}"
        export NIX_VOLUME="spindrift-nix"
        export FLAKE_IMAGE_ATTR=".#packages.${linuxSystem}.agent-image"
      '';

  goBuildPreamble =
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

  # Exported run defaults for the Go launcher wrapper.
  goRunDefaultsPreamble = renderDefaultsPreamble { export = true; };

  # BOX_ENV_VARS exported for the Go binary: the list of env vars it must forward
  # into each container, derived from schema boxEnv=true entries.
  boxEnvVarsPreamble = ''
    export BOX_ENV_VARS="${boxEnvVarsList}"
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
    src = ../cmd/launcher;
    vendorHash = null;
    subPackages = [ "." ]; # build only the launcher; heartbeat-filter is in-box only
    ldflags = [
      "-X main.version=${spindriftVersion}"
      "-X main.revision=${revision}"
    ];
    meta.license = lib.licenses.mit;
  };

  # Deprecated build alias: prints a one-line stderr notice and execs the same
  # build logic. Kept for one release (ADR-0010); removal target: next minor after 0.1.x.
  build =
    (hostPkgs.writeShellApplication {
      name = "build";
      runtimeInputs = [ hostPkgs.coreutils ];
      text = goBuildPreamble + ''
        >&2 echo "spindrift: 'nix run .#build' is deprecated; use 'spindrift build' instead. Removal: v0.2.0. See MIGRATING.md."
        exec ${launcherBin}/bin/launcher build
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  # Shared shell body used by both the spindrift CLI and the deprecated run alias.
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
    ];
    meta.license = lib.licenses.mit;
  };

  # Deprecated run alias: prints a one-line stderr notice and execs spindrift dispatch.
  # Kept for one release (ADR-0010); removal target: next minor after 0.1.x.
  run =
    (hostPkgs.writeShellApplication {
      name = "run";
      runtimeInputs = with hostPkgs; [
        gh
        git
        coreutils
      ];
      text = runShellBody + ''
        >&2 echo "spindrift: 'nix run .#run' is deprecated; use 'spindrift dispatch' instead. Removal: v0.2.0. See MIGRATING.md."
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
      imagePath
      promptDir
      skillsDir
      outcomeContractFile
      commsContractFile
      checkContractFile
      driverFunctionsFile
      heartbeatFilterBin
      ;

    packages = {
      inherit build run spindrift;
      spindrift-manpage = manpage;
    }
    # The OCI image is not relevant for the bwrap runner (no image build/load).
    // lib.optionalAttrs (isLinux && runtime != "bwrap") { agent-image = image; };

    apps = {
      build = {
        type = "app";
        program = "${build}/bin/build";
      };
      # apps.default is the primary entry point: spindrift CLI.
      default = {
        type = "app";
        program = "${spindrift}/bin/spindrift";
      };
      # Deprecated: kept for one release. Prints a notice and execs spindrift dispatch.
      run = {
        type = "app";
        program = "${run}/bin/run";
      };
    };
  }
