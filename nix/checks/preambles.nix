# Eval-level pins for lib/preambles.nix (issue #513): one assertion per
# preamble renderer, on top of the byte-identity equivalence checks in
# equivalence.nix that already cover mkHarness.nix's generated output.
{ pkgs, ... }:
let
  preambles = import ../../lib/preambles.nix;
  inherit (pkgs.lib) assertMsg hasInfix;
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
    assert assertMsg (hasInfix ''MAX_PARALLEL="''${MAX_PARALLEL:-5}"'' out)
      "renderDefaultsPreamble must emit VAR=\"\${VAR:-<baked>}\" per flakeOption entry";
    assert assertMsg (
      !hasInfix "export " out
    ) "renderDefaultsPreamble export=false must not emit `export`";
    assert assertMsg (hasInfix ''export MAX_PARALLEL="''${MAX_PARALLEL:-5}"'' outExported)
      "renderDefaultsPreamble export=true must prefix each line with `export `";
    pkgs.runCommand "preambles-defaults-shape" { } "touch $out";

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

  preambles-box-env-vars-preamble =
    let
      out = preambles.renderBoxEnvVarsPreamble {
        model = {
          env = "MODEL";
          boxEnv = true;
        };
      };
    in
    assert assertMsg (hasInfix ''export BOX_ENV_VARS="MODEL"'' out)
      "renderBoxEnvVarsPreamble must wrap the box-env list in export BOX_ENV_VARS=\"...\", got: ${out}";
    pkgs.runCommand "preambles-box-env-vars-preamble" { } "touch $out";

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

  preambles-run-artifacts-bwrap =
    let
      out = preambles.runArtifacts {
        runnerKind = "bwrap";
        driverEntry = {
          name = "claude";
          skillsDirRelative = ".claude/skills";
        };
        agentFilesPath = "/nix/store/aaa-agent-files";
        agentEnvPath = "/nix/store/bbb-agent-env";
        prefetch = "";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        runtime = "bwrap";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        linuxSystem = "x86_64-linux";
        boxEnvVars = "MODEL BASE_BRANCH";
      };
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
      out ? BAKED_PREFETCH
    ) "runArtifacts (bwrap) must set BAKED_PREFETCH, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.DRIVER_SKILLS_DIR == "/home/agent/.claude/skills"
    ) "runArtifacts (bwrap) must fold in the driver mount targets, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.BOX_ENV_VARS == "MODEL BASE_BRANCH"
    ) "runArtifacts (bwrap) must set BOX_ENV_VARS, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? IMAGE_ARCHIVE)
    ) "runArtifacts (bwrap) must not set OCI-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-bwrap" { } "touch $out";

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
        prefetch = "";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        runtime = "podman";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        linuxSystem = "x86_64-linux";
        boxEnvVars = "MODEL";
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
      out.DRIVER_SKILLS_DIR == "/home/agent/.claude/skills"
    ) "runArtifacts (oci) must fold in the driver mount targets, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? AGENT_FILES)
    ) "runArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-run-artifacts-oci" { } "touch $out";

  preambles-build-artifacts-bwrap =
    let
      out = preambles.buildArtifacts {
        runnerKind = "bwrap";
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        runtime = "bwrap";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        linuxSystem = "x86_64-linux";
      };
    in
    assert assertMsg (
      out.RUNTIME == "bwrap"
    ) "buildArtifacts (bwrap) must set RUNTIME=bwrap, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_FILES_DRV == "/nix/store/aaa-agent-files.drv"
    ) "buildArtifacts (bwrap) must set AGENT_FILES_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      out.AGENT_ENV_DRV == "/nix/store/bbb-agent-env.drv"
    ) "buildArtifacts (bwrap) must set AGENT_ENV_DRV, got: ${builtins.toJSON out}";
    assert assertMsg (
      !(out ? IMAGE_DRV)
    ) "buildArtifacts (bwrap) must not set OCI-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-build-artifacts-bwrap" { } "touch $out";

  preambles-build-artifacts-oci =
    let
      out = preambles.buildArtifacts {
        runnerKind = "oci";
        agentFilesDrv = "/nix/store/aaa-agent-files.drv";
        agentEnvDrv = "/nix/store/bbb-agent-env.drv";
        runtime = "podman";
        imagePath = "/nix/store/ccc-image";
        imageHash = "deadbeef";
        imageDrv = "/nix/store/ddd-image.drv";
        nixBuilderImage = "docker.io/nixos/nix@sha256:aaaa";
        linuxSystem = "x86_64-linux";
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
      !(out ? AGENT_FILES_DRV)
    ) "buildArtifacts (oci) must not set bwrap-only keys, got: ${builtins.toJSON out}";
    pkgs.runCommand "preambles-build-artifacts-oci" { } "touch $out";

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
