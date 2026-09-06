# Eval-level pins for lib/preambles.nix (issue #513): one assertion per
# preamble renderer, on top of the byte-identity equivalence checks in
# equivalence.nix that already cover mkHarness.nix's generated output.
{ pkgs, ... }:
let
  preambles = import ../../lib/preambles.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  # Shared by preambles-run-artifacts-bwrap and its
  # -nix-config-omitted sibling below, which differ only in whether
  # nixConfigPath is set (issue #2664).
  bwrapRunArtifactsBase = {
    runnerKind = "bwrap";
    driverEntry = {
      name = "claude";
      skillsDirRelative = ".claude/skills";
    };
    agentFilesPath = "/nix/store/aaa-agent-files";
    agentEnvPath = "/nix/store/bbb-agent-env";
    passwdFilePath = "/nix/store/eee-passwd";
    groupFilePath = "/nix/store/fff-group";
    # Same literal values preambles-build-artifacts-bwrap's own fixture uses
    # for these (below) -- runArtifacts and buildArtifacts render both the
    # run-time paths above and these build-time drvs from the same Consumer
    # config (issue #2672); preambles-run-build-artifacts-bwrap-key-parity
    # below asserts the two outputs actually agree on these six keys.
    agentFilesDrv = "/nix/store/aaa-agent-files.drv";
    agentEnvDrv = "/nix/store/bbb-agent-env.drv";
    passwdFileDrv = "/nix/store/eee-passwd.drv";
    groupFileDrv = "/nix/store/fff-group.drv";
    syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
    agentClosurePath = "/nix/store/ggg-agent-closure";
    prefetch = "";
    imagePath = "/nix/store/ccc-image";
    imageHash = "deadbeef";
    launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
    imageName = "spindrift";
    runtime = "bwrap";
    imageDrv = "/nix/store/ddd-image.drv";
    nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
    systems = {
      host = "aarch64-darwin";
      linux = "x86_64-linux";
    };
    boxEnvVars = "MODEL BASE_BRANCH";
    hostMediatedRemote = false;
    outboxRelayCapable = true;
    inBoxUnreachableTracker = false;
    fullyLocal = false;
    trackerAxisRead = "GITHUB";
    trackerAxisWrite = "GITHUB";
    trackerAxisFiler = "GH";
    forgeBackend = "GH";
    filerEnabled = true;
    workerProvisioned = true;
    scoutProvisioned = true;
    reviewLoopInline = true;
    reviewLoopOrchestrator = false;
    nixStoreWritable = true;
    syscallFilterPath = "/nix/store/fake-syscall-filter-path/filter.bpf";
  };
in
{
  preambles-defaults-shape =
    let
      out = preambles.renderDefaultsPreamble {
        flakeOptionEntries = {
          maxParallel = {
            env = "MAX_PARALLEL";
          };
        };
        mergedDefaults = {
          maxParallel = 5;
        };
      };
      outExported = preambles.renderDefaultsPreamble {
        export = true;
        flakeOptionEntries = {
          maxParallel = {
            env = "MAX_PARALLEL";
          };
        };
        mergedDefaults = {
          maxParallel = 5;
        };
      };
    in
    assert assertMsg (hasInfix "MAX_PARALLEL=\${MAX_PARALLEL:-5}" out)
      "renderDefaultsPreamble must emit VAR=\${VAR:-<baked>} per flakeOption entry";
    assert assertMsg (
      !hasInfix "export " out
    ) "renderDefaultsPreamble export=false must not emit `export`";
    assert assertMsg (hasInfix "export MAX_PARALLEL=\${MAX_PARALLEL:-5}" outExported)
      "renderDefaultsPreamble export=true must prefix each line with `export `";
    pkgs.runCommand "preambles-defaults-shape" { } "touch $out";

  # Issue #2234: renderDefaultsPreamble used to splice a baked flakeOption
  # default raw inside a double-quoted `VAR="${VAR:-<baked>}"` shell
  # assignment, so a default that itself contained double quotes (e.g. a
  # JSON string) broke out of that quoting and tripped shellcheck's SC2140.
  # The renderer now escapes each baked default with escapeShellArg. Pins
  # that the rendered preamble is shellcheck-clean and round-trips the baked
  # default unchanged when the env var is unset — for a value carrying both
  # double quotes and an embedded single quote, exercising escapeShellArg's
  # `'\''` path — while an explicit env override still wins.
  preambles-defaults-quote-containing =
    let
      verdicts = builtins.toJSON [
        {
          verdict = "approve";
          reason = "it's good";
        }
      ];
      out = preambles.renderDefaultsPreamble {
        flakeOptionEntries = {
          researchVerdicts = {
            env = "RESEARCH_VERDICTS";
          };
        };
        mergedDefaults = {
          researchVerdicts = verdicts;
        };
      };
    in
    pkgs.runCommand "preambles-defaults-quote-containing"
      {
        preamble = out;
        expected = verdicts;
        nativeBuildInputs = [ pkgs.shellcheck ];
      }
      ''
        cat >script.sh <<'SCRIPT_HEADER'
        #!/usr/bin/env bash
        set -euo pipefail
        SCRIPT_HEADER
        printf '%s\n' "$preamble" >>script.sh
        cat >>script.sh <<'SCRIPT_TAIL'
        printf '%s' "$RESEARCH_VERDICTS"
        SCRIPT_TAIL

        shellcheck script.sh

        # Round-trip in a command-substitution subshell so the sourced
        # script's `set -euo pipefail` stays contained, not leaked into this
        # builder shell.
        got=$(unset RESEARCH_VERDICTS; source script.sh)
        if [ "$got" != "$expected" ]; then
          echo "round-trip mismatch: got [$got] want [$expected]" >&2
          exit 1
        fi

        RESEARCH_VERDICTS=override bash -c 'source script.sh; test "$RESEARCH_VERDICTS" = override'

        touch $out
      '';

  preambles-box-env-vars-list =
    let
      out = preambles.renderBoxEnvVarsList {
        model = {
          env = "MODEL";
          boxEnv = true;
        };
        maxParallel = {
          env = "MAX_PARALLEL";
          boxEnv = false;
        };
        autoFormat = {
          env = "AUTO_FORMAT";
          boxEnv = true;
        };
      };
    in
    assert assertMsg (
      out == "AUTO_FORMAT MODEL"
    ) "renderBoxEnvVarsList must space-join only boxEnv=true entries' env names, got: ${out}";
    pkgs.runCommand "preambles-box-env-vars-list" { } "touch $out";

  preambles-driver-mount-with-cache =
    let
      out = preambles.renderDriverMountPreamble {
        skillsDirRelative = ".claude/skills";
        sessionCacheDirRelative = ".claude/projects";
      };
    in
    assert assertMsg (hasInfix "export DRIVER_SKILLS_DIR=/home/agent/.claude/skills" out)
      "renderDriverMountPreamble must export DRIVER_SKILLS_DIR under /home/agent, got: ${out}";
    assert assertMsg (hasInfix "export DRIVER_SESSION_CACHE_DIR=/home/agent/.claude/projects" out)
      "renderDriverMountPreamble must export DRIVER_SESSION_CACHE_DIR when the Driver declares one, got: ${out}";
    pkgs.runCommand "preambles-driver-mount-with-cache" { } "touch $out";

  preambles-driver-mount-without-cache =
    let
      out = preambles.renderDriverMountPreamble {
        skillsDirRelative = ".claude/skills";
      };
    in
    assert assertMsg (hasInfix "export DRIVER_SESSION_CACHE_DIR=''" out)
      "renderDriverMountPreamble must export an empty DRIVER_SESSION_CACHE_DIR when the Driver declares none, got: ${out}";
    pkgs.runCommand "preambles-driver-mount-without-cache" { } "touch $out";

  # Pins renderAgentPathsPreamble's shape: one `VAR=${VAR:-<baked>}`
  # fallback-preserving line per attrset entry (issue #2531), mirroring
  # renderDefaultsPreamble's shape above rather than renderDriverMountPreamble's
  # unconditional `export VAR=` lines -- these 8 vars must stay overridable by
  # an already-exported env var.
  preambles-agent-paths-shape =
    let
      out = preambles.renderAgentPathsPreamble {
        PROMPTS_DIR = "/agent/prompts";
        OUTCOME_CONTRACT_FILE = "/agent/outcome-contract.md";
      };
    in
    assert assertMsg (hasInfix "PROMPTS_DIR=\${PROMPTS_DIR:-/agent/prompts}" out)
      "renderAgentPathsPreamble must emit VAR=\${VAR:-<baked-path>} per entry, got: ${out}";
    assert assertMsg
      (hasInfix "OUTCOME_CONTRACT_FILE=\${OUTCOME_CONTRACT_FILE:-/agent/outcome-contract.md}" out)
      "renderAgentPathsPreamble must emit one line per attrset entry, got: ${out}";
    pkgs.runCommand "preambles-agent-paths-shape" { } "touch $out";

  # A path value containing a shell-special character (a space) must
  # round-trip safely through escapeShellArg, the same guard
  # preambles-defaults-quote-containing pins for renderDefaultsPreamble.
  preambles-agent-paths-escapes-value =
    let
      out = preambles.renderAgentPathsPreamble {
        PROMPTS_DIR = "/agent/weird path";
      };
    in
    pkgs.runCommand "preambles-agent-paths-escapes-value"
      {
        preamble = out;
        nativeBuildInputs = [ pkgs.shellcheck ];
      }
      ''
        cat >script.sh <<'SCRIPT_HEADER'
        #!/usr/bin/env bash
        set -euo pipefail
        SCRIPT_HEADER
        printf '%s\n' "$preamble" >>script.sh
        cat >>script.sh <<'SCRIPT_TAIL'
        printf '%s' "$PROMPTS_DIR"
        SCRIPT_TAIL

        shellcheck script.sh

        got=$(unset PROMPTS_DIR; source script.sh)
        if [ "$got" != "/agent/weird path" ]; then
          echo "round-trip mismatch: got [$got] want [/agent/weird path]" >&2
          exit 1
        fi

        touch $out
      '';

  # Proves the real production bindings (lib/agent-paths.nix) render
  # correctly -- lib/image.nix / lib/mkHarness.nix consume the rendered
  # preamble directly (issue #2531, commit 895a1fdc). Builds the
  # expected-lines list generically from the imported attrset instead of
  # hand-typing all 8, so a 9th path added later doesn't silently escape
  # this check's coverage.
  preambles-agent-paths-real-bindings-render =
    let
      inherit (pkgs.lib) mapAttrsToList removeSuffix;
      agentPaths = import ../../lib/agent-paths.nix;
      out = preambles.renderAgentPathsPreamble agentPaths;
      # Each expected line comes from the real renderer itself (called on a
      # single-entry attrset), not a hand-re-derived `VAR=${VAR:-path}`
      # shape -- a hand-typed shape drops renderAgentPathsPreamble's
      # escapeShellArg treatment and can silently pass even when the real
      # renderer's output diverges from it.
      missing = builtins.filter (line: !hasInfix line out) (
        mapAttrsToList (
          var: path: removeSuffix "\n" (preambles.renderAgentPathsPreamble { ${var} = path; })
        ) agentPaths
      );
    in
    assert assertMsg (missing == [ ])
      "renderAgentPathsPreamble (lib/agent-paths.nix) must render every real binding, missing: ${builtins.toJSON missing}, got: ${out}";
    pkgs.runCommand "preambles-agent-paths-real-bindings-render" { } "touch $out";

  preambles-run-artifacts-bwrap =
    let
      out = preambles.runArtifacts (
        bwrapRunArtifactsBase
        // {
          nixConfigPath = "/nix/store/fake-nix-conf-path/nix.conf";
          nixConfigDrv = "/nix/store/fake-nix-conf-path.drv";
        }
      );
    in
    assert assertMsg (
      out.RUNTIME == "bwrap"
    ) "runArtifacts (bwrap) must set RUNTIME=bwrap, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.DRIVER == "claude"
    ) "runArtifacts (bwrap) must set DRIVER, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_FILES == "/nix/store/aaa-agent-files"
    ) "runArtifacts (bwrap) must set AGENT_FILES, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_ENV == "/nix/store/bbb-agent-env"
    ) "runArtifacts (bwrap) must set AGENT_ENV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.PASSWD_FILE == "/nix/store/eee-passwd"
    ) "runArtifacts (bwrap) must set PASSWD_FILE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.GROUP_FILE == "/nix/store/fff-group"
    ) "runArtifacts (bwrap) must set GROUP_FILE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_FILES_DRV == "/nix/store/aaa-agent-files.drv"
    ) "runArtifacts (bwrap) must set AGENT_FILES_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_ENV_DRV == "/nix/store/bbb-agent-env.drv"
    ) "runArtifacts (bwrap) must set AGENT_ENV_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.PASSWD_FILE_DRV == "/nix/store/eee-passwd.drv"
    ) "runArtifacts (bwrap) must set PASSWD_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.GROUP_FILE_DRV == "/nix/store/fff-group.drv"
    ) "runArtifacts (bwrap) must set GROUP_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.SYSCALL_FILTER_DRV == "/nix/store/fake-syscall-filter-path/filter.bpf.drv"
    ) "runArtifacts (bwrap) must set SYSCALL_FILTER_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out ? BAKED_PREFETCH
    ) "runArtifacts (bwrap) must set BAKED_PREFETCH, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.DRIVER_SKILLS_DIR == "/home/agent/.claude/skills"
    ) "runArtifacts (bwrap) must fold in the driver mount targets, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.BOX_ENV_VARS == "MODEL BASE_BRANCH"
    ) "runArtifacts (bwrap) must set BOX_ENV_VARS, got: ${builtins.toJSON out}";
    assert assertMsg (out.HOST_MEDIATED_REMOTE == "false")
      "runArtifacts (bwrap) must render HOST_MEDIATED_REMOTE as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.OUTBOX_RELAY_CAPABLE == "true")
      "runArtifacts (bwrap) must render OUTBOX_RELAY_CAPABLE as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.IN_BOX_UNREACHABLE_TRACKER == "false")
      "runArtifacts (bwrap) must render IN_BOX_UNREACHABLE_TRACKER as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.FULLY_LOCAL == "false")
      "runArtifacts (bwrap) must render FULLY_LOCAL as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_READ == "GITHUB"
    ) "runArtifacts (bwrap) must render TRACKER_AXIS_READ, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_WRITE == "GITHUB"
    ) "runArtifacts (bwrap) must render TRACKER_AXIS_WRITE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_FILER == "GH"
    ) "runArtifacts (bwrap) must render TRACKER_AXIS_FILER, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FORGE_BACKEND == "GH"
    ) "runArtifacts (bwrap) must render FORGE_BACKEND, got: ${builtins.toJSON out}";
    assert assertMsg (out.FILER_ENABLED == "true")
      "runArtifacts (bwrap) must render FILER_ENABLED as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.WORKER_PROVISIONED == "true")
      "runArtifacts (bwrap) must render WORKER_PROVISIONED as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.SCOUT_PROVISIONED == "true")
      "runArtifacts (bwrap) must render SCOUT_PROVISIONED as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_INLINE == "true")
      "runArtifacts (bwrap) must render REVIEW_LOOP_INLINE as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_ORCHESTRATOR == "false")
      "runArtifacts (bwrap) must render REVIEW_LOOP_ORCHESTRATOR as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? IMAGE_ARCHIVE)
    ) "runArtifacts (bwrap) must not set OCI-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_LAUNCHER_ATTR == ".#packages.aarch64-darwin.launcher-currency"
    ) "runArtifacts (bwrap) must set FLAKE_LAUNCHER_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.LAUNCHER_CURRENCY_HASH == "deadbeefdeadbeefdeadbeefdeadbeef"
    ) "runArtifacts (bwrap) must set LAUNCHER_CURRENCY_HASH, got: ${builtins.toJSON out}";
    assert assertMsg (out.FLAKE_IMAGE_ATTR == ".#packages.x86_64-linux.agent-closure")
      "runArtifacts (bwrap) must set FLAKE_IMAGE_ATTR to the agent-closure package, got: ${builtins.toJSON out}";
    assert assertMsg (out.IMAGE_TAG == "/nix/store/ggg-agent-closure")
      "runArtifacts (bwrap) must set IMAGE_TAG to the loaded agent-closure output path, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.NIX_CONFIG_FILE == "/nix/store/fake-nix-conf-path/nix.conf"
    ) "runArtifacts (bwrap) must set NIX_CONFIG_FILE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.NIX_CONFIG_FILE_DRV == "/nix/store/fake-nix-conf-path.drv"
    ) "runArtifacts (bwrap) must set NIX_CONFIG_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (out.NIX_STORE_WRITABLE == "true")
      "runArtifacts (bwrap) must render NIX_STORE_WRITABLE as the literal string \"true\" when nixStoreWritable is true, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.SYSCALL_FILTER == "/nix/store/fake-syscall-filter-path/filter.bpf"
    ) "runArtifacts (bwrap) must set SYSCALL_FILTER, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-bwrap" { } "touch $out";

  # Issue #2665: the sibling knob to nixConfigPath/NIX_CONFIG_FILE -- proves
  # the literal-string rendering flips both ways, not just the "true" case
  # preambles-run-artifacts-bwrap above already pins.
  preambles-run-artifacts-bwrap-store-not-writable =
    let
      out = preambles.runArtifacts (
        bwrapRunArtifactsBase
        // {
          nixConfigPath = "/nix/store/fake-nix-conf-path/nix.conf";
          nixStoreWritable = false;
        }
      );
    in
    assert assertMsg (out.NIX_STORE_WRITABLE == "false")
      "runArtifacts (bwrap) must render NIX_STORE_WRITABLE as the literal string \"false\" when nixStoreWritable is false, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-bwrap-store-not-writable" { } "touch $out";

  # Issue #2664: nixConfigPath is optional (defaults to ""), the shape
  # mkHarness.nix relies on when the Consumer has nixInBox off -- the key
  # must still be present (not absent), just empty, matching the OCI branch's
  # "never even a key" absence being a *different* case (asserted below in
  # preambles-run-artifacts-oci).
  preambles-run-artifacts-bwrap-nix-config-omitted =
    let
      # nixConfigPath deliberately omitted -- pins the nixInBox-off default
      # mkHarness.nix's `if nixInBox then nixConfigFilePath else ""` relies
      # on, which a Go-side test can't see directly.
      out = preambles.runArtifacts bwrapRunArtifactsBase;
    in
    assert assertMsg (out.NIX_CONFIG_FILE == "")
      "runArtifacts (bwrap) must default NIX_CONFIG_FILE to \"\" when nixConfigPath is omitted (the nixInBox-off shape), got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-bwrap-nix-config-omitted" { } "touch $out";

  preambles-run-artifacts-oci =
    let
      out = preambles.runArtifacts {
        runnerKind = "oci";
        driverEntry = {
          name = "claude";
          skillsDirRelative = ".claude/skills";
        };
        agentFilesPath = "/nix/store/aaa-agent-files";
        agentEnvPath = "/nix/store/bbb-agent-env";
        passwdFilePath = "/nix/store/eee-passwd";
        groupFilePath = "/nix/store/fff-group";
        # Required (no default), same reasoning as nixStoreWritable/
        # syscallFilterPath below -- the OCI branch never reads these either
        # (issue #2672).
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        passwdFileDrv = "/nix/store/eee-passwd.drv";
        groupFileDrv = "/nix/store/fff-group.drv";
        agentClosurePath = "/nix/store/ggg-agent-closure";
        prefetch = "";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
        imageName = "spindrift";
        runtime = "podman";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        systems = {
          host = "aarch64-darwin";
          linux = "x86_64-linux";
        };
        boxEnvVars = "MODEL";
        hostMediatedRemote = true;
        outboxRelayCapable = false;
        inBoxUnreachableTracker = true;
        fullyLocal = true;
        trackerAxisRead = "LOCAL";
        trackerAxisWrite = "";
        trackerAxisFiler = "GH";
        forgeBackend = "GH";
        filerEnabled = false;
        workerProvisioned = false;
        scoutProvisioned = false;
        reviewLoopInline = false;
        reviewLoopOrchestrator = true;
        # OCI's own writable-store mechanism (lib/image.nix) is separate and
        # never reads this artifact -- false is a harmless placeholder here,
        # required only because nixStoreWritable has no default (issue #2665).
        nixStoreWritable = false;
        # Required (no default), same reasoning as nixStoreWritable above --
        # the OCI branch never reads it either (issue #2670).
        syscallFilterPath = "/nix/store/fake-syscall-filter-path/filter.bpf";
        syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
      };
    in
    assert assertMsg (
      out.IMAGE_ARCHIVE == "/nix/store/ccc-image"
    ) "runArtifacts (oci) must set IMAGE_ARCHIVE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_TAG == "spindrift:deadbeef"
    ) "runArtifacts (oci) must set IMAGE_TAG, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.RUNTIME == "podman"
    ) "runArtifacts (oci) must set the configured RUNTIME, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_DRV == "/nix/store/ddd-image.drv"
    ) "runArtifacts (oci) must set IMAGE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.NIX_BUILDER_IMAGE == "docker.io/nixos/nix@sha256:aaaa"
    ) "runArtifacts (oci) must set NIX_BUILDER_IMAGE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_IMAGE_ATTR == ".#packages.x86_64-linux.agent-image"
    ) "runArtifacts (oci) must set FLAKE_IMAGE_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_LAUNCHER_ATTR == ".#packages.aarch64-darwin.launcher-currency"
    ) "runArtifacts (oci) must set FLAKE_LAUNCHER_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.LAUNCHER_CURRENCY_HASH == "deadbeefdeadbeefdeadbeefdeadbeef"
    ) "runArtifacts (oci) must set LAUNCHER_CURRENCY_HASH, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.DRIVER_SKILLS_DIR == "/home/agent/.claude/skills"
    ) "runArtifacts (oci) must fold in the driver mount targets, got: ${builtins.toJSON out}";
    assert assertMsg (out.HOST_MEDIATED_REMOTE == "true")
      "runArtifacts (oci) must render HOST_MEDIATED_REMOTE as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.OUTBOX_RELAY_CAPABLE == "false")
      "runArtifacts (oci) must render OUTBOX_RELAY_CAPABLE as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.IN_BOX_UNREACHABLE_TRACKER == "true")
      "runArtifacts (oci) must render IN_BOX_UNREACHABLE_TRACKER as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.FULLY_LOCAL == "true")
      "runArtifacts (oci) must render FULLY_LOCAL as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_READ == "LOCAL"
    ) "runArtifacts (oci) must render TRACKER_AXIS_READ, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_WRITE == ""
    ) "runArtifacts (oci) must render TRACKER_AXIS_WRITE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_FILER == "GH"
    ) "runArtifacts (oci) must render TRACKER_AXIS_FILER, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FORGE_BACKEND == "GH"
    ) "runArtifacts (oci) must render FORGE_BACKEND, got: ${builtins.toJSON out}";
    assert assertMsg (out.FILER_ENABLED == "false")
      "runArtifacts (oci) must render FILER_ENABLED as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.WORKER_PROVISIONED == "false")
      "runArtifacts (oci) must render WORKER_PROVISIONED as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_INLINE == "false")
      "runArtifacts (oci) must render REVIEW_LOOP_INLINE as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_ORCHESTRATOR == "true")
      "runArtifacts (oci) must render REVIEW_LOOP_ORCHESTRATOR as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? AGENT_FILES)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? PASSWD_FILE)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? GROUP_FILE)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? NIX_CONFIG_FILE)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? NIX_STORE_WRITABLE)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? SYSCALL_FILTER)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-oci" { } "touch $out";

  # Issue #262 AC1: a driver-scoped image name flows into IMAGE_TAG, so an
  # opencode Box's run artifacts point the launcher at the spindrift-opencode
  # archive, not the historical spindrift one.
  preambles-run-artifacts-oci-driver-scoped-image-name =
    let
      out = preambles.runArtifacts {
        runnerKind = "oci";
        driverEntry = {
          name = "opencode";
          skillsDirRelative = ".claude/skills";
        };
        agentFilesPath = "/nix/store/aaa-agent-files";
        agentEnvPath = "/nix/store/bbb-agent-env";
        passwdFilePath = "/nix/store/eee-passwd";
        groupFilePath = "/nix/store/fff-group";
        # Required (no default), same reasoning as nixStoreWritable/
        # syscallFilterPath below -- the OCI branch never reads these either
        # (issue #2672).
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        passwdFileDrv = "/nix/store/eee-passwd.drv";
        groupFileDrv = "/nix/store/fff-group.drv";
        agentClosurePath = "/nix/store/ggg-agent-closure";
        prefetch = "";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
        imageName = "spindrift-opencode";
        runtime = "podman";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        systems = {
          host = "aarch64-darwin";
          linux = "x86_64-linux";
        };
        boxEnvVars = "MODEL";
        hostMediatedRemote = false;
        outboxRelayCapable = true;
        inBoxUnreachableTracker = false;
        fullyLocal = false;
        trackerAxisRead = "FORGEJO";
        trackerAxisWrite = "FORGEJO";
        trackerAxisFiler = "FORGEJO";
        forgeBackend = "FORGEJO";
        filerEnabled = true;
        workerProvisioned = false;
        scoutProvisioned = false;
        reviewLoopInline = true;
        reviewLoopOrchestrator = false;
        nixStoreWritable = false;
        syscallFilterPath = "/nix/store/fake-syscall-filter-path/filter.bpf";
        syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
      };
    in
    assert assertMsg (
      out.IMAGE_TAG == "spindrift-opencode:deadbeef"
    ) "runArtifacts (oci) must scope IMAGE_TAG to the driver image name, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_LAUNCHER_ATTR == ".#packages.aarch64-darwin.launcher-currency"
    ) "runArtifacts (oci-driver-scoped) must set FLAKE_LAUNCHER_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.LAUNCHER_CURRENCY_HASH == "deadbeefdeadbeefdeadbeefdeadbeef"
    ) "runArtifacts (oci-driver-scoped) must set LAUNCHER_CURRENCY_HASH, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_READ == "FORGEJO"
    ) "runArtifacts (oci-driver-scoped) must render TRACKER_AXIS_READ, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_WRITE == "FORGEJO"
    ) "runArtifacts (oci-driver-scoped) must render TRACKER_AXIS_WRITE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.TRACKER_AXIS_FILER == "FORGEJO"
    ) "runArtifacts (oci-driver-scoped) must render TRACKER_AXIS_FILER, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FORGE_BACKEND == "FORGEJO"
    ) "runArtifacts (oci-driver-scoped) must render FORGE_BACKEND, got: ${builtins.toJSON out}";
    assert assertMsg (out.FILER_ENABLED == "true")
      "runArtifacts (oci-driver-scoped) must render FILER_ENABLED as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.WORKER_PROVISIONED == "false")
      "runArtifacts (oci-driver-scoped) must render WORKER_PROVISIONED as the literal string \"false\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_INLINE == "true")
      "runArtifacts (oci-driver-scoped) must render REVIEW_LOOP_INLINE as the literal string \"true\", got: ${builtins.toJSON out}";
    assert assertMsg (out.REVIEW_LOOP_ORCHESTRATOR == "false")
      "runArtifacts (oci-driver-scoped) must render REVIEW_LOOP_ORCHESTRATOR as the literal string \"false\", got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-oci-driver-scoped-image-name" { } "touch $out";

  preambles-build-artifacts-bwrap =
    let
      out = preambles.buildArtifacts {
        runnerKind = "bwrap";
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        passwdFileDrv = "/nix/store/eee-passwd.drv";
        groupFileDrv = "/nix/store/fff-group.drv";
        runtime = "bwrap";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
        imageName = "spindrift";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        systems = {
          host = "aarch64-darwin";
          linux = "x86_64-linux";
        };
        nixConfigDrv = "/nix/store/fake-nix-conf-path.drv";
        syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
        agentClosurePath = "/nix/store/ggg-agent-closure";
      };
    in
    assert assertMsg (
      out.RUNTIME == "bwrap"
    ) "buildArtifacts (bwrap) must set RUNTIME=bwrap, got: ${builtins.toJSON out}";
    assert assertMsg (out.IMAGE_TAG == "/nix/store/ggg-agent-closure")
      "buildArtifacts (bwrap) must set IMAGE_TAG to the agent-closure output path (issue #2966), got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_FILES_DRV == "/nix/store/aaa-agent-files.drv"
    ) "buildArtifacts (bwrap) must set AGENT_FILES_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_ENV_DRV == "/nix/store/bbb-agent-env.drv"
    ) "buildArtifacts (bwrap) must set AGENT_ENV_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.PASSWD_FILE_DRV == "/nix/store/eee-passwd.drv"
    ) "buildArtifacts (bwrap) must set PASSWD_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.GROUP_FILE_DRV == "/nix/store/fff-group.drv"
    ) "buildArtifacts (bwrap) must set GROUP_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? IMAGE_DRV)
    ) "buildArtifacts (bwrap) must not set OCI-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_LAUNCHER_ATTR == ".#packages.aarch64-darwin.launcher-currency"
    ) "buildArtifacts (bwrap) must set FLAKE_LAUNCHER_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.LAUNCHER_CURRENCY_HASH == "deadbeefdeadbeefdeadbeefdeadbeef"
    ) "buildArtifacts (bwrap) must set LAUNCHER_CURRENCY_HASH, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.NIX_CONFIG_FILE_DRV == "/nix/store/fake-nix-conf-path.drv"
    ) "buildArtifacts (bwrap) must set NIX_CONFIG_FILE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.SYSCALL_FILTER_DRV == "/nix/store/fake-syscall-filter-path/filter.bpf.drv"
    ) "buildArtifacts (bwrap) must set SYSCALL_FILTER_DRV, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-build-artifacts-bwrap" { } "touch $out";

  # Issue #2672: runArtifacts and buildArtifacts render the six drv keys
  # from the same shared bwrapDrvArtifacts helper (lib/preambles.nix) against
  # the same Consumer config, so the two outputs must agree bit-for-bit --
  # unlike preambles-run-artifacts-bwrap and preambles-build-artifacts-bwrap
  # above, which each only pin their own output against a literal and would
  # both go on passing even if the two renderers silently diverged from each
  # other. IMAGE_TAG joins them (issue #2966): each renderer's own bwrap
  # branch spells it out rather than sharing a helper.
  preambles-run-build-artifacts-bwrap-key-parity =
    let
      runOut = preambles.runArtifacts (
        bwrapRunArtifactsBase
        // {
          nixConfigPath = "/nix/store/fake-nix-conf-path/nix.conf";
          nixConfigDrv = "/nix/store/fake-nix-conf-path.drv";
        }
      );
      buildOut = preambles.buildArtifacts {
        runnerKind = "bwrap";
        agentFilesDrv = bwrapRunArtifactsBase.agentFilesDrv;
        agentEnvDrv = bwrapRunArtifactsBase.agentEnvDrv;
        passwdFileDrv = bwrapRunArtifactsBase.passwdFileDrv;
        groupFileDrv = bwrapRunArtifactsBase.groupFileDrv;
        runtime = "bwrap";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
        imageName = "spindrift";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        systems = {
          host = "aarch64-darwin";
          linux = "x86_64-linux";
        };
        nixConfigDrv = "/nix/store/fake-nix-conf-path.drv";
        syscallFilterDrv = bwrapRunArtifactsBase.syscallFilterDrv;
        agentClosurePath = bwrapRunArtifactsBase.agentClosurePath;
      };
      mismatched = builtins.filter (key: runOut.${key} != buildOut.${key}) [
        "AGENT_FILES_DRV"
        "AGENT_ENV_DRV"
        "PASSWD_FILE_DRV"
        "GROUP_FILE_DRV"
        "NIX_CONFIG_FILE_DRV"
        "SYSCALL_FILTER_DRV"
        "IMAGE_TAG"
      ];
    in
    assert assertMsg (mismatched == [ ])
      "runArtifacts and buildArtifacts (bwrap) must render identical values for the shared bwrap keys when fed the same Consumer config, mismatched keys: ${builtins.toJSON mismatched}, runArtifacts: ${builtins.toJSON runOut}, buildArtifacts: ${builtins.toJSON buildOut}";
    pkgs.runCommand "preambles-run-build-artifacts-bwrap-key-parity" { } "touch $out";

  preambles-build-artifacts-oci =
    let
      out = preambles.buildArtifacts {
        runnerKind = "oci";
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        passwdFileDrv = "/nix/store/eee-passwd.drv";
        groupFileDrv = "/nix/store/fff-group.drv";
        runtime = "podman";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        launcherCurrencyHash = "deadbeefdeadbeefdeadbeefdeadbeef";
        imageName = "spindrift";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        systems = {
          host = "aarch64-darwin";
          linux = "x86_64-linux";
        };
        # Required (no default) -- the OCI branch never reads either
        # (issues #2670, #2966).
        syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
        agentClosurePath = "/nix/store/ggg-agent-closure";
      };
    in
    assert assertMsg (
      out.RUNTIME == "podman"
    ) "buildArtifacts (oci) must set the configured RUNTIME, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_ARCHIVE == "/nix/store/ccc-image"
    ) "buildArtifacts (oci) must set IMAGE_ARCHIVE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_TAG == "spindrift:deadbeef"
    ) "buildArtifacts (oci) must set IMAGE_TAG, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_DRV == "/nix/store/ddd-image.drv"
    ) "buildArtifacts (oci) must set IMAGE_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_IMAGE_ATTR == ".#packages.x86_64-linux.agent-image"
    ) "buildArtifacts (oci) must set FLAKE_IMAGE_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.FLAKE_LAUNCHER_ATTR == ".#packages.aarch64-darwin.launcher-currency"
    ) "buildArtifacts (oci) must set FLAKE_LAUNCHER_ATTR, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.LAUNCHER_CURRENCY_HASH == "deadbeefdeadbeefdeadbeefdeadbeef"
    ) "buildArtifacts (oci) must set LAUNCHER_CURRENCY_HASH, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? AGENT_FILES_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? PASSWD_FILE_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? GROUP_FILE_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? NIX_CONFIG_FILE_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? SYSCALL_FILTER_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-build-artifacts-oci" { } "touch $out";

  # documentArtifactKeys must be derived from what runArtifacts/buildArtifacts
  # actually emit across both runnerKind branches (issue #810), not a
  # hand-maintained list that can silently drift from them. Pins the exact
  # union so a key added/renamed/dropped in either renderer forces a
  # conscious update here instead of passing unnoticed.
  preambles-document-artifact-keys =
    let
      out = preambles.documentArtifactKeys;
      # Hand-maintained, unlike `out`: lib/preambles.nix:220 sorts the real
      # keys with builtins.sort, but this pin does not sort itself. Must stay
      # alphabetical or the assert below fails with a bare "got: [...]"
      # dump that doesn't say why. Insert new keys in sorted position.
      expected = [
        "AGENT_ENV"
        "AGENT_ENV_DRV"
        "AGENT_FILES"
        "AGENT_FILES_DRV"
        "BAKED_PREFETCH"
        "BOX_ENV_VARS"
        "DRIVER"
        "DRIVER_SESSION_CACHE_DIR"
        "DRIVER_SKILLS_DIR"
        "FILER_ENABLED"
        "FLAKE_IMAGE_ATTR"
        "FLAKE_LAUNCHER_ATTR"
        "FORGE_BACKEND"
        "FULLY_LOCAL"
        "GITHUB_OUTPUT"
        "GROUP_FILE"
        "GROUP_FILE_DRV"
        "HOST_MEDIATED_REMOTE"
        "IMAGE"
        "IMAGE_ARCHIVE"
        "IMAGE_DRV"
        "IMAGE_TAG"
        "IN_BOX_UNREACHABLE_TRACKER"
        "LAUNCHER_CURRENCY_HASH"
        "NIX_BUILDER_IMAGE"
        "NIX_CONFIG_FILE"
        "NIX_CONFIG_FILE_DRV"
        "NIX_STORE_WRITABLE"
        "NIX_VOLUME"
        "OUTBOX_RELAY_CAPABLE"
        "PASSWD_FILE"
        "PASSWD_FILE_DRV"
        "REVIEW_LOOP_INLINE"
        "REVIEW_LOOP_ORCHESTRATOR"
        "RUNNER_KIND"
        "RUNTIME"
        "SCOUT_PROVISIONED"
        "SYSCALL_FILTER"
        "SYSCALL_FILTER_DRV"
        "TRACKER_AXIS_FILER"
        "TRACKER_AXIS_READ"
        "TRACKER_AXIS_WRITE"
        "WORKER_PROVISIONED"
      ];
    in
    assert assertMsg (out == expected)
      "documentArtifactKeys must be the sorted union of runArtifacts/buildArtifacts output keys (both runnerKinds) plus the manual IMAGE and GITHUB_OUTPUT escape hatches, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-document-artifact-keys" { } "touch $out";

  # renderInputDocumentJSON must combine settings + artifacts into the
  # top-level {settings, artifacts} JSON object the Go inputDocument struct
  # parses (ADR 0020).
  preambles-render-input-document-json =
    let
      out = preambles.renderInputDocumentJSON {
        settings = {
          BASE_BRANCH = "develop";
        };
        artifacts = {
          RUNTIME = "podman";
        };
      };
      parsed = builtins.fromJSON out;
    in
    assert assertMsg (
      parsed.settings.BASE_BRANCH == "develop"
    ) "renderInputDocumentJSON must nest settings under .settings, got: ${out}";
    assert assertMsg (
      parsed.artifacts.RUNTIME == "podman"
    ) "renderInputDocumentJSON must nest artifacts under .artifacts, got: ${out}";
    pkgs.runCommand "preambles-render-input-document-json" { } "touch $out";
}
