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
  # The agent prompt template, a Consumer-owned artifact. Rendered to a store
  # path and mounted into the container at runtime — NOT baked into the image —
  # so it can be re-pointed via SPINDRIFT_PROMPT_DIR with zero rebuilds (see
  # scripts/run.sh).
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default) or "docker".
  runtime ? "podman",
  # Fallback Linux builder for when the host can't realise the Linux image itself
  # (the stock-mac case). Fully qualified so podman needs no default registry.
  nixBuilderImage ? "docker.io/nixos/nix:latest",
  # Bake a usable nix into the box (binary + a registered store DB + a
  # single-user, sandbox-off nix.conf) so `nix flake check` and `nix develop`
  # run inside the unprivileged throwaway container. On by default — this is the
  # nix-centric baseline every box gets; set to false for a lean, nix-free image.
  nixInBox ? true,
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

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults derived from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those again at runtime.
  schemaDefaults = lib.mapAttrs (_: e: e.default or "") flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # Plumbing every agent needs regardless of language: a shell, the VCS + GitHub
  # CLIs, Claude Code, CA certs, and the unix tools the entrypoint relies on.
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
      claude-code
      cacert
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
  # complete, standalone script — the bats suite exercises it raw — so its
  # shebang is stripped before it becomes this derivation's body.
  entrypoint = pkgs.writeShellApplication {
    name = "entrypoint";
    runtimeInputs = with pkgs; [
      git
      gh
      claude-code
      gettext # envsubst
      coreutils
      jq # extracts the outcome from the stream-json transcript
    ];
    # Prepend the schema-derived defaults block so the entrypoint carries the
    # baked values without hardcoding them in the source script.
    text = renderDefaultsPreamble { } + stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is baked in alongside the
  # entrypoint (not a host-path mount) so the Box is self-contained: a macOS
  # podman machine cannot bind-mount the host /nix/store into its Linux VM.
  # SPINDRIFT_PROMPT_DIR still mounts an override dir for zero-rebuild iteration
  # (see scripts/run.sh).
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent/prompts
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    cp ${pkgs.writeText "issue-prompt.md" prompt} $out/agent/prompts/issue-prompt.md
  '';

  # The rendered prompt as a host store-path directory (native-buildable on
  # darwin, so it needs no Linux builder). The `run` command bakes this path in
  # and bind-mounts it at /agent/prompts, where the entrypoint reads
  # issue-prompt.md and substitutes the per-issue variables.
  promptDir = hostPkgs.writeTextDir "issue-prompt.md" prompt;

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

  image = pkgs.dockerTools.buildLayeredImage {
    name = "spindrift";
    tag = "latest";
    contents = [
      agentEnv
      agentFiles
    ];
    extraCommands =
      ''
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
        ${pkgs.buildPackages.nix}/bin/nix-store --load-db < ${pkgs.closureInfo { rootPaths = [ agentEnv agentFiles ]; }}/registration
      '';
    # chown must be recorded in the image layer, so it runs under fakeroot after
    # the tree is staged. HOME and the clone dir must be writable by the agent.
    fakeRootCommands = ''
      chown -R 1000:1000 home/agent work
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
      ];
    };
  };

  # The image's store path as PLAIN TEXT (context discarded), so the launcher
  # commands embed the exact Linux image path WITHOUT taking a build-time
  # dependency on it. That lets `build`/`run` — and `nix flake check` — build
  # natively on darwin, while realising the image stays an explicit, Linux-only
  # `nix build .#spindrift`.
  imagePath = builtins.unsafeDiscardStringContext (toString image);

  # The 32-char nix store hash extracted from imagePath. Nix store paths are
  # always `/nix/store/<32-char-base32-hash>-<name>`, so characters 11–42
  # (0-indexed) are the hash. Used as the content-hash image tag so that a
  # changed flake produces a new hash → the old tag is absent → run rebuilds.
  imageHash = builtins.substring 11 32 imagePath;

  # The image's `.drv` path, also context-discarded. `build` realises this with
  # `nix build "<drv>^*"` before loading, so a fresh machine builds the image
  # instead of failing on an unrealised path — while discarding the context
  # keeps `nix flake check` and the launcher builds off any Linux build. Reading
  # `.drvPath` instantiates the derivation at eval time, so the .drv exists in
  # the store by the time `build` runs; only realising it needs a Linux builder.
  imageDrv = builtins.unsafeDiscardStringContext image.drvPath;

  # bwrap runner: store paths for the agent files and env, context-discarded so
  # the launcher commands embed the exact paths without a build-time dependency.
  # Reading `.drvPath` instantiates each derivation at eval time (creating the
  # .drv file) but does not realise the output — `bwrap build` does that.
  agentFilesPath = builtins.unsafeDiscardStringContext (toString agentFiles);
  agentFilesDrv = builtins.unsafeDiscardStringContext agentFiles.drvPath;
  agentEnvPath = builtins.unsafeDiscardStringContext (toString agentEnv);
  agentEnvDrv = builtins.unsafeDiscardStringContext agentEnv.drvPath;

  # Runner adapter record: selected once from the runtime, then rendered into
  # shell exports for both the `build` and `run` wrappers. No per-runtime
  # conditionals appear after this point (ADR 0006).
  #
  # vars:         simple-string baked facts, rendered as export VAR="value".
  # extraExports: optional snippet for values that need special shell quoting
  #               (e.g. multi-line BAKED_PREFETCH) — appended verbatim.
  #
  # RUNTIME is a baked fact (not a runtimeInput) so the runtime CLI remains a
  # checked host install; the scripts branch on it, not on a nix-provided binary.
  runnerKind = if runtime == "bwrap" then "bwrap" else "oci";
  runners = {
    oci = {
      vars = {
        RUNTIME = runtime;
        IMAGE_ARCHIVE = imagePath;
        IMAGE_TAG = "spindrift:${imageHash}";
        IMAGE_DRV = imageDrv;
        NIX_BUILDER_IMAGE = nixBuilderImage;
        NIX_VOLUME = "spindrift-nix";
        FLAKE_IMAGE_ATTR = ".#packages.${linuxSystem}.spindrift";
      };
      extraExports = "";
    };
    bwrap = {
      vars = {
        RUNTIME = "bwrap";
        AGENT_FILES = agentFilesPath;
        AGENT_ENV = agentEnvPath;
        AGENT_FILES_DRV = agentFilesDrv;
        AGENT_ENV_DRV = agentEnvDrv;
      };
      # BAKED_PREFETCH is an arbitrary multi-line shell snippet; escapeShellArg
      # produces a safely single-quoted assignment.
      extraExports = ''
        BAKED_PREFETCH=${lib.escapeShellArg prefetch}
        export BAKED_PREFETCH
      '';
    };
  };
  selectedRunner = runners.${runnerKind};

  # Render the selected runner's vars as `export VAR="value"` lines plus any
  # extraExports snippet.  `export` is required so the exec'd Go binary inherits
  # each value; plain assignments would be visible only to the shell wrapper.
  renderRunnerVars =
    rec:
    lib.concatMapStrings (k: ''export ${k}="${rec.vars.${k}}"'' + "\n") (lib.attrNames rec.vars)
    + rec.extraExports;
  goRunnerPreamble = renderRunnerVars selectedRunner;

  # One renderer used by both the shell and Go preamble families: iterates over
  # flakeOption schema entries and emits `[export ]VAR="${VAR:-<baked>}"` lines.
  # A matching env var (or harness.env, sourced by the wrapper) still wins at runtime.
  renderDefaultsPreamble =
    { export ? false }:
    lib.concatStrings (
      lib.mapAttrsToList (key: entry:
        let
          value = mergedDefaults.${key};
          prefix = if export then "export " else "";
        in
        ''${prefix}${entry.env}="''${${entry.env}:-${toString value}}"
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

  # Exported run defaults for the Go launcher wrapper.
  goRunDefaultsPreamble = renderDefaultsPreamble { export = true; };

  # BOX_ENV_VARS exported for the Go binary: the list of env vars it must forward
  # into each container, derived from schema boxEnv=true entries.
  boxEnvVarsPreamble = ''
    export BOX_ENV_VARS="${boxEnvVarsList}"
  '';

  # The Go launcher binary, built hermetically by buildGoModule.
  # No external dependencies → vendorHash = null.
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = "0.1.0";
    src = ../cmd/launcher;
    vendorHash = null;
  };

  # The build command: bake runner vars, then exec `launcher build`.
  # `launcher build` calls runner.EnsureReady() — OCI: ensure image present
  # (host-build or container-fallback); bwrap: realise agent store closures.
  # Both `run` and `build` share the same EnsureReady path (ADR 0004).
  build = hostPkgs.writeShellApplication {
    name = "build";
    runtimeInputs = [ ];
    text =
      goRunnerPreamble
      + ''
        exec ${launcherBin}/bin/launcher build
      '';
  };

  # The run command: a thin shell wrapper that bakes nix-computed config into
  # env vars, sources harness.env for runtime overrides, then execs the Go
  # binary. The binary contains no baked store paths of its own beyond those
  # injected here (ADR 0007).
  run = hostPkgs.writeShellApplication {
    name = "run";
    runtimeInputs = with hostPkgs; [
      gh
      git
      coreutils
    ];
    text =
      goRunnerPreamble
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
        exec ${launcherBin}/bin/launcher
      '';
  };

  # Realising the Linux image on darwin needs a Linux builder, so only offer it
  # as a package where it can actually build; the launcher commands (which merely
  # reference its path) are always available. `nix flake check` on darwin thus
  # never forces a Linux build.
  isLinux = system == linuxSystem;
in
{
  inherit
    image
    agentEnv
    agentFiles
    build
    run
    imagePath
    promptDir
    ;

  packages = {
    inherit build run;
  }
  # The OCI image is not relevant for the bwrap runner (no image build/load).
  // lib.optionalAttrs (isLinux && runtime != "bwrap") { spindrift = image; };

  apps = {
    build = {
      type = "app";
      program = "${build}/bin/build";
    };
    run = {
      type = "app";
      program = "${run}/bin/run";
    };
  };
}
