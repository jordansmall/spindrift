# A flake-parts shim over lib/mkHarness.nix (ADR 0001). It declares no defaults
# of its own: unset options are not forwarded, so mkHarness's defaults apply and
# the outputs stay byte-identical to a direct mkHarness call.
{
  lib,
  config,
  options,
  flake-parts-lib,
  inputs,
  self,
  ...
}:
let
  inherit (lib) mkOption types;
  mkHarness = import ./mkHarness.nix;
  skillsLib = import ./skills.nix;
  schema = import ./env-schema.nix;
  removedKnobs = import ./removed-knobs.nix;
  removedKnobFlake = import ./removed-knob-hits.nix { inherit lib; } removedKnobs;
  names = import ./connector-names.nix;
  connectorData = import ./connector-data.nix;
  backends = import ./backends/default.nix;
  resolveNixPath = import ./nixpath.nix;
  inherit (import ./renderers.nix) flagKind;
  runtimeValues = (import ./runners/index.nix { }).runtimes;
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
  # The Driver map (ADR 0064, issue #5052): which settings are Driver-valued,
  # where they sit, and the resolver deciding the default Driver.
  driverMap = import ./driver-map.nix;
  inherit (driverMap) driverValued;
  driversPath = driverMap.optionPaths.drivers;
  defaultDriverPath = driverMap.optionPaths.defaultDriver;

  # The Connectors registered as data, keyed by name. The option type already
  # refuses a second registration of a name, but an override of the list would
  # discard other registrations before that check runs, so it is refused here,
  # where every row reads it.
  dataByName =
    let
      registered = config.spindrift.connectorData;
    in
    if options.spindrift.connectorData.highestPrio < lib.modules.defaultOverridePriority then
      throw (names.dataOverrideError (map (d: d.name) registered))
    else
      lib.listToAttrs (map (d: lib.nameValuePair d.name d) registered);

  # The Driver rows Connectors register, keyed by Connector name: the names
  # are already unique, and mkHarness refuses a row whose Driver name differs
  # from its key. Read from the flake-level `config` here: the perSystem
  # lambda below shadows it.
  connectorDriverRows = lib.mapAttrs (_: r: r.driver) (
    lib.filterAttrs (_: r: r.driver != null) config.spindrift.connectors
  );
  # The backend rows network Connectors register (ADR 0073, issue #6805), keyed
  # by Connector name. `credential` (first-party rows only: forwarded here, but
  # mkHarness refuses a third-party row's, see `derivedCredentialEnv`) names one
  # of the row's `knobs`, or its own backend's core token (github's ghToken,
  # forgejo's forgejoToken), resolved to its env var here; any other core knob
  # or a Box-visible one is refused, since the host attaches it to the
  # Connector's routes and a core secret would leak to it.
  # The env is read through `envSchema`, whose collision fold refuses a knob
  # aliasing a core env var. `identity` (the Basic username) follows the same
  # rule but is no secret; `base` (the URL `{base}` route hosts resolve from) is
  # no secret either, so any env-schema knob may carry it.
  #
  # `acceptedRoutes` and `acceptedGrant` are the Consumer's reviewed baseline of
  # a Connector's whole grant (Routes, credential, scheme, base, Git remote),
  # read from its own tree, never an option: a Connector's module cannot set
  # its own, and a lock bump that widens a grant leaves it untouched so doctor
  # shows the diff. The path is built by concatenation, not "${self}", which
  # would copy a fixture's whole repo path into the store.
  acceptedRoutesFile = self.outPath + "/spindrift-connector-routes.json";
  acceptedRoutesBaseline =
    if builtins.pathExists acceptedRoutesFile then
      let
        parsed = builtins.fromJSON (builtins.readFile acceptedRoutesFile);
      in
      if builtins.isAttrs parsed then
        parsed
      else
        throw "spindrift-connector-routes.json: the top level must be an object keyed by Connector name"
    else
      { };
  # No key means no entry: the row carries no baseline fields and mkHarness
  # refuses to run it. An empty list would read as an accepted empty entry.
  baselineRowFields =
    name:
    lib.optionalAttrs (acceptedRoutesBaseline ? ${name}) (
      (import ./connector-baseline.nix).rowFields "spindrift-connector-routes.json" name
        acceptedRoutesBaseline.${name}
    );
  # Connectors whose row asks for shadow mode (ADR 0073, issues #6797, #6810).
  # A row with a `driver` is shadowed through mkHarness's `shadowDrivers`; a
  # `backend` row refuses `shadow` in checkedConnectors (issue #7456), and a
  # row with neither has nothing to shadow, so it is refused by name rather
  # than ignored.
  shadowRows = lib.mapAttrs (
    name: r:
    if r.driver == null && r.backend == null then
      throw "Connector '${name}': `shadow` needs a `driver` row to shadow"
    else
      r
  ) (lib.filterAttrs (_: r: r.shadow) config.spindrift.connectors);
  # connectorBackendRows filters every row through here, which forces each
  # row's `primary` checks.
  checkedConnectors = lib.mapAttrs (
    name: r:
    let
      nativeDriverNames = (import ./drivers/default.nix { inherit lib; }).nativeDriverNames;
      err =
        if r.backend != null && r.definedSwitches != [ ] then
          names.backendSwitchError name r.definedSwitches
        else if r.primary then
          names.primaryError nativeDriverNames name r
        else
          null;
    in
    if err != null then throw err else r
  ) config.spindrift.connectors;
  connectorBackendRows = lib.mapAttrs (
    name: r:
    let
      b = r.backend;
      # A knob the row names as `field` must be one of the Connector's own,
      # host-only and carry an `env`; `secret` is required of the credential only.
      ownKnobEnv =
        field: key: secret:
        let
          err = names.backendKnobError {
            inherit
              backends
              name
              field
              secret
              key
              ;
            ownKnob = r.knobs.${key} or null;
            coreEntry = config.spindrift.envSchema.${key} or null;
          };
        in
        if err != null then throw err else config.spindrift.envSchema.${key}.env;
      derived = names.derivedCredentialEnv name;
      # The row's own knob carrying the derived variable, if any; without one
      # the credential has no doctor-visible knob.
      derivedKnob = lib.findFirst (k: (r.knobs.${k}.env or null) == derived) null (lib.attrNames r.knobs);
      derivedKnobChecked =
        if derivedKnob == null then
          null
        else
          builtins.seq (ownKnobEnv "credential" derivedKnob true) derivedKnob;
      derivedOwner = lib.findFirst (
        o:
        o != name
        && lib.any (k: (config.spindrift.connectors.${o}.knobs.${k}.env or null) == derived) (
          lib.attrNames config.spindrift.connectors.${o}.knobs
        )
      ) null (lib.attrNames config.spindrift.connectors);
      clashErr = names.derivedCredentialClashError {
        inherit name derived;
        owner = if b.credential == null then derivedOwner else null;
      };
      baseEnv =
        config.spindrift.envSchema.${b.base}.env
          or (throw "Connector '${name}': backend base '${b.base}' is not an env-schema knob");
      gitRemoteErr = names.gitRemoteError {
        inherit backends name;
        value = b.gitRemote;
        knobKnown = b.gitRemote != null && config.spindrift.envSchema ? ${b.gitRemote};
      };
    in
    if clashErr != null then
      throw clashErr
    else if gitRemoteErr != null then
      throw gitRemoteErr
    else if b.scheme == "basic" && b.identity == null then
      throw "Connector '${name}': backend scheme 'basic' needs an `identity` (the knob holding the Basic username)"
    else if b.scheme == "bearer" && b.identity != null then
      throw "Connector '${name}': backend `identity` applies only to scheme 'basic'"
    else
      {
        inherit (b) module routes;
      }
      // baselineRowFields name
      // (
        if b.credential != null then
          {
            credentialEnv = ownKnobEnv "credential" b.credential true;
            credentialKnob = b.credential;
          }
        else
          lib.optionalAttrs (derivedKnobChecked != null) { credentialKnob = derivedKnobChecked; }
      )
      // lib.optionalAttrs (b.scheme != "bearer") { credentialScheme = b.scheme; }
      // lib.optionalAttrs (b.identity != null) { identityEnv = ownKnobEnv "identity" b.identity false; }
      // lib.optionalAttrs (b.base != null) { inherit baseEnv; }
      // lib.optionalAttrs (b.gitRemote != null) (
        if names.isGitOrigin b.gitRemote then
          { gitRemote = b.gitRemote; }
        else
          { gitRemoteEnv = config.spindrift.envSchema.${b.gitRemote}.env; }
      )
  ) (lib.filterAttrs (_: r: r.backend != null) checkedConnectors);
  shadowConnectors = lib.attrNames (lib.filterAttrs (_: r: r.driver != null) shadowRows);
  # Driver rows asking for primary mode, already checked by checkedConnectors.
  primaryDriverConnectors = lib.attrNames (
    lib.filterAttrs (_: r: r.primary && r.driver != null) checkedConnectors
  );
  # The fragment contributions Connectors register (issue #6857), a list in
  # Connector-name order; lib/fragment-paths.nix's `withConnectors` validates it.
  connectorFragments = lib.filter (f: f != null) (
    lib.mapAttrsToList (_: r: r.fragments) config.spindrift.connectors
  );

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
  # defaults. `choices` (issue #2519) is checked first so an out-of-enum value
  # fails at the option, naming its path and the valid choices. Int follows
  # flagKind, so `kind = "int"` types a knob with no default (issue #4420); bool
  # still infers from the default: every `kind = "bool"` entry carries one, so
  # honouring that kind would change no option type.
  mkKnobOption =
    _key: entry:
    mkOption {
      type =
        if entry ? choices then
          types.nullOr (types.enum (entry.choices ++ builtins.attrNames (entry.removedChoices or { })))
        else if entry.attrsOfStrings or false then
          types.nullOr (types.attrsOf types.str)
        else if flagKind entry == "int" then
          types.nullOr types.int
        else if builtins.isBool (entry.default or "") then
          types.nullOr types.bool
        else
          types.nullOr types.str;
      default = null;
      description = entry.doc;
    };

  # A removed knob (lib/removed-knobs.nix) keeps a hidden, never-set option so a
  # Consumer that still sets it reaches config.perSystem's "was removed" throw
  # instead of the module system's generic "option does not exist".
  removedKnobOption = mkOption {
    type = types.nullOr types.raw;
    default = null;
    visible = false;
  };

  mkSectionOption =
    sectionAttr: knobs:
    mkOption {
      type = types.submodule {
        options =
          lib.mapAttrs mkKnobOption knobs
          // lib.mapAttrs (_: _: removedKnobOption) (
            lib.filterAttrs (_: row: (row.legacySection or null) == sectionAttr) removedKnobs
          );
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

  removedKnobTreeEntries = removedKnobFlake.treeEntries removedKnobOption;

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
                  type = types.addCheck types.str skillsLib.isValidName // {
                    description = "string matching the pattern ${skillsLib.namePattern}, other than \".\" or \"..\"";
                  };
                  description = "Skill (directory) name; baked as <name>/SKILL.md. Must match ${skillsLib.namePattern} and not be \".\" or \"..\".";
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

  # One Driver's entry under agents.drivers.<name>: the Driver-valued options
  # at their agents-relative paths. byName and roster reuse the single-Driver
  # options; the entry has no Driver field, the attribute name is the Driver.
  structuralEntryOptions = {
    roster = structuralOptions.roster;
    byName = byNameOption;
  };
  driverEntryOptions = buildTree (
    map (key: {
      path = driverMap.paths.${key};
      opt = mkKnobOption key schema.${key};
    }) driverValued.env
    ++ map (key: {
      path = driverMap.paths.${key};
      opt =
        assert lib.assertMsg (structuralEntryOptions ? ${key})
          "lib/flakeModule.nix: structuralEntryOptions has no option for Driver-valued structural key \"${key}\"";
        structuralEntryOptions.${key};
    }) driverValued.structural
  );

  # Like byNameTreeEntries, these two have no flat spelling, so they sit
  # outside structuralOptions and get no deprecation shim.
  driverMapTreeEntries = [
    {
      path = driversPath;
      opt = mkOption {
        type = types.attrsOf (types.submodule { options = driverEntryOptions; });
        default = { };
        description = structuralOptionsDoc.drivers.doc;
      };
    }
    {
      path = defaultDriverPath;
      opt = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = structuralOptionsDoc.defaultDriver.doc;
      };
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
          lib.concatStringsSep "." (driverMap.driverEntryPath flatName structuralPlacements.${flatName})
        }.";
      }
      // lib.optionalAttrs (opt ? example) { inherit (opt) example; }
    )
  ) structuralOptions;
in
{
  # Flake-level, not perSystem: core builds each row here from
  # `spindrift.connectorData` (ADR 0073). A row's `knobs` fold into the env
  # schema; its `driver` reaches every mkHarness call as `driverRows`.
  options.spindrift = {
    connectors = mkOption {
      # Rows come from spindrift.connectorData alone; see names.registeredRowSet.
      type = names.registeredRowSet dataByName (
        types.attrsOf (
          let
            row = types.submodule (
              { name, options, ... }:
              {
                freeformType = types.lazyAttrsOf types.raw;
                options = {
                  name = mkOption {
                    type = types.str;
                    default = name;
                    readOnly = true;
                    description = "The Connector row's name, its attr name in `spindrift.connectors`.";
                  };
                  # The switches some module defined. `isDefined` counts the option
                  # default, and `shadowEvery` defaults to 10, so only a priority
                  # above the default says a row set one (issue #7456).
                  definedSwitches = mkOption {
                    type = types.listOf types.str;
                    internal = true;
                    readOnly = true;
                    default = builtins.filter (
                      n: options.${n}.highestPrio < (lib.mkOptionDefault null).priority
                    ) names.switchNames;
                    description = "The backend-row switches the Consumer set; checkedConnectors refuses them.";
                  };
                  knobs = mkOption {
                    type = types.lazyAttrsOf types.raw;
                    default = { };
                    description = "Env-schema entries this row owns, keyed by env-schema key (lib/row-knobs.nix).";
                  };
                  driver = mkOption {
                    type = types.nullOr types.raw;
                    default = null;
                    description = ''
                      The Connector's Driver row, in the lib/drivers/*.nix shape
                      (its `name` is the Driver's name and must equal this
                      Connector's name; mkHarness throws otherwise). A Driver outside the
                      core registry also sets `module`, a function of pkgs
                      returning the Driver Connector's wasm file path, e.g.
                      `module = pkgs: "''${drv}/driver.wasm";`. The Box loads
                      in-process only a Driver Connector whose wasm is the
                      batteries flake's own build (mkHarness derives that
                      mark; a row cannot set it). Null declares no Driver.
                    '';
                  };
                  backend = mkOption {
                    type = types.nullOr (
                      types.submodule {
                        options = {
                          module = mkOption {
                            type = types.raw;
                            description = "Function of pkgs returning the Connector's wasm file path.";
                          };
                          routes = mkOption {
                            # Items stay raw: lib/connector-routes.nix words the errors.
                            type = types.listOf types.raw;
                            description = "The Connector's Route table, a list of `{ method; host; path; }`, each with an optional GET-only boolean `followRedirect` and optional string lists `requestHeaders` and `responseHeaders`.";
                          };
                          credential = mkOption {
                            type = types.nullOr types.str;
                            default = null;
                            description = "A first-party row's own: the env-schema key of the knob holding its backend's secret (bearer token, or the Basic password under `scheme = \"basic\"`). A third-party row leaves it null: core derives `<NAME>_TOKEN` from the row name, and a row naming one is refused.";
                          };
                          scheme = mkOption {
                            type = types.enum [
                              "bearer"
                              "basic"
                            ];
                            default = "bearer";
                            description = "How the host attaches the credential: as a Bearer token, or as Basic credentials paired with `identity`.";
                          };
                          identity = mkOption {
                            type = types.nullOr types.str;
                            default = null;
                            description = "Env-schema key of the knob holding the Basic username (Jira Cloud: the account email). Required under `scheme = \"basic\"`, refused under bearer.";
                          };
                          base = mkOption {
                            type = types.nullOr types.str;
                            default = null;
                            description = "Env-schema key of the knob holding the site's https base URL. A route whose `host` is `{base}` resolves to that URL's host, with its path prefix prepended to the route path.";
                          };
                          gitRemote = mkOption {
                            type = types.nullOr types.str;
                            default = null;
                            description = "Where the forge's Git remote lives: a literal https origin (`https://host[:port]`, no path) or the env-schema key of the knob holding the forge's https web base URL (any env-schema knob, like `base`). Required of a Code Forge row (github, forgejo), where core derives the clone URL from it; other rows may omit it.";
                          };
                        };
                      }
                    );
                    default = null;
                    description = ''
                      The network Connector's backend row (ADR 0073, issue
                      #6805): `{ module; routes; credential; scheme; identity;
                      base; gitRemote; }`. `module` is a function of pkgs returning
                      the Connector's wasm file path, `routes` its Route table (a
                      list of `{ method; host; path; }` validated at evaluation
                      with the registry-route rules, so a blanket or non-canonical
                      route fails the build naming this Connector), and
                      `credential`, a first-party row's only, the env-schema key
                      of the knob holding its secret: one of its own `knobs`,
                      or its own backend's core token (github's `ghToken`,
                      forgejo's `forgejoToken`), secret and host-only
                      (`boxEnv = false`); any other core knob is refused. A
                      third-party row (a wasm that is not the batteries build
                      of that name) names none and is refused if it does: core
                      derives its credential variable `<NAME>_TOKEN` from the
                      row name, and a knob of its own with that env, secret and
                      host-only, becomes its doctor-visible credential knob.
                      `scheme` is "bearer"
                      (default) or "basic"; basic needs `identity`, the key of
                      one of the Connector's own host-only knobs holding the
                      username (not necessarily secret), and bearer refuses it.
                      `base` names any env-schema knob holding the site's https
                      base URL, which a route host of `{base}` resolves from.
                      `gitRemote` is a literal https origin or the key of a knob
                      holding the forge's https web base URL, required of a
                      github or forgejo row.
                      Null declares no backend; a name equal to a built-in
                      backend (git, local) is refused.
                    '';
                  };
                  fragments = mkOption {
                    type = types.nullOr (
                      types.submodule {
                        options = {
                          name = mkOption {
                            type = types.str;
                            description = "The Connector's name in fragment-path error messages.";
                          };
                          dir = mkOption {
                            type = types.path;
                            description = "A directory laid out `<folder>/<fragment>` like templates/default/prompts/fragments.";
                          };
                          shipped = mkOption {
                            type = types.attrsOf (types.listOf types.str);
                            description = "Per fragment file name, the tracker-axis folders of `dir` that carry it (the shape of lib/fragment-paths.nix's `shipped`).";
                          };
                        };
                      }
                    );
                    default = null;
                    description = ''
                      The prompt fragment files the Connector contributes to an
                      axis row's folders (issue #6857), e.g. the `jira/` folder
                      of a tracker-axis row: `{ name; dir; shipped; }`. mkHarness
                      overlays `dir` onto the image's prompts/fragments and
                      reads each declared path from it; a declaration that
                      disagrees with the axes, core's own files, another
                      Connector or `dir` fails evaluation naming the path.
                      Null contributes none.
                    '';
                  };
                  routes = mkOption {
                    type = types.listOf (
                      types.submodule {
                        options = {
                          method = mkOption {
                            type = types.str;
                            description = "HTTP method, matched exactly: GET, HEAD, POST, PUT, PATCH or DELETE.";
                          };
                          host = mkOption {
                            type = types.str;
                            description = "Host, matched exactly (host or host:port).";
                          };
                          path = mkOption {
                            type = types.str;
                            description = "Path, a subtree root; `{repo}` stands for the tracker's owner/name slug.";
                          };
                          followRedirect = mkOption {
                            type = types.nullOr types.bool;
                            default = null;
                            description = "GET only: let the host follow one redirect hop.";
                          };
                          requestHeaders = mkOption {
                            type = types.nullOr (types.listOf types.str);
                            default = null;
                            description = "Request headers a guest may send beyond the fixed set. Never Authorization, Cookie, Host, Forwarded, Via, X-Real-Ip, X-Forwarded-* or Proxy-*.";
                          };
                          responseHeaders = mkOption {
                            type = types.nullOr (types.listOf types.str);
                            default = null;
                            description = "Response headers a guest may read beyond the fixed set. Never Set-Cookie or Set-Cookie2.";
                          };
                        };
                      }
                    );
                    # Unset (null) fields are dropped so a Route that never sets
                    # `followRedirect` or a header list renders exactly as before the field existed.
                    apply = map (lib.filterAttrs (_: v: v != null));
                    default = [ ];
                    description = ''
                      A network Connector's Route table: the only HTTP traffic
                      its http_request Host call may send. Method is exact
                      uppercase, host an exact host[:port], and path a subtree
                      root; `{repo}` in a path is the tracker's owner/name slug.
                      A GET Route may set `followRedirect = true` to follow one
                      redirect hop, and any Route may declare `requestHeaders` and
                      `responseHeaders` beyond the fixed sets.
                      A backend row carries its own table in `backend.routes`.
                    '';
                  };
                  shadow = mkOption {
                    type = types.bool;
                    default = false;
                    description = ''
                      Run this Connector in shadow mode: the native Strategy of
                      the same name stays authoritative, and the Box replays each
                      Pass log through the row's Driver `module`, recording any
                      divergence from the native decode. A `driver` row must
                      name a core Driver and carry a `module`; only the Harness
                      running that Driver bakes the module. A Driver-only
                      switch: a `backend` row refuses it at evaluation, since
                      every backend is Connector-only with no native read to
                      sample beside. A row with neither throws. mkHarness
                      throws on any other mismatch. Off by default, which
                      leaves the Consumer's outputs byte-identical.
                    '';
                  };
                  primary = mkOption {
                    type = types.bool;
                    default = false;
                    description = ''
                      Make this Connector authoritative over the native
                      implementation of the same name (issues #6998, #6999).
                      A Driver-only switch: a `backend` row refuses it at
                      evaluation, since every backend is Connector-only with no
                      native backend to replace. On a `driver`
                      row it is the Driver in place of the native Strategy, in
                      the Box during the Pass and on the host when it reads
                      Pass logs back; a Connector that fails to load fails the
                      run, with no fallback to the native Strategy. A host that
                      cannot evaluate the module fails evaluation naming the row.
                      Exclusive with `shadow`, and a row with no native
                      implementation of its name throws, each naming the row.
                      Off by default, which leaves the Consumer's outputs
                      byte-identical. Dropping it from a `driver` row restores
                      the native Strategy.
                    '';
                  };
                  shadowEvery = mkOption {
                    type = types.ints.positive;
                    default = 10;
                    description = ''
                      Retired with backend shadowing (issue #7456): no Driver
                      samples, so the rate has no effect, and a `backend` row
                      refuses it at evaluation, like `shadow` and `primary`.
                    '';
                  };
                };
              }
            );
          in
          # A row is its Connector's data and takes only the switches from a
          # definition, so no module can combine into or replace it.
          names.dataRow (
            name: if dataByName ? ${name} then connectorData.row lib dataByName.${name} else null
          ) row
        )
      );
      default = { };
      description = "Connector rows, built from `spindrift.connectorData`; a Consumer sets only the switches.";
    };

    connectorData = mkOption {
      type = types.listOf types.raw // {
        description = "list of Connector data sets";
        # The merge sees every definition's file, which the list type's own
        # merge discards, so a second registration names every definer.
        merge =
          _: defs:
          let
            validated = map (d: d // { value = map connectorData.validate d.value; }) defs;
            error = names.registrationError validated;
          in
          if error != null then throw error else lib.concatMap (d: d.value) validated;
      };
      default = [ ];
      example = lib.literalExpression "[ (import ./data.nix { inherit core; }) ]";
      description = ''
        The Connectors registered as data (lib/connector-data.nix): flat
        attribute sets core alone turns into their `spindrift.connectors.<name>`
        rows. A name has one definer, and the list takes no override, so no
        definition discards another's registration.
      '';
    };

    envSchema = mkOption {
      type = types.lazyAttrsOf types.raw;
      readOnly = true;
      internal = true;
      description = "lib/env-schema.nix with every Connector row's knobs folded in as a core row's are.";
    };
  };

  config.spindrift.envSchema = (import ./row-knobs.nix).fold (
    import ./connector-knobs.nix (lib.attrValues config.spindrift.connectors)
  );

  options.perSystem = flake-parts-lib.mkPerSystemOption {
    options.spindrift =
      let
        # Deprecation shim for the legacy `settings.<section>.<knob>` paths
        # (ADR 0037). Section names come from the frozen map in
        # lib/legacy-settings-section.nix; which knobs exist is still
        # env-schema.nix's flakeOption subset. A set value forwards to the
        # domain-tree path via `lib.warn`.
        settingsOption = {
          settings = mkOption {
            type = types.submodule {
              options = lib.mapAttrs mkSectionOption sectionKnobs;
            };
            default = { };
            description = "Non-secret run defaults baked into the generated `run` command, grouped by section. A matching env var exits 6 (config-invalid); use the matching `--flag` instead.";
          };
        };
      in
      oldFlatShims
      // settingsOption
      // {
        # The single-Driver deprecation messages the module warned with; a
        # check reads it through flake-parts `debug = true`.
        driverDeprecations = mkOption {
          type = types.listOf types.str;
          readOnly = true;
          internal = true;
          visible = false;
        };
      }
      // (buildTree (
        flakeOptionTreeEntries
        ++ structuralTreeEntries
        ++ byNameTreeEntries
        ++ driverMapTreeEntries
        ++ removedKnobTreeEntries
      ));
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
            # placedPath, not resolveNixPath: a Driver-valued key points straight
            # at its agents.drivers.<name> path, not the deprecated top-level one.
            newPath = driverMap.placedPath "<name>" key entry;
          in
          if newVal != null then
            newVal
          else if oldVal != null then
            lib.warn "perSystem.spindrift.settings.${sectionAttr}.${key} is deprecated; use perSystem.spindrift.${newPath}" oldVal
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
          lib.warn "perSystem.spindrift.${flatName} is deprecated; use perSystem.spindrift.${lib.concatStringsSep "." (driverMap.driverEntryPath flatName newPath)}" oldVal
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

      # The single-Driver form's harness arguments; every declared Driver's
      # arguments extend sharedArgs, which is these minus the Driver-valued ones.
      args = {
        inherit system;
        nixpkgs = resolvedNixpkgs;
        revision = self.shortRev or self.dirtyShortRev or "unknown";
      }
      // structuralArgs
      // lib.optionalAttrs (connectorDriverRows != { }) { driverRows = connectorDriverRows; }
      // lib.optionalAttrs (connectorBackendRows != { }) { backendRows = connectorBackendRows; }
      // lib.optionalAttrs (connectorFragments != [ ]) { inherit connectorFragments; }
      // lib.optionalAttrs (shadowConnectors != [ ]) { shadowDrivers = shadowConnectors; }
      // lib.optionalAttrs (primaryDriverConnectors != [ ]) { primaryDrivers = primaryDriverConnectors; }
      // lib.optionalAttrs (byNameModels != null) { byName = byNameModels; }
      // lib.optionalAttrs (runDefaults != { }) { defaults = runDefaults; };
      removedKnobHits = removedKnobFlake.hits cfg;

      # What the Consumer set in the single-Driver form, by dotted path; the
      # resolver rejects mixing it with agents.drivers.
      topLevelSet =
        lib.optional (structuralResolved.driver != null) { path = "agents.driver"; }
        ++ map (key: {
          path = resolveNixPath key flakeOptionEntries.${key};
        }) (lib.filter (key: runDefaults ? ${key}) driverValued.env)
        ++ lib.optional (structuralResolved.roster != null) {
          path = lib.concatStringsSep "." structuralPlacements.roster;
        }
        ++ lib.optional (byNameModels != null) { path = lib.concatStringsSep "." byNamePaths.byName; };

      resolved = driverMap.resolve {
        inherit (cfg.agents) drivers defaultDriver;
        topLevel = topLevelSet;
      };
      shorthand = resolved.default == null;

      sharedDefaults = removeAttrs runDefaults driverValued.env;
      sharedArgs =
        removeAttrs args (
          [
            "driver"
            "defaults"
          ]
          ++ driverValued.structural
        )
        // lib.optionalAttrs (sharedDefaults != { }) { defaults = sharedDefaults; };

      # The non-null values of the given Driver-valued keys in a Driver's
      # entry, found by the marking's own paths.
      entryValues =
        entry: keys:
        lib.filterAttrs (_: v: v != null) (
          lib.genAttrs keys (key: lib.getAttrFromPath driverMap.paths.${key} entry)
        );

      # mkHarness arguments for one declared Driver: the shared arguments plus
      # that entry's own values. Under the shorthand `args` is used verbatim.
      argsFor =
        name:
        let
          entry = cfg.agents.drivers.${name};
          entryDefaults = entryValues entry driverValued.env;
          defaults = sharedDefaults // entryDefaults;
        in
        sharedArgs
        // {
          driver = name;
        }
        // entryValues entry driverValued.structural
        // lib.optionalAttrs (defaults != { }) { inherit defaults; }
        # harness.<driver>.env only means something with several Drivers; a
        # single-Driver flake must stay equal to a direct mkHarness call.
        // lib.optionalAttrs (otherDrivers != [ ]) { multiDriver = true; };

      shorthandHarness = mkHarness args;

      harness =
        if removedKnobHits != [ ] then
          throw "${(lib.head removedKnobHits).at}: ${(lib.head removedKnobHits).message}"
        else if resolved.error != null then
          throw resolved.error
        else if shorthand then
          # The list is built in the same fold step as the warn, so dropping
          # the fold empties it; the lib.warn call itself is not observable
          # in pure eval.
          lib.foldl'
            (
              acc: msg:
              lib.warn msg (
                acc
                // {
                  internals = acc.internals // {
                    driverDeprecations = acc.internals.driverDeprecations ++ [ msg ];
                  };
                }
              )
            )
            (
              shorthandHarness
              // {
                internals = shorthandHarness.internals // {
                  driverDeprecations = [ ];
                };
              }
            )
            (
              driverMap.deprecations {
                name = shorthandHarness.internals.driverEntry.name;
                # Only paths the Consumer wrote: a legacy shim already warned
                # with its own message and must not get a second one naming a
                # top-level path it never set.
                topLevel = lib.filter (s: lib.attrByPath (lib.splitString "." s.path) null cfg != null) topLevelSet;
              }
            )
        else
          mkHarness (argsFor resolved.default);

      defaultDriverName = if shorthand then harness.internals.driverEntry.name else resolved.default;
      inherit (harness.internals) imageAttrName;
      driverImageAttr = name: "${imageAttrName}-${name}";
      # The package and apps a Driver's Harness publishes under that Driver's
      # name; srcAttr is the image attribute the Harness itself built.
      driverOutputs = name: h: srcAttr: {
        packages = lib.optionalAttrs (h.packages ? ${srcAttr}) {
          ${driverImageAttr name} = h.packages.${srcAttr};
        };
        apps = {
          ${name} = h.apps.default;
          "${name}-daemon" = h.apps.daemon;
        };
      };
      defaultOutputs = driverOutputs defaultDriverName harness imageAttrName;
      # A non-default Driver's Harness contributes only its image/closure
      # package and two apps; its base packages stay the default's. It names
      # its own image attribute and Daemon apps so its freshness probe and
      # child dispatch never point at the default's.
      otherDrivers = lib.filter (n: n != defaultDriverName) (lib.attrNames cfg.agents.drivers);
      otherHarness =
        name:
        let
          a = argsFor name;
        in
        mkHarness (
          a
          // {
            imageAttrName = driverImageAttr name;
            defaults = (a.defaults or { }) // {
              daemonApp = ".#${name}";
              daemonSelfApp = ".#${name}-daemon";
            };
          }
        );
      otherOutputs =
        lib.foldl'
          (
            acc: name:
            let
              o = driverOutputs name (otherHarness name) (driverImageAttr name);
            in
            {
              packages = acc.packages // o.packages;
              apps = acc.apps // o.apps;
            }
          )
          {
            packages = { };
            apps = { };
          }
          (if shorthand then [ ] else otherDrivers);
      # nixfmt from the consumer's locked nixpkgs input, the same pin the
      # nix-fmt gate uses, so `nix fmt` fixes what the check catches.
      nixfmt = (import resolvedNixpkgs { inherit system; }).nixfmt;
    in
    {
      packages = harness.packages // defaultOutputs.packages // otherOutputs.packages;
      apps = harness.apps // defaultOutputs.apps // otherOutputs.apps;
      formatter = nixfmt;
      spindrift.driverDeprecations = harness.internals.driverDeprecations or [ ];
    };
}
