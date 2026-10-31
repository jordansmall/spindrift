{
  pkgs,
  fixtures,
  batsShards,
  ...
}:
let
  bashHooks = import ../../lib/bash-hooks.nix;
  agentHooks = import ../../lib/agent-hooks.nix pkgs;
  inherit (fixtures) batsHarness multiDriverBatsHarness;

  # Issue #2648 slice 1. `batsShards` is threaded in via `common`
  # (nix/checks/default.nix) rather than imported here, so its directory scan
  # and @test count run once per eval, not once per consumer.
  inherit (batsShards) shardFiles shardNames;

  # The bats-shard-N derivations (issue #2648) export the rendered
  # lib/fragments.nix JSON that entrypoint.sh hands box, the same bytes
  # nix/checks/promptassembly.nix reads.
  promptassemblyRegistryJsonFile = pkgs.writeText "fragments-registry.json" (
    builtins.toJSON (import ../../lib/fragments.nix)
  );

  promptContractRegistryJsonFile = pkgs.writeText "prompt-contract-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).validateMarkers
  );

  # Issue #2464. entrypoint.sh hands it to box's
  # `--forbidden-markers-registry` flag, which installs the read-only guards on
  # every non-BOX_WRITE_ENABLED run, so each suite that exports
  # PROMPT_CONTRACT_REGISTRY_FILE needs this sibling var too.
  forbiddenMarkersRegistryJsonFile = pkgs.writeText "forbidden-markers-registry.json" (
    builtins.toJSON (import ../../lib/prompt-contract.nix).forbiddenMarkers
  );

  # Tools every bats suite shells out to (issue #2751); shared by batsEnv.
  batsNativeBuildInputs = [
    pkgs.bats
    pkgs.bash
    pkgs.git
    pkgs.gettext
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.util-linux
    pkgs.gnused
    pkgs.jq
    # ci-changes.sh's comment-only parsers (issue #5158).
    pkgs.go
    pkgs.nix
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
    # (issue #1927). The scrub and tee hooks source credential-vars-parse from
    # beside themselves (issue #5373), so they run from the baked hooks dir,
    # not a lone store path.
    ENV_CREDENTIAL_SCRUB_HOOK_SCRIPT = "${agentHooks}/env-credential-scrub.sh";
    # Baked at /home/agent/.claude/hooks/bash-output-{tee,summary}.sh
    # (issue #1988); the tee hook runs from the baked dir like the scrub hook.
    BASH_OUTPUT_TEE_SCRIPT = "${agentHooks}/bash-output-tee.sh";
    BASH_OUTPUT_SUMMARY_SCRIPT = ../../agent/bash-output-summary.sh;
    # Issue #2057. tests/ is copied into the sandbox but its parent
    # ab-orchestrator.sh is not, so tests/ab-orchestrator.bats resolves the
    # script through this var rather than $BATS_TEST_DIRNAME/../.
    AB_ORCHESTRATOR_SH = ../../ab-orchestrator.sh;
    # tests/gh-token-refresher.bats (issue #3940) extracts mint_token
    # straight out of the composite action's `run:` block, so it tracks the
    # real body rather than a hand-copied stand-in.
    GH_TOKEN_REFRESHER_ACTION_YML = ../../.github/actions/gh-token-refresher/action.yml;
    # tests/agent-dispatch-blocked-step.bats (issue #5257) runs the Forgejo
    # dispatch workflow's "Detect a blocked marker" step body.
    FORGEJO_AGENT_DISPATCH_YML = ../../.forgejo/workflows/agent-dispatch.yml;
    # CI's change classifier and required-check verdict (issue #5155). The
    # classifier reads its Go helper and reader list beside it (issue #5158).
    CI_CHANGES_SH = "${../../.github/scripts}/ci-changes.sh";
    CI_OK_SH = ../../.github/scripts/ci-ok.sh;
    # Eval-cost table renderer and marginal-cost probe runner (issue #5860),
    # and the eval-profile top-N renderer (issue #5861).
    EVAL_STATS_RENDER_SH = ../../.github/scripts/eval-stats-render.sh;
    EVAL_PROBE_SH = ../../.github/scripts/eval-probe.sh;
    EVAL_PROFILE_TOP_SH = ../../.github/scripts/eval-profile-top.sh;
    # tests/gate.bats (issue #5862) runs the parallel-eval gate script against
    # stubbed nix-eval-jobs/nix.
    GATE_SH = ../../nix/gate.sh;
    PROMPTS_DIR = ../../templates/default/prompts;
    # The rendered contracts, driver/agent-paths preambles, fragment registry
    # and Claude settings.json tests/entrypoint-shim.bats and
    # tests/claude-guard-hooks.bats read: the same bytes the Go seam tests read.
    # helper.bash derives the per-file vars from this dir.
    SPINDRIFT_SEAM_FIXTURES_DIR = fixtures.seamFixtures;
    # The rendered registries entrypoint.sh hands box (see
    # comment above promptassemblyRegistryJsonFile).
    PROMPTASSEMBLY_REGISTRY_FILE = promptassemblyRegistryJsonFile;
    PROMPT_CONTRACT_REGISTRY_FILE = promptContractRegistryJsonFile;
    FORBIDDEN_MARKERS_REGISTRY_FILE = forbiddenMarkersRegistryJsonFile;
    # The launcher commands under test overlay `gh` with the fake
    # (batsHarness), since the real `gh` is pinned into its runtimeInputs
    # PATH and would otherwise shadow a PATH-injected fake.
    SPINDRIFT_CMD = "${batsHarness.spindrift}/bin/spindrift";
    SPINDRIFT_MULTI_DRIVER_CMD = "${multiDriverBatsHarness.spindrift}/bin/spindrift";
  };

  # Shared by the bats-shard-N derivations: stage a writable copy of tests/,
  # rewrite the fakes' shebangs for the sandboxed build host, and export
  # FAKES_DIR.
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
        # -x follows the hooks' `source credential-vars-parse` (issue #5373),
        # which needs the hooks and the helper to share one store directory.
        shellcheck --shell=bash -x \
          ${../../agent/entrypoint.sh} \
          ${../../agent/format-transcript.sh} \
          ${pkgs.lib.concatMapStringsSep " " (s: "${../../agent}/${s}") bashHooks.scripts} \
          ${pkgs.lib.concatMapStringsSep " " (s: "${../../agent}/${s}") bashHooks.helpers} \
          ${../../ab-orchestrator.sh} \
          ${../../.github/actions/forgejo-label-swap/label-swap.sh} \
          ${../../.github/actions/forgejo-issue-close/close-issue.sh} \
          ${../../.github/scripts/ci-changes.sh} \
          ${../../.github/scripts/ci-ok.sh} \
          ${../../.github/scripts/eval-stats-render.sh} \
          ${../../.github/scripts/eval-probe.sh} \
          ${../../nix/gate.sh} \
          ${../../.github/scripts/eval-profile-top.sh} \
          ${../../tests/fakes/runtime} \
          ${../../tests/fakes/gh} \
          ${../../tests/helper.bash}
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
