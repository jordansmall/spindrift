# Bats/shell orchestration: shellcheck over the launcher scripts and fakes,
# and the bash layer driven end-to-end through bats against those fakes.
{ pkgs, fixtures, ... }:
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

  # Registry-driven manifest (issue #2261 slice 2): one entry per Driver in
  # lib/drivers/default.nix's `entries`, pairing that Driver's rendered
  # preamble (the same _driver_extract_outcome/_driver_extract_near_miss_outcome
  # bodies the image bakes in) with its own canonical
  # testdata/outcome-fixture.jsonl transcript. A new Driver registry entry
  # with its own fixture is picked up automatically -- no edits needed here
  # or in tests/driver-registry-outcome-extraction.bats.
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverOutcomeManifest = pkgs.lib.mapAttrs (name: entry: {
    preamble = "${pkgs.writeText "driver-preamble-${name}.sh" (driverRegistry.renderPreamble entry)}";
    fixture = "${(../../cmd/launcher/internal/driver + "/${name}/testdata/outcome-fixture.jsonl")}";
  }) driverRegistry.entries;
  driverOutcomeManifestFile = pkgs.writeText "driver-outcome-manifest.json" (
    builtins.toJSON driverOutcomeManifest
  );

  # Build-time/runtime parity fixtures (issue #2320, parent #2244):
  # lib/prompt-contract.nix's `parityFixtures` -- one record per (severity==
  # "reject" validateMarkers row) x (gate) x (markerPresent) combination,
  # each pre-resolved to the real `buildTimeRejectVerdicts` verdict --
  # rendered as JSON so tests/prompt-contract-parity.bats can drive the
  # actual runtime validator against every fixture without hand-duplicating
  # the fold logic in bash.
  promptContractParityFixtureFile = pkgs.writeText "prompt-contract-parity-fixtures.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).parityFixtures
  );

  # Registry-driven (issue #2261 slices 4-6): runs the *unchanged*
  # entrypoint-outcome-{contract,recovery,backstop}.bats suites end-to-end
  # against every non-claude registered Driver's own fake binary
  # (tests/fakes/<name>) and rendered preamble (reused from
  # driverOutcomeManifest above -- no separate harness needed, renderPreamble
  # is pure string rendering over the entry's own data). claude keeps running
  # via the `bats` derivation above unchanged; a new Driver registry entry
  # with its own tests/fakes/<name> fake is covered automatically, no edits
  # needed here.
  nonClaudeDrivers = pkgs.lib.filterAttrs (name: _: name != "claude") driverRegistry.entries;
  outcomeBatsChecks = pkgs.lib.mapAttrs' (
    name: entry:
    pkgs.lib.nameValuePair "bats-outcome-${name}" (
      pkgs.runCommand "bats-outcome-${name}"
        {
          nativeBuildInputs = [
            pkgs.bats
            pkgs.bash
            pkgs.git
            pkgs.gettext
            pkgs.coreutils
            pkgs.gnugrep
            pkgs.gnused
            pkgs.jq
          ];
          ENTRYPOINT = ../../agent/entrypoint.sh;
          PROMPTS_DIR = ../../templates/default/prompts;
          OUTCOME_CONTRACT_FILE = batsHarness.outcomeContractFile;
          COMMS_CONTRACT_FILE = batsHarness.commsContractFile;
          CHECK_CONTRACT_FILE = batsHarness.checkContractFile;
          RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.researchOutcomeContractFile;
          DRIVER_PREAMBLE_FILE = driverOutcomeManifest.${name}.preamble;
          FRAGMENT_REGISTRY_FILE = batsHarness.fragmentRegistryFile;
          CONTRACT_REGISTRY_FILE = batsHarness.contractRegistryFile;
          DRIVER = name;
          DRIVER_SESSION_RESUMABLE = pkgs.lib.optionalString (entry ? sessionCacheDirRelative) "1";
        }
        ''
          export HOME="$TMPDIR/home"
          mkdir -p "$HOME"
          cp -r ${../../tests} tests
          chmod -R +w tests
          for f in tests/fakes/*; do
            substituteInPlace "$f" \
              --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
          done
          export FAKES_DIR="$PWD/tests/fakes"
          bats --print-output-on-failure \
            tests/entrypoint-outcome-contract.bats \
            tests/entrypoint-outcome-recovery.bats \
            tests/entrypoint-outcome-backstop.bats
          touch $out
        ''
    )
  ) nonClaudeDrivers;
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
          ${../../tests/box_env_gen.bash}
        touch $out
      '';

  # The bash layers under bats, driven entirely through fakes — no real
  # container, network, or LLM.
  bats =
    pkgs.runCommand "bats"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.bash
          pkgs.git
          pkgs.gettext
          pkgs.coreutils
          pkgs.gnugrep
          pkgs.gnused
          pkgs.jq
        ];
        # The launcher commands under test overlay `gh` with the fake
        # (batsHarness/customHarness/dockerHarness), since the real `gh`
        # is pinned into their runtimeInputs PATH and would otherwise
        # shadow a PATH-injected fake.
        RUN_CMD = "${batsHarness.run}/bin/run";
        SPINDRIFT_CMD = "${batsHarness.spindrift}/bin/spindrift";
        BUILD_CMD = "${batsHarness.build}/bin/build";
        BUILD_NO_RUNTIME_CMD = "${noRuntimeHarness.build}/bin/build";
        CUSTOM_RUN_CMD = "${customHarness.run}/bin/run";
        DOCKER_RUN_CMD = "${dockerHarness.run}/bin/run";
        BWRAP_RUN_CMD = "${bwrapHarness.run}/bin/run";
        BWRAP_BUILD_CMD = "${bwrapHarness.build}/bin/build";
        IMAGE_PATH = batsHarness.imagePath;
        ENTRYPOINT = ../../agent/entrypoint.sh;
        FORMAT_TRANSCRIPT_SCRIPT = ../../agent/format-transcript.sh;
        # The PreToolUse hook script baked into the image at
        # /home/agent/.claude/hooks/reject-background-bash.sh (issue #1609);
        # exercised here directly against its own source, not the baked copy,
        # since it takes no dependency on the Box environment.
        REJECT_BACKGROUND_BASH_SCRIPT = ../../agent/reject-background-bash.sh;
        # The PreToolUse hook script baked into the image at
        # /home/agent/.claude/hooks/credential-deny.sh (issue #1909); same
        # reasoning as REJECT_BACKGROUND_BASH_SCRIPT above.
        CREDENTIAL_DENY_HOOK_SCRIPT = ../../agent/credential-deny.sh;
        # The PreToolUse hook script baked into the image at
        # /home/agent/.claude/hooks/env-credential-scrub.sh (issue #1927);
        # same reasoning as CREDENTIAL_DENY_HOOK_SCRIPT above.
        ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT = ../../agent/env-credential-scrub.sh;
        # The PreToolUse/PostToolUse hook pair baked into the image at
        # /home/agent/.claude/hooks/bash-output-{tee,summary}.sh (issue
        # #1988); same reasoning as ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT above.
        BASH_OUTPUT_TEE_SCRIPT = ../../agent/bash-output-tee.sh;
        BASH_OUTPUT_SUMMARY_SCRIPT = ../../agent/bash-output-summary.sh;
        DOGFOOD_SH = ../../dogfood.sh;
        # The worker/coordinator A/B harness (issue #2057). tests/ is copied
        # into the sandbox but its parent ab-orchestrator.sh is not, so
        # tests/ab-orchestrator.bats resolves the script through this env var
        # rather than $BATS_TEST_DIRNAME/../ab-orchestrator.sh.
        AB_ORCHESTRATOR_SH = ../../ab-orchestrator.sh;
        PROMPTS_DIR = ../../templates/default/prompts;
        # The baked default prompt dir the `run` command mounts, and a
        # Consumer-configured one whose rendered content flows through
        # to the stubbed agent (#4).
        PROMPT_PATH = batsHarness.promptDir;
        PROMPT_HARNESS_DIR = promptHarness.promptDir;
        # The default-image outcome contract, so the entrypoint-*.bats suites run standalone
        # (no /agent/outcome-contract.md on the bats build host) still exercise
        # the same canonical text an image would bake (issue #420).
        OUTCOME_CONTRACT_FILE = batsHarness.outcomeContractFile;
        # Same reason, for the COMMS and CHECK/COMMIT blocks fix-prompt.md
        # shares with issue-prompt.md (issue #455).
        COMMS_CONTRACT_FILE = batsHarness.commsContractFile;
        CHECK_CONTRACT_FILE = batsHarness.checkContractFile;
        # Same reason, for the research dispatch kind's own outcome contract
        # (issue #640, exported here to close the parity gap from #735).
        RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.researchOutcomeContractFile;
        # The Driver's registry-rendered function definitions; helper.bash
        # prepends this before exec-ing the entrypoint so the bats suite
        # exercises the same bodies the image bakes in (issue #433).
        DRIVER_PREAMBLE_FILE = batsHarness.driverPreambleFile;
        # The Conditional fragment registry's rendered loop input and
        # substitution allowlist (issue #622); helper.bash prepends this
        # alongside DRIVER_PREAMBLE_FILE for the same reason.
        FRAGMENT_REGISTRY_FILE = batsHarness.fragmentRegistryFile;
        # The shared prompt block registry's rendered `_INJECT_BLOCK_ROWS`
        # array (issue #2246); helper.bash prepends this the same way, so
        # entrypoint.sh's `_contract_marker` lookup has data to read.
        CONTRACT_REGISTRY_FILE = batsHarness.contractRegistryFile;
        # tests/driver-registry-outcome-extraction.bats (issue #2261 slice 2)
        # lives under tests/ like every other suite, so this catch-all `bats
        # tests/` run picks it up too -- export the same registry-driven
        # manifest the dedicated driver-registry-outcome-extraction check
        # below exports, or that file's required-var guard fails here.
        DRIVER_OUTCOME_MANIFEST = driverOutcomeManifestFile;
        # claude's own resume-session test
        # (entrypoint-outcome-recovery.bats's "the resume pass targets the
        # pinned session via --resume") stays unconditionally green (issue
        # #2261 slices 4-6): claude is resumable, so this mirrors the
        # bats-outcome-<name> derivations' own per-driver
        # DRIVER_SESSION_RESUMABLE computed from sessionCacheDirRelative.
        DRIVER_SESSION_RESUMABLE = "1";
        # Harnesses with baked skills for skills-precedence tests.
        SKILLS_RUN_CMD = "${skillsHarness.run}/bin/run";
        SKILLS_BWRAP_RUN_CMD = "${skillsBwrapHarness.run}/bin/run";
        # Not read by bats directly, but forces Nix to realize
        # skillsBwrapHarness.agentFiles so its store path exists on disk when
        # the bwrap adapter calls os.Stat on the baked-skills subdirectory.
        # The run command embeds the path via unsafeDiscardStringContext, which
        # drops the Nix dependency — without this attr the path is absent.
        SKILLS_AGENT_FILES = skillsBwrapHarness.agentFiles;
        # The opencode Driver's registry-rendered preamble (issue #2262), so
        # the cross-half integration test derives DRIVER_AGENT_FILES_DIR from
        # the same rendered bytes an opencode image bakes in, instead of
        # retyping the relative path -- keeping it in lockstep with whatever
        # agentFilesTemplate below actually bakes into.
        OPENCODE_DRIVER_PREAMBLE_FILE = opencodeHarness.driverPreambleFile;
        # The opencode Driver's REAL baked agent-files template output (issue
        # #2262), so the same test renders through lib/drivers/opencode.nix's
        # agentFilesTemplate instead of write_agent_file's hand-written
        # fixture -- proof the entrypoint's rewrite loop works against actual
        # baked bytes, not just a fixture shaped to look like them.
        OPENCODE_AGENT_FILES = opencodeHarness.agentFiles;
      }
      ''
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
        bats --print-output-on-failure tests/
        touch $out
      '';

  # Registry-driven (issue #2261 slice 2): executes every registered Driver's
  # outcome-extraction shell bodies (_driver_extract_outcome/
  # _driver_extract_near_miss_outcome, lib/drivers/*.nix) against its own
  # canonical fixture (cmd/launcher/internal/driver/*/testdata/outcome-fixture.jsonl).
  # Deliberately excluded from image realization and driver package builds
  # (renderPreamble only touches string-valued attrs of each entry), so this
  # belongs in both `checks` and `checks-inbox` (nix/checks/default.nix).
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

  # Build-time/runtime parity check (issue #2320, parent #2244): drives the
  # actual agent/entrypoint.sh runtime validator (_validate_prompt_contract)
  # against every lib/prompt-contract.nix parityFixtures row and asserts its
  # exit code matches parityFold(fixture.verdict) -- the real cross-language
  # proof that nix/checks/prompt-contract-parity.nix's pure-Nix pinning of
  # the fold (slice 1) actually matches what the runtime bash validator
  # does. Lightweight/non-image-dependent, same shape as
  # driver-registry-outcome-extraction above: no batsHarness.run/build/
  # spindrift/imagePath reference, so this never forces the OCI image build.
  "bats-prompt-contract-parity" =
    pkgs.runCommand "bats-prompt-contract-parity"
      {
        nativeBuildInputs = [
          pkgs.bats
          pkgs.bash
          pkgs.git
          pkgs.gettext
          pkgs.coreutils
          pkgs.gnugrep
          pkgs.gnused
          pkgs.jq
        ];
        ENTRYPOINT = ../../agent/entrypoint.sh;
        PROMPTS_DIR = ../../templates/default/prompts;
        OUTCOME_CONTRACT_FILE = batsHarness.outcomeContractFile;
        COMMS_CONTRACT_FILE = batsHarness.commsContractFile;
        CHECK_CONTRACT_FILE = batsHarness.checkContractFile;
        RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.researchOutcomeContractFile;
        DRIVER_PREAMBLE_FILE = batsHarness.driverPreambleFile;
        FRAGMENT_REGISTRY_FILE = batsHarness.fragmentRegistryFile;
        CONTRACT_REGISTRY_FILE = batsHarness.contractRegistryFile;
        PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
      }
      ''
        export HOME="$TMPDIR/home"
        mkdir -p "$HOME"
        cp -r ${../../tests} tests
        chmod -R +w tests
        for f in tests/fakes/*; do
          substituteInPlace "$f" \
            --replace '#!/usr/bin/env bash' "#!${pkgs.bash}/bin/bash"
        done
        export FAKES_DIR="$PWD/tests/fakes"
        bats --print-output-on-failure tests/prompt-contract-parity.bats
        touch $out
      '';
}
// outcomeBatsChecks
