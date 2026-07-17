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
    ;
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
          ${../../tests/fakes/runtime} \
          ${../../tests/fakes/gh} \
          ${../../tests/fakes/claude} \
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
        DOGFOOD_SH = ../../dogfood.sh;
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
        # Harnesses with baked skills for skills-precedence tests.
        SKILLS_RUN_CMD = "${skillsHarness.run}/bin/run";
        SKILLS_BWRAP_RUN_CMD = "${skillsBwrapHarness.run}/bin/run";
        # Not read by bats directly, but forces Nix to realize
        # skillsBwrapHarness.agentFiles so its store path exists on disk when
        # the bwrap adapter calls os.Stat on the baked-skills subdirectory.
        # The run command embeds the path via unsafeDiscardStringContext, which
        # drops the Nix dependency — without this attr the path is absent.
        SKILLS_AGENT_FILES = skillsBwrapHarness.agentFiles;
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
}
