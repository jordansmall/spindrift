# This table gives each quickstart wizard knob its canonical nix option path
# (issue #2556). Single-sourced here rather than hand-typed into the wizard's
# rendered flake.nix, so the generated Nix literals
# (cmd/launcher/quickstart/quickstart_paths_gen.go) cannot drift from
# lib/env-schema.nix's own group/nixSubPath taxonomy (ADR 0037 Pass 2).
let
  schema = import ./env-schema.nix;
  resolveNixPath = import ./nixpath.nix;
  structuralPaths = import ./structural-paths.nix;
  backends = import ./backends/default.nix;
  knobNames = [
    "repoSlug"
    "gitUserName"
    "gitUserEmail"
    "issueTracker"
    "codeForge"
  ];
  schemaRootedPaths = builtins.listToAttrs (
    map (
      name:
      assert schema.${name}.flakeOption or false; # else resolveNixPath resolves a path the flake module never declares
      {
        inherit name;
        value = resolveNixPath name schema.${name};
      }
    ) knobNames
  );
  # Every flakeOption knob a quickstart-capable backend row owns (ADR 0068
  # decision 8), keyed by env name, so the wizard renders the values a Strategy
  # derives from the remote without naming a knob. A row without a quickstart
  # block contributes none.
  quickstartKnobs = builtins.listToAttrs (
    builtins.concatMap (
      row:
      if row ? quickstart then
        builtins.concatMap (
          name:
          let
            entry = row.knobs.${name};
          in
          if entry.flakeOption or false then
            [
              {
                name = entry.env;
                value = {
                  path = resolveNixPath name entry;
                  dflt = entry.default or "";
                };
              }
            ]
          else
            [ ]
        ) (builtins.attrNames (row.knobs or { }))
      else
        [ ]
    ) backends
  );
  # `runtime` is not a lib/env-schema.nix entry, so resolveNixPath cannot
  # resolve it. lib/structural-paths.nix holds its domain-tree path, joined
  # with "." the way resolveNixPath joins schema-rooted paths.
  structuralRootedPaths = {
    runtime = builtins.concatStringsSep "." structuralPaths.runtime;
  };
in
{
  paths = schemaRootedPaths // structuralRootedPaths;
  knobs = quickstartKnobs;
  # The host `git config` keys the wizard reads to pre-fill the git identity
  # prompts: every schema member carrying a hostGitConfigKey.
  hostGitConfigKeys = builtins.listToAttrs (
    map (name: {
      inherit name;
      value = schema.${name}.hostGitConfigKey;
    }) (builtins.filter (name: schema.${name} ? hostGitConfigKey) (builtins.attrNames schema))
  );
  # Quickstart omits the codeForge line when the chosen row equals this.
  defaultCodeForge = schema.codeForge.default;
}
