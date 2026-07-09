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
  conflictResolvePrompt ? builtins.readFile ../templates/default/prompts/conflict-resolve-prompt.md,
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

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults derived from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those again at runtime.
  schemaDefaults = lib.mapAttrs (_: e: e.default or "") flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # Unknown defaults keys are caught at eval time — a typo like `basebranch`
  # would otherwise be silently ignored, never baked, never surfaced.
  unknownDefaultKeys = lib.filter (k: !(lib.hasAttr k flakeOptionEntries)) (lib.attrNames defaults);

  # --agents JSON baked at eval time via builtins.toJSON so model names are never
  # string-interpolated in bash (ADR 0007 tier-1). Each subagent is composed
  # independently by its own model knob; the --agents flag is omitted only when
  # no subagent model is set — the conditional is resolved at build time, not
  # in the entrypoint.
  agentsJsonTemplate =
    let
      sm = mergedDefaults.scoutModel or "";
      rm = mergedDefaults.reviewModel or "";
      agents =
        lib.optionalAttrs (sm != "") {
          scout = {
            description = "Map relevant files, seams, and tests; return a structured brief";
            prompt = "";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
              "WebSearch"
              "Glob"
              "Grep"
            ];
            model = sm;
          };
        }
        // lib.optionalAttrs (rm != "") {
          reviewer = {
            description = "Review the branch diff for spec compliance and coding standards";
            prompt = "";
            tools = [
              "Read"
              "Bash"
              "WebFetch"
            ];
            model = rm;
          };
        };
    in
    if agents == { } then "" else builtins.toJSON agents;

  # Version sourced from the release-please manifest so mkHarness always tracks
  # the bot-maintained source of truth (ADR-0010).
  spindriftVersion = (builtins.fromJSON (builtins.readFile ../.release-please-manifest.json)).".";

  # In-box heartbeat filter: reuses the #182 heartbeat parser as a CLI binary
  # so the entrypoint can pipe claude's stream-json output through it without
  # modifying the raw capture channel. Built for Linux (pkgs, not hostPkgs).
  heartbeatFilterBin = pkgs.buildGoModule {
    pname = "spindrift-heartbeat-filter";
    version = spindriftVersion;
    src = ../cmd/launcher;
    vendorHash = null;
    subPackages = [ "spindrift-heartbeat-filter" ];
    meta.license = lib.licenses.mit;
  };

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
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    cp ${pkgs.writeText "issue-prompt.md" prompt} $out/agent/prompts/issue-prompt.md
    cp ${pkgs.writeText "scout-prompt.md" scoutPrompt} $out/agent/prompts/scout-prompt.md
    cp ${pkgs.writeText "review-prompt.md" reviewPrompt} $out/agent/prompts/review-prompt.md
    cp ${pkgs.writeText "conflict-resolve-prompt.md" conflictResolvePrompt} $out/agent/prompts/conflict-resolve-prompt.md
    ${lib.optionalString (skills != [ ]) ''
      mkdir -p $out/home/agent/.claude/skills
      ${lib.concatMapStrings (f: ''
        cp ${f} $out/home/agent/.claude/skills/${
          if lib.isDerivation f then f.name else builtins.baseNameOf f
        }
      '') skills}
    ''}
  '';

  # The rendered prompt directory as a host store path (native-buildable on
  # darwin, so it needs no Linux builder). The prompt is normally baked into
  # the image via agentFiles; this output exists so tests can assert it is NOT
  # bind-mounted by default, and so SPINDRIFT_PROMPT_DIR can point to it.
  promptDir = hostPkgs.runCommand "prompt-dir" { } ''
    mkdir -p $out
    cp ${hostPkgs.writeText "issue-prompt.md" prompt} $out/issue-prompt.md
    cp ${hostPkgs.writeText "scout-prompt.md" scoutPrompt} $out/scout-prompt.md
    cp ${hostPkgs.writeText "review-prompt.md" reviewPrompt} $out/review-prompt.md
    cp ${hostPkgs.writeText "conflict-resolve-prompt.md" conflictResolvePrompt} $out/conflict-resolve-prompt.md
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

  image = pkgs.dockerTools.buildLayeredImage {
    name = "spindrift";
    tag = "latest";
    contents = [
      agentEnv
      agentFiles
    ];
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
          ];
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
  # flag reference while `spindrift --help` stays concise (the OPTIONS groups
  # mirror groupOrder in cmd/launcher/flags.go; keep the two lists in step).
  manpageRoff =
    let
      inherit (lib)
        filter
        sort
        concatMapStrings
        replaceStrings
        toLower
        elem
        ;
      esc = replaceStrings [ "\\" ] [ "\\\\" ]; # neutralise stray backslashes
      escFlag = f: replaceStrings [ "-" ] [ "\\-" ] f; # roff renders \- as a minus
      toKebab = env: toLower (replaceStrings [ "_" ] [ "-" ] env);
      flagKind = e: if builtins.isInt (e.default or null) then "int" else "string";
      flagDflt =
        e: if e ? default then (if builtins.isInt e.default then toString e.default else e.default) else "";
      nonSecret = filter (e: !(e.secret or false)) (lib.attrValues schema);
      secretEntries = filter (e: e.secret or false) (lib.attrValues schema);
      # Display order of OPTIONS headings; mirrors groupOrder in flags.go.
      groupOrder = [
        "Issue discovery"
        "Lifecycle labels"
        "Branches & merge"
        "Concurrency & dependency waves"
        "Models"
        "Self-healing & retries"
        "Sandbox & resources"
        "Repository & identity"
        "Prompt & skill iteration"
      ];
      usedGroups = lib.unique (map (e: e.group or "") nonSecret);
      unknownGroups = filter (g: !(elem g groupOrder)) usedGroups;
      optionBlock =
        e:
        let
          names =
            "\\-\\-" + escFlag (toKebab e.env) + (if e ? alias then ", \\-\\-" + escFlag e.alias else "");
          dflt = flagDflt e;
          dfltSentence = if dflt == "" then "No default." else "Default: " + esc dflt + ".";
        in
        ".TP\n.B ${names} \\fI${flagKind e}\\fR\n\\&${esc e.doc}. ${dfltSentence}\n";
      groupSection =
        g:
        let
          entries = sort (a: b: a.env < b.env) (filter (e: (e.group or "") == g) nonSecret);
        in
        if entries == [ ] then "" else ".SS ${g}\n" + concatMapStrings optionBlock entries;
      secretBlock =
        e:
        ".TP\n.B ${e.env}\n\\&${esc e.doc}. Supply via the environment or \\-\\-${toKebab e.env}\\-file (reads the value from a file path; takes precedence over the environment).\n";
    in
    assert lib.assertMsg (
      unknownGroups == [ ]
    ) "manpageRoff: knob group(s) absent from groupOrder: ${lib.concatStringsSep ", " unknownGroups}";
    ''
      .TH SPINDRIFT 1 "${spindriftVersion}" "spindrift ${spindriftVersion}" "Spindrift Manual"
      .SH NAME
      spindrift \- fan out headless Claude Code agents across GitHub issues
      .SH SYNOPSIS
      .B spindrift
      [\fIflags\fR] \fIsubcommand\fR [\fIargs\fR]
      .SH DESCRIPTION
      .B spindrift
      dispatches one disposable, nix-built container per GitHub issue, runs a
      headless Claude Code agent inside it, and drives each resulting pull request
      through a merge gate. Every runtime knob is set by flag, environment
      variable, or baked default, in that precedence order. Non-secret knobs also
      read from a gitignored
      .I harness.env
      in the working directory.
      .SH SUBCOMMANDS
      .TP
      .B dispatch [\-\-no-build] [\-\-yes] [issue...]
      Fan out agents. With no issue list, discover dispatchable issues by label;
      an explicit issue list dispatches exactly those, bypassing the label and
      barrier gates.
      .TP
      .B preview [issue...]
      Dry run: show what dispatch would pick up, in order, without launching any
      agent.
      .TP
      .B build
      Realize the agent image (or store closures) without running any agent.
      .TP
      .B recover <issue>
      Run the merge gate for a single issue whose agent already finished.
      .SH "DISPATCH FLAGS"
      .TP
      .B \-\-no-build
      Fail fast if the image is absent instead of building it; pair with
      .B spindrift build
      for split build/run flows.
      .TP
      .B \-\-yes
      Skip the confirmation prompt when dispatching unlabeled issues. Alias:
      .BR \-\-force .
      .SH OPTIONS
      Flags take precedence over environment variables, which take precedence over
      baked defaults.
      ${concatMapStrings groupSection groupOrder}.SH ENVIRONMENT
      Secret knobs are never exposed as value flags; they are read from the
      environment or from a file via their
      .B \-\-<name>-file
      flag.
      ${concatMapStrings secretBlock secretEntries}.SH FILES
      .TP
      .I harness.env
      Gitignored per-checkout config and secrets, sourced from the working
      directory at dispatch time.
      .SH EXAMPLES
      .TP
      Dispatch every ready issue, three containers at a time:
      .B spindrift dispatch \-\-max-parallel 3
      .TP
      Dispatch a single issue, skipping the image build:
      .B spindrift dispatch \-\-no-build 42
      .TP
      Preview the queue without launching anything:
      .B spindrift preview
      .TP
      Print the full flag reference in the terminal:
      .B spindrift \-\-help \-\-all
      .SH "SEE ALSO"
      .BR git (1),
      .BR gh (1)
    '';

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
