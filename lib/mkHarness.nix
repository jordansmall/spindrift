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

  mergedConfig = { allowUnfree = true; } // config;

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
  # once spindrift is a store path.
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent/prompts
    cp ${../agent/entrypoint.sh} $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    cp -r ${../prompts}/. $out/agent/prompts/
  '';

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

  mkCommand =
    name: src:
    hostPkgs.runCommand "spindrift-${name}" { } ''
      mkdir -p $out/bin
      substitute ${src} $out/bin/${name} \
        --replace-fail '@imagePath@' '${imagePath}'
      chmod +x $out/bin/${name}
    '';

  build = mkCommand "build" ./scripts/build.sh;
  run = mkCommand "run" ./scripts/run.sh;

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
