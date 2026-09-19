# This table gives each quickstart wizard knob its canonical nix option path
# (issue #2556). Single-sourced here rather than hand-typed into the wizard's
# rendered flake.nix, so the generated Nix literals
# (cmd/launcher/quickstart/quickstart_paths_gen.go) cannot drift from
# lib/env-schema.nix's own group/nixSubPath taxonomy (ADR 0037 Pass 2).
let
  schema = import ./env-schema.nix;
  resolveNixPath = import ./nixpath.nix;
  structuralPaths = import ./structural-paths.nix;
  knobNames = [
    "repoSlug"
    "gitUserName"
    "gitUserEmail"
    "issueTracker"
    "codeForge"
    "forgejoBaseURL"
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
  # `runtime` is not a lib/env-schema.nix entry, so resolveNixPath cannot
  # resolve it. lib/structural-paths.nix holds its domain-tree path, joined
  # with "." the way resolveNixPath joins schema-rooted paths.
  structuralRootedPaths = {
    runtime = builtins.concatStringsSep "." structuralPaths.runtime;
  };
in
schemaRootedPaths // structuralRootedPaths
