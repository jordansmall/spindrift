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
  self,
  ...
}:
let
  inherit (lib) mkOption types;
  mkHarness = import ./mkHarness.nix;
  schema = import ./env-schema.nix;
  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Map from groupOrder heading (cmd/launcher/flags.go) to the attr name used
  # under perSystem.spindrift.settings.  Sections with no flakeOption knobs
  # (Self-healing & retries, Repository & identity, Prompt & skill iteration)
  # never appear as settings sections.
  groupToAttr = {
    "Issue discovery" = "issueDiscovery";
    "Lifecycle labels" = "lifecycleLabels";
    "Branches & merge" = "branches";
    "Concurrency & dependency waves" = "concurrency";
    "Models" = "models";
    "Self-healing & retries" = "selfHealing";
    "Sandbox & resources" = "sandbox";
    "Repository & identity" = "repository";
    "Prompt & skill iteration" = "promptSkillIteration";
  };

  # Group flakeOptionEntries by their section attr name; the result is
  # { sectionAttr = { knobName = entry; ... }; ... }.
  sectionKnobs = lib.foldl' (
    acc: knobName:
    let
      entry = flakeOptionEntries.${knobName};
      sectionAttr = groupToAttr.${entry.group} or null;
    in
    if sectionAttr == null then
      acc
    else
      acc
      // {
        ${sectionAttr} = (acc.${sectionAttr} or { }) // {
          ${knobName} = entry;
        };
      }
  ) { } (lib.attrNames flakeOptionEntries);

  # Generate one mkOption per knob; type is nullOr str/int so unset knobs fall
  # through to mkHarness's schema defaults.
  mkKnobOption =
    _key: entry:
    mkOption {
      type =
        if builtins.isInt (entry.default or "") then types.nullOr types.int else types.nullOr types.str;
      default = null;
      description = entry.doc;
    };

  # Generate one section option (a submodule containing all knobs in the section).
  mkSectionOption =
    _sectionAttr: knobs:
    mkOption {
      type = types.submodule {
        options = lib.mapAttrs mkKnobOption knobs;
      };
      default = { };
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

      # Generated from env-schema.nix: one sub-option per section (matching
      # groupOrder in cmd/launcher/flags.go), one per consumer-tunable knob
      # within each section.  Undeclared section or knob names are rejected at
      # eval time by the NixOS module system.
      settings = mkOption {
        type = types.submodule {
          options = lib.mapAttrs mkSectionOption sectionKnobs;
        };
        default = { };
        description = "Non-secret run defaults baked into the generated `run` command, grouped by section. A matching env var wins at runtime.";
      };

      runtime = mkOption {
        type = types.nullOr (
          types.enum [
            "podman"
            "docker"
            "bwrap"
          ]
        );
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
      ...
    }:
    let
      cfg = config.spindrift;
      # Flatten settings.<section>.<knob> to a flat attrset, then drop nulls so
      # mkHarness receives only the knobs the Consumer explicitly set and supplies
      # its own schema defaults for the rest.
      runDefaults = lib.filterAttrs (_: v: v != null) (
        lib.foldl' lib.mergeAttrs { } (lib.mapAttrsToList (_section: knobs: knobs) cfg.settings)
      );
      # Forward only the options the Consumer actually set; the rest fall
      # through to mkHarness's defaults.
      args = {
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
