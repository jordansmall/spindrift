{
  pkgs,
  fixtures,
  batsShards,
  ...
}:
let
  inherit (fixtures)
    batsHarness
    noRuntimeHarness
    customHarness
    dockerHarness
    bwrapHarness
    promptHarness
    skillsHarness
    skillsBwrapHarness
    opencodeHarness
    ;

  # Issue #2261 slice 2. driverOutcomeManifest below pairs each registered
  # Driver's rendered preamble with its own testdata/outcome-fixture.jsonl, so
  # a new registry entry needs no edit here or in
  # tests/driver-registry-outcome-extraction.bats.
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };

  # Issue #2648 slice 1. `batsShards` is threaded in via `common`
  # (nix/checks/default.nix) rather than imported here, so its directory scan
  # and @test count run once per eval, not once per consumer.
  inherit (batsShards) shardFiles shardNames;
  driverOutcomeManifest = pkgs.lib.mapAttrs (name: entry: {
    preamble = "${pkgs.writeText "driver-preamble-${name}.sh" (driverRegistry.renderPreamble entry)}";
    fixture = "${(../../cmd/launcher/internal/driver + "/${name}/testdata/outcome-fixture.jsonl")}";
  }) driverRegistry.entries;
  driverOutcomeManifestFile = pkgs.writeText "driver-outcome-manifest.json" (
    builtins.toJSON driverOutcomeManifest
  );

  # Issues #2320 and #2356, parent #2244. Rendering lib/prompt-contract.nix's
  # parityFixtures as JSON lets tests/prompt-contract-parity.bats drive the
  # real runtime validator over every row without duplicating the fold logic
  # in bash.
  promptContractParityFixtureFile = pkgs.writeText "prompt-contract-parity-fixtures.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).parityFixtures
  );

  # tests/prompt-assembly-parity.bats (issue #2349) lands in one of the
  # bats-shard-N derivations (issue #2648), so those shards export the same
  # driver-exec binary and rendered lib/fragments.nix JSON that
  # nix/checks/promptassembly.nix does, or the suite's required-var guard
  # fails here.
  promptassemblyRegistryJsonFile = pkgs.writeText "fragments-registry.json" (
    builtins.toJSON (import ../../lib/fragments.nix)
  );

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).validateMarkers
  );

  # Issue #2464. entrypoint.sh's install_readonly_guards passes
  # `--forbidden-markers-registry` to `driver-exec readonly-guards` on every
  # non-BOX_WRITE_ENABLED run, so each suite that exports
  # PROMPT_CONTRACT_REGISTRY_FILE needs this sibling var too.
  forbiddenMarkersRegistryJsonFile = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).forbiddenMarkers
  );

  # Issue #2261 slices 4-6. The unchanged entrypoint-outcome-*.bats suites run
  # against every non-claude Driver's own fake (tests/fakes/<name>) and
  # rendered preamble; claude stays covered by the shard derivations. A new
  # registry entry that ships a fake is picked up without edits here.
  nonClaudeDrivers = pkgs.lib.filterAttrs (name: _: name != "claude") driverRegistry.entries;

  # Issue #2751. The env vars below stay hand-listed instead of building on
  # `batsEnv // { ... }`: merging batsEnv wholesale would pull unrelated
  # harnesses (skillsHarness, opencodeHarness, promptHarness, DOGFOOD_SH and
  # the rest) into this derivation's closure for vars its suites never read.
  outcomeBatsChecks = pkgs.lib.mapAttrs' (
    name: entry:
    pkgs.lib.nameValuePair "bats-outcome-${name}" (
      pkgs.runCommand "bats-outcome-${name}"
        {
          nativeBuildInputs = batsNativeBuildInputs;
          ENTRYPOINT = ../../agent/entrypoint.sh;
          PROMPTS_DIR = ../../templates/default/prompts;
          OUTCOME_CONTRACT_FILE = batsHarness.internals.outcomeContractFile;
          COMMS_CONTRACT_FILE = batsHarness.internals.commsContractFile;
          CHECK_CONTRACT_FILE = batsHarness.internals.checkContractFile;
          RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.internals.researchOutcomeContractFile;
          DRIVER_PREAMBLE_FILE = driverOutcomeManifest.${name}.preamble;
          AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
          FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
          DRIVER = name;
          DRIVER_SESSION_RESUMABLE = pkgs.lib.optionalString (entry ? sessionCacheDirRelative) "1";
          # entrypoint.sh's phase_prompt_assembly unconditionally shells out
          # to `driver-exec assemble-prompt` (issue #2354) whatever the Driver
          # is, so every suite that drives $ENTRYPOINT needs these vars.
          DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
          ORCHESTRATOR_BIN = "${batsHarness.internals.orchestratorBin}/bin/orchestrator";
          PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
          PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
          FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
        }
        ''
          ${batsBuilderSetup}
          bats --print-output-on-failure \
            tests/entrypoint-outcome-contract.bats \
            tests/entrypoint-outcome-recovery.bats \
            tests/entrypoint-outcome-backstop.bats
          touch $out
        ''
    )
  ) nonClaudeDrivers;

  # Issue #2751. Shared by batsEnv, outcomeBatsChecks, and
  # bats-prompt-contract-parity. `driver-registry-outcome-extraction` below
  # hand-lists a narrower set instead, since it needs neither git nor gettext.
  batsNativeBuildInputs = [
    pkgs.bats
    pkgs.bash
    pkgs.git
    pkgs.gettext
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.gnused
    pkgs.jq
    pkgs.socat
  ];

  batsEnv = {
    nativeBuildInputs = batsNativeBuildInputs;
    ENTRYPOINT = ../../agent/entrypoint.sh;
    FORMAT_TRANSCRIPT_SCRIPT = ../../agent/format-transcript.sh;
    # The PreToolUse hook baked into the image at
    # /home/agent/.claude/hooks/reject-background-bash.sh (issue #1609),
    # tested against its own source since it needs nothing from the Box.
    REJECT_BACKGROUND_BASH_SCRIPT = ../../agent/reject-background-bash.sh;
    # Baked at /home/agent/.claude/hooks/credential-deny.sh (issue #1909),
    # same reasoning.
    CREDENTIAL_DENY_HOOK_SCRIPT = ../../agent/credential-deny.sh;
    # Baked at /home/agent/.claude/hooks/env-credential-scrub.sh
    # (issue #1927), same reasoning.
    ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT = ../../agent/env-credential-scrub.sh;
    # Baked at /home/agent/.claude/hooks/bash-output-{tee,summary}.sh
    # (issue #1988), same reasoning.
    BASH_OUTPUT_TEE_SCRIPT = ../../agent/bash-output-tee.sh;
    BASH_OUTPUT_SUMMARY_SCRIPT = ../../agent/bash-output-summary.sh;
    DOGFOOD_SH = ../../dogfood.sh;
    # Issue #2057. tests/ is copied into the sandbox but its parent
    # ab-orchestrator.sh is not, so tests/ab-orchestrator.bats resolves the
    # script through this var rather than $BATS_TEST_DIRNAME/../.
    AB_ORCHESTRATOR_SH = ../../ab-orchestrator.sh;
    PROMPTS_DIR = ../../templates/default/prompts;
    # The baked default prompt dir the `run` command mounts, plus a
    # Consumer-configured one whose rendered content reaches the stubbed
    # agent (#4).
    PROMPT_PATH = batsHarness.internals.promptDir;
    PROMPT_HARNESS_DIR = promptHarness.internals.promptDir;
    # The bats build host has no /agent/outcome-contract.md, so the
    # entrypoint-*.bats suites read the same canonical text an image would
    # bake (issue #420).
    OUTCOME_CONTRACT_FILE = batsHarness.internals.outcomeContractFile;
    # Same reason, for the COMMS and CHECK/COMMIT blocks fix-prompt.md
    # shares with issue-prompt.md (issue #455).
    COMMS_CONTRACT_FILE = batsHarness.internals.commsContractFile;
    CHECK_CONTRACT_FILE = batsHarness.internals.checkContractFile;
    # Same reason, for the research dispatch kind's own outcome contract
    # (issue #640, exported here to close the parity gap from #735).
    RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.internals.researchOutcomeContractFile;
    # The Driver's registry-rendered function definitions; helper.bash
    # prepends this before exec-ing the entrypoint so the bats suite
    # exercises the same bodies the image bakes in (issue #433).
    DRIVER_PREAMBLE_FILE = batsHarness.internals.driverPreambleFile;
    # The 8 baked /agent/* path literals' rendered fallback preamble
    # (issue #2531); helper.bash prepends this between DRIVER_PREAMBLE_FILE
    # and FRAGMENT_REGISTRY_FILE for the same reason, matching lib/image.nix's
    # own concatenation order.
    AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
    # The Conditional fragment registry's rendered loop input and
    # substitution allowlist (issue #622); helper.bash prepends this
    # alongside DRIVER_PREAMBLE_FILE for the same reason.
    FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
    # tests/driver-registry-outcome-extraction.bats (issue #2261 slice 2)
    # lands in one of the bats-shard-N derivations (issue #2648), so the
    # shards export the same manifest the dedicated check below does, or that
    # file's required-var guard fails here.
    DRIVER_OUTCOME_MANIFEST = driverOutcomeManifestFile;
    # claude is resumable, so its resume-session test in
    # entrypoint-outcome-recovery.bats stays green (issue #2261 slices 4-6).
    # The bats-outcome-<name> derivations compute this per Driver from
    # sessionCacheDirRelative instead.
    DRIVER_SESSION_RESUMABLE = "1";
    # The in-repo harness-owned skill bodies (lib/image.nix's harnessSkills
    # reads the same directory). batsBuilderSetup stages only tests/, so a
    # BATS_TEST_DIRNAME-relative path cannot reach them.
    SKILLS_TEMPLATE_DIR = ../../templates/default/skills;
    # The dogfood-only nix-checks skill body (issue #3223) the CHECK
    # section's Nix lore moved into. Separate from SKILLS_TEMPLATE_DIR
    # above because it is not harness-owned: it lives at the repo root and
    # is baked only via nix/dogfood-skills.nix.
    NIX_CHECKS_SKILL = ../../skills/nix-checks/SKILL.md;
    SKILLS_RUN_CMD = "${skillsHarness.internals.run}/bin/run";
    SKILLS_BWRAP_RUN_CMD = "${skillsBwrapHarness.internals.run}/bin/run";
    # Not read by bats, but forces Nix to realize
    # skillsBwrapHarness.internals.agentFiles so its store path exists when
    # the bwrap adapter stats the baked-skills subdirectory. The run command
    # embeds that path via unsafeDiscardStringContext, which drops the Nix
    # dependency, so without this attr the path is absent.
    SKILLS_AGENT_FILES = skillsBwrapHarness.internals.agentFiles;
    # Each mkHarness invocation bakes a distinct agent-closure store path, so
    # skills.bats's stub_nix_var_snapshot (tests/helper.bash) needs THIS
    # harness's generation (bwrap.go closureGeneration, issue #2680), not
    # bwrapHarness's below, to satisfy bwrapAdapter.IsReady's
    # generation-scoped snapshot check.
    SKILLS_BWRAP_IMAGE_TAG = skillsBwrapHarness.internals.agentClosurePath;
    # The opencode Driver's rendered preamble (issue #2262), so the
    # cross-half integration test derives DRIVER_AGENT_FILES_DIR from the same
    # bytes an opencode image bakes rather than retyping the relative path.
    OPENCODE_DRIVER_PREAMBLE_FILE = opencodeHarness.internals.driverPreambleFile;
    # The real baked agent-files template output (issue #2262), so that test
    # renders through lib/drivers/opencode.nix's agentFilesTemplate instead of
    # write_agent_file's hand-written fixture. It proves the entrypoint's
    # rewrite loop works on the baked bytes, not on a lookalike.
    OPENCODE_AGENT_FILES = opencodeHarness.internals.agentFiles;
    # tests/prompt-contract-parity.bats lands in one of the bats-shard-N
    # derivations (issue #2648), so the shards export the same fixture the
    # dedicated check below does, or its required-var guard fails here.
    PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
    # tests/prompt-assembly-parity.bats's required env (see comment above
    # promptassemblyRegistryJsonFile).
    DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
    ORCHESTRATOR_BIN = "${batsHarness.internals.orchestratorBin}/bin/orchestrator";
    PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
    # Widens wait_for_log_lines' (tests/helper.bash) default poll patience
    # from 2s to 10s for this gate (issue #2649); that function's doc comment
    # carries the sandbox-isolation reason.
    WAIT_FOR_LOG_LINES_TIMEOUT = "10";
    # The launcher commands under test overlay `gh` with the fake
    # (batsHarness/customHarness/dockerHarness), since the real `gh`
    # is pinned into their runtimeInputs PATH and would otherwise
    # shadow a PATH-injected fake.
    RUN_CMD = "${batsHarness.internals.run}/bin/run";
    SPINDRIFT_CMD = "${batsHarness.spindrift}/bin/spindrift";
    BUILD_CMD = "${batsHarness.internals.build}/bin/build";
    BUILD_NO_RUNTIME_CMD = "${noRuntimeHarness.internals.build}/bin/build";
    CUSTOM_RUN_CMD = "${customHarness.internals.run}/bin/run";
    DOCKER_RUN_CMD = "${dockerHarness.internals.run}/bin/run";
    BWRAP_RUN_CMD = "${bwrapHarness.internals.run}/bin/run";
    # bwrapHarness's agent-closure store path, the generation name
    # bwrapAdapter.IsReady derives via closureGeneration(IMAGE_TAG)
    # (issue #2680). helper.bash's stub_nix_var_snapshot reads it to stub
    # the snapshot under the same generation the launcher computes, not the
    # flat pre-#2680 path.
    BWRAP_IMAGE_TAG = bwrapHarness.internals.agentClosurePath;
    BWRAP_BUILD_CMD = "${bwrapHarness.internals.build}/bin/build";
    IMAGE_PATH = batsHarness.internals.imagePath;
  };

  # Shared by the bats-shard-N derivations, bats-outcome-<name>, and
  # bats-prompt-contract-parity: stage a writable copy of tests/, rewrite the
  # fakes' shebangs for the sandboxed build host, and export FAKES_DIR.
  # `driver-registry-outcome-extraction` below invokes no fake, so it needs
  # neither the shebang rewrite nor FAKES_DIR, and copies tests/ itself.
  batsBuilderSetup = ''
    export HOME="$TMPDIR/home"
    mkdir -p "$HOME"
    cp -r ${../../tests} tests
    chmod -R +w tests
    # The fakes ship a `#!/usr/bin/env bash` shebang, which the
    # host's launchers exec by path. A sandboxed Linux build has no
    # /usr/bin/env, so rewrite them to the store bash before use.
    for f in tests/fakes/*; do
      substituteInPlace "$f" \
        --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
    done
    export FAKES_DIR="$PWD/tests/fakes"
  '';

  # The bash layers under bats, driven entirely through fakes, with no real
  # container, network, or LLM. Sharded (issue #2648) so Nix builds the slices
  # concurrently, each shard taking an explicit file list from
  # batsShards.shardFiles.
  batsShardChecks = pkgs.lib.listToAttrs (
    pkgs.lib.imap0 (
      idx: name:
      let
        files = shardFiles idx;
      in
      {
        inherit name;
        value = pkgs.runCommand name batsEnv (
          batsBuilderSetup
          # A shard can legitimately be empty when tests/*.bats files are
          # fewer than shards, and `bats` with no file arguments is a usage
          # error, so skip the invocation rather than pass an empty list.
          + (
            if files == [ ] then
              "touch $out"
            else
              ''
                bats --print-output-on-failure ${
                  pkgs.lib.concatMapStringsSep " " (f: pkgs.lib.escapeShellArg "tests/${f}") files
                }
                touch $out
              ''
          )
        );
      }
    ) shardNames
  );
in
{
  shellcheck =
    pkgs.runCommand "shellcheck"
      {
        nativeBuildInputs = [ pkgs.shellcheck ];
      }
      ''
        # The launcher scripts are body fragments (they reference the
        # nix-rendered preamble), so they are shellcheck'd by
        # writeShellApplication at build time, not standalone here.
        shellcheck --shell=bash \
          ${../../dogfood.sh} \
          ${../../agent/entrypoint.sh} \
          ${../../agent/format-transcript.sh} \
          ${../../agent/reject-background-bash.sh} \
          ${../../agent/credential-deny.sh} \
          ${../../agent/env-credential-scrub.sh} \
          ${../../agent/bash-output-tee.sh} \
          ${../../agent/bash-output-summary.sh} \
          ${../../ab-orchestrator.sh} \
          ${../../.github/actions/forgejo-label-swap/label-swap.sh} \
          ${../../tests/fakes/runtime} \
          ${../../tests/fakes/gh} \
          ${../../tests/fakes/claude} \
          ${../../tests/fakes/opencode} \
          ${../../tests/fakes/_driver-common.bash} \
          ${../../tests/fakes/nix} \
          ${../../tests/fakes/driver-exec} \
          ${../../tests/helper.bash} \
          ${../../tests/box_env_gen.bash} \
          ${../../tests/default_models_gen.bash}
        touch $out
      '';

  # Issue #2261 slice 2. Runs every registered Driver's outcome-extraction
  # shell bodies against its own canonical fixture. renderPreamble only reads
  # string-valued attrs, so this needs no image realization and belongs in
  # both `checks` and `checks-inbox` (nix/checks/default.nix).
  "driver-registry-outcome-extraction" =
    pkgs.runCommand "driver-registry-outcome-extraction"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.bash
          pkgs.jq
          pkgs.gnugrep
          pkgs.gnused
          pkgs.coreutils
        ];
        DRIVER_OUTCOME_MANIFEST = driverOutcomeManifestFile;
      }
      ''
        cp -r ${../../tests} tests
        chmod -R +w tests
        bats --print-output-on-failure tests/driver-registry-outcome-extraction.bats
        touch $out
      '';

  # Issue #2320, parent #2244. Drives agent/entrypoint.sh's runtime validator
  # (_validate_prompt_contract) over every parityFixtures row and asserts the
  # exit code matches parityFold(fixture.verdict), the cross-language proof
  # that nix/checks/prompt-contract-parity.nix's pure-Nix fold matches the
  # bash one. Env vars stay hand-listed for outcomeBatsChecks' reason (#2751).
  "bats-prompt-contract-parity" =
    pkgs.runCommand "bats-prompt-contract-parity"
      {
        nativeBuildInputs = batsNativeBuildInputs;
        ENTRYPOINT = ../../agent/entrypoint.sh;
        PROMPTS_DIR = ../../templates/default/prompts;
        OUTCOME_CONTRACT_FILE = batsHarness.internals.outcomeContractFile;
        COMMS_CONTRACT_FILE = batsHarness.internals.commsContractFile;
        CHECK_CONTRACT_FILE = batsHarness.internals.checkContractFile;
        RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.internals.researchOutcomeContractFile;
        DRIVER_PREAMBLE_FILE = batsHarness.internals.driverPreambleFile;
        AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
        FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
        PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
        # Same reason as outcomeBatsChecks' copy of these vars: $ENTRYPOINT
        # unconditionally calls `driver-exec assemble-prompt` (issue #2354).
        DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
        ORCHESTRATOR_BIN = "${batsHarness.internals.orchestratorBin}/bin/orchestrator";
        PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
        PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
        FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
      }
      ''
        ${batsBuilderSetup}
        bats --print-output-on-failure tests/prompt-contract-parity.bats
        touch $out
      '';

  # Eval-time guard derivations from bats-shards.nix, merged into sourceChecks
  # (nix/checks/default.nix) so every `nix build .#checks`/`.#checks-inbox`
  # forces each attribute and with it the `assert` that is its real content.
  # The `touch $out` body builds nothing. Left as let-bindings they would only
  # be reachable by hand-importing the module.
  "bats-shard-partition-covers-all-suites" = batsShards."bats-shard-partition-covers-all-suites";
  "bats-shard-partition-is-balanced" = batsShards."bats-shard-partition-is-balanced";
  "bats-shard-partition-fills-every-shard" = batsShards."bats-shard-partition-fills-every-shard";
  "bats-shard-ceiling-formula-is-safe" = batsShards."bats-shard-ceiling-formula-is-safe";
}
// outcomeBatsChecks
// batsShardChecks
