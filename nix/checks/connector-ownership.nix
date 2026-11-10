# Issue #6770: every Connector subflake under connectors/<name> is its own
# release-please package and has its own CODEOWNERS entry, so its releases and
# reviews never fall through to the root package or the `*` owner.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatMap
    concatStringsSep
    filter
    filterAttrs
    hasInfix
    hasPrefix
    last
    optional
    removePrefix
    removeSuffix
    splitString
    ;
  config = builtins.fromJSON (builtins.readFile ../../.release-please-config.json);
  manifest = builtins.fromJSON (builtins.readFile ../../.release-please-manifest.json);
  codeowners = builtins.readFile ../../.github/CODEOWNERS;

  connectorsDir = ../../connectors;
  realNames =
    if builtins.pathExists connectorsDir then
      builtins.attrNames (filterAttrs (_: t: t == "directory") (builtins.readDir connectorsDir))
    else
      [ ];

  codeownersEntries =
    text:
    concatMap (
      line:
      let
        tokens = filter (t: t != "") (filter builtins.isString (builtins.split "[ \t]+" line));
      in
      if tokens != [ ] && !(hasPrefix "#" (builtins.head tokens)) then
        [
          {
            pattern = removeSuffix "/" (removePrefix "/" (builtins.head tokens));
            hasOwner = builtins.length tokens >= 2 && !(hasPrefix "#" (builtins.elemAt tokens 1));
          }
        ]
      else
        [ ]
    ) (splitString "\n" text);

  # Last matching pattern wins in CODEOWNERS, so only lines covering
  # connectors/<name> count (not a general glob engine): the last of them must
  # be the Connector's own line and carry an owner, since an owner-less line
  # un-owns the path. Patterns match with or without leading/trailing slashes.
  isOwned =
    path: entries:
    let
      covering = [
        "*"
        "**"
        "connectors"
        "connectors/*"
        "connectors/**"
        path
        "${path}/**"
      ];
      matching = filter (e: builtins.elem e.pattern covering) entries;
      own = last matching;
    in
    matching != [ ] && (own.pattern == path || own.pattern == "${path}/**") && own.hasOwner;

  # Approximates release-please's getComponent() (ignores
  # `include-component-in-tag`): an unset component is "", the
  # root package's, so a Connector would share the root's bare `v…` tag series.
  componentOf = pkg: pkg.component or pkg."package-name" or "";

  problemsOf =
    {
      names,
      config,
      manifest,
      codeowners,
    }:
    let
      components = map componentOf (builtins.attrValues config.packages);
      entries = codeownersEntries codeowners;
      rootProblems = optional (
        !(builtins.elem "connectors" (map (removeSuffix "/") (config.packages."."."exclude-paths" or [ ])))
      ) ''root package "." in .release-please-config.json lacks "connectors" in exclude-paths'';
      perConnector =
        name:
        let
          path = "connectors/${name}";
          component = componentOf config.packages.${path};
        in
        optional (
          !(config.packages ? ${path})
        ) "Connector ${name}: no packages.\"${path}\" in .release-please-config.json"
        ++
          optional
            (
              config.packages ? ${path}
              && (component == "" || builtins.length (filter (c: c == component) components) > 1)
            )
            "Connector ${name}: packages.\"${path}\" needs a \"component\" unique to it, so it gets its own ${name}-v… tag series instead of sharing another package's"
        ++ optional (
          !(manifest ? ${path})
        ) "Connector ${name}: no \"${path}\" version in .release-please-manifest.json"
        ++ optional (
          !(isOwned path entries)
        ) "Connector ${name}: no owned /${path}/ line in .github/CODEOWNERS, after any line covering it";
    in
    rootProblems ++ concatMap perConnector names;

  assertWired =
    inputs:
    let
      problems = problemsOf inputs;
    in
    assert assertMsg (problems == [ ])
      "Connector ownership is not wired:\n  ${concatStringsSep "\n  " problems}\nSee the \"Adding a Connector\" checklist in CONTRIBUTING.md.";
    true;

  wiredConfig = {
    packages = {
      "." = {
        exclude-paths = [ "connectors" ];
      };
      "connectors/demo" = {
        component = "demo";
      };
    };
  };
  wiredManifest = {
    "." = "0.1.0";
    "connectors/demo" = "0.1.0";
  };
  wiredCodeowners = ''
    # comment
    * @root
    /connectors/demo/ @owner
  '';
  wired = {
    names = [ "demo" ];
    config = wiredConfig;
    manifest = wiredManifest;
    codeowners = wiredCodeowners;
  };
in
{
  connector-ownership = builtins.seq (assertWired {
    names = realNames;
    inherit config manifest codeowners;
  }) (pkgs.runCommand "connector-ownership" { } "touch $out");

  # Each fixture removes exactly one piece of the wiring and must be named in
  # the problem list; the wired baseline and the pattern spellings must pass,
  # so the check cannot pass vacuously nor reject a valid entry.
  connector-ownership-regression =
    let
      mentions = problems: needle: builtins.any (p: hasInfix needle p) problems;
      withPackages = packages: wired // { config.packages = wiredConfig.packages // packages; };
      rejects =
        label: needle: inputs:
        let
          problems = problemsOf inputs;
        in
        assert assertMsg (mentions problems needle)
          "connector-ownership-regression: expected a problem mentioning '${needle}' for ${label}, got [${concatStringsSep "; " problems}]";
        true;
      accepts =
        label: inputs:
        let
          problems = problemsOf inputs;
        in
        assert assertMsg (problems == [ ])
          "connector-ownership-regression: expected no problems for ${label}, got [${concatStringsSep "; " problems}]";
        true;
    in
    assert accepts "a fully wired Connector" wired;
    assert accepts "CODEOWNERS patterns without the slashes" (
      wired // { codeowners = "connectors/demo @owner @other\n"; }
    );
    assert accepts "a /connectors/<name>/** line" (
      wired // { codeowners = "* @root\n/connectors/demo/** @owner\n"; }
    );
    assert accepts "a package-name in place of a component" (withPackages {
      "connectors/demo" = {
        "package-name" = "demo";
      };
    });
    assert accepts "a root exclude-paths entry with a trailing slash" (withPackages {
      "." = {
        exclude-paths = [ "connectors/" ];
      };
    });
    assert accepts "no Connectors at all" (
      wired
      // {
        names = [ ];
        codeowners = "";
      }
    );
    assert rejects "a missing release-please package" "Connector demo: no packages" (
      wired // { config.packages = removeAttrs wiredConfig.packages [ "connectors/demo" ]; }
    );
    assert rejects "a missing manifest version" "Connector demo: no \"connectors/demo\" version" (
      wired
      // {
        manifest = {
          "." = "0.1.0";
        };
      }
    );
    assert rejects "a missing CODEOWNERS line" "Connector demo: no owned" (
      wired // { codeowners = "* @root\n"; }
    );
    assert rejects "a commented-out CODEOWNERS line" "Connector demo: no owned" (
      wired // { codeowners = "# /connectors/demo/ @owner\n"; }
    );
    assert rejects "a CODEOWNERS pattern with no owner" "Connector demo: no owned" (
      wired // { codeowners = "/connectors/demo/\n"; }
    );
    assert rejects "a package with no component" "needs a \"component\" unique" (withPackages {
      "connectors/demo" = { };
    });
    assert rejects "a component shared with the root" "needs a \"component\" unique" (withPackages {
      "." = wiredConfig.packages."." // {
        component = "demo";
      };
    });
    assert rejects "a component shared between Connectors" "Connector two: packages" (
      withPackages {
        "connectors/one".component = "x";
        "connectors/two".component = "x";
      }
      // {
        names = [
          "one"
          "two"
        ];
        manifest = wiredManifest // {
          "connectors/one" = "0.1.0";
          "connectors/two" = "0.1.0";
        };
        codeowners = "* @root\n/connectors/one/ @o\n/connectors/two/ @o\n";
      }
    );
    assert rejects "a Connector line before the catch-all" "Connector demo: no owned" (
      wired // { codeowners = "/connectors/demo/ @owner\n* @root\n"; }
    );
    assert rejects "a trailing owner-less /connectors/ line" "Connector demo: no owned" (
      wired // { codeowners = "* @root\n/connectors/demo/ @owner\n/connectors/\n"; }
    );
    assert rejects "a root package without the exclusion" "exclude-paths" (withPackages {
      "." = { };
    });
    pkgs.runCommand "connector-ownership-regression" { } "touch $out";
}
