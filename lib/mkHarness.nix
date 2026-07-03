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

  # Built-in run defaults; the Consumer's `defaults` override them per key, and a
  # matching env var overrides those again at runtime (see scripts/run.sh).
  mergedDefaults = {
    label = "ready-for-agent";
    baseBranch = "main";
    maxParallel = 3;
    branchPrefix = "agent/issue-";
    # Label lifecycle (issue #15): dispatch swaps `label` -> `inProgressLabel` so
    # the query stays idempotent; a non-zero Box swaps it -> `failedLabel` for
    # human triage.
    inProgressLabel = "agent-in-progress";
    failedLabel = "agent-failed";
    completeLabel = "agent-complete";
    # Agent model (issue #16), promoted out of the image so `MODEL=...` switches
    # models at runtime with zero image rebuild.
    model = "claude-opus-4-8";
    # Subagent model tiers (issue #36): empty by default so --agents is omitted
    # unless the caller explicitly pins scout/reviewer models.
    scoutModel = "";
    reviewModel = "";
  }
  // defaults;

  # Plumbing every agent needs regardless of language: a shell, the VCS + GitHub
  # CLIs, Claude Code, CA certs, and the unix tools the entrypoint relies on.
  harnessPackages = with pkgs; [
    bashInteractive
    coreutils
    gnugrep
    gnused
    findutils
    gettext # envsubst, used by agent/entrypoint.sh
    git
    gh
    claude-code
    cacert
  ];

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
    ];
    text = stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is deliberately NOT baked here:
  # it is a runtime mount (see promptDir + scripts/run.sh) so it can be tuned
  # per project and hot-overridden without rebuilding the image.
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
  '';

  # The rendered prompt as a host store-path directory (native-buildable on
  # darwin, so it needs no Linux builder). The `run` command bakes this path in
  # and bind-mounts it at /agent/prompts, where the entrypoint reads
  # issue-prompt.md and substitutes the per-issue variables.
  promptDir = hostPkgs.writeTextDir "issue-prompt.md" prompt;

  image = pkgs.dockerTools.buildLayeredImage {
    name = "spindrift";
    tag = "latest";
    contents = [
      agentEnv
      agentFiles
    ];
    extraCommands = ''
      mkdir -p tmp home/agent work
      chmod 1777 tmp
    '';
    config = {
      Entrypoint = [ "/bin/bash" ];
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

  # The image's `.drv` path, also context-discarded. `build` realises this with
  # `nix build "<drv>^*"` before loading, so a fresh machine builds the image
  # instead of failing on an unrealised path — while discarding the context
  # keeps `nix flake check` and the launcher builds off any Linux build. Reading
  # `.drvPath` instantiates the derivation at eval time, so the .drv exists in
  # the store by the time `build` runs; only realising it needs a Linux builder.
  imageDrv = builtins.unsafeDiscardStringContext image.drvPath;

  # Nix-rendered shell preamble of baked config the launchers consume; every
  # value is interpolated straight into shell and shellcheck'd with the script.
  # IMAGE_ARCHIVE + RUNTIME are shared by both launchers. RUNTIME is deliberately
  # NOT a runtimeInput — it stays a checked host install (see the
  # `command -v "$RUNTIME"` guard in the scripts).
  imagePreamble = ''
    IMAGE_ARCHIVE="${imagePath}"
    RUNTIME="${runtime}"
  '';

  # Build-only config, layered on top of imagePreamble for the `build` launcher.
  # IMAGE_DRV is the derivation `build` realises; NIX_BUILDER_IMAGE/NIX_VOLUME
  # drive the ephemeral-container fallback (a named /nix volume keeps it
  # incremental); FLAKE_IMAGE_ATTR is the Linux image attribute that fallback
  # builds from the Consumer flake in $PWD.
  buildPreamble = ''
    IMAGE_DRV="${imageDrv}"
    NIX_BUILDER_IMAGE="${nixBuilderImage}"
    NIX_VOLUME="spindrift-nix"
    FLAKE_IMAGE_ATTR=".#packages.${linuxSystem}.spindrift"
  '';

  # Each run default renders as `NAME="''${NAME:-<baked>}"`, derived from the
  # merged defaults attrset, so a matching env var (or harness.env, sourced by
  # the script) still wins at runtime.
  runDefaultsPreamble = lib.concatStrings (
    lib.mapAttrsToList (envName: value: ''
      ${envName}="''${${envName}:-${toString value}}"
    '')
      {
        LABEL = mergedDefaults.label;
        BASE_BRANCH = mergedDefaults.baseBranch;
        MAX_PARALLEL = mergedDefaults.maxParallel;
        BRANCH_PREFIX = mergedDefaults.branchPrefix;
        IN_PROGRESS_LABEL = mergedDefaults.inProgressLabel;
        FAILED_LABEL = mergedDefaults.failedLabel;
        COMPLETE_LABEL = mergedDefaults.completeLabel;
        MODEL = mergedDefaults.model;
        SCOUT_MODEL = mergedDefaults.scoutModel;
        REVIEW_MODEL = mergedDefaults.reviewModel;
      }
  );

  # The launcher commands: a nix-rendered preamble + the script body, wrapped by
  # writeShellApplication (shebang, `set -euo pipefail`, a build-time shellcheck,
  # and a runtimeInputs PATH that pins the host tools they call).
  build = hostPkgs.writeShellApplication {
    name = "build";
    runtimeInputs = [ hostPkgs.coreutils ];
    text = imagePreamble + buildPreamble + builtins.readFile ./scripts/build.sh;
  };

  run = hostPkgs.writeShellApplication {
    name = "run";
    runtimeInputs = with hostPkgs; [
      gh
      git
      coreutils
    ];
    text =
      imagePreamble
      + runDefaultsPreamble
      # Baked with string context so `nix build .#run` realises the prompt dir
      # into the store; SPINDRIFT_PROMPT_DIR can still override it at run time.
      + ''
        PROMPT_DIR="${promptDir}"
      ''
      + builtins.readFile ./scripts/run.sh;
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
  // lib.optionalAttrs isLinux { spindrift = image; };

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
