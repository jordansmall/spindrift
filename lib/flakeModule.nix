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
  resolveNixPath = import ./nixpath.nix;
  runtimeValues = import ./runtime-values.nix;
  # Doc prose + doc-metadata (docType/docDefault) for the structural knobs
  # below and byNameOption, factored into plain data (issue #2572) so
  # lib/renderers.nix's pure-builtins renderStructuralOptionsDoc can import
  # it directly instead of reaching it through a full flake-parts eval.
  structuralOptionsDoc = import ./structural-options-doc.nix;
  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Frozen snapshot (ADR 0037 Pass 2): each flakeOption knob's original
  # ADR-0015-era `settings.<section>` attr name — factored into its own file
  # (mirroring lib/structural-paths.nix) so nix/checks/schema-drift.nix's
  # legacy-settings-section-coverage check (issue #2522) can import it
  # standalone, the same reason structural-paths.nix was factored out.
  legacySettingsSection = import ./legacy-settings-section.nix;
  # byName domain-tree path (single source of truth, mirroring
  # structuralPlacements below) so nix/checks/schema-drift.nix's
  # flake-nixpath-exhaustive-disjoint check (issue #2731) can import the
  # same literal standalone instead of it being duplicated inline here.
  byNamePaths = import ./byname-paths.nix;

  # Group flakeOptionEntries by their section attr name; the result is
  # { sectionAttr = { knobName = entry; ... }; ... }.
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

  # Generate one mkOption per knob; type is nullOr str/int so unset knobs fall
  # through to mkHarness's schema defaults. A knob that declares `choices`
  # (issue #2519) is generated as `types.nullOr (types.enum entry.choices)`
  # instead — checked ahead of the int/bool/str inference below — so a
  # Consumer setting an out-of-enum value fails `nix eval`/`nix build` at the
  # option, naming the option path and the valid choices (the same behavior
  # `structuralPlacements.runtime`'s hand-written enum already gets, just
  # schema-driven here).
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

  # Generate one section option (a submodule containing all knobs in the section).
  mkSectionOption =
    _sectionAttr: knobs:
    mkOption {
      type = types.submodule {
        options = lib.mapAttrs mkKnobOption knobs;
      };
      default = { };
    };

  # ADR 0037 Pass 1 (issue #2179): build the new domain-tree option surface
  # generically from a flat list of { path; opt; } entries. Entries are
  # grouped by their first path segment; a length-1 path is a leaf (the
  # option itself), a longer path recurses into a submodule. Derived flake
  # paths (lib/nixpath.nix) are prefix-disjoint (enforced by
  # nix/checks/schema-drift.nix's flake-nixpath-exhaustive-disjoint), so no
  # segment is ever both a leaf and a namespace.
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

  # flakeOption leaves: one entry per Consumer-tunable knob, keyed by its
  # derived flake path (lib/nixpath.nix).
  flakeOptionTreeEntries = lib.mapAttrsToList (key: entry: {
    path = lib.splitString "." (resolveNixPath key entry);
    opt = mkKnobOption key entry;
  }) flakeOptionEntries;

  # Structural knobs: the ONE hand-written mkOption definition (type,
  # default, description) per structural knob (issue #2522), keyed by its
  # flat (legacy, == mkHarness arg) name — the same keys as
  # structuralPlacements below. Both the domain-tree leaf
  # (structuralTreeEntries) and the flat legacy shim (oldFlatShims) are
  # generated from this single declaration, so there is no longer a
  # hand-copy to keep in sync between the two surfaces.
  structuralOptions = {
    driver = mkOption {
      # A plain string, not `types.enum`, so the lib/drivers/ registry (not
      # this option) stays the single source of truth for valid names —
      # mkHarness.nix throws at eval time on a name absent from the
      # registry (ADR 0009).
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

  # Name-keyed model/effort shorthand (issue #2560), a brand-new option with
  # no pre-existing flat spelling -- deliberately declared OUTSIDE
  # structuralOptions (whose keys drive the oldFlatShims/structuralPlacements
  # dual-path legacy-migration machinery below) so it never grows a
  # fabricated "old" flat alias and never emits a deprecation warning.
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

  # Standalone tree entry (not merged into flakeOptionTreeEntries or
  # structuralTreeEntries) since byNameOption skips both of those surfaces'
  # legacy-migration machinery. The path segments live in lib/byname-paths.nix
  # (byNamePaths above) rather than being hardcoded here, so
  # nix/checks/schema-drift.nix's flake-nixpath-exhaustive-disjoint check can
  # see them the same way structuralPlacements' paths already are (issue
  # #2731). Asserted against byNamePaths' own key set first — a stray/unwired
  # key would otherwise silently inflate that check's disjointness set with a
  # path no option actually occupies.
  byNameTreeEntries =
    assert lib.assertMsg (lib.attrNames byNamePaths == [ "byName" ])
      "lib/flakeModule.nix: lib/byname-paths.nix (byNamePaths) must have exactly the key set wired into byNameTreeEntries below";
    [
      {
        path = byNamePaths.byName;
        opt = byNameOption;
      }
    ];

  # Structural leaves: each structuralOptions entry, hand-placed at its new
  # domain-tree path (slice 2's placement map). Asserted against
  # structuralPlacements' own key set first — a row added to only one of the
  # two maps would otherwise surface as an opaque `attribute 'X' missing`
  # deep inside this mapAttrsToList rather than a named error.
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

  # The old flat path -> new dotted domain-tree path each structural knob
  # moved to (slice 2's placement map), keyed by the flat option name — used
  # both to declare the deprecation-shim options and to resolve the
  # new-wins-old precedence in config.perSystem below.
  structuralPlacements = import ./structural-paths.nix;

  # The 13 old flat structural options, now null-default deprecation shims
  # (ADR 0037 Pass 1, generated per issue #2522): reuses each
  # structuralOptions entry's type (precise errors on old paths for free),
  # a null default so config.perSystem's forwarding below distinguishes
  # unset from set, and a one-line auto-generated rename pointer as the
  # description — the real type/default/description now lives on the new
  # domain-tree option (structuralTreeEntries above). A consumer that still
  # sets the old path is forwarded via `lib.warn` in config.perSystem below
  # (matching wording). Kept DECLARED (not removed) so a typo on the old
  # path still throws instead of silently doing nothing.
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
        # Legacy `settings.<section>.<knob>` deprecation shim (ADR 0037): the
        # primary surface is now the domain tree built by `buildTree`. Generated
        # from env-schema.nix — one sub-option per section (matching groupOrder
        # in cmd/launcher/flags.go), one per consumer-tunable knob within each
        # section. A set value forwards to the new domain-tree path via
        # `lib.warn`; undeclared section or knob names are rejected at eval time
        # by the NixOS module system.
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

      # ADR 0037 Pass 1 (issue #2179): resolve each flakeOption knob's
      # run-default from the new domain-tree path first, falling back to its
      # old settings.<section>.<knob> path (forwarded through `lib.warn` so a
      # Consumer still on the old path sees a deprecation notice at eval
      # time — `lib.warn` returns the value unchanged, so a Consumer entirely
      # on old paths still gets byte-identical mkHarness `defaults`).  Keyed
      # by schema key, matching mkHarness's flat `defaults` shape.
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

      # Same new-wins-old resolution for the 13 structural knobs that moved
      # off a flat top-level option onto a domain-tree path
      # (structuralPlacements above), keyed by the flat (== mkHarness arg)
      # name.
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

      # structuralResolved.nixpkgs is null when the Consumer set neither the
      # new nor the deprecated path; the flake's own locked input is the
      # default in that case (kept out of structuralTreeEntries' default so
      # the old-path fallback above still gets consulted).
      resolvedNixpkgs =
        if structuralResolved.nixpkgs != null then structuralResolved.nixpkgs else inputs.nixpkgs;

      # Derive the structural-knob forwarding chain from the same canonical
      # 13-key `structuralPlacements` map (lib/structural-paths.nix) that
      # `structuralResolved` above is keyed by, instead of hand-writing one
      # `lib.optionalAttrs` clause per knob (issue #2522 slice 2) — adding a
      # 14th structural knob to structural-paths.nix + structuralOptions
      # needs no edit here. `nixpkgs` is excluded: it's resolved separately
      # above (resolvedNixpkgs) and forwarded unconditionally below.
      structuralArgs = lib.foldl' (
        acc: flatName:
        acc
        // lib.optionalAttrs (structuralResolved.${flatName} != null) {
          ${flatName} = structuralResolved.${flatName};
        }
      ) { } (lib.filter (n: n != "nixpkgs") (lib.attrNames structuralPlacements));

      # Forward only the options the Consumer actually set; the rest fall
      # through to mkHarness's defaults.
      args = {
        inherit system;
        nixpkgs = resolvedNixpkgs;
        revision = self.shortRev or self.dirtyShortRev or "unknown";
      }
      // structuralArgs
      // lib.optionalAttrs (cfg.agents.models.byName != null) { byName = cfg.agents.models.byName; }
      // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; };
      harness = mkHarness args;
      # nixfmt from the consumer's locked nixpkgs input — same pin the
      # nix-fmt gate uses — so `nix fmt` fixes what the check catches.
      nixfmt = (import resolvedNixpkgs { inherit system; }).nixfmt;
    in
    {
      inherit (harness) packages apps;
      formatter = nixfmt;
    };
}
