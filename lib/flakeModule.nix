# A flake-parts shim over lib/mkHarness.nix (ADR 0001). It declares no defaults
# of its own: unset options are not forwarded, so mkHarness's defaults apply and
# the outputs stay byte-identical to a direct mkHarness call.
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
  resolveNixPath = import ./nixpath.nix;
  runtimeValues = import ./runtime-values.nix;
  # Doc prose for the structural knobs, kept as plain data (issue #2572) so
  # lib/renderers.nix's pure-builtins renderStructuralOptionsDoc can import it
  # directly instead of reaching it through a full flake-parts eval.
  structuralOptionsDoc = import ./structural-options-doc.nix;
  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Frozen snapshot (ADR 0037 Pass 2) of each flakeOption knob's original
  # ADR-0015-era `settings.<section>` attr name. It lives in its own file so
  # nix/checks/schema-drift.nix's legacy-settings-section-coverage check
  # (issue #2522) can import it standalone.
  legacySettingsSection = import ./legacy-settings-section.nix;
  # Single source of truth for the byName path, so
  # nix/checks/schema-drift.nix's flake-nixpath-exhaustive-disjoint check
  # (issue #2731) imports the same literal instead of a copy inlined here.
  byNamePaths = import ./byname-paths.nix;

  sectionKnobs = lib.foldl' (
    acc: knobName:
    let
      entry = flakeOptionEntries.${knobName};
      sectionAttr = legacySettingsSection.${knobName} or null;
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

  # Every type is nullOr so unset knobs fall through to mkHarness's schema
  # defaults. The `choices` case (issue #2519) is checked ahead of the
  # int/bool/str inference so an out-of-enum value fails at the option itself,
  # naming the option path and the valid choices.
  mkKnobOption =
    _key: entry:
    mkOption {
      type =
        if entry ? choices then
          types.nullOr (types.enum entry.choices)
        else if builtins.isInt (entry.default or "") then
          types.nullOr types.int
        else if builtins.isBool (entry.default or "") then
          types.nullOr types.bool
        else
          types.nullOr types.str;
      default = null;
      description = entry.doc;
    };

  mkSectionOption =
    _sectionAttr: knobs:
    mkOption {
      type = types.submodule {
        options = lib.mapAttrs mkKnobOption knobs;
      };
      default = { };
    };

  # Builds the domain-tree options from a flat list of { path; opt; } entries
  # (ADR 0037 Pass 1, issue #2179). Derived flake paths (lib/nixpath.nix) are
  # prefix-disjoint, enforced by nix/checks/schema-drift.nix's
  # flake-nixpath-exhaustive-disjoint check, so no segment is ever both a leaf
  # and a namespace.
  buildTree =
    entries:
    let
      grouped = lib.foldl' (
        acc: entry:
        let
          head = builtins.head entry.path;
        in
        acc
        // {
          ${head} = (acc.${head} or [ ]) ++ [ entry ];
        }
      ) { } entries;
    in
    lib.mapAttrs (
      _head: group:
      if builtins.length group == 1 && builtins.length (builtins.head group).path == 1 then
        (builtins.head group).opt
      else
        mkOption {
          type = types.submodule {
            options = buildTree (
              map (e: {
                path = builtins.tail e.path;
                opt = e.opt;
              }) group
            );
          };
          default = { };
        }
    ) grouped;

  flakeOptionTreeEntries = lib.mapAttrsToList (key: entry: {
    path = lib.splitString "." (resolveNixPath key entry);
    opt = mkKnobOption key entry;
  }) flakeOptionEntries;

  # The one hand-written mkOption per structural knob (issue #2522), keyed by
  # its flat legacy name, which is also the mkHarness arg name and the key set
  # of structuralPlacements below. Both the domain-tree leaf
  # (structuralTreeEntries) and the flat shim (oldFlatShims) are generated
  # from it, so the two paths cannot drift apart.
  structuralOptions = {
    driver = mkOption {
      # A plain string, not `types.enum`, so the lib/drivers/ registry stays
      # the single source of truth for valid names. mkHarness.nix throws at
      # eval time on a name the registry does not have (ADR 0009).
      type = types.nullOr types.str;
      default = null;
      description = structuralOptionsDoc.driver.doc;
    };

    prompt = mkOption {
      type = types.nullOr types.lines;
      default = null;
      description = structuralOptionsDoc.prompt.doc;
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
      description = structuralOptionsDoc.skills.doc;
    };

    roster = mkOption {
      type = types.nullOr (types.listOf types.attrs);
      default = null;
      description = structuralOptionsDoc.roster.doc;
    };

    runtime = mkOption {
      type = types.nullOr (types.enum runtimeValues);
      default = null;
      description = structuralOptionsDoc.runtime.doc;
    };

    packages = mkOption {
      type = types.nullOr (types.functionTo (types.listOf types.package));
      default = null;
      description = structuralOptionsDoc.packages.doc;
    };

    prefetch = mkOption {
      type = types.nullOr types.lines;
      default = null;
      description = structuralOptionsDoc.prefetch.doc;
    };

    extraClosures = mkOption {
      type = types.nullOr (types.functionTo (types.listOf types.package));
      default = null;
      description = structuralOptionsDoc.extraClosures.doc;
    };

    nixInBox = mkOption {
      type = types.nullOr types.bool;
      default = null;
      description = structuralOptionsDoc.nixInBox.doc;
    };

    nixStoreWritable = mkOption {
      type = types.nullOr types.bool;
      default = null;
      description = structuralOptionsDoc.nixStoreWritable.doc;
    };

    nixpkgs = mkOption {
      type = types.nullOr types.raw;
      default = null;
      description = structuralOptionsDoc.nixpkgs.doc;
    };

    overlays = mkOption {
      type = types.nullOr (types.listOf types.raw);
      default = null;
      description = structuralOptionsDoc.overlays.doc;
    };

    config = mkOption {
      type = types.nullOr types.attrs;
      default = null;
      example = {
        allowUnfree = true;
      };
      description = structuralOptionsDoc.config.doc;
    };
  };

  # Name-keyed model/effort shorthand (issue #2560). It has no pre-existing
  # flat spelling, so it is declared outside structuralOptions, whose keys
  # drive the oldFlatShims legacy-migration machinery below. That keeps it
  # from growing a fabricated "old" alias or emitting a deprecation warning.
  byNameOption = mkOption {
    type = types.nullOr (
      types.attrsOf (
        types.submodule {
          options = {
            model = mkOption {
              type = types.nullOr types.str;
              default = null;
              description = "Model override for this roster entry (lib/roster.nix's defaultRoster). An explicit empty string (\"\") opts this agent out, the same convention the roster-native `models` shorthand already uses.";
            };
            effort = mkOption {
              type = types.nullOr types.str;
              default = null;
              description = "Effort/reasoning-level override for this roster entry. For the reviewer entry, the deprecated reviewEffort knob (agents.models.reviewEffort), when set, overrides this after the fact.";
            };
          };
        }
      )
    );
    default = null;
    description = structuralOptionsDoc.byName.doc;
  };

  # Kept standalone because byNameOption skips the legacy-migration machinery
  # the other two entry lists carry. The assert guards byNamePaths' key set: a
  # stray key there would silently inflate the
  # flake-nixpath-exhaustive-disjoint check's path set (issue #2731) with a
  # path no option occupies.
  byNameTreeEntries =
    assert lib.assertMsg (lib.attrNames byNamePaths == [ "byName" ])
      "lib/flakeModule.nix: lib/byname-paths.nix (byNamePaths) must have exactly the key set wired into byNameTreeEntries below";
    [
      {
        path = byNamePaths.byName;
        opt = byNameOption;
      }
    ];

  # The assert compares key sets first: a row added to only one of
  # structuralOptions and structuralPlacements would otherwise fail as an
  # opaque `attribute 'X' missing` inside this mapAttrsToList.
  structuralTreeEntries =
    assert lib.assertMsg
      (
        (lib.sort builtins.lessThan (lib.attrNames structuralOptions))
        == (lib.sort builtins.lessThan (lib.attrNames structuralPlacements))
      )
      "lib/flakeModule.nix: structuralOptions and lib/structural-paths.nix (structuralPlacements) must share the same key set";
    lib.mapAttrsToList (flatName: opt: {
      path = structuralPlacements.${flatName};
      inherit opt;
    }) structuralOptions;

  # Maps each structural knob's flat option name to the domain-tree path it
  # moved to. oldFlatShims below reads it to declare the deprecation shims, and
  # config.perSystem reads it to resolve the new-wins-old precedence.
  structuralPlacements = import ./structural-paths.nix;

  # Deprecation shims for the old flat structural options (ADR 0037 Pass 1,
  # issue #2522). The null default lets config.perSystem below tell unset from
  # set, and reusing each structuralOptions type keeps errors on old paths
  # precise. These stay declared rather than removed so a typo on an old path
  # still throws instead of silently doing nothing.
  oldFlatShims = lib.mapAttrs (
    flatName: opt:
    mkOption (
      {
        inherit (opt) type;
        default = null;
        description = "perSystem.spindrift.${flatName} is deprecated; use perSystem.spindrift.${
          lib.concatStringsSep "." structuralPlacements.${flatName}
        }.";
      }
      // lib.optionalAttrs (opt ? example) { inherit (opt) example; }
    )
  ) structuralOptions;
in
{
  options.perSystem = flake-parts-lib.mkPerSystemOption {
    options.spindrift =
      let
        # Deprecation shim for the legacy `settings.<section>.<knob>` paths
        # (ADR 0037); the domain tree built by `buildTree` is now primary.
        # Sections come from env-schema.nix and match groupOrder in
        # cmd/launcher/flags.go. A set value forwards to the new path via
        # `lib.warn`.
        settingsOption = {
          settings = mkOption {
            type = types.submodule {
              options = lib.mapAttrs mkSectionOption sectionKnobs;
            };
            default = { };
            description = "Non-secret run defaults baked into the generated `run` command, grouped by section. A matching env var wins at runtime.";
          };
        };
      in
      oldFlatShims
      // settingsOption
      // (buildTree (flakeOptionTreeEntries ++ structuralTreeEntries ++ byNameTreeEntries));
  };

  config.perSystem =
    {
      config,
      system,
      ...
    }:
    let
      cfg = config.spindrift;

      # New domain-tree path wins, old settings.<section>.<knob> path is the
      # fallback (ADR 0037 Pass 1, issue #2179). `lib.warn` returns the value
      # unchanged, so a Consumer entirely on old paths still gets
      # byte-identical mkHarness `defaults`. Keyed by schema key, matching
      # mkHarness's flat `defaults` shape.
      runDefaults = lib.filterAttrs (_: v: v != null) (
        lib.mapAttrs (
          key: entry:
          let
            newVal = lib.attrByPath (lib.splitString "." (resolveNixPath key entry)) null cfg;
            sectionAttr = legacySettingsSection.${key} or null;
            oldVal = if sectionAttr == null then null else cfg.settings.${sectionAttr}.${key} or null;
          in
          if newVal != null then
            newVal
          else if oldVal != null then
            lib.warn "perSystem.spindrift.settings.${sectionAttr}.${key} is deprecated; use perSystem.spindrift.${resolveNixPath key entry}" oldVal
          else
            null
        ) flakeOptionEntries
      );

      # The same new-wins-old resolution for the structural knobs, keyed by
      # the flat name, which is also the mkHarness arg name.
      structuralResolved = lib.mapAttrs (
        flatName: newPath:
        let
          newVal = lib.attrByPath newPath null cfg;
          oldVal = cfg.${flatName};
        in
        if newVal != null then
          newVal
        else if oldVal != null then
          lib.warn "perSystem.spindrift.${flatName} is deprecated; use perSystem.spindrift.${lib.concatStringsSep "." newPath}" oldVal
        else
          null
      ) structuralPlacements;

      # The flake's own locked input is the default when the Consumer set
      # neither path. This default is kept off the option itself so the
      # old-path fallback above still gets consulted.
      resolvedNixpkgs =
        if structuralResolved.nixpkgs != null then structuralResolved.nixpkgs else inputs.nixpkgs;

      # Derived from `structuralPlacements` so adding a structural knob there
      # and in structuralOptions needs no edit here (issue #2522 slice 2).
      # `nixpkgs` is excluded because resolvedNixpkgs above already resolved
      # it and the args below forward it unconditionally.
      structuralArgs = lib.foldl' (
        acc: flatName:
        acc
        // lib.optionalAttrs (structuralResolved.${flatName} != null) {
          ${flatName} = structuralResolved.${flatName};
        }
      ) { } (lib.filter (n: n != "nixpkgs") (lib.attrNames structuralPlacements));

      # getAttrFromPath, not attrByPath with a default: a rename in
      # lib/byname-paths.nix must throw here rather than silently resolve to a
      # default and drop byName.
      byNameModels = lib.getAttrFromPath byNamePaths.byName cfg;

      args = {
        inherit system;
        nixpkgs = resolvedNixpkgs;
        revision = self.shortRev or self.dirtyShortRev or "unknown";
      }
      // structuralArgs
      // lib.optionalAttrs (byNameModels != null) { byName = byNameModels; }
      // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; };
      harness = mkHarness args;
      # nixfmt from the consumer's locked nixpkgs input, the same pin the
      # nix-fmt gate uses, so `nix fmt` fixes what the check catches.
      nixfmt = (import resolvedNixpkgs { inherit system; }).nixfmt;
    in
    {
      inherit (harness) packages apps;
      formatter = nixfmt;
    };
}
