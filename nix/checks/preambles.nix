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
    assert assertMsg (!hasInfix "export " out)
      "renderDefaultsPreamble export=false must not emit `export`";
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
    assert assertMsg (out == "AUTO_FORMAT MODEL")
      "renderBoxEnvVarsList must space-join only boxEnv=true entries' env names, got: ${out}";
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

  preambles-go-run-preamble-bwrap =
    let
      out = preambles.renderGoRunPreamble {
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
      };
    in
    assert assertMsg (hasInfix ''export RUNTIME="bwrap"'' out)
      "renderGoRunPreamble (bwrap) must export RUNTIME=bwrap, got: ${out}";
    assert assertMsg (hasInfix ''export DRIVER="claude"'' out)
      "renderGoRunPreamble (bwrap) must export DRIVER, got: ${out}";
    assert assertMsg (hasInfix ''export AGENT_FILES="/nix/store/aaa-agent-files"'' out)
      "renderGoRunPreamble (bwrap) must export AGENT_FILES, got: ${out}";
    assert assertMsg (hasInfix ''export AGENT_ENV="/nix/store/bbb-agent-env"'' out)
      "renderGoRunPreamble (bwrap) must export AGENT_ENV, got: ${out}";
    assert assertMsg (hasInfix "export BAKED_PREFETCH" out)
      "renderGoRunPreamble (bwrap) must export BAKED_PREFETCH, got: ${out}";
    assert assertMsg (hasInfix "export DRIVER_SKILLS_DIR=" out)
      "renderGoRunPreamble (bwrap) must fold in the driver mount preamble, got: ${out}";
    assert assertMsg (!hasInfix "IMAGE_ARCHIVE" out)
      "renderGoRunPreamble (bwrap) must not export OCI-only vars, got: ${out}";
    pkgs.runCommand "preambles-go-run-preamble-bwrap" { } "touch $out";

  preambles-go-run-preamble-oci =
    let
      out = preambles.renderGoRunPreamble {
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
      };
    in
    assert assertMsg (hasInfix ''export IMAGE_ARCHIVE="/nix/store/ccc-image"'' out)
      "renderGoRunPreamble (oci) must export IMAGE_ARCHIVE, got: ${out}";
    assert assertMsg (hasInfix ''export IMAGE_TAG="spindrift:deadbeef"'' out)
      "renderGoRunPreamble (oci) must export IMAGE_TAG, got: ${out}";
    assert assertMsg (hasInfix ''export RUNTIME="podman"'' out)
      "renderGoRunPreamble (oci) must export the configured RUNTIME, got: ${out}";
    assert assertMsg (hasInfix ''export IMAGE_DRV="/nix/store/ddd-image.drv"'' out)
      "renderGoRunPreamble (oci) must export IMAGE_DRV, got: ${out}";
    assert assertMsg (hasInfix ''export NIX_BUILDER_IMAGE="docker.io/nixos/nix@sha256:aaaa"'' out)
      "renderGoRunPreamble (oci) must export NIX_BUILDER_IMAGE, got: ${out}";
    assert assertMsg (hasInfix ''export FLAKE_IMAGE_ATTR=".#packages.x86_64-linux.agent-image"'' out)
      "renderGoRunPreamble (oci) must export FLAKE_IMAGE_ATTR, got: ${out}";
    assert assertMsg (hasInfix "export DRIVER_SKILLS_DIR=" out)
      "renderGoRunPreamble (oci) must fold in the driver mount preamble, got: ${out}";
    assert assertMsg (!hasInfix "AGENT_FILES=" out)
      "renderGoRunPreamble (oci) must not export bwrap-only vars, got: ${out}";
    pkgs.runCommand "preambles-go-run-preamble-oci" { } "touch $out";

  preambles-go-build-preamble-bwrap =
    let
      out = preambles.renderGoBuildPreamble {
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
    assert assertMsg (hasInfix ''export RUNTIME="bwrap"'' out)
      "renderGoBuildPreamble (bwrap) must export RUNTIME=bwrap, got: ${out}";
    assert assertMsg (hasInfix ''export AGENT_FILES_DRV="/nix/store/aaa-agent-files.drv"'' out)
      "renderGoBuildPreamble (bwrap) must export AGENT_FILES_DRV, got: ${out}";
    assert assertMsg (hasInfix ''export AGENT_ENV_DRV="/nix/store/bbb-agent-env.drv"'' out)
      "renderGoBuildPreamble (bwrap) must export AGENT_ENV_DRV, got: ${out}";
    assert assertMsg (!hasInfix "IMAGE_DRV=" out)
      "renderGoBuildPreamble (bwrap) must not export OCI-only vars, got: ${out}";
    pkgs.runCommand "preambles-go-build-preamble-bwrap" { } "touch $out";

  preambles-go-build-preamble-oci =
    let
      out = preambles.renderGoBuildPreamble {
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
    assert assertMsg (hasInfix ''export RUNTIME="podman"'' out)
      "renderGoBuildPreamble (oci) must export the configured RUNTIME, got: ${out}";
    assert assertMsg (hasInfix ''export IMAGE_ARCHIVE="/nix/store/ccc-image"'' out)
      "renderGoBuildPreamble (oci) must export IMAGE_ARCHIVE, got: ${out}";
    assert assertMsg (hasInfix ''export IMAGE_TAG="spindrift:deadbeef"'' out)
      "renderGoBuildPreamble (oci) must export IMAGE_TAG, got: ${out}";
    assert assertMsg (hasInfix ''export IMAGE_DRV="/nix/store/ddd-image.drv"'' out)
      "renderGoBuildPreamble (oci) must export IMAGE_DRV, got: ${out}";
    assert assertMsg (hasInfix ''export FLAKE_IMAGE_ATTR=".#packages.x86_64-linux.agent-image"'' out)
      "renderGoBuildPreamble (oci) must export FLAKE_IMAGE_ATTR, got: ${out}";
    assert assertMsg (!hasInfix "AGENT_FILES_DRV=" out)
      "renderGoBuildPreamble (oci) must not export bwrap-only vars, got: ${out}";
    pkgs.runCommand "preambles-go-build-preamble-oci" { } "touch $out";
}
