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
#
# The parameters below are grouped into six attrsets — the package-set
# pair, the driver entry, the resolved agents, the contract files, the
# prompt tree, and the knob subset the image consumes — instead of one
# loose parameter per value; `pkgs` and `lib` stay top-level as module
# plumbing, not Consumer/mkHarness state. mkHarness threads the same
# already-computed values in through the groups below, and the body
# references each value through its group (e.g. `driver.driverEntry`,
# `agents.agentsJsonTemplate`) rather than destructuring the groups back to
# bare names.
{
  pkgs,
  lib,
  # The Consumer's package set: project-specific tools baked into the image
  # on top of the harness plumbing (`packages`), and extra derivations whose
  # closures are baked into the image contents and, when nixInBox is on,
  # registered in the store DB alongside the runtime closure (`extraClosures`,
  # issue #469). Both are functions of the (Linux) pkgs, so a darwin-host
  # Consumer's packages/closures stay correct.
  packageSet,
  # The selected Driver's in-box half (ADR 0009): the driver entry
  # (invocation binary/flags, skill wiring, outcome extraction), the in-box
  # Driver runner (#626), the in-box orchestrator (#1996), the Driver's
  # entrypoint.sh preamble, and its on-disk subagent files (AC4).
  driver,
  # The resolved agent roster: --agents JSON (ADR 0009), the per-agent
  # prompt-file map (issue #264), custom roster entries with their own
  # inline prompt (issue #264), and the skills baked at
  # /home/agent/.claude/skills (issue #597).
  agents,
  # The contract files sliced from issue-prompt.md / research-prompt.md by
  # mkHarness (issues #419, #455, #640) and their injectors, plus the
  # fragment/prompt-contract/forbidden-markers registries baked as JSON for
  # the Go `driver-exec` verbs (issues #2354, #2356, #2464). Also carries
  # lib/agent-paths.nix's attrset (`agentPaths`, the single source of truth
  # for the 8 baked /agent/* path literals driving agentFiles' cp
  # destinations below) and its rendered fallback-defaults preamble
  # (`agentPathsPreamble`, lib/preambles.nix's renderAgentPathsPreamble,
  # issue #2531) — both are contract/registry-file-location data, the same
  # family as the rest of this group.
  contracts,
  # The prompt tree: the Consumer's agent prompt template and the subagent
  # system prompts, the research prompts (ADR 0022, issues #640, #2202), the
  # conditional fragments source dir (issue #463), and the fragment
  # registry's entrypoint.sh preamble (issue #622).
  prompts,
  # The knob subset the image consumes: nix-in-box / writable-store wiring
  # (ADR 0018), the forgejo-backend selector (issue #1963), the prefetch
  # snippet, the Driver-scoped image name (issue #262 AC1), and the
  # schema-derived entrypoint defaults preamble.
  knobs,
}:
let
  # The baked-skill name list (issue #2532), single-sourced for harnessSkills
  # below so a skill's name is never hand-typed a second time here.
  bakedSkills = import ./baked-skills.nix;
  # Whether ISSUE_TRACKER or CODE_FORGE selects the forgejo backend; bakes fj
  # (forgejo-cli) into the image when true, absent otherwise (issue #1963).
  # Defaults to false since a caller may omit it; a derived value, not a
  # plain group field, so it stays a bare local binding.
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
      (driver.driverEntry.package pkgs)
      cacert
      driver.driverExecBin # in-box Driver runner (#626)
      driver.orchestratorBin # in-box orchestrator (#1996)
    ])
    # The nix CLI is included by default so `nix flake check` / `nix develop`
    # work inside the box. Omitted only when the Consumer opts into the lean image.
    ++ lib.optional knobs.nixInBox pkgs.nix
    # fj (forgejo-cli): baked only for a forgejo-backend Consumer (issue
    # #1963), so a github-backend image never gains an unused CLI.
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

  # The in-container entrypoint, via writeShellApplication so shellcheck runs at
  # build time and its tools are pinned. Built for Linux. The source stays a
  # complete, standalone script (the bats harness prepends driverPreambleFile
  # before exec-ing it) so its shebang is stripped before it becomes this
  # derivation's body.
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
      # fj (forgejo-cli): resolved on PATH for the entrypoint's fj credential
      # setup only when the Consumer's backend is forgejo (issue #1963).
      ++ lib.optional forgejoBackend pkgs.forgejo-cli;
    # Prepend the schema-derived defaults block so the entrypoint carries the
    # baked values without hardcoding them in the source script.
    # AGENTS_JSON_TEMPLATE is baked as a fixed value (not a :-default) because it
    # is derived from the configured models, not a standalone knob.
    text =
      "AGENTS_JSON_TEMPLATE="
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
  #
  # Registers the third PreToolUse hook (issue #1927, spec #1907):
  # env-credential-scrub.sh rewrites every Bash call (via
  # hookSpecificOutput.updatedInput) to `unset` ANTHROPIC_API_KEY and
  # CLAUDE_CODE_OAUTH_TOKEN before it runs, and denies outright any Bash call
  # that references a /proc/<pid>/environ path at all. Re-introduces the
  # protection #1926 reverted -- CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1 -- via a
  # Box-controlled rewrite instead of Claude Code's own subprocess-scrub
  # feature, which forces the Driver's permission mode to `default` and
  # wraps every Bash subprocess in a nested bwrap sandbox that can't mount
  # /proc inside the Box's own bwrap sandbox (reproduced directly -- see
  # agent/env-credential-scrub.sh's header). Only a Bash matcher entry, not
  # Read, since the threat is a spawned subprocess's environment and Read
  # never spawns one.
  #
  # Registers the fourth PreToolUse hook and the first PostToolUse hook
  # (issue #1988): bash-output-tee.sh rewrites every Bash call, via the same
  # updatedInput mechanism as env-credential-scrub.sh, to tee its combined
  # stdout+stderr to a per-command log file while the real exit code still
  # propagates; bash-output-summary.sh (PostToolUse, Bash matcher) then
  # replaces the tool result with exit code + log path + a bounded,
  # error-oriented tail once that log file crosses the inline bound, via
  # hookSpecificOutput.updatedToolOutput. Together they subsume the earlier
  # "run-check wrapper"/"truncating post-hook" ideas into one uniform
  # interceptor: full output always lands on disk, only a bounded tail ever
  # enters the model's context, uniformly for every Bash call -- not just
  # the overflow case BASH_MAX_OUTPUT_LENGTH (issue #1987) already covers.
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

  # Baked into the image at /agent — there is no working tree to bind-mount from
  # once spindrift is a store path. The prompt is baked in alongside the
  # entrypoint (not a host-path mount) so the Box is self-contained: a macOS
  # podman machine cannot bind-mount the host /nix/store into its Linux VM.
  # SPINDRIFT_PROMPT_DIR still mounts an override dir for zero-rebuild iteration
  # (the Go launcher mounts it in cmd/launcher/internal/runner).
  # Harness-owned skills (issues #2489, #2490): baked into every image
  # unconditionally, independent of the Consumer's own `skills` list, so
  # a Box always has something at /auto-format and /auto-lint to invoke
  # regardless of consumer skills config. Derived from lib/baked-skills.nix's
  # `harnessOwned` rows (issue #2532) rather than hand-typed, so a skill's
  # name is single-sourced there instead of duplicated here.
  harnessSkills = builtins.map (s: {
    inherit (s) name;
    src = builtins.readFile (../templates/default/skills + "/${s.name}/SKILL.md");
  }) (builtins.filter (s: s.harnessOwned or false) bakedSkills);

  agentFiles = pkgs.runCommand "spindrift-agent-files" { } ''
    # PROMPTS_DIR is currently /agent/prompts, so this is also the only line
    # that creates $out/agent itself -- every OUTCOME_CONTRACT_FILE/
    # COMMS_CONTRACT_FILE/CHECK_CONTRACT_FILE/RESEARCH_OUTCOME_CONTRACT_FILE/
    # PROMPTASSEMBLY_REGISTRY_FILE/PROMPT_CONTRACT_REGISTRY_FILE/
    # FORBIDDEN_MARKERS_REGISTRY_FILE `cp` destination below is a sibling of
    # PROMPTS_DIR under that same $out/agent dir (issue #420) and relies on
    # this mkdir having created it. A future lib/agent-paths.nix rename that
    # moves PROMPTS_DIR out from under /agent would silently break those `cp`
    # calls unless this mkdir (or an explicit one) moves with it.
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
    cp ${pkgs.writeText "conflict-resolve-cherry-pick-prompt.md" prompts.conflictResolveCherryPickPrompt} $out${contracts.agentPaths.PROMPTS_DIR}/conflict-resolve-cherry-pick-prompt.md
    cp ${pkgs.writeText "fix-prompt.md" (contracts.injectFixSharedBlocks prompts.fixPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/fix-prompt.md
    cp ${pkgs.writeText "research-prompt.md" (contracts.injectResearchOutcomeContract prompts.researchPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/research-prompt.md
    cp ${pkgs.writeText "research-self-contained-prompt.md" (contracts.injectResearchOutcomeContract prompts.researchSelfContainedPrompt)} $out${contracts.agentPaths.PROMPTS_DIR}/research-self-contained-prompt.md
    cp -r ${prompts.fragmentsSourceDir} $out${contracts.agentPaths.PROMPTS_DIR}/fragments
    ${lib.optionalString ((harnessSkills ++ agents.skills) != [ ]) ''
      mkdir -p $out/agent/skills
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
        # skill directory copied verbatim under its own basename. Baked to
        # the fixed /agent/skills path (a sibling of /agent/prompts), not
        # under the Driver's declared skills dir, so harnessSkills (always
        # present) and any Consumer-configured skills land together
        # regardless of Driver; agent/entrypoint.sh's phase_prompt_assembly
        # copies from here into the Driver's actual runtime skills dir at
        # box startup.
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
    ''
    # Make nix operable in an unprivileged throwaway container: a single-user,
    # sandbox-off nix.conf and a store DB registered from the baked closure, so
    # `nix flake check` reuses the image's store instead of treating it as empty.
    + lib.optionalString knobs.nixInBox ''
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
        # Lower Claude Code's own output-cap knobs so its built-in
        # file-spillover engages early -- see "Claude Code output caps" in
        # docs/reference.md for the values and rationale (issue #1987).
        "BASH_MAX_OUTPUT_LENGTH=8192"
        "MAX_MCP_OUTPUT_TOKENS=2000"
      ];
    };
  };
in
{
  inherit image agentEnv agentFiles;
}
