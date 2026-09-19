# Box image assembly (issue #514), including the nix-in-box store DB
# registration, the writable-store self-test wiring (ADR 0018), and the
# fakeroot chowns. This came out of lib/mkHarness.nix as a pure code move, so
# the image derivation must stay byte-identical: every value below is copied
# verbatim or threaded in as a parameter, never re-derived here.
{
  pkgs,
  lib,
  # Project tools baked on top of the harness plumbing (`packages`), plus extra
  # derivations whose closures are baked into the image and, when nixInBox is
  # on, registered in the store DB (`extraClosures`, issue #469). Both are
  # functions of the (Linux) pkgs, so a darwin-host Consumer's packages and
  # closures stay correct.
  packageSet,
  # The selected Driver's in-box half (ADR 0009): the driver entry, the in-box
  # Driver runner (#626), the in-box orchestrator (#1996), the entrypoint.sh
  # preamble, and the on-disk subagent files (AC4).
  driver,
  # The resolved agent roster: --agents JSON (ADR 0009), the per-agent prompt
  # files and custom roster entries (issue #264), and the baked skills
  # (issue #597).
  agents,
  # The contract files sliced from issue-prompt.md / research-prompt.md
  # (issues #419, #455, #640) and their injectors, plus the fragment,
  # prompt-contract and forbidden-markers registries baked as JSON for the Go
  # `driver-exec` verbs (issues #2354, #2356, #2464). Also lib/agent-paths.nix's
  # attrset, the single source for the baked /agent/* paths (issue #2531).
  contracts,
  # The prompt tree: the agent prompt template and subagent system prompts, the
  # research prompts (ADR 0022, issues #640, #2202), the conditional fragments
  # source dir (issue #463), and the fragment registry's preamble (issue #622).
  prompts,
  # The knob subset the image consumes: nix-in-box / writable-store wiring
  # (ADR 0018), the forgejo-backend selector (issue #1963), the prefetch
  # snippet, the Driver-scoped image name (issue #262 AC1), and the
  # schema-derived entrypoint defaults preamble.
  knobs,
}:
let
  # Single-sourced so a baked skill's name is never hand-typed here (#2532).
  bakedSkills = import ./baked-skills.nix;
  # Bakes fj (forgejo-cli) into the image when ISSUE_TRACKER or CODE_FORGE
  # selects the forgejo backend (issue #1963). Defaults to false because a
  # caller may omit the knob.
  forgejoBackend = knobs.forgejoBackend or false;

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
      (driver.driverEntry.package pkgs)
      cacert
      socat # unix-socket<->TCP Forwarder for the registry proxy (ADR 0044, issue #2849)
      driver.driverExecBin # in-box Driver runner (#626)
      driver.orchestratorBin # in-box orchestrator (#1996)
    ])
    # The nix CLI so `nix flake check` / `nix develop` work inside the box.
    # Omitted only when the Consumer opts into the lean image.
    ++ lib.optional knobs.nixInBox pkgs.nix
    # Baked only for a forgejo-backend Consumer (issue #1963), so a
    # github-backend image never gains an unused CLI.
    ++ lib.optional forgejoBackend pkgs.forgejo-cli;

  agentEnv = pkgs.buildEnv {
    name = "agent-env";
    paths = harnessPackages ++ packageSet.packages pkgs;
    pathsToLink = [
      "/bin"
      "/lib"
      "/etc"
      "/share"
      "/include"
    ];
  };

  # writeShellApplication so shellcheck runs at build time and the tools stay
  # pinned. The source stays a complete, standalone script (the bats harness
  # prepends driverPreambleFile before exec-ing it), so its shebang is stripped
  # before it becomes this derivation's body.
  entrypoint = pkgs.writeShellApplication {
    name = "entrypoint";
    runtimeInputs =
      (with pkgs; [
        git
        gh
        (driver.driverEntry.package pkgs)
        gettext # envsubst
        coreutils
        jq # extracts the outcome from the stream-json transcript
        driver.driverExecBin # in-box Driver runner (#626)
        driver.orchestratorBin # in-box orchestrator (#1996)
      ])
      # On PATH for the entrypoint's fj credential setup only when the
      # Consumer's backend is forgejo (issue #1963).
      ++ lib.optional forgejoBackend pkgs.forgejo-cli;
    # AGENTS_JSON_TEMPLATE is baked as a fixed value, not a :-default, because
    # it comes from the configured models rather than a standalone knob.
    text =
      "export AGENTS_JSON_TEMPLATE="
      + lib.escapeShellArg agents.agentsJsonTemplate
      + "\n"
      + "AGENTS_PROMPT_FILES="
      + lib.escapeShellArg agents.agentsPromptFilesJson
      + "\n"
      + driver.driverPreamble
      + contracts.agentPathsPreamble
      + prompts.fragmentRegistryPreamble
      + knobs.entrypointDefaultsPreamble
      + stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # reject-background-bash.sh (issue #1609): --disallowedTools strips whole
  # tools, but run_in_background is a parameter of the Bash tool, not a tool
  # name, so only a PreToolUse hook can reject it. Home-wide rather than gated
  # behind a Driver attribute, because the restriction applies to every pass
  # sharing this $HOME, not to one Driver invocation's flags.

  # credential-deny.sh (issue #1909, spec #1907) rejects a Read or Bash call
  # targeting a known credential path. Read and Bash each need their own matcher
  # entry pointing at the same script, because the PreToolUse matcher is
  # per-tool.

  # env-credential-scrub.sh (issue #1927, spec #1907) unsets ANTHROPIC_API_KEY
  # and CLAUDE_CODE_OAUTH_TOKEN for every Bash call and denies any reference to
  # /proc/<pid>/environ. Claude Code's own CLAUDE_CODE_SUBPROCESS_ENV_SCRUB
  # cannot do this job: it forces the Driver's permission mode to `default` and
  # nests a bwrap sandbox that cannot mount /proc inside the Box's own (#1926).

  # bash-output-tee.sh and bash-output-summary.sh (issue #1988) tee every Bash
  # call's combined output to a log file and replace the tool result with a
  # bounded tail, so full output lands on disk and only the tail enters the
  # model's context. This covers every call, not just the overflow case
  # BASH_MAX_OUTPUT_LENGTH already handles (issue #1987).

  # Rendered with builtins.toJSON rather than a hand-built string so the
  # settings.json is always valid JSON (ADR 0007 tier-1).
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
        {
          matcher = "Bash";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/env-credential-scrub.sh";
            }
          ];
        }
        {
          matcher = "Bash";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/bash-output-tee.sh";
            }
          ];
        }
      ];
      PostToolUse = [
        {
          matcher = "Bash";
          hooks = [
            {
              type = "command";
              command = "/home/agent/.claude/hooks/bash-output-summary.sh";
            }
          ];
        }
      ];
    };
  };

  # Everything under /agent is baked into the image rather than bind-mounted:
  # there is no working tree once spindrift is a store path, and a macOS podman
  # machine cannot bind-mount the host /nix/store into its Linux VM.
  # SPINDRIFT_PROMPT_DIR still mounts an override dir for zero-rebuild
  # iteration.

  # Harness-owned skills (issues #2489, #2490) are baked unconditionally,
  # independent of the Consumer's own `skills` list, so a Box always has
  # /auto-format and /auto-lint to invoke. The names come from
  # lib/baked-skills.nix (issue #2532) instead of being hand-typed here.
  harnessSkills = builtins.map (s: {
    inherit (s) name;
    src = builtins.readFile (../templates/default/skills + "/${s.name}/SKILL.md");
  }) (builtins.filter (s: s.harnessOwned or false) bakedSkills);

  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    # PROMPTS_DIR is currently /agent/prompts, so this is also the only
    # line that creates $out/agent itself -- every OUTCOME_CONTRACT_FILE/
    # COMMS_CONTRACT_FILE/CHECK_CONTRACT_FILE/RESEARCH_OUTCOME_CONTRACT_FILE/
    # PROMPTASSEMBLY_REGISTRY_FILE/
    # PROMPT_CONTRACT_REGISTRY_FILE/FORBIDDEN_MARKERS_REGISTRY_FILE `cp`
    # destination below is a sibling of PROMPTS_DIR under that same $out/agent
    # dir (issue #420) and relies on this mkdir having created it. A future
    # lib/agent-paths.nix rename that moves PROMPTS_DIR out from under /agent
    # would silently break those `cp` calls unless this mkdir (or an explicit
    # one) moves with it.
    mkdir -p $out${contracts.agentPaths.PROMPTS_DIR}
    ${lib.optionalString (driver.driverEntry ? sessionCacheDirRelative) ''
      # Pre-create the driver-cache mountpoint so podman reuses the agent-owned
      # directory instead of fabricating root-owned parents (issue #447).
      mkdir -p $out/home/agent/${driver.driverEntry.sessionCacheDirRelative}
    ''}
    mkdir -p $out/home/agent/.claude/hooks
    cp ${../agent/reject-background-bash.sh} $out/home/agent/.claude/hooks/reject-background-bash.sh
    chmod +x $out/home/agent/.claude/hooks/reject-background-bash.sh
    cp ${../agent/credential-deny.sh} $out/home/agent/.claude/hooks/credential-deny.sh
    chmod +x $out/home/agent/.claude/hooks/credential-deny.sh
    cp ${../agent/env-credential-scrub.sh} $out/home/agent/.claude/hooks/env-credential-scrub.sh
    chmod +x $out/home/agent/.claude/hooks/env-credential-scrub.sh
    cp ${../agent/bash-output-tee.sh} $out/home/agent/.claude/hooks/bash-output-tee.sh
    chmod +x $out/home/agent/.claude/hooks/bash-output-tee.sh
    cp ${../agent/bash-output-summary.sh} $out/home/agent/.claude/hooks/bash-output-summary.sh
    chmod +x $out/home/agent/.claude/hooks/bash-output-summary.sh
    cp ${pkgs.writeText "settings.json" boxSettings} $out/home/agent/.claude/settings.json
    cp ${entrypoint}/bin/entrypoint $out/agent/entrypoint.sh
    chmod +x $out/agent/entrypoint.sh
    # A sibling of prompts/, not inside it, so a SPINDRIFT_PROMPT_DIR mount
    # (which shadows only /agent/prompts) never hides it from the entrypoint
    # (issue #420).
    cp ${pkgs.writeText "outcome-contract.md" contracts.outcomeContract} $out${contracts.agentPaths.OUTCOME_CONTRACT_FILE}
    cp ${pkgs.writeText "comms-contract.md" contracts.commsBlock} $out${contracts.agentPaths.COMMS_CONTRACT_FILE}
    cp ${pkgs.writeText "check-contract.md" contracts.checkBlock} $out${contracts.agentPaths.CHECK_CONTRACT_FILE}
    cp ${pkgs.writeText "research-outcome-contract.md" contracts.researchOutcomeContract} $out${contracts.agentPaths.RESEARCH_OUTCOME_CONTRACT_FILE}
    cp ${pkgs.writeText "fragments-registry.json" contracts.fragmentsRegistryJson} $out${contracts.agentPaths.PROMPTASSEMBLY_REGISTRY_FILE}
    cp ${pkgs.writeText "prompt-contract-registry.json" contracts.promptContractRegistryJson} $out${contracts.agentPaths.PROMPT_CONTRACT_REGISTRY_FILE}
    cp ${pkgs.writeText "forbidden-markers-registry.json" contracts.forbiddenMarkersRegistryJson} $out${contracts.agentPaths.FORBIDDEN_MARKERS_REGISTRY_FILE}
    cp ${pkgs.writeText "issue-prompt.md" (contracts.injectOutcomeContract prompts.prompt)} $out${contracts.agentPaths.PROMPTS_DIR}/issue-prompt.md
    cp ${pkgs.writeText "scout-prompt.md" prompts.scoutPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/scout-prompt.md
    cp ${pkgs.writeText "review-prompt.md" prompts.reviewPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/review-prompt.md
    cp ${pkgs.writeText "review-axis-prompt.md" prompts.reviewAxisPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/review-axis-prompt.md
    cp ${pkgs.writeText "filer-prompt.md" prompts.filerPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/filer-prompt.md
    cp ${pkgs.writeText "worker-prompt.md" prompts.workerPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/worker-prompt.md
    ${lib.concatMapStrings (
      e:
      let
        pf = e.promptFile;
      in
      "cp ${pkgs.writeText pf e.prompt} $out${contracts.agentPaths.PROMPTS_DIR}/${pf}\n"
    ) agents.customRosterPromptFiles}
    cp ${pkgs.writeText "conflict-resolve-prompt.md" prompts.conflictResolvePrompt} $out${contracts.agentPaths.PROMPTS_DIR}/conflict-resolve-prompt.md
    cp ${pkgs.writeText "fix-prompt.md" (contracts.injectFixSharedBlocks prompts.fixPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/fix-prompt.md
    cp ${pkgs.writeText "research-prompt.md" (contracts.injectResearchOutcomeContract prompts.researchPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/research-prompt.md
    cp ${pkgs.writeText "research-self-contained-prompt.md" (contracts.injectResearchOutcomeContract prompts.researchSelfContainedPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/research-self-contained-prompt.md
    cp -r ${prompts.fragmentsSourceDir} $out${contracts.agentPaths.PROMPTS_DIR}/fragments
    ${lib.optionalString ((harnessSkills ++ agents.skills) != [ ]) ''
      mkdir -p $out/agent/skills
      ${lib.concatMapStrings (
        f:
        # Claude Code discovers a skill only as a directory holding SKILL.md, so
        # each entry is baked under its own <name>/ directory below the fixed
        # /agent/skills, which entrypoint.sh copies into the Driver's runtime
        # skills dir at startup. A { name; src; } entry is re-realized with THIS
        # pkgs, so no consumer host's system reaches the derivation graph (#597).
        if builtins.isAttrs f && !(lib.isDerivation f) then
          ''
            mkdir -p $out/agent/skills/${f.name}
            cp ${pkgs.writeText "SKILL.md" f.src} $out/agent/skills/${f.name}/SKILL.md
          ''
        else
          ''
            cp -r ${f} $out/agent/skills/${if lib.isDerivation f then f.name else builtins.baseNameOf f}
          ''
      ) (harnessSkills ++ agents.skills)}
    ''}
    ${lib.concatStrings (
      lib.mapAttrsToList (relPath: content: ''
        mkdir -p "$(dirname $out/home/agent/${relPath})"
        cp ${pkgs.writeText (baseNameOf relPath) content} $out/home/agent/${relPath}
      '') driver.driverAgentFiles
    )}
  '';

  # A non-root `agent` user (uid/gid 1000). Claude Code refuses
  # --dangerously-skip-permissions under root or sudo, and the Box relies on
  # that flag. The container itself is the isolation boundary, so running as an
  # unprivileged in-container user costs nothing.
  passwdFile = pkgs.writeText "passwd" ''
    root:x:0:0:root:/root:/bin/bash
    agent:x:1000:1000:agent:/home/agent:/bin/bash
  '';
  groupFile = pkgs.writeText "group" ''
    root:x:0:
    agent:x:1000:
  '';

  # Single-sourced so the OCI image and the bwrap runner consume the identical
  # file. Never write it twice.
  nixConfigFile = pkgs.writeText "nix.conf" ''
    experimental-features = nix-command flakes
    sandbox = false
    filter-syscalls = false
    # Fixed, conservative bound -- not host-detected (Nix eval is
    # pure/hermetic, can't shell out to nproc here without IFD) -- so a
    # single Box's nix build never claims every core on hosts with many.
    cores = 4
  '';

  # bwrap-only hardening (issue #2670), unrelated to nix-in-box. Always
  # computed, never gated on knobs.nixInBox.
  syscallFilter = import ./seccomp.nix { inherit pkgs; };

  # Evaluated once so the image's contents, closure registration, and Env marker
  # below all see the identical set of extra derivations.
  extraClosurePaths = packageSet.extraClosures pkgs;

  image = pkgs.dockerTools.buildLayeredImage {
    name = knobs.imageName;
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
      # contents (below) merges agentFiles into this layer as symlinks back
      # into its own store output, so home/agent's leaf files are not really
      # part of this layer at all -- fakeRootCommands' chown/chmod on
      # home/agent only ever reaches the symlinks themselves, never the
      # store bytes the symlinks point at, which stay in agentFiles' own
      # immutable, root-owned closure layer (issue #2843). Replacing the
      # merged symlinks with a real recursive copy makes home/agent's
      # contents genuinely part of this (mutable, still-building) layer, so
      # the chown/chmod below actually land on them.
      rm -rf home/agent
      cp -r ${agentFiles}/home/agent home/agent
    ''
    # Make nix operable in an unprivileged throwaway container: a single-user,
    # sandbox-off nix.conf and a store DB registered from the baked closure, so
    # `nix flake check` reuses the image's store instead of treating it as empty.
    + lib.optionalString knobs.nixInBox ''
      mkdir -p etc/nix nix/var/nix/db nix/var/nix/gcroots nix/var/nix/profiles nix/var/nix/temproots nix/var/log/nix
      cp ${nixConfigFile} etc/nix/nix.conf
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
    # the tree is staged. HOME and the clone dir must be writable by the agent,
    # and nix/var too, so uid 1000 can lock the SQLite store DB and write
    # gcroots/profiles when nix commands run inside the container.
    fakeRootCommands = ''
      chown -R 1000:1000 home/agent work
      # agentFiles cp's its tree in from the read-only Nix store, so the
      # copied files keep the store's read-only mode bits; chown alone
      # changes ownership, not permission, so home/agent also needs an
      # explicit chmod to become writable by its new owner.
      chmod -R u+w home/agent
    ''
    + lib.optionalString knobs.nixInBox ''
      chown -R 1000:1000 nix/var
    ''
    # Non-recursive: only the store directory itself becomes agent-writable,
    # so existing baked paths stay root-owned and immutable (self-test mode,
    # ADR 0018).
    + lib.optionalString knobs.nixStoreWritable ''
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
        "PREFETCH=${knobs.prefetch}"
        "NIX_STORE_WRITABLE=${lib.boolToString knobs.nixStoreWritable}"
        # Lower Claude Code's own output caps so its built-in file spillover
        # engages early (issue #1987). See "Claude Code output caps" in
        # docs/reference.md for the rationale.
        "BASH_MAX_OUTPUT_LENGTH=8192"
        "MAX_MCP_OUTPUT_TOKENS=2000"
      ];
    };
  };
in
{
  inherit
    image
    agentEnv
    agentFiles
    passwdFile
    groupFile
    nixConfigFile
    syscallFilter
    ;
}
