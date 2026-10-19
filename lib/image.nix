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
  # (issues #419, #455, #640), plus the fragment, prompt-contract and
  # forbidden-markers registries baked as JSON for the Go `driver-exec` verbs
  # (issues #2354, #2356, #2464). Also lib/agent-paths.nix's attrset, the
  # single source for the baked /agent/* paths (issue #2531).
  contracts,
  # The prompt tree: the subagent system prompts, the already-injected kind
  # prompts (kindPrompts, one { file; baked } per dispatch kind row prompt),
  # the conditional fragments source dir (issue #463), and the fragment
  # registry's preamble (issue #622).
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
  bashHooks = import ./bash-hooks.nix;
  # Single-sourced so Claude Code's baked output caps are never hand-typed
  # here or in the research-verdict prompt budget that derives from them
  # (issue #3669).
  outputCaps = import ./output-caps.nix;
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
      gettext # envsubst (promptassembly, conflictresolve)
      jq # agent/*.sh hooks (reject-background-bash.sh and others)
      git
      gh
      (driver.driverEntry.package pkgs)
      cacert
      socat # unix-socket<->TCP Forwarder for the registry proxy (ADR 0044, issue #2849)
      driver.driverExecBin # in-box Driver runner (#626)
      driver.orchestratorBin # in-box orchestrator (#1996)
      driver.boxBin # in-box settle sequence (#4292)
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
  # pinned. agent/entrypoint.sh is a pure shim (ADR 0058, issue #4302): these
  # preambles plus one `exec box`. The source stays a complete, standalone
  # script (the bats harness prepends driverPreambleFile before exec-ing it),
  # so its shebang is stripped before it becomes this derivation's body.
  entrypoint = pkgs.writeShellApplication {
    name = "entrypoint";
    # box and the orchestrator it spawns resolve their tools from this PATH.
    runtimeInputs =
      (with pkgs; [
        git
        gh
        (driver.driverEntry.package pkgs)
        gettext # envsubst (promptassembly, conflictresolve)
        coreutils
        jq # agent/*.sh hooks (reject-background-bash.sh and others)
        driver.driverExecBin # in-box Driver runner (#626)
        driver.orchestratorBin # in-box orchestrator (#1996)
        driver.boxBin # in-box settle sequence (#4292)
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
      + knobs.entrypointDefaultsPreamble
      + stripShebang (builtins.readFile ../agent/entrypoint.sh);
  };

  # guardHook: Claude runs the tool on any hook exit but 0 or 2, so an unrunnable
  # hook (126/127) fails open (#5399, #5400). Guard hooks exit only 0 or 2, so
  # any other code blocks. jq is exercised first because a hook without a
  # working jq exits 0 and allows. bash-output-tee.sh failures blocking is
  # deliberate (it unsets credential vars). Inline sh: a wrapper could be missing.
  # A hung hook would run the tool too (Claude's own hook timeout is non-blocking),
  # so timeout bounds it at opencode's 10 s (agent/opencode-guards.js); -k reaps a
  # TERM-ignoring hook (137, refused); a missing timeout exits 127, so it refuses.
  # A hook's own exit 124 also reads as timed out (still refused).
  guardBound = 10;
  guardGrace = 2;
  guardHook =
    script:
    "jq -n true >/dev/null 2>&1 || "
    + "{ echo \"spindrift guard hook ${script} cannot run (no working jq), refusing the call\" >&2; exit 2; }; "
    + "timeout -k ${toString guardGrace} ${toString guardBound} /home/agent/.claude/hooks/${script}; rc=$?; "
    + "case \"$rc\" in 0|2) exit \"$rc\";; 126|127) why=\"cannot run\";; 124) why=\"timed out\";; *) why=failed;; esac; "
    + "echo \"spindrift guard hook ${script} $why (exit $rc), refusing the call\" >&2; exit 2";

  # Rendered with builtins.toJSON rather than a hand-built string so the
  # settings.json is always valid JSON (ADR 0007 tier-1).
  boxSettings = builtins.toJSON {
    # groupBy keeps table order within each event.
    hooks = lib.mapAttrs (
      _:
      lib.concatMap (
        row:
        map (matcher: {
          inherit matcher;
          hooks = [
            (
              {
                type = "command";
                command =
                  if row.event == "PreToolUse" then
                    guardHook row.script
                  else
                    "/home/agent/.claude/hooks/${row.script}";
              }
              // lib.optionalAttrs (row.event == "PreToolUse") {
                # Must stay above guardBound + guardGrace.
                timeout = 3 * guardBound;
              }
            )
          ];
        }) row.matchers
      )
    ) (builtins.groupBy (row: row.event) bashHooks.hooks);
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

  # A configFiles path that collides with an agentFiles path would silently
  # replace the rendered subagent file under `//`.
  driverFileCollisions = lib.attrNames (
    lib.intersectAttrs driver.driverAgentFiles driver.driverConfigFiles
  );
  allDriverFiles =
    assert lib.assertMsg (driverFileCollisions == [ ])
      "Driver ${driver.driverEntry.name}: configFiles paths collide with agentFilesTemplate paths: ${lib.concatStringsSep ", " driverFileCollisions}";
    driver.driverAgentFiles // driver.driverConfigFiles;

  # The /agent/skills and /home/agent slice of agentFiles, written with the
  # given pkgs: agentFiles passes the Linux pkgs, and homeLayoutFor realizes the
  # same tree on the host for the Go seam test's fixture (issue #4296).
  homeLayoutScript = p: ''
    ${lib.optionalString (driver.driverEntry ? sessionCacheDirRelative) ''
      # Pre-create the driver-cache mountpoint so podman reuses the agent-owned
      # directory instead of fabricating root-owned parents (issue #447).
      mkdir -p $out/home/agent/${driver.driverEntry.sessionCacheDirRelative}
    ''}
    mkdir -p $out/home/agent/.claude/hooks
    cp ${import ./agent-hooks.nix p}/* $out/home/agent/.claude/hooks/
    cp ${p.writeText "settings.json" boxSettings} $out/home/agent/.claude/settings.json
    ${lib.optionalString ((harnessSkills ++ agents.skills) != [ ]) ''
      mkdir -p $out/agent/skills
      ${lib.concatMapStrings (
        f:
        # Claude Code discovers a skill only as a directory holding SKILL.md, so
        # each entry is baked under its own <name>/ directory below the fixed
        # /agent/skills, which box copies into the Driver's runtime skills dir at
        # startup. A { name; src; } entry is re-realized with the given pkgs, so
        # no consumer host's system reaches the derivation graph (#597).
        if builtins.isAttrs f && !(lib.isDerivation f) then
          ''
            mkdir -p $out/agent/skills/${lib.escapeShellArg f.name}
            cp ${p.writeText "SKILL.md" f.src} $out/agent/skills/${lib.escapeShellArg f.name}/SKILL.md
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
        cp ${p.writeText (baseNameOf relPath) content} $out/home/agent/${relPath}
      '') allDriverFiles
    )}
    ${lib.concatStrings (
      lib.mapAttrsToList (relPath: inPackage: ''
        target=${driver.driverEntry.package p}/${inPackage}
        [ -e "$target" ] || {
          echo "Driver ${driver.driverEntry.name}: package has no ${inPackage} to link at ${relPath}" >&2
          exit 1
        }
        mkdir -p "$(dirname $out/home/agent/${relPath})"
        ln -s "$target" $out/home/agent/${relPath}
      '') (driver.driverEntry.packageLinks or { })
    )}
  '';

  homeLayoutFor = p: p.runCommand "spindrift-home-layout" { } (homeLayoutScript p);

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
    ${homeLayoutScript pkgs}
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
    ${lib.concatMapStrings (
      e: "cp ${pkgs.writeText e.file e.baked} $out${contracts.agentPaths.PROMPTS_DIR}/${e.file}\n"
    ) prompts.kindPrompts}
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
    cp -r ${prompts.fragmentsSourceDir} $out${contracts.agentPaths.PROMPTS_DIR}/fragments
    cp -r ${prompts.choresSourceDir} $out${contracts.agentPaths.PROMPTS_DIR}/chores
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
    # Two derivations at once lets independent checks overlap; more risks
    # the Box's MEMORY_LIMIT and PIDS_LIMIT (each job gets `cores` threads).
    max-jobs = 2
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
        # Lower Claude Code's own output caps far below their stock defaults
        # (issue #1987): on a dispatch run the cap is simply where a tool
        # result is cut, so it bounds both transcript growth and a relayed
        # marker line's payload. See "Claude Code output caps" in
        # docs/reference.md for the rationale.
        "BASH_MAX_OUTPUT_LENGTH=${toString outputCaps.bashMaxOutputLength}"
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
    homeLayoutFor
    passwdFile
    groupFile
    nixConfigFile
    syscallFilter
    ;
}
