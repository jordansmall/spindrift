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
  # as a function of the (Linux) pkgs. This is the language-toolchain surface:
  # the Consumer supplies its own compiler and build tools here.
  packages ? (_pkgs: [ ]),
  # Optional shell snippet the entrypoint runs after cloning, to warm any
  # caches the toolchain wants (e.g. a fetch of pinned dependencies). Baked
  # into the image; default is a no-op.
  prefetch ? "",
  # The agent prompt template, a Consumer-owned artifact (it is mutated per
  # project). Rendered to a store path and mounted into the container at
  # runtime — NOT baked into the image — so it can be re-pointed via
  # SPINDRIFT_PROMPT_DIR with zero rebuilds (see scripts/run.sh). The default
  # is the scaffolded issue-prompt.md, a working starting point out of the box.
  # The entrypoint substitutes the per-issue variables into it at run time.
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  # Non-secret run configuration baked into the generated `run` command as its
  # built-in defaults. A matching env var (LABEL/BASE_BRANCH/MAX_PARALLEL/
  # BRANCH_PREFIX) still wins at runtime, so one built command can be
  # re-pointed without a rebuild.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default) or
  # "docker". Baked in — it selects which binary `build`/`run` invoke for image
  # existence checks, load, and run.
  runtime ? "podman",
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

  # Host toolset: the launcher commands run on the Consumer's own system.
  hostPkgs = import nixpkgs {
    inherit system;
    config = mergedConfig;
  };

  inherit (pkgs) lib;

  # Built-in run defaults; the Consumer's `defaults` override them per key, and a
  # matching env var overrides those again at runtime (see scripts/run.sh).
  mergedDefaults = {
    label = "ready-for-agent";
    baseBranch = "main";
    maxParallel = 3;
    branchPrefix = "agent/issue-";
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

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is deliberately NOT baked here:
  # it is a runtime mount (see promptDir + scripts/run.sh) so it can be tuned
  # per project and hot-overridden without rebuilding the image.
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent
    cp ${../agent/entrypoint.sh} $out/agent/entrypoint.sh
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
        # Consumer-supplied cache warm-up snippet; the entrypoint runs it after
        # cloning. Empty by default (no-op).
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

  # `replacements` is the list of `--replace-fail '@k@' 'v'` flags for this
  # script; every placeholder listed must be present in the source (that is what
  # `--replace-fail` guarantees), keeping the scripts and their bakes in sync.
  mkCommand =
    name: src: replacements:
    hostPkgs.runCommand "spindrift-${name}" { } ''
      mkdir -p $out/bin
      substitute ${src} $out/bin/${name} ${lib.concatStringsSep " " replacements}
      chmod +x $out/bin/${name}
    '';

  commonReplace = [
    "--replace-fail '@imagePath@' '${imagePath}'"
    "--replace-fail '@runtime@' '${runtime}'"
  ];

  build = mkCommand "build" ./scripts/build.sh commonReplace;
  run = mkCommand "run" ./scripts/run.sh (
    commonReplace
    ++ [
      "--replace-fail '@label@' '${mergedDefaults.label}'"
      "--replace-fail '@baseBranch@' '${mergedDefaults.baseBranch}'"
      "--replace-fail '@maxParallel@' '${toString mergedDefaults.maxParallel}'"
      "--replace-fail '@branchPrefix@' '${mergedDefaults.branchPrefix}'"
      # Baked with string context so `nix build .#run` realises the prompt dir
      # into the store; SPINDRIFT_PROMPT_DIR can still override it at run time.
      "--replace-fail '@promptDir@' '${promptDir}'"
    ]
  );

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
