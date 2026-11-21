# The bwrap runner row: data plus the env knobs it owns (issue #5686, ADR 0068).
# The registry (./default.nix) documents the fields and fixes this row's order;
# lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "bwrap";
  goName = "Bwrap";
  runtimes = [ "bwrap" ];
  artifact = "store-closure";
  # The flake package the Harness exposes this family's artifact under by default.
  imageAttrName = "agent-closure";
  # No "no-host-loopback": bwrap has no rendering distinct from "open" (issue #2666).
  networkModes = [
    "open"
    "none"
    "host"
  ];

  # The run-document keys only this family renders (ADR 0069 decision 7); the
  # keys both families share live in lib/preambles.nix.
  runArtifacts =
    {
      # The flake attr of the agent closure, precomputed by lib/preambles.nix.
      # Every row renders it so Probe() can compare it without caring which
      # family produced it (issue #2667).
      flakeImageAttr,
      agentFilesPath,
      agentEnvPath,
      passwdFilePath,
      groupFilePath,
      # `spindrift build` runs against this run document, so the family needs
      # the build-time drv counterparts (issue #2672).
      agentFilesDrv,
      agentEnvDrv,
      passwdFileDrv,
      groupFileDrv,
      agentClosurePath,
      prefetch,
      # The bwrap-only nix.conf artifact (issue #2664), from the nixConfigFile
      # derivation the OCI image bakes in. Defaults to "" so a non-nixInBox
      # Consumer renders the key present-but-empty.
      nixConfigPath ? "",
      # The bwrap ephemeral-overlay-store knob (ADR 0042, issue #2665). No `?`
      # default: it is a Consumer knob independent of nixInBox and always
      # renders its true value; runner/bwrap/bwrap.go ANDs it with
      # NixConfigFile.
      nixStoreWritable,
      # The drv counterpart of nixConfigPath above (issue #2672), defaulting
      # to "" for the same nixInBox-off reason.
      nixConfigDrv ? "",
      # The compiled BPF syscall-filter artifact (issue #2670 slice 3): it
      # always builds, so no `?` default and no on/off knob.
      syscallFilterPath,
      syscallFilterDrv,
      # The static Linux connector-host output dir and its drv: the bwrap
      # adapter binds that one store path into a Connector sandbox.
      connectorHostPath,
      connectorHostDrv,
      ...
    }:
    {
      FLAKE_IMAGE_ATTR = flakeImageAttr;
      AGENT_FILES = agentFilesPath;
      AGENT_ENV = agentEnvPath;
      PASSWD_FILE = passwdFilePath;
      GROUP_FILE = groupFilePath;
      BAKED_PREFETCH = prefetch;
      NIX_CONFIG_FILE = nixConfigPath;
      NIX_STORE_WRITABLE = if nixStoreWritable then "true" else "false";
      SYSCALL_FILTER = syscallFilterPath;
      # Carries the bwrap agent closure's output path in the key the OCI family
      # fills with its image tag, so Probe() can compare it without caring
      # which family produced it (issue #2667).
      IMAGE_TAG = agentClosurePath;
      # cmdBuild reads the run document, so it needs the drv behind each
      # bwrap artifact, as the OCI family carries IMAGE_DRV (issue #2672).
      AGENT_FILES_DRV = agentFilesDrv;
      AGENT_ENV_DRV = agentEnvDrv;
      PASSWD_FILE_DRV = passwdFileDrv;
      GROUP_FILE_DRV = groupFileDrv;
      NIX_CONFIG_FILE_DRV = nixConfigDrv;
      SYSCALL_FILTER_DRV = syscallFilterDrv;
      CONNECTOR_HOST = connectorHostPath;
      CONNECTOR_HOST_DRV = connectorHostDrv;
    };

  knobs = {
    bwrapUnshareNet = {
      env = "BWRAP_UNSHARE_NET";
      group = "infra";
      default = false;
      # `kind` opts this flag into presence parsing (issue #2145): it takes
      # `--bwrap-unshare-net` bare or `--bwrap-unshare-net=<value>`, but never a
      # space-separated value.
      kind = "bool";
      doc = "when non-empty, forces bwrap's network-namespace isolation on (pasta-backed since issue #2666, no longer DNS-breaking); redundant with the new isolate-by-default posture unless paired with NETWORK_MODE=host, which nix eval already rejects -- see network.mode";
      flakeOption = true;
      nixSubPath = "network.bwrapUnshare";
      boxEnv = false;
    };
  };
}
