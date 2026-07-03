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
        description = "Agent prompt template rendered to a store path and mounted at run time.";
      };

      defaults = mkOption {
        type = types.submodule {
          options = {
            label = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
            baseBranch = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
            maxParallel = mkOption {
              type = types.nullOr types.int;
              default = null;
            };
            branchPrefix = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
            inProgressLabel = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
            failedLabel = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
            model = mkOption {
              type = types.nullOr types.str;
              default = null;
            };
          };
        };
        default = { };
        description = "Non-secret run defaults baked into the generated `run` command.";
      };

      runtime = mkOption {
        type = types.nullOr (types.enum [
          "podman"
          "docker"
        ]);
        default = null;
        description = "Container runtime the launcher commands drive.";
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
    { config, system, ... }:
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
        }
        // lib.optionalAttrs (cfg.overlays != null) { inherit (cfg) overlays; }
        // lib.optionalAttrs (cfg.config != null) { inherit (cfg) config; }
        // lib.optionalAttrs (cfg.packages != null) { inherit (cfg) packages; }
        // lib.optionalAttrs (cfg.prefetch != null) { inherit (cfg) prefetch; }
        // lib.optionalAttrs (cfg.prompt != null) { inherit (cfg) prompt; }
        // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; }
        // lib.optionalAttrs (cfg.runtime != null) { inherit (cfg) runtime; }
        // lib.optionalAttrs (cfg.nixInBox != null) { inherit (cfg) nixInBox; };
      harness = mkHarness args;
    in
    {
      inherit (harness) packages apps;
    };
}
