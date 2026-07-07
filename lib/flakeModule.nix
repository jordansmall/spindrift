# A thin flake-parts shim over lib/mkHarness.nix: exposes every mkHarness knob as
# a `perSystem.spindrift.*` option and wires the image and launcher commands into
# `packages`/`apps` (ADR 0001).
#
# The shim declares no defaults of its own — unset options are simply not
# forwarded, so mkHarness's defaults apply and the outputs stay byte-identical to
# a direct mkHarness call.
{
  lib,
  flake-parts-lib,
  inputs,
  ...
}:
let
  inherit (lib) mkOption types;
  mkHarness = import ./mkHarness.nix;
  schema = import ./env-schema.nix;
  # flakeOption entries are the Consumer-tunable subset that becomes `defaults.*`.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;
  # Generate one mkOption per flakeOption schema entry; type is nullOr str/int so
  # unset options fall through to mkHarness's schema defaults.
  mkDefaultOption =
    _key: entry:
    mkOption {
      type = if builtins.isInt (entry.default or "") then types.nullOr types.int else types.nullOr types.str;
      default = null;
      description = entry.doc;
    };
in
{
  options.perSystem = flake-parts-lib.mkPerSystemOption {
    options.spindrift = {
      nixpkgs = mkOption {
        type = types.raw;
        default = inputs.nixpkgs;
        defaultText = lib.literalExpression "inputs.nixpkgs";
        description = "Locked nixpkgs input the image and host commands build from.";
      };

      overlays = mkOption {
        type = types.nullOr (types.listOf types.raw);
        default = null;
        description = "Overlays applied to the instantiated nixpkgs.";
      };

      config = mkOption {
        type = types.nullOr types.attrs;
        default = null;
        example = {
          allowUnfree = true;
        };
        description = "nixpkgs config attrs.";
      };

      packages = mkOption {
        type = types.nullOr (types.functionTo (types.listOf types.package));
        default = null;
        description = "Project tools baked into the image, as a function of the (Linux) pkgs.";
      };

      prefetch = mkOption {
        type = types.nullOr types.lines;
        default = null;
        description = "Shell snippet the entrypoint runs after cloning to warm caches.";
      };

      prompt = mkOption {
        type = types.nullOr types.lines;
        default = null;
        description = "Agent prompt template baked into the image; changing it requires an image rebuild. Set SPINDRIFT_PROMPT_DIR at runtime to override without a rebuild.";
      };

      skills = mkOption {
        type = types.nullOr (types.listOf types.path);
        default = null;
        description = "Skill files baked into the image at /home/agent/.claude/skills. Each element is a path to a skill file. SPINDRIFT_SKILLS_DIR at runtime mounts over the same path and takes precedence.";
      };

      # One sub-option per schema flakeOption entry — generated so adding a knob
      # to env-schema.nix propagates here automatically.
      defaults = mkOption {
        type = types.submodule {
          options = lib.mapAttrs mkDefaultOption flakeOptionEntries;
        };
        default = { };
        description = "Non-secret run defaults baked into the generated `run` command.";
      };

      runtime = mkOption {
        type = types.nullOr (types.enum [
          "podman"
          "docker"
          "bwrap"
        ]);
        default = null;
        description = "Runner the launcher commands drive: OCI runtimes (podman/docker) or the daemonless bubblewrap runner (bwrap, Linux-only).";
      };

      nixInBox = mkOption {
        type = types.nullOr types.bool;
        default = null;
        description = ''
          Bake nix (binary + registered store DB + sandbox-off config) into the
          box so `nix flake check` and `nix develop` work inside the container.
          Defaults to true (the nix-centric baseline); set to false for a lean,
          nix-free image.
        '';
      };
    };
  };

  config.perSystem =
    {
      config,
      system,
      self,
      ...
    }:
    let
      cfg = config.spindrift;
      # Drop unset run defaults so mkHarness supplies its own per key.
      runDefaults = lib.filterAttrs (_: v: v != null) cfg.defaults;
      # Forward only the options the Consumer actually set; the rest fall
      # through to mkHarness's defaults.
      args =
        {
          inherit system;
          nixpkgs = cfg.nixpkgs;
          revision = self.shortRev or self.dirtyShortRev or "unknown";
        }
        // lib.optionalAttrs (cfg.overlays != null) { inherit (cfg) overlays; }
        // lib.optionalAttrs (cfg.config != null) { inherit (cfg) config; }
        // lib.optionalAttrs (cfg.packages != null) { inherit (cfg) packages; }
        // lib.optionalAttrs (cfg.prefetch != null) { inherit (cfg) prefetch; }
        // lib.optionalAttrs (cfg.prompt != null) { inherit (cfg) prompt; }
        // lib.optionalAttrs (cfg.skills != null) { inherit (cfg) skills; }
        // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; }
        // lib.optionalAttrs (cfg.runtime != null) { inherit (cfg) runtime; }
        // lib.optionalAttrs (cfg.nixInBox != null) { inherit (cfg) nixInBox; };
      harness = mkHarness args;
    in
    {
      inherit (harness) packages apps;
    };
}
