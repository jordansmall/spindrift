# Bats/shell orchestration: shellcheck over the launcher scripts and fakes,
# and the bash layer driven end-to-end through bats against those fakes.
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

  # Registry-driven manifest (issue #2261 slice 2): one entry per Driver in
  # lib/drivers/default.nix's `entries`, pairing that Driver's rendered
  # preamble (the same _driver_extract_outcome/_driver_extract_near_miss_outcome
  # bodies the image bakes in) with its own canonical
  # testdata/outcome-fixture.jsonl transcript. A new Driver registry entry
  # with its own fixture is picked up automatically -- no edits needed here
  # or in tests/driver-registry-outcome-extraction.bats.
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };

  # Shard partition for tests/*.bats (issue #2648 slice 1,
  # nix/checks/bats-shards.nix): shardFiles/shardNames drive the per-shard
  # `bats-shard-N` derivations below, replacing the single catch-all `bats`
  # derivation that ran the whole suite serially. `batsShards` itself is
  # threaded in via `common` (nix/checks/default.nix) rather than imported
  # here, so the directory scan/@test count it runs happens once per eval,
  # not once per consumer.
  inherit (batsShards) shardFiles shardNames;
  driverOutcomeManifest = pkgs.lib.mapAttrs (name: entry: {
    preamble = "${pkgs.writeText "driver-preamble-${name}.sh" (driverRegistry.renderPreamble entry)}";
    fixture = "${(../../cmd/launcher/internal/driver + "/${name}/testdata/outcome-fixture.jsonl")}";
  }) driverRegistry.entries;
  driverOutcomeManifestFile = pkgs.writeText "driver-outcome-manifest.json" (
    builtins.toJSON driverOutcomeManifest
  );

  # Build-time/runtime parity fixtures (issue #2320, parent #2244; widened to
  # every row by issue #2356): lib/prompt-contract.nix's `parityFixtures` --
  # one record per (validateMarkers row) x (gate) x (markerPresent)
  # combination, each pre-resolved to the real `buildTimeRejectVerdicts`
  # verdict for severity=="reject" rows, or "advise" by construction for
  # severity=="warn" rows -- rendered as JSON so tests/prompt-contract-
  # parity.bats can drive the actual runtime validator against every fixture
  # without hand-duplicating the fold logic in bash.
  promptContractParityFixtureFile = pkgs.writeText "prompt-contract-parity-fixtures.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).parityFixtures
  );

  # tests/prompt-assembly-parity.bats (issue #2349) lives under tests/ like
  # every other suite, so whichever bats-shard-N it lands in (issue #2648)
  # picks it up too (mirrors the DRIVER_OUTCOME_MANIFEST/
  # PROMPT_CONTRACT_PARITY_FIXTURE comments above) -- export the same
  # driver-exec binary and nix-rendered lib/fragments.nix JSON the dedicated
  # promptassembly-parity check (nix/checks/promptassembly.nix) exports, or
  # that suite's required-var guard fails here.
  promptassemblyRegistryJsonFile = pkgs.writeText "fragments-registry.json" (
    builtins.toJSON (import ../../lib/fragments.nix)
  );

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).validateMarkers
  );

  # lib/prompt-contract.nix's forbiddenMarkers list, rendered the same way as
  # promptContractRegistryJsonFile above (issue #2464): entrypoint.sh's
  # install_readonly_guards unconditionally passes
  # `--forbidden-markers-registry` to `driver-exec readonly-guards` for any
  # non-BOX_WRITE_ENABLED run (issue #2513: assemble-prompt no longer takes
  # this flag), so every suite exporting PROMPT_CONTRACT_REGISTRY_FILE below
  # needs this sibling var as well.
  forbiddenMarkersRegistryJsonFile = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).forbiddenMarkers
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
  # nativeBuildInputs migrated to the shared batsNativeBuildInputs (issue
  # #2751); the env vars below stay hand-listed rather than building on
  # `batsLightweightEnv // { ... }` -- unlike batsShardChecks (batsEnv) this
  # derivation never runs the suites that need
  # batsLightweightEnv's other vars (SKILLS_*/OPENCODE_*/PROMPT_PATH/
  # PROMPT_HARNESS_DIR/DRIVER_OUTCOME_MANIFEST/PROMPT_CONTRACT_PARITY_FIXTURE/
  # WAIT_FOR_LOG_LINES_TIMEOUT/etc.), so merging batsLightweightEnv wholesale
  # would silently pull those harnesses (skillsHarness, skillsBwrapHarness,
  # opencodeHarness, promptHarness, ...) into this derivation's build closure
  # and change its output path for no behavioral benefit -- exactly the
  # "forces realization" cost batsLightweightEnv/batsImageEnv were split to
  # avoid in the first place.
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
          # entrypoint.sh's phase_prompt_assembly now unconditionally shells
          # out to `driver-exec assemble-prompt` (issue #2354) regardless of
          # which Driver is selected, so every suite that drives $ENTRYPOINT
          # needs the same two vars the main `bats` derivation exports (see
          # its own comment above promptassemblyRegistryJsonFile) -- this
          # derivation's own entrypoint-outcome-*.bats run is no exception.
          DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
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

  # nativeBuildInputs shared by every bats-shaped derivation in this file
  # (batsLightweightEnv/batsImageEnv below, plus outcomeBatsChecks above,
  # which now consumes this binding, and bats-prompt-contract-parity, which
  # still hand-duplicates this list inline pending a later migration slice
  # -- issue #2751).
  batsNativeBuildInputs = [
    pkgs.bats
    pkgs.bash
    pkgs.git
    pkgs.gettext
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.gnused
    pkgs.jq
  ];

  # The default DRIVER_SESSION_RESUMABLE value batsLightweightEnv exports:
  # claude (the only Driver run through the `bats` derivations built on
  # batsLightweightEnv/batsEnv) is resumable, mirroring the bats-outcome-<name>
  # derivations' own per-driver value computed from sessionCacheDirRelative
  # (see the DRIVER_SESSION_RESUMABLE comment further down). Pulled out as its
  # own binding so a future per-Driver consumer of batsLightweightEnv can
  # override it via `batsLightweightEnv // { DRIVER_SESSION_RESUMABLE = ...; }`
  # instead of forking the whole attrset.
  batsDriverSessionResumableDefault = "1";

  # Split (issue #2751) so a future lightweight bats-shaped consumer -- one
  # that never touches $RUN_CMD/$BUILD_CMD/friends or $IMAGE_PATH -- can reuse
  # batsLightweightEnv without paying for the OCI image realization those
  # image-build-forcing vars trigger (same "deliberately excluded from image
  # realization" reasoning as driver-registry-outcome-extraction and
  # bats-prompt-contract-parity above). batsEnv below recombines the two for
  # the existing bats-shard-N consumers, unchanged.
  #
  # batsLightweightEnv: nativeBuildInputs plus every env var that doesn't
  # force the image build -- contract fixtures, driver preambles, skills
  # harness vars, prompt/fragment registries, and the wait_for_log_lines
  # timeout override.
  batsLightweightEnv = {
    nativeBuildInputs = batsNativeBuildInputs;
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
    PROMPT_PATH = batsHarness.internals.promptDir;
    PROMPT_HARNESS_DIR = promptHarness.internals.promptDir;
    # The default-image outcome contract, so the entrypoint-*.bats suites run standalone
    # (no /agent/outcome-contract.md on the bats build host) still exercise
    # the same canonical text an image would bake (issue #420).
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
    # lives under tests/ like every other suite, so whichever bats-shard-N it
    # lands in (issue #2648) picks it up too -- export the same
    # registry-driven manifest the dedicated driver-registry-outcome-extraction
    # check below exports, or that file's required-var guard fails here.
    DRIVER_OUTCOME_MANIFEST = driverOutcomeManifestFile;
    # claude's own resume-session test
    # (entrypoint-outcome-recovery.bats's "the resume pass targets the
    # pinned session via --resume") stays unconditionally green (issue
    # #2261 slices 4-6): claude is resumable, so this mirrors the
    # bats-outcome-<name> derivations' own per-driver
    # DRIVER_SESSION_RESUMABLE computed from sessionCacheDirRelative.
    DRIVER_SESSION_RESUMABLE = batsDriverSessionResumableDefault;
    # Harnesses with baked skills for skills-precedence tests.
    SKILLS_RUN_CMD = "${skillsHarness.internals.run}/bin/run";
    SKILLS_BWRAP_RUN_CMD = "${skillsBwrapHarness.internals.run}/bin/run";
    # Not read by bats directly, but forces Nix to realize
    # skillsBwrapHarness.internals.agentFiles so its store path exists on disk when
    # the bwrap adapter calls os.Stat on the baked-skills subdirectory.
    # The run command embeds the path via unsafeDiscardStringContext, which
    # drops the Nix dependency — without this attr the path is absent.
    SKILLS_AGENT_FILES = skillsBwrapHarness.internals.agentFiles;
    # The opencode Driver's registry-rendered preamble (issue #2262), so
    # the cross-half integration test derives DRIVER_AGENT_FILES_DIR from
    # the same rendered bytes an opencode image bakes in, instead of
    # retyping the relative path -- keeping it in lockstep with whatever
    # agentFilesTemplate below actually bakes into.
    OPENCODE_DRIVER_PREAMBLE_FILE = opencodeHarness.internals.driverPreambleFile;
    # The opencode Driver's REAL baked agent-files template output (issue
    # #2262), so the same test renders through lib/drivers/opencode.nix's
    # agentFilesTemplate instead of write_agent_file's hand-written
    # fixture -- proof the entrypoint's rewrite loop works against actual
    # baked bytes, not just a fixture shaped to look like them.
    OPENCODE_AGENT_FILES = opencodeHarness.internals.agentFiles;
    # tests/prompt-contract-parity.bats lives under tests/ like every
    # other suite, so whichever bats-shard-N it lands in (issue #2648) picks
    # it up too (mirrors the DRIVER_OUTCOME_MANIFEST comment above) -- export
    # the same fixture file the dedicated bats-prompt-contract-parity check
    # below exports, or that suite's required-var guard fails here.
    PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
    # tests/prompt-assembly-parity.bats's required env (see comment above
    # promptassemblyRegistryJsonFile).
    DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
    PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
    # Widens wait_for_log_lines' (tests/helper.bash, issue #2649) default
    # poll patience from 2s for this gate specifically: a serially-run
    # bats suite on a loaded host can outrun the tight local-dev
    # default, and Nix's scrubbed build sandbox is the only place a
    # human can move that number without editing every default-timeout
    # call site -- a shell-level `WAIT_FOR_LOG_LINES_TIMEOUT=N nix
    # flake check` never reaches this sandboxed bash process. A bare
    # `bats tests/` run outside this derivation doesn't fall back to the
    # tighter 2s default either -- it never gets that far, dying in every
    # test's setup() on FAKES_DIR's `: "${FAKES_DIR:?...}"` guard (and the
    # suite's other required env vars this derivation exports), since
    # nothing else supplies them.
    WAIT_FOR_LOG_LINES_TIMEOUT = "10";
  };

  # batsImageEnv: just the vars that force the OCI image build --
  # batsShardChecks' suites overlay `gh` with the fake and exercise `run`/
  # `build` end-to-end against the launcher, so they need the image realized;
  # a future lightweight consumer that skips these vars entirely never
  # triggers that realization.
  batsImageEnv = {
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
    BWRAP_BUILD_CMD = "${bwrapHarness.internals.build}/bin/build";
    IMAGE_PATH = batsHarness.internals.imagePath;
  };

  # batsEnv: the lightweight and heavyweight pieces recombined, kept under the
  # original name so batsShardChecks (the only current consumer) below needs
  # zero changes and keeps building the exact same derivations it always has.
  batsEnv = batsLightweightEnv // batsImageEnv;

  # Shared builder setup for the bats-shard-N derivations: stage a writable copy of
  # tests/, rewrite the fakes' shebangs for the sandboxed build host (see
  # inline comment below), and export FAKES_DIR.
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

  # The bash layers under bats, driven entirely through fakes — no real
  # container, network, or LLM. Sharded (issue #2648) across shardNames'
  # parallel derivations instead of one catch-all `bats tests/` run, each
  # given its own explicit file-list slice of tests/*.bats (batsShards.
  # shardFiles) so Nix can build the shards concurrently; batsEnv/
  # batsBuilderSetup stay shared and unduplicated across shards.
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
          # A shard can legitimately be empty when there are fewer tests/*.bats
          # files than shards (fills-every-shard only requires every shard
          # non-empty once file count >= shardCount); `bats` with no file
          # arguments is a usage error, so skip invoking it rather than pass
          # an empty argument list.
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
  # driver-registry-outcome-extraction above: no batsHarness.internals.run/build/
  # imagePath reference (batsHarness.spindrift is a separate, Consumer-surface
  # key), so this never forces the OCI image build.
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
        OUTCOME_CONTRACT_FILE = batsHarness.internals.outcomeContractFile;
        COMMS_CONTRACT_FILE = batsHarness.internals.commsContractFile;
        CHECK_CONTRACT_FILE = batsHarness.internals.checkContractFile;
        RESEARCH_OUTCOME_CONTRACT_FILE = batsHarness.internals.researchOutcomeContractFile;
        DRIVER_PREAMBLE_FILE = batsHarness.internals.driverPreambleFile;
        AGENT_PATHS_PREAMBLE_FILE = batsHarness.internals.agentPathsPreambleFile;
        FRAGMENT_REGISTRY_FILE = batsHarness.internals.fragmentRegistryFile;
        PROMPT_CONTRACT_PARITY_FIXTURE = promptContractParityFixtureFile;
        # Same reason as outcomeBatsChecks' own copy of these two vars above:
        # $ENTRYPOINT unconditionally calls `driver-exec assemble-prompt`
        # now (issue #2354).
        DRIVER_EXEC_BIN = "${batsHarness.internals.driverExecBin}/bin/driver-exec";
        PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
        PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
        FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
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

  # Eval-time guard derivations from bats-shards.nix (coverage, balance, and
  # no-empty-shard), carried through into sourceChecks (nix/checks/
  # default.nix) rather than left as dangling let-bindings only reachable by
  # hand-importing the module. Each is a `touch $out` placeholder -- its
  # actual content is the `assert` guarding it (bats-shards.nix, scoped to
  # this one attribute's own definition so forcing it doesn't drag in the
  # sibling guards' asserts too); merging them here means every `nix build
  # .#checks`/`.#checks-inbox` run forces this attribute and so forces that
  # assert, not that anything is meaningfully "built".
  "bats-shard-partition-covers-all-suites" = batsShards."bats-shard-partition-covers-all-suites";
  "bats-shard-partition-is-balanced" = batsShards."bats-shard-partition-is-balanced";
  "bats-shard-partition-fills-every-shard" = batsShards."bats-shard-partition-fills-every-shard";
}
// outcomeBatsChecks
// batsShardChecks
