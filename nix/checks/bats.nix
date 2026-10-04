{
  pkgs,
  fixtures,
  batsShards,
  ...
}:
let
  inherit (fixtures)
    batsHarness
    promptHarness
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

  researchVerdictsParityFile = import ../research-verdicts-parity.nix { inherit pkgs; };

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

  # Issue #2751. Shared by batsEnv and
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
    # Issue #2057. tests/ is copied into the sandbox but its parent
    # ab-orchestrator.sh is not, so tests/ab-orchestrator.bats resolves the
    # script through this var rather than $BATS_TEST_DIRNAME/../.
    AB_ORCHESTRATOR_SH = ../../ab-orchestrator.sh;
    # tests/gh-token-refresher.bats (issue #3940) extracts mint_token
    # straight out of the composite action's `run:` block, so it tracks the
    # real body rather than a hand-copied stand-in.
    GH_TOKEN_REFRESHER_ACTION_YML = ../../.github/actions/gh-token-refresher/action.yml;
    PROMPTS_DIR = ../../templates/default/prompts;
    # Issue #4293. The Driver-invocation goldens live in the Go tree (box's seam
    # test reads the same files), which batsBuilderSetup does not stage.
    DRIVER_INVOCATION_GOLDEN_DIR = ../../cmd/launcher/box/testdata/driver-invocation;
    # A Consumer-configured prompt dir whose rendered content reaches the
    # stubbed agent (#4).
    PROMPT_HARNESS_DIR = promptHarness.internals.promptDir;
    # The rendered contracts, driver/agent-paths preambles and fragment
    # registry the entrypoint-*.bats suites read: the same bytes the Go seam
    # tests read. helper.bash derives the per-file vars from this dir.
    SPINDRIFT_SEAM_FIXTURES_DIR = fixtures.seamFixtures;
    # tests/driver-registry-outcome-extraction.bats (issue #2261 slice 2)
    # lands in one of the bats-shard-N derivations (issue #2648), so the
    # shards export the same manifest the dedicated check below does, or that
    # file's required-var guard fails here.
    DRIVER_OUTCOME_MANIFEST = driverOutcomeManifestFile;
    # The in-repo harness-owned skill bodies (lib/image.nix's harnessSkills
    # reads the same directory). batsBuilderSetup stages only tests/, so a
    # BATS_TEST_DIRNAME-relative path cannot reach them.
    SKILLS_TEMPLATE_DIR = ../../templates/default/skills;
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
    BOX_BIN = "${batsHarness.internals.boxBin}/bin/box";
    PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    RESEARCH_VERDICTS_PARITY_FILE = researchVerdictsParityFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
    # Widens wait_for_log_lines' (tests/helper.bash) default poll patience
    # from 2s to 10s for this gate (issue #2649); that function's doc comment
    # carries the sandbox-isolation reason.
    WAIT_FOR_LOG_LINES_TIMEOUT = "10";
    # The launcher commands under test overlay `gh` with the fake
    # (batsHarness), since the real `gh` is pinned into its runtimeInputs
    # PATH and would otherwise shadow a PATH-injected fake.
    SPINDRIFT_CMD = "${batsHarness.spindrift}/bin/spindrift";
  };

  # Shared by the bats-shard-N derivations and
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
          ${../../agent/entrypoint.sh} \
          ${../../agent/format-transcript.sh} \
          ${../../agent/reject-background-bash.sh} \
          ${../../agent/credential-deny.sh} \
          ${../../agent/env-credential-scrub.sh} \
          ${../../agent/bash-output-tee.sh} \
          ${../../agent/bash-output-summary.sh} \
          ${../../ab-orchestrator.sh} \
          ${../../.github/actions/forgejo-label-swap/label-swap.sh} \
          ${../../.github/actions/forgejo-issue-close/close-issue.sh} \
          ${../../tests/fakes/runtime} \
          ${../../tests/fakes/gh} \
          ${../../tests/fakes/claude} \
          ${../../tests/fakes/opencode} \
          ${../../tests/fakes/_driver-common.bash} \
          ${../../tests/fakes/nix} \
          ${../../tests/fakes/driver-exec} \
          ${../../tests/helper.bash} \
          ${../../tests/box_env_gen.bash}
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
  # bash one. Env vars stay hand-listed instead of building on
  # `batsEnv // { ... }`, which would pull unrelated harnesses
  # (opencodeHarness, promptHarness) into this closure (#2751).
  "bats-prompt-contract-parity" =
    pkgs.runCommand "bats-prompt-contract-parity"
      {
        nativeBuildInputs = batsNativeBuildInputs;
        ENTRYPOINT = ../../agent/entrypoint.sh;
        PROMPTS_DIR = ../../templates/default/prompts;
        SPINDRIFT_SEAM_FIXTURES_DIR = fixtures.seamFixtures;
        PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
        # $ENTRYPOINT unconditionally calls `driver-exec assemble-prompt` (issue #2354).
        DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
        BOX_BIN = "${batsHarness.internals.boxBin}/bin/box";
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
// batsShardChecks
