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
  renderers = import ./renderers.nix;
  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Map from groupOrder heading to the attr name used under
  # perSystem.spindrift.settings. Sections with no flakeOption knobs (Prompt &
  # skill iteration) are silently skipped when rendering.
  inherit (renderers) groupToAttr;

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
        if builtins.isInt (entry.default or "") then
          types.nullOr types.int
        else if builtins.isBool (entry.default or "") then
          types.nullOr types.bool
        else
          types.nullOr types.str;
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
        type = types.nullOr (
          types.listOf (
            types.either types.path (
              types.submodule {
                options = {
                  name = mkOption {
                    type = types.str;
                    description = "Skill (directory) name; baked as <name>/SKILL.md.";
                  };
                  src = mkOption {
                    type = types.str;
                    description = "SKILL.md body, re-realized with the image's own Linux pkgs.";
                  };
                };
              }
            )
          )
        );
        default = null;
        description = "Skills baked into the image at /home/agent/.claude/skills. Each is baked as a <name>/SKILL.md directory — the only layout Claude Code discovers (a flat <name>.md is ignored). An element is a path to a skill directory, or a { name; src; } content entry (name + SKILL.md body) realized with the image's Linux pkgs (issue #597). SPINDRIFT_SKILLS_DIR at runtime mounts over the same path and takes precedence.";
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

      driver = mkOption {
        # A plain string, not `types.enum`, so the lib/drivers/ registry (not
        # this option) stays the single source of truth for valid names —
        # mkHarness.nix throws at eval time on a name absent from the
        # registry (ADR 0009).
        type = types.nullOr types.str;
        default = null;
        description = "The agent CLI Driver (ADR 0009): a build-time choice selecting one entry from the lib/drivers/ registry, baked into the image and threaded to the launcher as DRIVER. \"claude\" is the only Driver today.";
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

      nixStoreWritable = mkOption {
        type = types.nullOr types.bool;
        default = null;
        description = ''
          Self-test mode (ADR 0018): make the /nix/store directory writable by
          the agent uid in the built OCI image, so `nix flake check` can
          substitute/build new store paths inside the Box instead of hitting
          EACCES. New paths land only in the container's ephemeral
          copy-on-write layer. Defaults to false; the entrypoint prints a loud
          warning when enabled. OCI-runner only — the bwrap runner keeps its
          read-only store bind.
        '';
      };

      extraClosures = mkOption {
        type = types.nullOr (types.functionTo (types.listOf types.package));
        default = null;
        description = ''
          Extra derivations, as a function of the (Linux) pkgs, whose closures
          are baked into the image contents and registered in the store DB
          alongside the runtime closure — so in-box nix sees them as already
          present instead of cold-substituting the world on every Box.
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
      // lib.optionalAttrs (cfg.driver != null) { inherit (cfg) driver; }
      // lib.optionalAttrs (cfg.nixInBox != null) { inherit (cfg) nixInBox; }
      // lib.optionalAttrs (cfg.nixStoreWritable != null) { inherit (cfg) nixStoreWritable; }
      // lib.optionalAttrs (cfg.extraClosures != null) { inherit (cfg) extraClosures; };
      harness = mkHarness args;
      # nixfmt from the consumer's locked nixpkgs input — same pin the
      # nix-fmt gate uses — so `nix fmt` fixes what the check catches.
      nixfmt = (import cfg.nixpkgs { inherit system; }).nixfmt;
    in
    {
      inherit (harness) packages apps;
      formatter = nixfmt;
    };
}
