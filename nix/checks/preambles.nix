# Eval-level pins for lib/preambles.nix (issue #513), on top of the
# byte-identity equivalence checks in equivalence.nix that already cover
# mkHarness.nix's generated output.
{ pkgs, ... }:
let
  preambles = import ../../lib/preambles.nix;
  inherit (pkgs.lib) assertMsg hasInfix;

  bwrapRow = import ../../lib/runners/bwrap.nix;
  ociRow = import ../../lib/runners/oci.nix;
  allRows = (import ../../lib/runners/index.nix { }).rows;

  # One check per runner row, so a new family is covered by adding its row
  # file, never by editing this file: runArtifacts must layer the row's own
  # keys under the shared trailer and identify the row.
  runArtifactsRowChecks =
    rows:
    builtins.listToAttrs (
      map (
        row:
        let
          runtime = builtins.head row.runtimes;
          out = preambles.runArtifacts (preambles.dummyRunArtifactArgs // { inherit row runtime; });
          rowKeys = builtins.attrNames (row.runArtifacts preambles.dummyRunArtifactArgs);
          missing = builtins.filter (k: !(out ? ${k})) rowKeys;
          name = "preambles-run-artifacts-row-${row.name}";
        in
        {
          inherit name;
          value =
            assert assertMsg (
              out.RUNNER_KIND == row.name
            ) "runArtifacts (${row.name}) must set RUNNER_KIND to the row name, got: ${builtins.toJSON out}";
            assert assertMsg (out.BOX_ARTIFACT == row.artifact)
              "runArtifacts (${row.name}) must set BOX_ARTIFACT to the row artifact, got: ${builtins.toJSON out}";
            assert assertMsg (out.RUNTIME == runtime)
              "runArtifacts (${row.name}) must set RUNTIME to the configured runtime, got: ${builtins.toJSON out}";
            assert assertMsg (missing == [ ])
              "runArtifacts (${row.name}) dropped the row's own keys ${builtins.toJSON missing}, got: ${builtins.toJSON out}";
            pkgs.runCommand name { } "touch $out";
        }
      ) rows
    );

  stubRow = import ../fixtures/stub-runner-row.nix;

  # Shared by preambles-run-artifacts-bwrap and its -nix-config-omitted
  # sibling below, which differ only in whether nixConfigPath is set
  # (issue #2664).
  bwrapRunArtifactsBase = {
    row = bwrapRow;
    driverEntry = {
      name = "claude";
      skillsDirRelative = ".claude/skills";
    };
    agentFilesPath = "/nix/store/aaa-agent-files";
    agentEnvPath = "/nix/store/bbb-agent-env";
    passwdFilePath = "/nix/store/eee-passwd";
    groupFilePath = "/nix/store/fff-group";
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
    imageAttrName = "agent-closure";
    runtime = builtins.head bwrapRow.runtimes;
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
    choreCatalog = "chore-a chore-b";
    roleModels = ''{"reviewer":"claude-opus-4"}'';
    driverVersion = "1.2.3";
    nixStoreWritable = true;
    syscallFilterPath = "/nix/store/fake-syscall-filter-path/filter.bpf";
  };

  # Shared by preambles-run-artifacts-oci and the OCI case of
  # preambles-run-artifacts-image-attr-name below.
  ociRunArtifactsBase = {
    row = ociRow;
    driverEntry = {
      name = "claude";
      skillsDirRelative = ".claude/skills";
    };
    agentFilesPath = "/nix/store/aaa-agent-files";
    agentEnvPath = "/nix/store/bbb-agent-env";
    passwdFilePath = "/nix/store/eee-passwd";
    groupFilePath = "/nix/store/fff-group";
    # Required (no default) even though the OCI branch never reads them
    # (issue #2672), like nixStoreWritable and syscallFilterPath below.
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
    imageAttrName = "agent-image";
    runtime = builtins.head ociRow.runtimes;
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
    choreCatalog = "chore-a chore-b";
    roleModels = ''{"reviewer":"claude-opus-4"}'';
    driverVersion = "1.2.3";
    # OCI's writable-store mechanism (lib/image.nix) never reads this
    # artifact, so false is a placeholder, required only because
    # nixStoreWritable has no default (issue #2665).
    nixStoreWritable = false;
    # Required (no default) even though the OCI branch never reads it
    # (issue #2670).
    syscallFilterPath = "/nix/store/fake-syscall-filter-path/filter.bpf";
    syscallFilterDrv = "/nix/store/fake-syscall-filter-path/filter.bpf.drv";
  };
in
runArtifactsRowChecks allRows
// {
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

  # Bool knobs reach the Box set-but-empty for explicit off, so the baked
  # default must use the colon-less `${VAR-<baked>}`; `:-` re-defaults the
  # empty to on (issue #4290). A non-bool knob keeps `:-`, so its empty
  # value re-defaults. mergedDefaults carries a real bool, as in
  # lib/mkHarness.nix.
  preambles-defaults-bool-empty-is-off =
    let
      out = preambles.renderDefaultsPreamble {
        flakeOptionEntries = {
          autoFormat = {
            env = "AUTO_FORMAT";
            kind = "bool";
          };
          maxParallel = {
            env = "MAX_PARALLEL";
          };
        };
        mergedDefaults = {
          autoFormat = true;
          maxParallel = 5;
        };
      };
    in
    pkgs.runCommand "preambles-defaults-bool-empty-is-off"
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
        printf '%s' "$AUTO_FORMAT,$MAX_PARALLEL"
        SCRIPT_TAIL

        shellcheck script.sh

        got=$(unset AUTO_FORMAT; source script.sh)
        [ "$got" = 1,5 ] || { echo "unset: got [$got] want [1,5]" >&2; exit 1; }

        got=$(AUTO_FORMAT="" source script.sh)
        [ "$got" = ,5 ] || { echo "set-but-empty: got [$got] want [,5]" >&2; exit 1; }

        got=$(AUTO_FORMAT=1 source script.sh)
        [ "$got" = 1,5 ] || { echo "set: got [$got] want [1,5]" >&2; exit 1; }

        got=$(MAX_PARALLEL="" source script.sh)
        [ "$got" = 1,5 ] || { echo "non-bool set-but-empty: got [$got] want [1,5]" >&2; exit 1; }

        touch $out
      '';

  # Issue #2234: renderDefaultsPreamble used to splice a baked default raw
  # inside a double-quoted `VAR="${VAR:-<baked>}"` assignment, so a default
  # containing double quotes broke out of that quoting and tripped SC2140.
  # The renderer now uses escapeShellArg. The fixture value carries both a
  # double quote and a single quote to exercise the `'\''` path.
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

  # renderAgentPathsPreamble must emit `VAR=${VAR:-<baked>}` lines
  # (issue #2531), not renderDriverMountPreamble's unconditional
  # `export VAR=`: these 8 vars stay overridable by an already-exported
  # env var.
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

  # A path containing a shell-special character must round-trip through
  # escapeShellArg, the guard preambles-defaults-quote-containing pins for
  # renderDefaultsPreamble.
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

  # Covers the real bindings (lib/agent-paths.nix), which lib/image.nix and
  # lib/mkHarness.nix consume directly (issue #2531, commit 895a1fdc). The
  # expected list is built from the imported attrset rather than hand-typed,
  # so a 9th path added later cannot escape this check.
  preambles-agent-paths-real-bindings-render =
    let
      inherit (pkgs.lib) mapAttrsToList removeSuffix;
      agentPaths = import ../../lib/agent-paths.nix;
      out = preambles.renderAgentPathsPreamble agentPaths;
      # Each expected line comes from the renderer itself, not a hand-typed
      # `VAR=${VAR:-path}` shape: a hand-typed shape drops escapeShellArg and
      # can pass even when the renderer's real output diverges from it.
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
      out.RUNTIME == builtins.head bwrapRow.runtimes
    ) "runArtifacts (bwrap) must set RUNTIME to the row runtime, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.BOX_ARTIFACT == "store-closure"
    ) "runArtifacts (bwrap) must set BOX_ARTIFACT=store-closure, got: ${builtins.toJSON out}";
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
    assert assertMsg (out.CHORE_CATALOG == "chore-a chore-b")
      "runArtifacts (bwrap) must render CHORE_CATALOG from its choreCatalog input, got: ${builtins.toJSON out}";
    assert assertMsg
      (out.ROLE_MODELS == ''{"reviewer":"claude-opus-4"}'' && out.DRIVER_VERSION == "1.2.3")
      "runArtifacts (bwrap) must render ROLE_MODELS and DRIVER_VERSION from its inputs, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? IMAGE_ARCHIVE) && !(out ? IMAGE_DRV)
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

  # Issue #2665: the literal-string rendering must flip both ways, not just
  # the "true" case preambles-run-artifacts-bwrap above already pins.
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

  # Issue #2664: nixConfigPath is optional, the shape mkHarness.nix relies on
  # when the Consumer has nixInBox off. The key must still be present, just
  # empty. The OCI branch omitting the key entirely is a different case,
  # asserted in preambles-run-artifacts-oci below.
  preambles-run-artifacts-bwrap-nix-config-omitted =
    let
      # nixConfigPath is omitted on purpose: it pins the nixInBox-off default
      # mkHarness.nix's `if nixInBox then nixConfigFilePath else ""` relies
      # on, which a Go-side test cannot see.
      out = preambles.runArtifacts bwrapRunArtifactsBase;
    in
    assert assertMsg (out.NIX_CONFIG_FILE == "")
      "runArtifacts (bwrap) must default NIX_CONFIG_FILE to \"\" when nixConfigPath is omitted (the nixInBox-off shape), got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-bwrap-nix-config-omitted" { } "touch $out";

  # A non-default imageAttrName must reach FLAKE_IMAGE_ATTR on both runner
  # branches (ADR 0064, issue #5049); the defaults are pinned above.
  preambles-run-artifacts-image-attr-name =
    let
      bwrap = preambles.runArtifacts (bwrapRunArtifactsBase // { imageAttrName = "agent-closure-test"; });
      oci = preambles.runArtifacts (ociRunArtifactsBase // { imageAttrName = "agent-image-test"; });
    in
    assert assertMsg (bwrap.FLAKE_IMAGE_ATTR == ".#packages.x86_64-linux.agent-closure-test")
      "runArtifacts (bwrap) must derive FLAKE_IMAGE_ATTR from imageAttrName, got: ${builtins.toJSON bwrap}";
    assert assertMsg (
      oci.FLAKE_IMAGE_ATTR == ".#packages.x86_64-linux.agent-image-test"
    ) "runArtifacts (oci) must derive FLAKE_IMAGE_ATTR from imageAttrName, got: ${builtins.toJSON oci}";
    pkgs.runCommand "preambles-run-artifacts-image-attr-name" { } "touch $out";

  preambles-run-artifacts-oci =
    let
      out = preambles.runArtifacts ociRunArtifactsBase;
    in
    assert assertMsg (
      out.IMAGE_ARCHIVE == "/nix/store/ccc-image"
    ) "runArtifacts (oci) must set IMAGE_ARCHIVE, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.IMAGE_TAG == "spindrift:deadbeef"
    ) "runArtifacts (oci) must set IMAGE_TAG, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.RUNTIME == builtins.head ociRow.runtimes
    ) "runArtifacts (oci) must set the configured RUNTIME, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.BOX_ARTIFACT == "oci-image"
    ) "runArtifacts (oci) must set BOX_ARTIFACT=oci-image, got: ${builtins.toJSON out}";
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
    assert assertMsg (
      out.NIX_VOLUME == "spindrift-nix"
    ) "runArtifacts (oci) must name the spindrift-nix volume, got: ${builtins.toJSON out}";
    assert
      let
        leaked = builtins.filter (k: out ? ${k}) [
          "AGENT_FILES"
          "PASSWD_FILE"
          "GROUP_FILE"
          "NIX_CONFIG_FILE"
          "NIX_STORE_WRITABLE"
          "SYSCALL_FILTER"
          "AGENT_FILES_DRV"
          "AGENT_ENV_DRV"
          "PASSWD_FILE_DRV"
          "GROUP_FILE_DRV"
          "NIX_CONFIG_FILE_DRV"
          "SYSCALL_FILTER_DRV"
        ];
      in
      assertMsg (leaked == [ ])
        "runArtifacts (oci) must not set bwrap-only keys ${builtins.toJSON leaked}, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-oci" { } "touch $out";

  # Issue #262 AC1: a driver-scoped image name must reach IMAGE_TAG, so an
  # opencode Box's run artifacts point the launcher at the spindrift-opencode
  # archive rather than the plain spindrift one.
  preambles-run-artifacts-oci-driver-scoped-image-name =
    let
      out = preambles.runArtifacts {
        row = ociRow;
        driverEntry = {
          name = "opencode";
          skillsDirRelative = ".claude/skills";
        };
        agentFilesPath = "/nix/store/aaa-agent-files";
        agentEnvPath = "/nix/store/bbb-agent-env";
        passwdFilePath = "/nix/store/eee-passwd";
        groupFilePath = "/nix/store/fff-group";
        # Required (no default) even though the OCI branch never reads them
        # (issue #2672), like nixStoreWritable and syscallFilterPath below.
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
        imageAttrName = "agent-image";
        runtime = builtins.head ociRow.runtimes;
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
        choreCatalog = "chore-a chore-b";
        roleModels = ''{"reviewer":"claude-opus-4"}'';
        driverVersion = "1.2.3";
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
    pkgs.runCommand "preambles-run-artifacts-oci-driver-scoped-image-name" { } "touch $out";

  # documentArtifactKeys must be derived from what runArtifacts emits across
  # every runner row (issue #810), not a hand-maintained list that can drift.
  # Only the keys the shared trailer renders are pinned by hand, so a shared
  # key gained or lost forces a conscious update here; each row's own keys
  # (FLAKE_IMAGE_ATTR included) come from the row.
  preambles-document-artifact-keys =
    let
      out = preambles.documentArtifactKeys;
      sharedKeys = [
        "BOX_ARTIFACT"
        "BOX_ENV_VARS"
        "CHORE_CATALOG"
        "DRIVER"
        "DRIVER_SESSION_CACHE_DIR"
        "DRIVER_SKILLS_DIR"
        "DRIVER_VERSION"
        "FILER_ENABLED"
        "FLAKE_LAUNCHER_ATTR"
        "FORGE_BACKEND"
        "FULLY_LOCAL"
        "GITHUB_OUTPUT"
        "HOST_MEDIATED_REMOTE"
        "IMAGE"
        "IN_BOX_UNREACHABLE_TRACKER"
        "LAUNCHER_CURRENCY_HASH"
        "OUTBOX_RELAY_CAPABLE"
        "ROLE_MODELS"
        "RUNNER_KIND"
        "RUNTIME"
        "SCOUT_PROVISIONED"
        "TRACKER_AXIS_FILER"
        "TRACKER_AXIS_READ"
        "TRACKER_AXIS_WRITE"
        "WORKER_PROVISIONED"
      ];
      rowKeys = builtins.concatMap (
        row: builtins.attrNames (row.runArtifacts preambles.dummyRunArtifactArgs)
      ) allRows;
      expected = pkgs.lib.unique (builtins.sort builtins.lessThan (sharedKeys ++ rowKeys));
    in
    assert assertMsg (out == expected)
      "documentArtifactKeys must be the sorted union of runArtifacts output keys (every runner row) plus the manual IMAGE and GITHUB_OUTPUT escape hatches, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-document-artifact-keys" { } "touch $out";

  # mkHarness hands runArtifacts the union of every row's inputs, so `...` must
  # not swallow a misspelled name: an argument that is neither the shared
  # trailer's nor any row's input fails eval.
  preambles-run-artifacts-rejects-unknown-arg =
    let
      args = preambles.dummyRunArtifactArgs // {
        row = ociRow;
        runtime = "podman";
      };
      bad = builtins.tryEval (
        builtins.deepSeq (preambles.runArtifacts (args // { imageNmae = "typo"; })) true
      );
    in
    assert assertMsg (builtins.isAttrs (
      preambles.runArtifacts args
    )) "runArtifacts must accept every name in dummyRunArtifactArgs";
    assert assertMsg (
      !bad.success
    ) "runArtifacts must throw on an argument no row or the trailer takes";
    pkgs.runCommand "preambles-run-artifacts-rejects-unknown-arg" { } "touch $out";

  # A stub row yields its per-row check with no test written for it; its
  # run-document keys are schema-drift.nix's runners-stub-row-run-artifacts.
  preambles-run-artifacts-stub-row-checks =
    let
      checks = runArtifactsRowChecks [ stubRow ];
    in
    assert assertMsg (checks ? preambles-run-artifacts-row-stub)
      "runArtifactsRowChecks must yield preambles-run-artifacts-row-stub for a stub row, got: ${builtins.toJSON (builtins.attrNames checks)}";
    builtins.seq checks.preambles-run-artifacts-row-stub.drvPath (
      pkgs.runCommand "preambles-run-artifacts-stub-row-checks" { } "touch $out"
    );

  # The {settings, artifacts} nesting is what the Go inputdoc.Document
  # struct (cmd/launcher/internal/inputdoc/inputdoc.go) parses (ADR 0020).
  preambles-render-input-document-json =
    let
      out = preambles.renderInputDocumentJSON {
        settings = {
          BASE_BRANCH = "develop";
        };
        artifacts = {
          RUNTIME = "sample-runtime";
        };
      };
      parsed = builtins.fromJSON out;
    in
    assert assertMsg (
      parsed.settings.BASE_BRANCH == "develop"
    ) "renderInputDocumentJSON must nest settings under .settings, got: ${out}";
    assert assertMsg (
      parsed.artifacts.RUNTIME == "sample-runtime"
    ) "renderInputDocumentJSON must nest artifacts under .artifacts, got: ${out}";
    pkgs.runCommand "preambles-render-input-document-json" { } "touch $out";
}
