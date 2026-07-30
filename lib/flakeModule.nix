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
  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Frozen snapshot (ADR 0037 Pass 2): each flakeOption knob's original
  # ADR-0015-era `settings.<section>` attr name. Pass 2 re-cut the schema
  # `group` field to domains, so the legacy section name can no longer be
  # derived from `group`; this frozen map preserves the exact deprecated
  # `perSystem.spindrift.settings.<section>.<knob>` alias spelling until the
  # whole legacy surface is removed at 1.0.
  legacySettingsSection = {
    autoFormat = "promptSkillIteration";
    autoLint = "promptSkillIteration";
    baseBranch = "branches";
    boxForgeAndIssueAccess = "repository";
    branchPrefix = "branches";
    bwrapUnshareNet = "sandbox";
    codeForge = "repository";
    codeForgeAccumulationRepoDir = "repository";
    codeForgeRemoteURL = "repository";
    completeLabel = "lifecycleLabels";
    continuousDispatch = "concurrency";
    devShellName = "sandbox";
    devShellProbeTimeout = "sandbox";
    failedLabel = "lifecycleLabels";
    filerModel = "models";
    ghTokenRefreshFile = "repository";
    gitUserEmail = "repository";
    gitUserName = "repository";
    holdJitterSecs = "selfHealing";
    inProgressLabel = "lifecycleLabels";
    issueTracker = "issueDiscovery";
    jiraBaseURL = "repository";
    jiraEmail = "repository";
    jiraIncludeComments = "issueDiscovery";
    jiraProjectKey = "repository";
    jiraStatusMapping = "lifecycleLabels";
    label = "issueDiscovery";
    localIssueReference = "issueDiscovery";
    localIssuesDir = "issueDiscovery";
    maxBudgetTokens = "selfHealing";
    maxBudgetUSD = "selfHealing";
    maxFixAttempts = "selfHealing";
    maxJobs = "concurrency";
    maxParallel = "concurrency";
    maxRebaseAttempts = "selfHealing";
    memoryLimit = "sandbox";
    mergeGuardPaths = "branches";
    mergeMode = "branches";
    mergePollInterval = "branches";
    mergePollTimeout = "branches";
    model = "models";
    orchestratorEnabled = "promptSkillIteration";
    overlapGate = "concurrency";
    pidsLimit = "sandbox";
    podmanNetwork = "sandbox";
    preflightStaleBase = "selfHealing";
    repoSlug = "repository";
    reviewModel = "models";
    scoutModel = "models";
    transientBackoffSecs = "selfHealing";
    transientRetryMax = "selfHealing";
    workerModel = "models";
  };

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

  # Structural leaves: hand-placed at their new domain-tree path (slice 2's
  # placement map), carrying the SAME real mkOption definition (type,
  # default, description) the flat option had before this slice.
  structuralTreeEntries = [
    {
      path = structuralPlacements.driver;
      opt = mkOption {
        # A plain string, not `types.enum`, so the lib/drivers/ registry (not
        # this option) stays the single source of truth for valid names —
        # mkHarness.nix throws at eval time on a name absent from the
        # registry (ADR 0009).
        type = types.nullOr types.str;
        default = null;
        description = "The agent CLI Driver (ADR 0009): a build-time choice selecting one entry from the lib/drivers/ registry, baked into the image and threaded to the launcher as DRIVER. \"claude\" is the only Driver today.";
      };
    }
    {
      path = structuralPlacements.prompt;
      opt = mkOption {
        type = types.nullOr types.lines;
        default = null;
        description = "Agent prompt template baked into the image; changing it requires an image rebuild. Set SPINDRIFT_PROMPT_DIR at runtime to override without a rebuild.";
      };
    }
    {
      path = structuralPlacements.skills;
      opt = mkOption {
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
    }
    {
      path = structuralPlacements.roster;
      opt = mkOption {
        type = types.nullOr (types.listOf types.attrs);
        default = null;
        description = ''
          The first-class N-agent roster (issue #264, lib/roster.nix): a list of
          `{ name; model; mode; description; tools; promptFile; prompt }`
          attrsets that both Drivers render subagents from, replacing the four
          hardcoded scout/reviewer/filer/worker model knobs. An explicit
          `roster` always wins over the legacy per-agent model knobs
          (scoutModel/reviewModel/filerModel/workerModel), the same precedence
          `mkHarness.nix` applies to a raw call. Untyped (`types.attrs`
          elements, not a submodule) so the forwarded list matches the
          Consumer's input verbatim, byte-for-byte, with no default-injection.
        '';
      };
    }
    {
      path = structuralPlacements.runtime;
      opt = mkOption {
        type = types.nullOr (
          types.enum [
            "podman"
            "docker"
            "rancher"
            "bwrap"
          ]
        );
        default = null;
        description = "Runner the launcher commands drive: OCI runtimes (podman/docker/rancher, the last an alias for Rancher Desktop's nerdctl) or the daemonless bubblewrap runner (bwrap, Linux-only).";
      };
    }
    {
      path = structuralPlacements.packages;
      opt = mkOption {
        type = types.nullOr (types.functionTo (types.listOf types.package));
        default = null;
        description = "Project tools baked into the image, as a function of the (Linux) pkgs.";
      };
    }
    {
      path = structuralPlacements.prefetch;
      opt = mkOption {
        type = types.nullOr types.lines;
        default = null;
        description = "Shell snippet the entrypoint runs after cloning to warm caches.";
      };
    }
    {
      path = structuralPlacements.extraClosures;
      opt = mkOption {
        type = types.nullOr (types.functionTo (types.listOf types.package));
        default = null;
        description = ''
          Extra derivations, as a function of the (Linux) pkgs, whose closures
          are baked into the image contents and registered in the store DB
          alongside the runtime closure — so in-box nix sees them as already
          present instead of cold-substituting the world on every Box.
        '';
      };
    }
    {
      path = structuralPlacements.nixInBox;
      opt = mkOption {
        type = types.nullOr types.bool;
        default = null;
        description = ''
          Bake nix (binary + registered store DB + sandbox-off config) into the
          box so `nix flake check` and `nix develop` work inside the container.
          Defaults to true (the nix-centric baseline); set to false for a lean,
          nix-free image.
        '';
      };
    }
    {
      path = structuralPlacements.nixStoreWritable;
      opt = mkOption {
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
    }
    {
      path = structuralPlacements.nixpkgs;
      opt = mkOption {
        type = types.nullOr types.raw;
        default = null;
        description = "Locked nixpkgs input the image and host commands build from.";
      };
    }
    {
      path = structuralPlacements.overlays;
      opt = mkOption {
        type = types.nullOr (types.listOf types.raw);
        default = null;
        description = "Overlays applied to the instantiated nixpkgs.";
      };
    }
    {
      path = structuralPlacements.config;
      opt = mkOption {
        type = types.nullOr types.attrs;
        default = null;
        example = {
          allowUnfree = true;
        };
        description = "nixpkgs config attrs.";
      };
    }
  ];

  # The old flat path -> new dotted domain-tree path each structural knob
  # moved to (slice 2's placement map), keyed by the flat option name — used
  # both to declare the deprecation-shim options and to resolve the
  # new-wins-old precedence in config.perSystem below.
  structuralPlacements = import ./structural-paths.nix;
in
{
  options.perSystem = flake-parts-lib.mkPerSystemOption {
    options.spindrift =
      let
        # The 13 old flat structural options, now null-default deprecation
        # shims (ADR 0037 Pass 1): the real type/default/description now
        # lives on the new domain-tree option (structuralTreeEntries above);
        # a consumer that still sets the old path is forwarded via
        # `lib.warn` in config.perSystem below. Kept DECLARED (not removed)
        # so a typo on the old path still throws instead of silently doing
        # nothing.
        oldFlatShims = {
          nixpkgs = mkOption {
            type = types.nullOr types.raw;
            default = null;
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

          runtime = mkOption {
            type = types.nullOr (
              types.enum [
                "podman"
                "docker"
                "rancher"
                "bwrap"
              ]
            );
            default = null;
            description = "Runner the launcher commands drive: OCI runtimes (podman/docker/rancher, the last an alias for Rancher Desktop's nerdctl) or the daemonless bubblewrap runner (bwrap, Linux-only).";
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

          roster = mkOption {
            type = types.nullOr (types.listOf types.attrs);
            default = null;
            description = ''
              The first-class N-agent roster (issue #264, lib/roster.nix): a list of
              `{ name; model; mode; description; tools; promptFile; prompt }`
              attrsets that both Drivers render subagents from, replacing the four
              hardcoded scout/reviewer/filer/worker model knobs. An explicit
              `roster` always wins over the legacy per-agent model knobs
              (scoutModel/reviewModel/filerModel/workerModel), the same precedence
              `mkHarness.nix` applies to a raw call. Untyped (`types.attrs`
              elements, not a submodule) so the forwarded list matches the
              Consumer's input verbatim, byte-for-byte, with no default-injection.
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
      oldFlatShims // settingsOption // (buildTree (flakeOptionTreeEntries ++ structuralTreeEntries));
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

      # Forward only the options the Consumer actually set; the rest fall
      # through to mkHarness's defaults.
      args = {
        inherit system;
        nixpkgs = resolvedNixpkgs;
        revision = self.shortRev or self.dirtyShortRev or "unknown";
      }
      // lib.optionalAttrs (structuralResolved.overlays != null) {
        inherit (structuralResolved) overlays;
      }
      // lib.optionalAttrs (structuralResolved.config != null) { inherit (structuralResolved) config; }
      // lib.optionalAttrs (structuralResolved.packages != null) {
        inherit (structuralResolved) packages;
      }
      // lib.optionalAttrs (structuralResolved.prefetch != null) {
        inherit (structuralResolved) prefetch;
      }
      // lib.optionalAttrs (structuralResolved.prompt != null) { inherit (structuralResolved) prompt; }
      // lib.optionalAttrs (structuralResolved.skills != null) { inherit (structuralResolved) skills; }
      // lib.optionalAttrs (structuralResolved.roster != null) { inherit (structuralResolved) roster; }
      // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; }
      // lib.optionalAttrs (structuralResolved.runtime != null) { inherit (structuralResolved) runtime; }
      // lib.optionalAttrs (structuralResolved.driver != null) { inherit (structuralResolved) driver; }
      // lib.optionalAttrs (structuralResolved.nixInBox != null) {
        inherit (structuralResolved) nixInBox;
      }
      // lib.optionalAttrs (structuralResolved.nixStoreWritable != null) {
        inherit (structuralResolved) nixStoreWritable;
      }
      // lib.optionalAttrs (structuralResolved.extraClosures != null) {
        inherit (structuralResolved) extraClosures;
      };
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
