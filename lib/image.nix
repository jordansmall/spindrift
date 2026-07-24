# Box image assembly (issue #514): the harness plumbing package set, the
# agent environment, the agent files, the passwd/group files, and the
# layered OCI image build itself — including the nix-in-box store DB
# registration, the writable-store self-test wiring (ADR 0018), and the
# fakeroot chowns. Pulled out of lib/mkHarness.nix as a pure code move: the
# image derivation must be byte-identical before and after, so every value
# below is either copied verbatim or threaded in as a parameter from
# mkHarness's existing computation — nothing here is re-derived or
# reformatted. The context-discarded store-path trick that lets darwin build
# launchers without a Linux builder stays on the mkHarness side, applied to
# this module's outputs.
{
  pkgs,
  lib,
  # Project-specific tools baked into the image on top of the harness plumbing,
  # as a function of the (Linux) pkgs — the Consumer's language/toolchain surface.
  packages,
  # Optional shell snippet the entrypoint runs after cloning, to warm toolchain
  # caches (e.g. fetch pinned deps). Baked into the image; default is a no-op.
  prefetch,
  nixInBox,
  nixStoreWritable,
  # Extra derivations whose closures are baked into the image contents and,
  # when nixInBox is on, registered in the store DB alongside the runtime
  # closure — so in-box nix sees them as already present instead of
  # cold-substituting the world on every Box. A function of the (Linux) pkgs,
  # like `packages`, so Consumer-supplied derivations stay correct on a
  # darwin host. A generic Consumer knob, not a spindrift special case
  # (issue #469).
  extraClosures,
  # The selected Driver's in-box half (ADR 0009): invocation binary/flags,
  # skill wiring, and outcome extraction.
  driverEntry,
  # In-box Driver runner (#626), built for Linux by mkHarness: runs one
  # Driver invocation direct or inside the devShell, tees the stream, and
  # filters heartbeats in-process (absorbing the former standalone
  # spindrift-heartbeat-filter binary, #183).
  driverExecBin,
  # --agents JSON, rendered by the selected Driver (ADR 0009).
  agentsJsonTemplate,
  # The Driver's in-box half rendered into agent/entrypoint.sh's
  # ${DRIVER_*:-<default>} vars and the Driver function definitions.
  driverPreamble,
  # The Conditional fragment registry (issue #622) rendered into
  # agent/entrypoint.sh's _FRAGMENT_ROWS loop input and _FRAGMENT_SUBST_VARS
  # substitution allowlist.
  fragmentRegistryPreamble,
  # The schema-derived defaults block (mkHarness's `renderDefaultsPreamble { }`),
  # prepended to the entrypoint so it carries the baked values without
  # hardcoding them in the source script.
  entrypointDefaultsPreamble,
  # The agent prompt template, a Consumer-owned artifact, and the subagent
  # system prompts.
  prompt,
  scoutPrompt,
  reviewPrompt,
  filerPrompt,
  conflictResolvePrompt,
  fixPrompt,
  # Driven instead of `prompt` when DISPATCH_KIND=research (ADR 0022, issue #640).
  researchPrompt,
  # The SPINDRIFT_OUTCOME / COMMS / CHECK-COMMIT shared blocks (issues #419,
  # #455) and their injectors, sliced from issue-prompt.md by mkHarness so the
  # host-side contract files (which stay in mkHarness) cannot drift from what
  # gets baked here.
  outcomeContract,
  commsBlock,
  checkBlock,
  # The research dispatch kind's own outcome contract (issue #640), sliced
  # from research-prompt.md the same way.
  researchOutcomeContract,
  injectOutcomeContract,
  injectFixSharedBlocks,
  injectResearchOutcomeContract,
  # The conditional prompt fragments directory (issue #463).
  fragmentsSourceDir,
  # Skills baked into the image at /home/agent/.claude/skills. Each element is
  # baked as a `<name>/SKILL.md` directory — Claude Code discovers skills only
  # as directories, never flat `<name>.md` files. A { name; src; } content
  # entry (issue #597) supplies the skill name and SKILL.md body, realized with
  # this (image) pkgs; a path or derivation is a skill directory copied under
  # its own basename.
  skills,
}:
let
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
      driverExecBin # in-box Driver runner (#626)
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
  # complete, standalone script (the bats harness prepends driverPreambleFile
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
      driverExecBin # in-box Driver runner (#626)
    ];
    # Prepend the schema-derived defaults block so the entrypoint carries the
    # baked values without hardcoding them in the source script.
    # AGENTS_JSON_TEMPLATE is baked as a fixed value (not a :-default) because it
    # is derived from the configured models, not a standalone knob.
    text =
      "AGENTS_JSON_TEMPLATE="
      + lib.escapeShellArg agentsJsonTemplate
      + "\n"
      # Always-on structural default (issue #1909, spec #1907): Claude Code
      # strips Anthropic/cloud-provider credentials from every subprocess it
      # spawns, so a `env`/`printenv` from a Bash tool call can't dump
      # ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN. Not a flakeOption schema
      # knob -- there is no operator override -- so it's a fixed exported
      # line here, the same way AGENTS_JSON_TEMPLATE is a baked, non-tunable
      # value rather than a schema-derived default. `export`ed (unlike the
      # schema-derived entrypointDefaultsPreamble lines below, which stay
      # local bash variables) because it must survive the exec into
      # driver-exec and reach the Driver process's own environment.
      + "export CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1\n"
      + driverPreamble
      + fragmentRegistryPreamble
      + entrypointDefaultsPreamble
      + stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # Registers the PreToolUse hook (issue #1609, layer 2) that rejects a Bash
  # call with run_in_background: true: --disallowedTools (lib/drivers/claude.nix)
  # strips ScheduleWakeup/Cron*/RemoteTrigger/Monitor from the Driver's tool
  # surface, but run_in_background is a parameter of the Bash tool, not a
  # tool name, so it cannot be stripped the same way. Rendered via
  # builtins.toJSON, not a hand-built string, so the settings.json is always
  # valid JSON (ADR 0007 tier-1 -- same reasoning as agentsJsonTemplate).
  # Home-wide rather than gated behind a Driver attribute: only the claude
  # Driver exists today and this is Claude Code's own hook mechanism, but the
  # restriction applies to every pass sharing this $HOME (main run,
  # conflict-resolve, fix), not to any one Driver invocation's flags.
  # Registers the second PreToolUse hook (issue #1909, spec #1907):
  # credential-deny.sh rejects a Read/Bash call targeting a known credential
  # path (~/.claude/.credentials.json, **/.env, ~/.config/gh/hosts.yml). Two
  # matcher entries, not one -- Claude Code's PreToolUse matcher is per-tool,
  # so Read and Bash each need their own entry even though both point at the
  # same script. Merged into one PreToolUse array below, alongside the
  # reject-background-bash entry, since the image ships a single
  # ~/.claude/settings.json.
  boxSettings = builtins.toJSON {
    hooks = {
      PreToolUse = [
        {
          matcher = "Bash";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/reject-background-bash.sh";
            }
          ];
        }
        {
          matcher = "Read";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/credential-deny.sh";
            }
          ];
        }
        {
          matcher = "Bash";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/credential-deny.sh";
            }
          ];
        }
      ];
    };
  };

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is baked in alongside the
  # entrypoint (not a host-path mount) so the Box is self-contained: a macOS
  # podman machine cannot bind-mount the host /nix/store into its Linux VM.
  # SPINDRIFT_PROMPT_DIR still mounts an override dir for zero-rebuild iteration
  # (the Go launcher mounts it in cmd/launcher/internal/runner).
  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    mkdir -p $out/agent/prompts
    ${lib.optionalString (driverEntry ? sessionCacheDirRelative) ''
      # Pre-create the driver-cache mountpoint so podman reuses the agent-owned
      # directory instead of fabricating root-owned parents (issue #447).
      mkdir -p $out/home/agent/${driverEntry.sessionCacheDirRelative}
    ''}
    mkdir -p $out/home/agent/.claude/hooks
    cp ${../agent/reject-background-bash.sh} $out/home/agent/.claude/hooks/reject-background-bash.sh
    chmod +x $out/home/agent/.claude/hooks/reject-background-bash.sh
    cp ${../agent/credential-deny.sh} $out/home/agent/.claude/hooks/credential-deny.sh
    chmod +x $out/home/agent/.claude/hooks/credential-deny.sh
    cp ${pkgs.writeText "settings.json" boxSettings} $out/home/agent/.claude/settings.json
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    # A sibling of prompts/, not inside it, so a SPINDRIFT_PROMPT_DIR mount
    # (which shadows only /agent/prompts) never hides it from the entrypoint
    # (issue #420).
    cp ${pkgs.writeText "outcome-contract.md" outcomeContract} $out/agent/outcome-contract.md
    cp ${pkgs.writeText "comms-contract.md" commsBlock} $out/agent/comms-contract.md
    cp ${pkgs.writeText "check-contract.md" checkBlock} $out/agent/check-contract.md
    cp ${pkgs.writeText "research-outcome-contract.md" researchOutcomeContract} $out/agent/research-outcome-contract.md
    cp ${pkgs.writeText "issue-prompt.md" (injectOutcomeContract prompt)} $out/agent/prompts/issue-prompt.md
    cp ${pkgs.writeText "scout-prompt.md" scoutPrompt} $out/agent/prompts/scout-prompt.md
    cp ${pkgs.writeText "review-prompt.md" reviewPrompt} $out/agent/prompts/review-prompt.md
    cp ${pkgs.writeText "filer-prompt.md" filerPrompt} $out/agent/prompts/filer-prompt.md
    cp ${pkgs.writeText "conflict-resolve-prompt.md" conflictResolvePrompt} $out/agent/prompts/conflict-resolve-prompt.md
    cp ${pkgs.writeText "fix-prompt.md" (injectFixSharedBlocks fixPrompt)} $out/agent/prompts/fix-prompt.md
    cp ${pkgs.writeText "research-prompt.md" (injectResearchOutcomeContract researchPrompt)} $out/agent/prompts/research-prompt.md
    cp -r ${fragmentsSourceDir} $out/agent/prompts/fragments
    ${lib.optionalString (skills != [ ]) ''
      mkdir -p $out/home/agent/${driverEntry.skillsDirRelative}
      ${lib.concatMapStrings (
        f:
        # Claude Code discovers a skill only as a directory holding a SKILL.md
        # (~/.claude/skills/<name>/SKILL.md); a flat <name>.md file is ignored,
        # so every entry is baked under its own <name>/ directory. A
        # { name; src; } content entry names the skill via `name` and is
        # re-realized with THIS pkgs (the image's own Linux instantiation,
        # mirroring the prompts above) rather than copied as a pre-built
        # derivation, so the skill never carries a consumer host's system into
        # the image's derivation graph (#597); a path/derivation entry is a
        # skill directory copied verbatim under its own basename.
        if builtins.isAttrs f && !(lib.isDerivation f) then
          ''
            mkdir -p $out/home/agent/${driverEntry.skillsDirRelative}/${f.name}
            cp ${pkgs.writeText "SKILL.md" f.src} $out/home/agent/${driverEntry.skillsDirRelative}/${f.name}/SKILL.md
          ''
        else
          ''
            cp -r ${f} $out/home/agent/${driverEntry.skillsDirRelative}/${
              if lib.isDerivation f then f.name else builtins.baseNameOf f
            }
          ''
      ) skills}
    ''}
  '';

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
in
{
  inherit image agentEnv agentFiles;
}
